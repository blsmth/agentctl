package cmd

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/blsmth/agentctl/internal/llmusage"
)

// Background LLM calls (the usage ledger, internal/llmusage) are reported in
// their own section and never mixed into session totals.

// llmTotals sums ledger records. Calls with no known price count in Unpriced
// and add nothing to Cost.
type llmTotals struct {
	Calls      int
	Failed     int
	Prompt     int
	Completion int
	Cached     int
	Cost       float64
	Unpriced   int
}

func (t *llmTotals) add(r llmusage.Record) {
	t.Calls++
	if !r.OK {
		t.Failed++
	}
	t.Prompt += r.PromptTokens
	t.Completion += r.CompletionTokens
	t.Cached += r.CachedTokens
	if r.CostUSD != nil {
		t.Cost += *r.CostUSD
	} else {
		t.Unpriced++
	}
}

type llmGroup struct {
	Key string
	llmTotals
}

// groupLLM aggregates records by model, day or project. Any other by (agent,
// or none) yields no groups: the section then shows the total only.
func groupLLM(recs []llmusage.Record, by string) []llmGroup {
	if by != "model" && by != "day" && by != "project" {
		return nil
	}
	idx := map[string]*llmGroup{}
	for _, r := range recs {
		var k string
		switch by {
		case "model":
			k = r.Model
		case "project":
			k = r.Project
		case "day":
			k = r.TS.In(time.Local).Format("2006-01-02")
		}
		if k == "" {
			k = "(none)"
		}
		g, ok := idx[k]
		if !ok {
			g = &llmGroup{Key: k}
			idx[k] = g
		}
		g.add(r)
	}
	out := make([]llmGroup, 0, len(idx))
	for _, g := range idx {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if by == "day" {
			return out[i].Key < out[j].Key
		}
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func sumLLM(recs []llmusage.Record) llmTotals {
	var t llmTotals
	for _, r := range recs {
		t.add(r)
	}
	return t
}

// loadLLMUsage reads the ledger for the last since. A read failure is an empty
// section, not an error: the ledger is advisory.
var loadLLMUsage = func(since time.Duration, now time.Time) []llmusage.Record {
	recs, err := llmusage.Load(now.Add(-since))
	if err != nil {
		return nil
	}
	return recs
}

func llmCostCell(t llmTotals) string {
	s := fmt.Sprintf("$%.4f", t.Cost)
	if t.Unpriced > 0 {
		s += "+?"
	}
	return s
}

func printLLMSection(recs []llmusage.Record, by string) {
	if len(recs) == 0 {
		return
	}
	totals := sumLLM(recs)
	fmt.Println()
	fmt.Println(cliAccent().Render("Background LLM calls"))
	headers := []string{"CALLS", "FAILED", "PROMPT", "CACHED", "OUTPUT", "COST"}
	row := func(t llmTotals) []string {
		return []string{strconv.Itoa(t.Calls), excludedCell(t.Failed), fmtCount(t.Prompt), fmtCount(t.Cached),
			fmtCount(t.Completion), llmCostCell(t)}
	}
	if groups := groupLLM(recs, by); groups != nil {
		t := costTable(append([]string{upper(by)}, headers...)...)
		for _, g := range groups {
			t.Row(append([]string{g.Key}, row(g.llmTotals)...)...)
		}
		t.Row(append([]string{"TOTAL"}, row(totals)...)...)
		fmt.Println(t.Render())
	} else {
		t := costTable(headers...)
		t.Row(row(totals)...)
		fmt.Println(t.Render())
	}
	if totals.Unpriced > 0 {
		fmt.Println(cliMuted().Render(fmt.Sprintf("%d call(s) have no known price (+?) and are not in the cost", totals.Unpriced)))
	}
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// ─── JSON ─────────────────────────────────────────────────────────────────────

type llmTotalsJSON struct {
	Calls            int     `json:"calls"`
	Failed           int     `json:"failed"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	Unpriced         int     `json:"unpriced_calls"`
}

type llmGroupJSON struct {
	Key string `json:"key"`
	llmTotalsJSON
}

type llmSectionJSON struct {
	By     string         `json:"by,omitempty"`
	Groups []llmGroupJSON `json:"groups,omitempty"`
	Totals llmTotalsJSON  `json:"totals"`
}

func (t llmTotals) json() llmTotalsJSON {
	return llmTotalsJSON{Calls: t.Calls, Failed: t.Failed, PromptTokens: t.Prompt, CompletionTokens: t.Completion,
		CachedTokens: t.Cached, CostUSD: t.Cost, Unpriced: t.Unpriced}
}

func llmSection(recs []llmusage.Record, by string) llmSectionJSON {
	out := llmSectionJSON{Totals: sumLLM(recs).json()}
	if groups := groupLLM(recs, by); groups != nil {
		out.By = by
		out.Groups = []llmGroupJSON{}
		for _, g := range groups {
			out.Groups = append(out.Groups, llmGroupJSON{Key: g.Key, llmTotalsJSON: g.llmTotals.json()})
		}
	}
	return out
}
