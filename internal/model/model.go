package model

// Stage is a single challenge stage parsed from the guide + policies.
type Stage struct {
	Number          int
	Title           string
	Module          string
	Objective       string
	Contract        string
	AcceptanceTests []string
	Study           []string
	Questions       []string
	Mode            string // auto | hybrid | manual
	Checks          []string
	MinimumEvidence int
}

// CheckResult is the outcome of one check function.
type CheckResult struct {
	Name            string  `json:"name"`
	Passed          bool    `json:"passed"`
	Detail          string  `json:"detail"`
	DurationSeconds float64 `json:"duration_seconds"`
	Stdout          string  `json:"stdout"`
	Stderr          string  `json:"stderr"`
}
