package runner

import (
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
