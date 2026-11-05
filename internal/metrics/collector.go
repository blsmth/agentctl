// Package metrics provides Prometheus metrics collection for agent activity
// observed from logstream entries.
package metrics

import (
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/prometheus/client_golang/prometheus"
)

// Collector holds Prometheus metrics updated per-entry from logstream subscribers.
type Collector struct {
	tokensTotal  *prometheus.CounterVec // labels: agent, direction (input|output), cache_type (none|read|create)
	toolUseTotal *prometheus.CounterVec // labels: agent, tool
	turnsTotal   *prometheus.CounterVec // labels: agent, model
	contextPct   *prometheus.GaugeVec   // labels: agent
	costEstimate *prometheus.CounterVec // labels: agent, model — shared across all CostEstimator instances
}

// NewCollector registers all metrics on reg and returns a ready-to-use Collector.
func NewCollector(reg prometheus.Registerer) *Collector {
	c := &Collector{}

	c.tokensTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agentctl_tokens_total",
		Help: "Total tokens processed, partitioned by agent, direction, and cache type.",
	}, []string{"agent", "direction", "cache_type"})

	c.toolUseTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agentctl_tool_use_total",
		Help: "Total tool invocations observed in logstream entries.",
	}, []string{"agent", "tool"})

	c.turnsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agentctl_turns_total",
		Help: "Total assistant turns observed, partitioned by agent and model.",
	}, []string{"agent", "model"})

	c.contextPct = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "agentctl_context_pct",
		Help: "Current context window usage percentage (0–100) per agent.",
	}, []string{"agent"})

	c.costEstimate = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agentctl_cost_estimate_dollars",
		Help: "Estimated cost in USD accumulated from assistant turns, partitioned by agent and model.",
	}, []string{"agent", "model"})

	reg.MustRegister(
		c.tokensTotal,
		c.toolUseTotal,
		c.turnsTotal,
		c.contextPct,
		c.costEstimate,
	)

	return c
}

// NewCostEstimator returns a CostEstimator for the given agent that shares the
// Collector's pre-registered cost counter. Calling this multiple times for
// different agents is safe — they all write to distinct label combinations on
// the same CounterVec and never re-register anything. Pricing comes from the
// shared internal/tokens model registry, not a caller-supplied table.
func (c *Collector) NewCostEstimator(agent string) *CostEstimator {
	return NewCostEstimator(agent, c.costEstimate)
}

// ForAgent returns an AgentCollector scoped to the given agent name.
func (c *Collector) ForAgent(agent string) *AgentCollector {
	return &AgentCollector{agent: agent, collector: c}
}

// AgentCollector wraps Collector with a bound agent name and implements
// logstream.Subscriber. One AgentCollector is created per agent window.
type AgentCollector struct {
	agent     string
	collector *Collector
}

// OnEntry inspects a logstream entry and updates Prometheus counters.
//
// For assistant entries with usage data:
//   - Increments tokensTotal{direction=input, cache_type=none} by InputTokens
//   - Increments tokensTotal{direction=output, cache_type=none} by OutputTokens
//   - Increments tokensTotal{direction=input, cache_type=read} by CacheReadTokens
//   - Increments tokensTotal{direction=input, cache_type=create} by CacheCreateTokens
//   - Increments turnsTotal{model=entry.Model}
//
// For all content blocks with type "tool_use":
//   - Increments toolUseTotal{tool=block.ToolName}
func (a *AgentCollector) OnEntry(entry logstream.Entry) {
	c := a.collector

	if entry.Type == "assistant" && entry.Usage != nil {
		if entry.Usage.InputTokens > 0 {
			c.tokensTotal.WithLabelValues(a.agent, "input", "none").Add(float64(entry.Usage.InputTokens))
		}
		if entry.Usage.OutputTokens > 0 {
			c.tokensTotal.WithLabelValues(a.agent, "output", "none").Add(float64(entry.Usage.OutputTokens))
		}
		if entry.Usage.CacheReadTokens > 0 {
			c.tokensTotal.WithLabelValues(a.agent, "input", "read").Add(float64(entry.Usage.CacheReadTokens))
		}
		if entry.Usage.CacheCreateTokens > 0 {
			c.tokensTotal.WithLabelValues(a.agent, "input", "create").Add(float64(entry.Usage.CacheCreateTokens))
		}
		model := entry.Model
		if model == "" {
			model = "unknown"
		}
		c.turnsTotal.WithLabelValues(a.agent, model).Inc()
	}

	for _, block := range entry.Content {
		if block.Type == "tool_use" && block.ToolName != "" {
			c.toolUseTotal.WithLabelValues(a.agent, block.ToolName).Inc()
		}
	}
}

// OnReset is called when the underlying stream switches to a new log file
// (e.g. after /clear). Counters are cumulative and not reset. The contextPct
// gauge is reset to 0 since the session context is now fresh.
func (a *AgentCollector) OnReset() {
	a.collector.contextPct.WithLabelValues(a.agent).Set(0)
}
