package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Increment when older verification results no longer establish a stage pass.
const VerificationRevision = 2

// Stage 4 previously omitted its process-snapshot acceptance test. Invalidate
// that stage's old passes while preserving other stages and all history.
func verificationRevision(number int) int {
	if number == 4 && VerificationRevision < 3 {
		return 3
	}
	return VerificationRevision
}

// UTCNow mirrors state.utc_now (ISO-8601 UTC).
func UTCNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// Store persists .dmux-verifier/progress.json.
// Layout is JSON-compatible with the Python verifier.
type Store struct {
	Path string
	Data map[string]any
}

// New loads path when present, else starts empty.
func New(path string) (*Store, error) {
	s := &Store{Path: path, Data: map[string]any{"version": 1, "stages": map[string]any{}}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s.Data); err != nil {
		return nil, err
	}
	if _, ok := s.Data["stages"]; !ok {
		s.Data["stages"] = map[string]any{}
	}
	return s, nil
}

// Save writes atomically via a .tmp rename.
func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.Data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

func (s *Store) stages() map[string]any {
	m, ok := s.Data["stages"].(map[string]any)
	if !ok {
		m = map[string]any{}
		s.Data["stages"] = m
	}
	return m
}

// Stage returns (creating) the per-stage record.
func (s *Store) Stage(number int) map[string]any {
	key := itoa(number)
	m := s.stages()
	v, ok := m[key].(map[string]any)
	if !ok {
		v = map[string]any{}
		m[key] = v
	}
	return v
}

// IsPassed reports status == "passed".
func (s *Store) IsPassed(number int) bool {
	item := s.Stage(number)
	run, _ := item["last_run"].(map[string]any)
	return item["status"] == "passed" && fmt.Sprint(run["verification_revision"]) == fmt.Sprint(verificationRevision(number))
}

// RecordRun appends history and updates status.
func (s *Store) RecordRun(number int, payload map[string]any) error {
	payload["verification_revision"] = verificationRevision(number)
	item := s.Stage(number)
	item["last_run"] = payload
	hist, _ := item["history"].([]any)
	item["history"] = append(hist, payload)
	if payload["passed"] == true {
		item["status"] = "passed"
	} else {
		item["status"] = "failed"
	}
	item["updated_at"] = UTCNow()
	return s.Save()
}

func (s *Store) RecordReview(number int, review map[string]any) error {
	item := s.Stage(number)
	item["review"] = review
	delete(item, "manual_approval")
	if item["status"] == "passed" {
		item["status"] = "ready"
	}
	item["updated_at"] = UTCNow()
	return s.Save()
}

// AddEvidence appends an evidence record.
func (s *Store) AddEvidence(number int, ev map[string]any) error {
	item := s.Stage(number)
	list, _ := item["evidence"].([]any)
	item["evidence"] = append(list, ev)
	item["updated_at"] = UTCNow()
	return s.Save()
}

// Approve marks manual review approved.
func (s *Store) Approve(number int, note string) error {
	item := s.Stage(number)
	item["manual_approval"] = map[string]any{"approved": true, "note": note, "at": UTCNow()}
	item["updated_at"] = UTCNow()
	return s.Save()
}

// EvidenceCount counts evidence entries.
func (s *Store) EvidenceCount(number int) int {
	list, _ := s.Stage(number)["evidence"].([]any)
	return len(list)
}

// Approved reports manual approval.
func (s *Store) Approved(number int) bool {
	m, _ := s.Stage(number)["manual_approval"].(map[string]any)
	return m["approved"] == true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
