package sessions

import "fmt"

// TurnCost computes the estimated USD cost for a single TurnMetric.
// Uses the same pricing table as EstimateCost. Unknown models default to Sonnet pricing.
func TurnCost(tm TurnMetric, model string) float64 {
	p := pricingForModel(model)
	return (float64(tm.InputTokens)*p.Input +
		float64(tm.OutputTokens)*p.Output +
		float64(tm.CacheCreate)*p.CacheWrite +
		float64(tm.CacheRead)*p.CacheRead) / 1_000_000
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

// EstimateCostFromSummary computes estimated USD cost from a parsed summary.
// Uses the same pricing table as EstimateCost (in meta.go). Unknown models
// default to Sonnet pricing.
func EstimateCostFromSummary(sum *Summary) float64 {
	if sum == nil {
		return 0
	}
	p := pricingForModel(sum.ModelName)

	// TotalInputTokens is raw (uncached) input only; cache tokens are separate.
	return (float64(sum.TotalInputTokens)*p.Input +
		float64(sum.TotalOutputTokens)*p.Output +
		float64(sum.CacheCreateTokens)*p.CacheWrite +
		float64(sum.CacheReadTokens)*p.CacheRead) / 1_000_000
}

// FormatCost returns a display string like "$2.41" or "$0.00".
func FormatCost(usd float64) string {
	return fmt.Sprintf("$%.2f", usd)
}
