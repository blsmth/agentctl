package sessionaudit

import "sort"

// RuleCount is how often one rule fired across sessions.
type RuleCount struct {
	Rule     string   `json:"rule"`
	Title    string   `json:"title"`
	Category Category `json:"category"`
	Severity Severity `json:"severity"`
	Count    int      `json:"count"`
	Sessions int      `json:"sessions"`
}

// Aggregate summarizes many reports.
type Aggregate struct {
	Sessions int         `json:"sessions"`
	Skipped  int         `json:"skipped"` // transcripts that could not be read
	Rules    []RuleCount `json:"rules"`   // most frequent first
	// Narrative is the LLM verdict over the run (`--narrate`); absent otherwise.
	Narrative string `json:"narrative,omitempty"`
}

// Multi is the payload of `audit report --all --json`.
type Multi struct {
	Schema    int       `json:"schema"`
	Reports   []*Report `json:"reports"`
	Aggregate Aggregate `json:"aggregate"`
}

// Summarize counts rule hits across reports. skipped is passed through.
func Summarize(reports []*Report, skipped int) Aggregate {
	agg := Aggregate{Sessions: len(reports), Skipped: skipped, Rules: []RuleCount{}}
	byRule := map[string]*RuleCount{}
	for _, r := range reports {
		hitHere := map[string]bool{}
		for _, f := range r.Findings {
			rc := byRule[f.Rule]
			if rc == nil {
				rc = &RuleCount{Rule: f.Rule, Title: f.Title, Category: f.Category, Severity: f.Severity}
				byRule[f.Rule] = rc
			}
			rc.Count++
			if !hitHere[f.Rule] {
				hitHere[f.Rule] = true
				rc.Sessions++
			}
		}
	}
	for _, rc := range byRule {
		agg.Rules = append(agg.Rules, *rc)
	}
	sort.Slice(agg.Rules, func(i, j int) bool {
		a, b := agg.Rules[i], agg.Rules[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() > b.Severity.Rank()
		}
		return a.Rule < b.Rule
	})
	return agg
}
