package runner

import (
	"fmt"
	"github.com/Alisjj/durablemux-verifier/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunTimeoutKillsDescendantsHoldingOutputPipes(t *testing.T) {
	c := &Context{Workdir: t.TempDir(), Timeout: time.Second, Env: os.Environ()}
	started := time.Now()
	result := c.Run([]string{"sh", "-c", "sleep 3 & wait"}, nil, 100*time.Millisecond, "")
	elapsed := time.Since(started)

	if result.ReturnCode != 124 {
		t.Fatalf("return code=%d, want 124", result.ReturnCode)
	}
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("100ms timeout waited %v for descendant", elapsed)
	}
}

func TestReviewCleanupOnlySelectsItsGeneratedNames(t *testing.T) {
	project := t.TempDir()
	cfg := config.Default()
	cfg["binary"] = "./dmux"
	c, err := New(project, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	name := c.ReviewSessionName()
	log := filepath.Join(project, "killed")
	body := fmt.Sprintf(`#!/bin/sh
case "$1" in
 list) printf '%%s\n' '[{"name":"normal-session"},{"name":"%s"},{"name":"%s-extra"}]' ;;
 kill) printf '%%s\n' "$2" >> '%s' ;;
esac
`, name, name, log)
	if err := os.WriteFile(c.Binary, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c.DiscoverSessions = true
	c.Close()
	c.Close()
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "normal-session") {
		t.Fatal("cleanup selected an unrelated session")
	}
	rows := strings.Fields(string(raw))
	if len(rows) != 2 || !strings.Contains(string(raw), name+"-extra") {
		t.Fatalf("expected two review sessions exactly once, got %q", raw)
	}
}

func TestNewRejectsRuntimeOutsideTemporaryDirectory(t *testing.T) {
	project := t.TempDir()
	_, err := New(project, map[string]any{
		"runtime_environment": map[string]any{"XDG_RUNTIME_DIR": project},
	})
	if err == nil {
		t.Fatal("expected external runtime path to be rejected")
	}
}

func TestNewDoesNotInheritCallerRuntime(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "real-runtime"))
	c, err := New(t.TempDir(), map[string]any{"runtime_environment": nil})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !strings.HasPrefix(c.Environment("XDG_RUNTIME_DIR"), c.TempDir+string(os.PathSeparator)) {
		t.Fatalf("runtime was not isolated: %s", c.Environment("XDG_RUNTIME_DIR"))
	}
}
