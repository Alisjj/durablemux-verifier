package checks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Alisjj/durablemux-verifier/internal/config"
	"github.com/Alisjj/durablemux-verifier/internal/model"
	"github.com/Alisjj/durablemux-verifier/internal/runner"
)

func TestTailOneIsBounded(t *testing.T) {
	full := []byte("LOG01\nLOG19\nLOG20\n")
	if tailOneIsBounded(full, full) {
		t.Fatal("an implementation that ignores --tail must not pass")
	}
	if !tailOneIsBounded(full, []byte("LOG20\n")) {
		t.Fatal("one final log line should pass")
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
fi
exit 0
`)
	for _, result := range results {
		if !result.Passed {
			t.Fatalf("%s failed: %s", result.Name, result.Detail)
		}
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
