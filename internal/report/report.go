package report

import (
	"fmt"
	"strings"

	"durablemux-verifier/internal/model"
	"durablemux-verifier/internal/store"
)

// Markdown builds the progress report (parity with Python report.py).
func Markdown(stages map[int]*model.Stage, s *store.Store) string {
	var b strings.Builder
	b.WriteString("# DurableMux Verification Report\n\n")
	passed := 0
	for n := range stages {
		if s.IsPassed(n) {
			passed++
		}
	}
	fmt.Fprintf(&b, "**Progress:** %d/%d stages passed\n\n", passed, len(stages))
	b.WriteString("| Stage | Title | Mode | Status | Evidence |\n")
	b.WriteString("|---:|---|---|---|---:|\n")
	for _, n := range sortedKeys(stages) {
		st := stages[n]
		item := s.Stage(n)
		status, _ := item["status"].(string)
		if status == "" {
			if n > 1 && !s.IsPassed(n-1) {
				status = "locked"
			} else {
				status = "ready"
			}
		}
		ev, _ := item["evidence"].([]any)
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %d |\n", n, st.Title, st.Mode, status, len(ev))
	}
	b.WriteString("\n")
	for _, n := range sortedKeys(stages) {
		st := stages[n]
		item := s.Stage(n)
		if item["last_run"] == nil && item["evidence"] == nil {
			continue
		}
		fmt.Fprintf(&b, "## Stage %d: %s\n\n", n, st.Title)
		if run, ok := item["last_run"].(map[string]any); ok {
			if run["passed"] == true {
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
