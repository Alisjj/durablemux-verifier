// Package review validates requirement-by-requirement human review records.
package review

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Alisjj/durablemux-verifier/internal/model"
	"github.com/Alisjj/durablemux-verifier/internal/proctree"
	"github.com/Alisjj/durablemux-verifier/internal/store"
	"github.com/Alisjj/durablemux-verifier/internal/util"
)

func CriterionID(text string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
}

// Complete requires a passing observation and intact recorded evidence for
// every current criterion. Approval of a different binary cannot be reused.
func Complete(stage *model.Stage, s *store.Store, root, binarySHA string) error {
	record, _ := s.Stage(stage.Number)["review"].(map[string]any)
	if record["binary_sha256"] != binarySHA || binarySHA == "" {
		return fmt.Errorf("review is missing or belongs to a different binary")
	}
	items, _ := record["requirements"].(map[string]any)
	criteria := stage.ReviewCriteria()
	type evidenceKey struct {
		path        string
		processTree bool
	}
	validated := map[evidenceKey]bool{}
	if len(criteria) == 0 {
		return fmt.Errorf("stage has no review criteria")
	}
	for _, text := range criteria {
		item, _ := items[CriterionID(text)].(map[string]any)
		if item["result"] != "pass" {
			return fmt.Errorf("a requirement is failed or unreviewed: %s", text)
		}
		if note, ok := item["note"].(string); !ok || strings.TrimSpace(note) == "" {
			return fmt.Errorf("a passing requirement needs an observation: %s", text)
		}
		path, _ := item["evidence"].(string)
		key := evidenceKey{path, needsProcessTree(stage, text)}
		if !validated[key] {
			if err := CriterionEvidenceValid(stage, text, s, root, path); err != nil {
				return err
			}
			validated[key] = true
		}
	}
	return nil
}

func needsProcessTree(stage *model.Stage, text string) bool {
	return stage.Number == 4 && !strings.HasPrefix(text, "Explain:")
}

// CriterionEvidenceValid checks integrity and the stage-specific content needed
// to support a passing requirement.
func CriterionEvidenceValid(stage *model.Stage, text string, s *store.Store, root, path string) error {
	if err := EvidenceValid(s, stage.Number, root, path); err != nil {
		return err
	}
	if !needsProcessTree(stage, text) {
		return nil
	}
	items, _ := s.Stage(4)["evidence"].([]any)
	for _, value := range items {
		ev, _ := value.(map[string]any)
		if ev["path"] != path {
			continue
		}
		if code, ok := ev["exit_code"]; ok && fmt.Sprint(code) != "0" {
			return fmt.Errorf("stage 4 cannot pass with a failed process capture; use the tree experiment or import a successful snapshot")
		}
		if ev["timed_out"] == true {
			return fmt.Errorf("stage 4 process capture timed out")
		}
		if message, ok := ev["runner_error"].(string); ok && message != "" {
			return fmt.Errorf("stage 4 process capture failed: %s", message)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		return err
	}
	return proctree.ValidateText(data)
}

func EvidenceValid(s *store.Store, stage int, root, path string) error {
	if path == "" || filepath.IsAbs(path) {
		return fmt.Errorf("a requirement needs a recorded evidence reference")
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("evidence must stay inside the project")
	}
	evidence, _ := s.Stage(stage)["evidence"].([]any)
	for _, v := range evidence {
		ev, _ := v.(map[string]any)
		if ev["path"] != path {
			continue
		}
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("evidence must be an existing non-empty regular file: %s", path)
		}
		sha, err := util.Sha256File(filepath.Join(root, path))
		if err != nil || sha == "" || ev["sha256"] != sha {
			return fmt.Errorf("evidence is missing, changed, or predates integrity tracking: %s", path)
		}
		return nil
	}
	return fmt.Errorf("evidence is not recorded for stage %d: %s", stage, path)
}

func EvidenceCount(s *store.Store, stage int, root string) int {
	items, _ := s.Stage(stage)["evidence"].([]any)
	seen := map[string]bool{}
	for _, item := range items {
		ev, _ := item.(map[string]any)
		path, _ := ev["path"].(string)
		if !seen[path] && EvidenceValid(s, stage, root, path) == nil {
			seen[path] = true
		}
	}
	return len(seen)
}
