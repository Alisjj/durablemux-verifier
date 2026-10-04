package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Alisjj/durablemux-verifier/internal/guide"
	"github.com/Alisjj/durablemux-verifier/internal/ptyproc"
)

func runCLIInput(t *testing.T, bin, project, input string, args ...string) (int, string) {
	t.Helper()
	c := exec.Command(bin, append([]string{"--project", project}, args...)...)
	c.Stdin = strings.NewReader(input)
	var output bytes.Buffer
	c.Stdout, c.Stderr = &output, &output
	rc := 0
	if err := c.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			rc = e.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return rc, output.String()
}

func TestGuidedReviewAndEvidenceIntegrity(t *testing.T) {
	bin := buildBinary(t)
	project := t.TempDir()
	fake := absPath(t, "tests/fixtures/fake_dmux.py")
	if rc, _, err := runCLI(t, bin, project, "init", "--binary", fake); rc != 0 {
		t.Fatal(err)
	}
	if rc, _, _ := runCLI(t, bin, project, "evidence", "21", "--command", "printf observed"); rc != 0 {
		t.Fatal("evidence capture failed")
	}
	if rc, _, _ := runCLI(t, bin, project, "approve", "21", "--note", "generic approval"); rc != 2 {
		t.Fatal("generic approval bypassed requirement review")
	}
	stages, err := guide.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	criteria := stages[21].ReviewCriteria()
	var input strings.Builder
	for range criteria {
		input.WriteString("p\nObserved and explained against the transcript\n1\n")
	}
	input.WriteString("y\n")
	if rc, out := runCLIInput(t, bin, project, input.String(), "review", "21"); rc != 0 {
		t.Fatalf("review failed: %s", out)
	}
	if rc, out, err := runCLI(t, bin, project, "verify", "21", "--force"); rc != 0 {
		t.Fatalf("reviewed stage failed: %s %s", out, err)
	}
	progressPath := filepath.Join(project, ".dmux-verifier", "progress.json")
	raw, err := os.ReadFile(progressPath)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	stage := state["stages"].(map[string]any)["21"].(map[string]any)
	requirements := stage["review"].(map[string]any)["requirements"].(map[string]any)
	if len(requirements) != len(criteria) {
		t.Fatal("requirements were not persisted")
	}
	evidence := stage["evidence"].([]any)[0].(map[string]any)["path"].(string)
	if err := os.WriteFile(filepath.Join(project, evidence), []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rc, _, _ := runCLI(t, bin, project, "verify", "21", "--force"); rc != 1 {
		t.Fatal("modified evidence retained a pass")
	}
}

func TestGuidedReviewSavesPartialResultsAndResumes(t *testing.T) {
	bin := buildBinary(t)
	project := t.TempDir()
	if rc, _, err := runCLI(t, bin, project, "init", "--binary", absPath(t, "tests/fixtures/fake_dmux.py")); rc != 0 {
		t.Fatal(err)
	}
	if rc, out := runCLIInput(t, bin, project, "p\nProcess relationships observed\nc\nprintf process-evidence\nq\n", "review", "4"); rc != 1 || !strings.Contains(out, "Review saved") {
		t.Fatalf("partial review failed: %s", out)
	}
	if rc, _, _ := runCLI(t, bin, project, "verify", "4", "--force"); rc != 1 {
		t.Fatal("partial review passed")
	}
	stages, err := guide.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	input := "\n"
	for i := 1; i < len(stages[4].ReviewCriteria()); i++ {
		input += "p\nExplained from process evidence\n1\n"
	}
	input += "y\n"
	if rc, out := runCLIInput(t, bin, project, input, "review", "4"); rc != 0 {
		t.Fatalf("resumed review failed: %s", out)
	}
	if rc, _, _ := runCLI(t, bin, project, "verify", "4", "--force"); rc != 0 {
		t.Fatal("complete resumed review did not pass")
	}
}

func TestVerificationDetectsPriorStageRegression(t *testing.T) {
	bin := buildBinary(t)
	project := t.TempDir()
	source, err := os.ReadFile(absPath(t, "tests/fixtures/fake_dmux.py"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(project, "dmux")
	if err := os.WriteFile(target, source, 0o755); err != nil {
		t.Fatal(err)
	}
	if rc, _, err := runCLI(t, bin, project, "init"); rc != 0 {
		t.Fatal(err)
	}
	if rc, _, _ := runCLI(t, bin, project, "verify", "1"); rc != 0 {
		t.Fatal("initial stage failed")
	}
	source = []byte(strings.Replace(string(source), "print(\"fake-dmux 0.1.0\")", "print(\"broken\\nversion\")", 1))
	if err := os.WriteFile(target, source, 0o755); err != nil {
		t.Fatal(err)
	}
	if rc, out, err := runCLI(t, bin, project, "verify", "2"); rc != 1 || !strings.Contains(out+err, "regressed") {
		t.Fatalf("regression not detected: %s %s", out, err)
	}
}

func TestLiveReviewReturnsToPromptsWithHealthyTerminal(t *testing.T) {
	bin := buildBinary(t)
	project := t.TempDir()
	if rc, _, err := runCLI(t, bin, project, "init", "--binary", absPath(t, "tests/fixtures/fake_dmux.py")); rc != 0 {
		t.Fatal(err)
	}
	p, err := ptyproc.Start([]string{bin, "--project", project, "review", "4"}, project, os.Environ(), 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Terminate()
	if _, err := p.ReadUntil([]byte("Result:"), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	command := `python3 -u -c 'print("__LIVE_READY__",flush=True); print("LIVE:"+input(),flush=True)'`
	if err := p.Write([]byte("l\n" + command + "\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ReadUntil([]byte("Live command started."), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// The ready marker can also appear in command echo; require the actual line.
	if _, err := p.ReadUntil([]byte("\r\n__LIVE_READY__\r\n"), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := p.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ReadUntil([]byte("LIVE:hello"), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ReadUntil([]byte("Now record the observed result."), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := p.Write([]byte("p\nObserved live process behaviour\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ReadUntil([]byte("[2/"), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	stages, err := guide.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(stages[4].ReviewCriteria()); i++ {
		if err := p.Write([]byte("p\nExplained from live transcript\n1\n")); err != nil {
			t.Fatal(err)
		}
		marker := "Approve all reviewed requirements?"
		if i+1 < len(stages[4].ReviewCriteria()) {
			marker = fmt.Sprintf("[%d/", i+2)
		}
		if _, err := p.ReadUntil([]byte(marker), 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	_ = p.Write([]byte("y\n"))
	rc, err := p.Wait(5 * time.Second)
	if err != nil || rc != 0 {
		t.Fatalf("live review failed: exit=%d error=%v\n%s", rc, err, p.Buffer)
	}
	restored, err := p.TerminalRestored()
	if err != nil || !restored {
		t.Fatalf("terminal not restored: %v", err)
	}
}
