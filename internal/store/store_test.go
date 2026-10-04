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
