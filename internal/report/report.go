package report

import (
	"fmt"
	"strings"

	"github.com/Alisjj/durablemux-verifier/internal/model"
	"github.com/Alisjj/durablemux-verifier/internal/review"
	"github.com/Alisjj/durablemux-verifier/internal/store"
)

// Markdown builds the progress report (parity with Python report.py).
func Markdown(stages map[int]*model.Stage, s *store.Store) string {
	var b strings.Builder
	b.WriteString("# DurableMux Verification Report\n\n")
	passed := 0
	for n := range stages {
		if stageStatus(n, stages, s) == "passed" {
			passed++
		}
	}
	fmt.Fprintf(&b, "**Progress:** %d/%d stages passed\n\n", passed, len(stages))
	b.WriteString("| Stage | Title | Mode | Status | Evidence |\n")
	b.WriteString("|---:|---|---|---|---:|\n")
	for _, n := range sortedKeys(stages) {
		st := stages[n]
		item := s.Stage(n)
		status := stageStatus(n, stages, s)
		ev, _ := item["evidence"].([]any)
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %d |\n", n, st.Title, st.Mode, status, len(ev))
	}
	b.WriteString("\n")
	for _, n := range sortedKeys(stages) {
		st := stages[n]
		item := s.Stage(n)
		if item["last_run"] == nil && item["evidence"] == nil && item["review"] == nil {
			continue
		}
		fmt.Fprintf(&b, "## Stage %d: %s\n\n", n, st.Title)
		if run, ok := item["last_run"].(map[string]any); ok {
			if run["passed"] == true && !s.IsPassed(n) {
				b.WriteString("Historical result: **PASS — re-verification required**\n\n")
			} else if run["passed"] == true {
				b.WriteString("Result: **PASS**\n\n")
			} else {
				b.WriteString("Result: **FAIL**\n\n")
			}
			if checks, ok := run["checks"].([]any); ok {
				for _, c := range checks {
					if m, ok := c.(map[string]any); ok {
						icon := "FAIL"
						if m["passed"] == true {
							icon = "PASS"
						}
						fmt.Fprintf(&b, "- **%s:** %v — %v\n", icon, m["name"], m["detail"])
					}
				}
			}
		}
		if record, ok := item["review"].(map[string]any); ok {
			fmt.Fprintf(&b, "\nGuided review (binary SHA-256: `%v`):\n\n", record["binary_sha256"])
			if requirements, ok := record["requirements"].(map[string]any); ok {
				for _, criterion := range st.ReviewCriteria() {
					if requirement, ok := requirements[review.CriterionID(criterion)].(map[string]any); ok {
						fmt.Fprintf(&b, "- **%v:** %v — %v (evidence: %v)\n", requirement["result"], strings.ReplaceAll(fmt.Sprint(requirement["text"]), "\n", " "), requirement["note"], requirement["evidence"])
					}
				}
			}
		}
		if ev, ok := item["evidence"].([]any); ok && len(ev) > 0 {
			b.WriteString("\nEvidence:\n")
			for _, e := range ev {
				if m, ok := e.(map[string]any); ok {
					ref := m["path"]
					if ref == nil {
						ref = m["command"]
					}
					fmt.Fprintf(&b, "- %v — %v\n", ref, m["note"])
				}
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func stageStatus(number int, stages map[int]*model.Stage, s *store.Store) string {
	for _, prior := range sortedKeys(stages) {
		if prior >= number {
			break
		}
		if !s.IsPassed(prior) {
			return "locked"
		}
	}
	if s.IsPassed(number) {
		return "passed"
	}
	status, _ := s.Stage(number)["status"].(string)
	if status == "passed" {
		return "stale"
	}
	if status != "" {
		return status
	}
	return "ready"
}

func sortedKeys(stages map[int]*model.Stage) []int {
	keys := make([]int, 0, len(stages))
	for k := range stages {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
