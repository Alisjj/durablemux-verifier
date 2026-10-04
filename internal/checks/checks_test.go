package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alisjj/durablemux-verifier/internal/config"
	"github.com/Alisjj/durablemux-verifier/internal/model"
	"github.com/Alisjj/durablemux-verifier/internal/runner"
)

func TestTailOneIsBounded(t *testing.T) {
	var lines strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&lines, "LOG%02d\n", i)
	}
	full := []byte(lines.String())
	if tailOneIsBounded(full, full) {
		t.Fatal("an implementation that ignores --tail must not pass")
	}
	if !tailOneIsBounded(full, []byte("LOG20\n")) {
		t.Fatal("one final log line should pass")
	}
	if tailOneIsBounded([]byte("LOG20\npadding"), []byte("LOG20\n")) {
		t.Fatal("losing earlier history must not pass")
	}
}

func TestStage33RejectsUnsafeRuntimeHandling(t *testing.T) {
	results := runStage33WithScript(t, `
rt=$DMUX_RUNTIME_DIR
if [ "$1" = new ]; then
  mkdir -p "$rt"
  : > "$rt/state"
  chmod 600 "$rt/state"
fi
exit 0
`)
	if results[1].Passed {
		t.Fatal("symlink-following implementation passed the symlink probe")
	}
	if results[2].Passed {
		t.Fatal("implementation that accepts mode 0755 passed the insecure-path probe")
	}
}

func TestStage33AcceptsSafeRuntimeHandling(t *testing.T) {
	results := runStage33WithScript(t, `
rt=$DMUX_RUNTIME_DIR
if [ -L "$rt" ]; then
  exit 2
fi
mkdir -p "$rt"
chmod 700 "$rt"
if [ "$1" = new ]; then
  : > "$rt/state"
  chmod 600 "$rt/state"
  python3 -c 'import os,socket; os.chdir(os.environ["DMUX_RUNTIME_DIR"]); s=socket.socket(socket.AF_UNIX); s.bind("control.sock"); os.chmod("control.sock",0o600)'
fi
exit 0
`)
	for _, result := range results {
		if !result.Passed {
			t.Fatalf("%s failed: %s", result.Name, result.Detail)
		}
	}
}

func TestStage33RejectsOverwritingSymlinkTarget(t *testing.T) {
	results := runStage33WithScript(t, `
rt=$DMUX_RUNTIME_DIR
if [ -L "$rt" ]; then
  printf compromised > "$rt/sentinel"
  exit 2
fi
chmod 700 "$rt"
exit 0
`)
	if results[1].Passed {
		t.Fatal("overwriting the sentinel through a symlink must fail")
	}
	if results[0].Passed {
		t.Fatal("an empty runtime cannot prove session permissions")
	}
}

func runStage33WithScript(t *testing.T, body string) []model.CheckResult {
	t.Helper()
	project := t.TempDir()
	bin := filepath.Join(project, "fake-dmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg["binary"] = bin
	ctx, err := runner.New(project, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Close()
	return stage33(ctx)
}
