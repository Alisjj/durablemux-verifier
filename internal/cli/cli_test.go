package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Alisjj/durablemux-verifier/internal/guide"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "dmux-verify-go")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/dmux-verify")
	// find module root by walking up from cwd
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

func runCLI(t *testing.T, bin, project string, args ...string) (int, string, string) {
	t.Helper()
	full := append([]string{"--project", project}, args...)
	c := exec.Command(bin, full...)
	var soB, seB bytes.Buffer
	c.Stdout = &soB
	c.Stderr = &seB
	rc := 0
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		} else {
			t.Fatalf("run failed: %v", err)
		}
	}
	return rc, soB.String(), seB.String()
}

func TestInitAndFirstThreeStages(t *testing.T) {
	bin := buildBinary(t)
	project := t.TempDir()
	fake := absPath(t, "tests/fixtures/fake_dmux.py")
	if rc, _, se := runCLI(t, bin, project, "init", "--binary", fake); rc != 0 {
		t.Fatalf("init failed: %s", se)
	}
	for _, s := range []string{"1", "2", "3"} {
		if rc, so, se := runCLI(t, bin, project, "verify", s); rc != 0 {
			t.Fatalf("verify %s failed: %s %s", s, so, se)
		}
	}
	rc, so, _ := runCLI(t, bin, project, "status", "--json")
	if rc != 0 {
		t.Fatal("status failed")
	}
	var payload []map[string]any
	if err := json.Unmarshal([]byte(so), &payload); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if payload[0]["status"] != "passed" || payload[1]["status"] != "passed" || payload[2]["status"] != "passed" {
		t.Fatalf("unexpected statuses: %v", payload[:4])
	}
	if payload[3]["status"] != "ready" {
		t.Fatalf("stage 4 should be ready, got %v", payload[3]["status"])
	}
}

func TestLocking(t *testing.T) {
	bin := buildBinary(t)
	project := t.TempDir()
	fake := absPath(t, "tests/fixtures/fake_dmux.py")
	if rc, _, se := runCLI(t, bin, project, "init", "--binary", fake); rc != 0 {
		t.Fatalf("init failed: %s", se)
	}
	rc, _, se := runCLI(t, bin, project, "verify", "2")
	if rc != 2 {
		t.Fatalf("expected exit 2, got %d (%s)", rc, se)
	}
	if !containsFold(se, "locked") {
		t.Fatalf("expected locked message, got %s", se)
	}
}

func TestForceDoesNotUnlockLaterStages(t *testing.T) {
	bin := buildBinary(t)
	project := t.TempDir()
	fake := absPath(t, "tests/fixtures/fake_dmux.py")
	if rc, _, se := runCLI(t, bin, project, "init", "--binary", fake); rc != 0 {
		t.Fatalf("init failed: %s", se)
	}
	if rc, so, se := runCLI(t, bin, project, "verify", "2", "--force"); rc != 0 {
		t.Fatalf("forced stage 2 failed: %s %s", so, se)
	}
	rc, _, se := runCLI(t, bin, project, "verify", "3")
	if rc != 2 {
		t.Fatalf("expected stage 3 to remain locked, got %d (%s)", rc, se)
	}
	if !containsFold(se, "pass stage 1 first") {
		t.Fatalf("expected missing prerequisite message, got %s", se)
	}
}

func TestInvalidInitFlagHasNoSideEffects(t *testing.T) {
	bin := buildBinary(t)
	project := t.TempDir()
	rc, _, _ := runCLI(t, bin, project, "init", "--not-a-real-flag")
	if rc != 2 {
		t.Fatalf("expected usage exit 2, got %d", rc)
	}
	if _, err := os.Stat(filepath.Join(project, ".dmux-verifier", "config.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid init created config: %v", err)
	}
}

func TestMalformedCustomCheckFailsWithoutPanic(t *testing.T) {
	bin := buildBinary(t)
	project := t.TempDir()
	fake := absPath(t, "tests/fixtures/fake_dmux.py")
	if rc, _, se := runCLI(t, bin, project, "init", "--binary", fake); rc != 0 {
		t.Fatalf("init failed: %s", se)
	}
	configPath := filepath.Join(project, ".dmux-verifier", "config.json")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["custom_checks"] = map[string]any{"4": []any{map[string]any{"name": "empty"}}}
	raw, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	rc, so, se := runCLI(t, bin, project, "verify", "4", "--force")
	if rc != 1 {
		t.Fatalf("expected failed verification, got %d (%s %s)", rc, so, se)
	}
	if !containsFold(so, "custom check command must contain an executable") {
		t.Fatalf("expected configuration failure, got %s", so)
	}
}

func TestGuideContains38CoreStages(t *testing.T) {
	stages, err := guide.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 38 {
		t.Fatalf("expected 38 stages, got %d", len(stages))
	}
	min, max := 999, 0
	for n := range stages {
		if n < min {
			min = n
		}
		if n > max {
			max = n
		}
	}
	if min != 1 || max != 38 {
		t.Fatalf("expected range 1..38, got %d..%d", min, max)
	}
}

func absPath(t *testing.T, rel string) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, rel)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("module root not found")
		}
		dir = parent
	}
}

func containsFold(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		h, n := hay, needle
		for i := 0; i+len(n) <= len(h); i++ {
			match := true
			for j := 0; j < len(n); j++ {
				a, b := h[i+j], n[j]
				if a >= 'A' && a <= 'Z' {
					a += 'a' - 'A'
				}
				if b >= 'A' && b <= 'Z' {
					b += 'a' - 'A'
				}
				if a != b {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
		return false
	})()
}
