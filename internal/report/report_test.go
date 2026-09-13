package report

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alisjj/durablemux-verifier/internal/model"
	"github.com/Alisjj/durablemux-verifier/internal/store"
)

func TestMarkdownLocksPassedStageWhenEarlierPrerequisiteIsMissing(t *testing.T) {
	stages := map[int]*model.Stage{
		1: {Number: 1, Title: "one", Mode: "auto"},
		2: {Number: 2, Title: "two", Mode: "auto"},
		3: {Number: 3, Title: "three", Mode: "auto"},
	}
	s, err := store.New(filepath.Join(t.TempDir(), "progress.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRun(2, map[string]any{"passed": true}); err != nil {
		t.Fatal(err)
	}

	got := Markdown(stages, s)
	if !strings.Contains(got, "**Progress:** 0/3 stages passed") {
		t.Fatalf("locked pass was counted as progress:\n%s", got)
	}
	if !strings.Contains(got, "| 2 | two | auto | locked |") {
		t.Fatalf("stage 2 was not reported as locked:\n%s", got)
	}
}
