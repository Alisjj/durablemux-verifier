package review

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Alisjj/durablemux-verifier/internal/model"
	"github.com/Alisjj/durablemux-verifier/internal/store"
	"github.com/Alisjj/durablemux-verifier/internal/util"
)

func TestReviewRequiresEveryCriterionAndUnchangedEvidence(t *testing.T) {
	root := t.TempDir()
	s, err := store.New(filepath.Join(root, "progress.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := "evidence.txt"
	if err := os.WriteFile(filepath.Join(root, path), []byte("observed results"), 0o600); err != nil {
		t.Fatal(err)
	}
	sha, err := util.Sha256File(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddEvidence(10, map[string]any{"path": path, "sha256": sha}); err != nil {
		t.Fatal(err)
	}
	if count := EvidenceCount(s, 10, root); count != 1 {
		t.Fatalf("intact evidence count=%d", count)
	}
	stage := &model.Stage{Number: 10, ReviewRequirements: []string{"redraw", "responsiveness"}}
	items := map[string]any{CriterionID("redraw"): map[string]any{"result": "pass", "note": "screen redrew", "evidence": path}}
	if err := s.RecordReview(10, map[string]any{"binary_sha256": "binary-a", "requirements": items}); err != nil {
		t.Fatal(err)
	}
	if err := Complete(stage, s, root, "binary-a"); err == nil {
		t.Fatal("incomplete review accepted")
	}
	items[CriterionID("responsiveness")] = map[string]any{"result": "pass", "note": "input remained responsive", "evidence": path}
	if err := Complete(stage, s, root, "binary-a"); err != nil {
		t.Fatal(err)
	}
	if err := Complete(stage, s, root, "binary-b"); err == nil {
		t.Fatal("review of a different binary accepted")
	}
	items[CriterionID("responsiveness")].(map[string]any)["result"] = "fail"
	if err := Complete(stage, s, root, "binary-a"); err == nil {
		t.Fatal("failed requirement accepted")
	}
	items[CriterionID("responsiveness")].(map[string]any)["result"] = "pass"
	if err := os.WriteFile(filepath.Join(root, path), []byte("changed evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Complete(stage, s, root, "binary-a"); err == nil {
		t.Fatal("modified evidence accepted")
	}
	if count := EvidenceCount(s, 10, root); count != 0 {
		t.Fatalf("modified evidence counted: %d", count)
	}
	if err := os.Remove(filepath.Join(root, path)); err != nil {
		t.Fatal(err)
	}
	if err := Complete(stage, s, root, "binary-a"); err == nil {
		t.Fatal("deleted evidence accepted")
	}
}

func TestReviewChangeRevokesApprovalAndStagePass(t *testing.T) {
	s, err := store.New(filepath.Join(t.TempDir(), "progress.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(10, "reviewed"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRun(10, map[string]any{"passed": true}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReview(10, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if s.Approved(10) || s.IsPassed(10) {
		t.Fatal("editing a review must invalidate its approval and pass")
	}
}
