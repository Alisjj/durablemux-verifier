package store

import (
	"path/filepath"
	"testing"
)

func TestLegacyPassDoesNotUnlockStages(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "progress.json"))
	if err != nil {
		t.Fatal(err)
	}
	item := s.Stage(10)
	item["status"], item["last_run"] = "passed", map[string]any{"passed": true}
	if s.IsPassed(10) {
		t.Fatal("legacy checks cannot establish a current pass")
	}
	if err := s.RecordRun(10, map[string]any{"passed": true}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.IsPassed(10) {
		t.Fatal("current revision was not persisted")
	}
}

func TestOldStage4PassRequiresNewProcessSnapshotReview(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "progress.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{3, 4, 5} {
		item := s.Stage(n)
		item["status"] = "passed"
		item["last_run"] = map[string]any{"passed": true, "verification_revision": 2}
	}
	if s.IsPassed(4) || !s.IsPassed(3) || !s.IsPassed(5) {
		t.Fatal("only stage 4's old pass should require re-verification")
	}
	if err := s.RecordRun(4, map[string]any{"passed": true}); err != nil {
		t.Fatal(err)
	}
	s, err = New(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.IsPassed(4) {
		t.Fatal("new stage 4 verification was not persisted")
	}
}
