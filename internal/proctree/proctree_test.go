package proctree

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Alisjj/durablemux-verifier/internal/config"
	"github.com/Alisjj/durablemux-verifier/internal/runner"
	"github.com/Alisjj/durablemux-verifier/internal/util"
)

const sampleSnapshot = `PID PPID PGID SID TPGID STAT TTY COMMAND
10 1 10 10 12 Ss pts/1 bash
11 10 12 10 12 S pts/1 dmux run -- sleep 30
12 11 12 10 12 S+ pts/1 sleep 30

[standard descriptors]
PID 12 fd 0 -> /dev/pts/1
`

func TestSnapshotValidation(t *testing.T) {
	if err := ValidateText([]byte(sampleSnapshot)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"[exit] 1\n" + sampleSnapshot,
		"[timeout] true\n" + sampleSnapshot,
		"[runner error] launch failed\n" + sampleSnapshot,
		strings.ReplaceAll(sampleSnapshot, " SID", " SESS"),
		strings.Replace(sampleSnapshot, "12 S+", "12 Z+", 1),
		strings.Replace(sampleSnapshot, "12 11 12", "12 99 12", 1),
		strings.Replace(sampleSnapshot, "12 11 12 10", "12 11 12 0", 1),
		strings.Replace(sampleSnapshot, "10 1 10 10 12 Ss pts/1 bash\n", "", 1),
		strings.Replace(sampleSnapshot, "\n[standard descriptors]", "\n12 11 12 10 12 Z+ pts/1 sleep 30\n[standard descriptors]", 1),
		"process-evidence",
	} {
		if err := ValidateText([]byte(bad)); err == nil {
			t.Fatalf("invalid snapshot accepted:\n%s", bad)
		}
	}
}

func TestReturningWithOrphanChildFailsAndCleansUp(t *testing.T) {
	project := t.TempDir()
	bin, pidFile := filepath.Join(project, "dmux"), filepath.Join(project, "child.pid")
	script := fmt.Sprintf("#!/bin/sh\nsleep 30 &\necho $! > '%s'\nexit 0\n", pidFile)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg["binary"], cfg["timeout_seconds"] = bin, 2.0
	ctx, err := runner.New(project, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Close()
	if _, err := Capture(ctx); err == nil {
		t.Fatal("returning while leaving a background child produced passing evidence")
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		t.Fatal(err)
	}
	if !util.WaitUntil(func() bool { return !util.ProcessAlive(pid) }, time.Second) {
		t.Fatalf("orphan child was not cleaned up: %d", pid)
	}
}

func TestCaptureShowsLiveCommandAndCleansUp(t *testing.T) {
	project := t.TempDir()
	bin := filepath.Join(project, "dmux with spaces")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = launch ]; then shift; shift; \"$@\" & child=$!; wait \"$child\"; exit $?; fi\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg["binary"] = bin
	cfg["commands"].(map[string]any)["run"] = []any{"launch", "--"}
	ctx, err := runner.New(project, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Close()
	data, err := Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateText(data); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if !strings.Contains(string(data), "[standard descriptors]") {
		t.Fatal("descriptor observations omitted")
	}
	var childPID int
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Command PID:") {
			fmt.Sscanf(line, "Command PID: %d", &childPID)
		}
	}
	if childPID <= 1 || !util.WaitUntil(func() bool { return !util.ProcessAlive(childPID) }, time.Second) {
		t.Fatalf("captured child was not cleaned up: pid=%d", childPID)
	}
}

func TestCaptureSupportsNestedPTYAndSeparateSession(t *testing.T) {
	source, err := os.ReadFile("../../tests/fixtures/pty_dmux.py")
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	bin := filepath.Join(project, "dmux")
	if err := os.WriteFile(bin, source, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg["binary"] = bin
	ctx, err := runner.New(project, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Close()
	data, err := Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateText(data); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 9 && fields[7] == "sleep" && fields[8] == "30" {
			pid, err := strconv.Atoi(fields[0])
			if err != nil {
				t.Fatal(err)
			}
			if fields[3] != fields[0] {
				t.Fatalf("nested command's SID is not its real session-leader PID: %s", line)
			}
			if !util.WaitUntil(func() bool { return !util.ProcessAlive(pid) }, time.Second) {
				t.Fatalf("nested command was not cleaned up: %d", pid)
			}
			return
		}
	}
	t.Fatalf("did not capture the nested command: %s", data)
}

func TestImmediateReturnCannotCreateProcessEvidence(t *testing.T) {
	project := t.TempDir()
	bin := filepath.Join(project, "dmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg["binary"], cfg["timeout_seconds"] = bin, 2.0
	ctx, err := runner.New(project, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Close()
	if _, err := Capture(ctx); err == nil {
		t.Fatal("a command that does not run sleep produced successful evidence")
	}
}
