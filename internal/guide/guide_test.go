package guide

import (
	"strings"
	"testing"
)

func TestStandaloneAcceptanceTestsArePreserved(t *testing.T) {
	stages, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(stages[4].AcceptanceTests) != 1 || !strings.Contains(stages[4].AcceptanceTests[0], "ps -o pid,ppid,pgid,sid,tpgid,stat,tty,cmd") {
		t.Fatalf("stage 4 process snapshot acceptance test was lost: %v", stages[4].AcceptanceTests)
	}
	if len(stages[9].AcceptanceTests) != 1 || !strings.Contains(stages[9].AcceptanceTests[0], "Compare terminal dimensions") {
		t.Fatalf("stage 9 prose acceptance test was lost: %v", stages[9].AcceptanceTests)
	}
}

func TestBossTestsDoNotBecomeStageQuestions(t *testing.T) {
	stages, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(stages[4].Questions) != 2 || len(stages[11].Questions) != 2 || len(stages[38].Questions) != 2 {
		t.Fatalf("boss-test requirements leaked into stage questions: stage 4=%v, stage 11=%v, stage 38=%v", stages[4].Questions, stages[11].Questions, stages[38].Questions)
	}
}

func TestHeadingsInsideAcceptanceCodeDoNotEndStage(t *testing.T) {
	stages, err := Parse("## Stage 4: Process tree\n### Acceptance tests\nCapture:\n```text\n## command output\n### Questions to answer\n```\n### Questions to answer\n- Actual question\n## Module A Boss Test\n- Not a stage question\n", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages[4].AcceptanceTests) != 1 || !strings.Contains(stages[4].AcceptanceTests[0], "## command output") || len(stages[4].Questions) != 1 {
		t.Fatalf("fenced command output was interpreted as guide headings: %+v", stages[4])
	}
}
