package metrics

import (
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/tokens"
	"github.com/prometheus/client_golang/prometheus"
)

// CostEstimator implements logstream.Subscriber and accumulates cost estimates
// using the shared model registry (internal/tokens) for pricing.
type CostEstimator struct {
	agent         string
	costCounter   *prometheus.CounterVec // agentctl_cost_estimate_dollars{agent, model}
	unknownLogged map[string]bool        // tracks models for which we've already logged a warning

	// Exported for direct reads (same pattern as UsageAccumulator).
	TotalCost float64
	ByModel   map[string]float64
}

// NewCostEstimator creates a CostEstimator for the given agent using a
// pre-registered CounterVec. The counter must already be registered on the
// desired Prometheus registry; callers are responsible for creating and
// registering it exactly once (e.g. via Collector.NewCostEstimator).
func NewCostEstimator(agent string, counter *prometheus.CounterVec) *CostEstimator {
	return &CostEstimator{
		agent:         agent,
		costCounter:   counter,
		unknownLogged: make(map[string]bool),
		ByModel:       make(map[string]float64),
	}
}

// OnEntry inspects a logstream entry and accumulates cost when it is an
// assistant entry with usage data and a model the registry has pricing for.
func (c *CostEstimator) OnEntry(entry logstream.Entry) {
	if entry.Type != "assistant" || entry.Usage == nil {
		return
	}
	if entry.Model == "" || entry.Model == "<synthetic>" {
		return
	}

	m := tokens.Lookup(entry.Model)
	if m.Pricing == (tokens.Pricing{}) {
		// Only log when there's truly nothing to go on — family fallback
		// (m.Family != FamilyUnknown) now supplies pricing, so models like
		// claude-opus-5 no longer get silently dropped here.
		if !m.Known && m.Family == tokens.FamilyUnknown {
			if !c.unknownLogged[entry.Model] {
				logging.Log("cost_estimator: unknown model %q — skipping cost calculation", entry.Model)
				c.unknownLogged[entry.Model] = true
			}
		}
		return
	}

	u := entry.Usage
	p := m.Pricing
	cost := (float64(u.InputTokens)*p.InputPerMTok +
		float64(u.OutputTokens)*p.OutputPerMTok +
		float64(u.CacheReadTokens)*p.CacheReadPerMTok +
		float64(u.CacheCreateTokens)*p.CacheCreatePerMTok) / 1_000_000

	c.TotalCost += cost
	c.ByModel[m.Key] += cost
	c.costCounter.WithLabelValues(c.agent, m.Key).Add(cost)
}

// OnReset zeroes accumulated cost state. Called when the stream switches to a new
// log file (e.g. after /clear).
func (c *CostEstimator) OnReset() {
	c.TotalCost = 0
	c.ByModel = make(map[string]float64)
}
