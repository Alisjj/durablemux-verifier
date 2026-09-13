package guide

import (
	_ "embed"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/Alisjj/durablemux-verifier/internal/model"
)

//go:embed resources/durablemux-codecrafters-guide.md
var embeddedGuide string

//go:embed resources/stage_policies.json
var embeddedPolicies string

var (
	stageRE   = regexp.MustCompile(`^## Stage (\d+): (.+)$`)
	extRE     = regexp.MustCompile(`^## Extension (\d+): (.+)$`)
	moduleRE  = regexp.MustCompile(`^# Module ([A-Z]) — (.+)$`)
	sectionRE = regexp.MustCompile(`^(### (Objective|Contract|Acceptance tests|Study before implementation|Questions to answer))$`)
	numListRE = regexp.MustCompile(`^\d+\.\s+`)
)

func cleanList(lines []string) []string {
	var out []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- ") {
			out = append(out, strings.TrimSpace(t[2:]))
		} else if numListRE.MatchString(t) {
			out = append(out, numListRE.ReplaceAllString(t, ""))
		}
	}
	return out
}

func cleanText(lines []string) string {
	var kept []string
	inFence := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			kept = append(kept, strings.TrimRight(line, " \t\r\n"))
		} else if t := strings.TrimSpace(line); t != "" && t != "---" {
			kept = append(kept, t)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// Parse builds stages from guide markdown + policies JSON text.
func Parse(guideText, policiesText string) (map[int]*model.Stage, error) {
	var policies map[string]map[string]any
	if err := json.Unmarshal([]byte(policiesText), &policies); err != nil {
		return nil, err
	}
	lines := strings.Split(guideText, "\n")
	stages := map[int]*model.Stage{}
	currentModule := ""
	i := 0
	for i < len(lines) {
		if m := moduleRE.FindStringSubmatch(lines[i]); m != nil {
			currentModule = "Module " + m[1] + " — " + m[2]
			i++
			continue
		}
		m := stageRE.FindStringSubmatch(lines[i])
		if m == nil {
			i++
			continue
		}
		var number int
		for _, c := range m[1] {
			number = number*10 + int(c-'0')
		}
		title := m[2]
		i++
		sections := map[string][]string{}
		currentSection := ""
		for i < len(lines) && stageRE.FindString(lines[i]) == "" && extRE.FindString(lines[i]) == "" && moduleRE.FindString(lines[i]) == "" {
			if sm := sectionRE.FindStringSubmatch(lines[i]); sm != nil {
				currentSection = sm[2]
				sections[currentSection] = []string{}
				i++
				continue
			}
			if currentSection != "" {
				sections[currentSection] = append(sections[currentSection], lines[i])
			}
			i++
		}
		pol := policies[itoa(number)]
		mode := strField(pol, "mode", "manual")
		var checks []string
		if v, ok := pol["checks"]; ok {
			if arr, ok := v.([]any); ok {
				for _, e := range arr {
					if s, ok := e.(string); ok {
						checks = append(checks, s)
					}
				}
			}
		}
		minEv := 0
		if v, ok := pol["minimum_evidence"]; ok {
			if f, ok := v.(float64); ok {
				minEv = int(f)
			}
		}
		stages[number] = &model.Stage{
			Number:          number,
			Title:           title,
			Module:          currentModule,
			Objective:       cleanText(sections["Objective"]),
			Contract:        cleanText(sections["Contract"]),
			AcceptanceTests: cleanList(sections["Acceptance tests"]),
			Study:           cleanList(sections["Study before implementation"]),
			Questions:       cleanList(sections["Questions to answer"]),
			Mode:            mode,
			Checks:          checks,
			MinimumEvidence: minEv,
		}
	}
	return stages, nil
}

func strField(m map[string]any, key, def string) string {
	if m == nil {
		return def
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return def
}

// SortedKeys returns stage numbers ascending.
func SortedKeys(stages map[int]*model.Stage) []int {
	keys := make([]int, 0, len(stages))
	for k := range stages {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

// LoadEmbedded parses the embedded guide + policies.
func LoadEmbedded() (map[int]*model.Stage, error) {
	return Parse(embeddedGuide, embeddedPolicies)
}

// LoadFiles parses guide/policies from disk (parity with Python CLI).
func LoadFiles(guidePath, policiesPath string) (map[int]*model.Stage, error) {
	g, err := os.ReadFile(guidePath)
	if err != nil {
		return nil, err
	}
	p, err := os.ReadFile(policiesPath)
	if err != nil {
		return nil, err
	}
	return Parse(string(g), string(p))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
