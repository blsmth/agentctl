package sessions

import (
	"fmt"
	"sort"

	"github.com/blsmth/agentctl/internal/tokens"
)

// TurnCost computes the estimated USD cost of a single request. ok is false
// when the model has no known price; the returned cost is then 0 and must be
// shown as unknown, never as a default model's price.
func TurnCost(tm TurnMetric, model string) (usd float64, ok bool) {
	m := tokens.Lookup(model)
	if !m.HasPricing() {
		return 0, false
	}
	return m.Pricing.Cost(turnUsage(tm)), true
}

func turnUsage(tm TurnMetric) tokens.Usage {
	return tokens.Usage{Input: tm.InputTokens, Output: tm.OutputTokens, CacheRead: tm.CacheRead, CacheCreate: tm.CacheCreate}
}

// ModelCost is one model's share of a session.
type ModelCost struct {
	Model string
	Turns int
	Usage tokens.Usage
	USD   float64
	Known bool // false: the model has no price; USD is 0 and excluded
}

// SessionCost is the priced result for one session.
type SessionCost struct {
	USD     float64 // sum over priced models (API-equivalent for sub, real for metered)
	Known   bool    // false when any model had no price: USD is then partial and must not be totalled
	Billing Billing
	ByModel []ModelCost // sorted by USD descending
	Usage   tokens.Usage
}

// CostOfSummary prices a session. The total is summed PER REQUEST (tiered
// models such as Haiku 5.5 bill each request by its own prompt size, so
// aggregate token totals cannot be priced correctly); aggregates are used only
// when the summary carries no per-turn data. A free billing class costs $0
// whatever the model. A nil summary is unknown.
func CostOfSummary(sum *Summary, billing Billing) SessionCost {
	if sum == nil {
		return SessionCost{Billing: billing}
	}
	type acc struct {
		mc ModelCost
	}
	byModel := map[string]*acc{}
	var order []string
	add := func(model string, u tokens.Usage) {
		a, ok := byModel[model]
		if !ok {
			m := tokens.Lookup(model)
			a = &acc{mc: ModelCost{Model: model, Known: m.HasPricing() || billing == BillingFree}}
			byModel[model] = a
			order = append(order, model)
		}
		a.mc.Turns++
		a.mc.Usage.Input += u.Input
		a.mc.Usage.Output += u.Output
		a.mc.Usage.CacheRead += u.CacheRead
		a.mc.Usage.CacheCreate += u.CacheCreate
		if billing != BillingFree && a.mc.Known {
			a.mc.USD += tokens.Lookup(model).Pricing.Cost(u)
		}
	}

	if len(sum.TurnMetrics) > 0 {
		for _, tm := range sum.TurnMetrics {
			model := tm.Model
			if model == "" {
				model = sum.ModelName
			}
			add(model, turnUsage(tm))
		}
	} else if sum.TotalInputTokens+sum.TotalOutputTokens+sum.CacheReadTokens+sum.CacheCreateTokens > 0 {
		add(sum.ModelName, tokens.Usage{
			Input: sum.TotalInputTokens, Output: sum.TotalOutputTokens,
			CacheRead: sum.CacheReadTokens, CacheCreate: sum.CacheCreateTokens,
		})
	}

	res := SessionCost{Billing: billing, Known: true}
	for _, model := range order {
		mc := byModel[model].mc
		res.ByModel = append(res.ByModel, mc)
		res.USD += mc.USD
		res.Usage.Input += mc.Usage.Input
		res.Usage.Output += mc.Usage.Output
		res.Usage.CacheRead += mc.Usage.CacheRead
		res.Usage.CacheCreate += mc.Usage.CacheCreate
		if !mc.Known {
			res.Known = false
		}
	}
	sort.SliceStable(res.ByModel, func(i, j int) bool { return res.ByModel[i].USD > res.ByModel[j].USD })
	if len(res.ByModel) == 0 {
		// No usage at all: nothing to price, which is a known $0 only if the
		// session names no unpriced model.
		res.Known = billing == BillingFree || tokens.Lookup(sum.ModelName).HasPricing() || sum.ModelName == ""
	}
	return res
}

// SampleTurnMetrics returns a representative sample of turn metrics capped at
// maxRows rows. For short sessions (<=maxRows turns) every turn is included.
// For longer sessions, turns are sampled evenly such that the result has at
// most maxRows entries, always including the first and last turn.
func SampleTurnMetrics(metrics []TurnMetric, maxRows int) []TurnMetric {
	if len(metrics) == 0 {
		return nil
	}
	if maxRows <= 0 {
		maxRows = 15
	}
	if len(metrics) <= maxRows {
		return metrics
	}

	result := make([]TurnMetric, 0, maxRows)
	// Always include first and last; sample the rest evenly.
	step := float64(len(metrics)-1) / float64(maxRows-1)
	seen := make(map[int]bool)
	for i := 0; i < maxRows; i++ {
		idx := int(float64(i)*step + 0.5)
		if idx >= len(metrics) {
			idx = len(metrics) - 1
		}
		if !seen[idx] {
			seen[idx] = true
			result = append(result, metrics[idx])
		}
	}
	return result
}

// EstimateCostFromSummary computes estimated USD cost from a parsed summary,
// per request, using the model alone to decide billing (no backend knowledge:
// use CostOfSummary with a Billing for that). ok is false when the model has no
// price.
func EstimateCostFromSummary(sum *Summary) (usd float64, ok bool) {
	if sum == nil {
		return 0, false
	}
	c := CostOfSummary(sum, ClassifyBilling(sum.ModelName, "", nil))
	return c.USD, c.Known
}

// FormatCostOK is FormatCost for a possibly-unknown cost: "?" when !ok.
func FormatCostOK(usd float64, ok bool) string {
	if !ok {
		return "?"
	}
	return FormatCost(usd)
}

// FormatCost returns a display string like "$2.41" or "$0.00".
func FormatCost(usd float64) string {
	return fmt.Sprintf("$%.2f", usd)
}
