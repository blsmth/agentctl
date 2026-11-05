package metrics

import (
	"testing"

	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// newTestCollector returns a Collector registered on an isolated registry.
func newTestCollector(t *testing.T) *Collector {
	t.Helper()
	reg := prometheus.NewRegistry()
	return NewCollector(reg)
}

// counterValue reads the current value of a counter with the given label values.
func counterValue(t *testing.T, vec *prometheus.CounterVec, lvs ...string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := vec.WithLabelValues(lvs...).Write(m); err != nil {
		t.Fatalf("read counter %v: %v", lvs, err)
	}
	return m.GetCounter().GetValue()
}

// gaugeValue reads the current value of a gauge with the given label values.
func gaugeValue(t *testing.T, vec *prometheus.GaugeVec, lvs ...string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := vec.WithLabelValues(lvs...).Write(m); err != nil {
		t.Fatalf("read gauge %v: %v", lvs, err)
	}
	return m.GetGauge().GetValue()
}

func assistantEntry(usage *logstream.Usage, model string, blocks ...logstream.ContentBlock) logstream.Entry {
	return logstream.Entry{
		Type:    "assistant",
		Usage:   usage,
		Model:   model,
		Content: blocks,
	}
}

func toolBlock(name string) logstream.ContentBlock {
	return logstream.ContentBlock{Type: "tool_use", ToolName: name}
}

func TestOnEntry_AssistantWithUsage(t *testing.T) {
	c := newTestCollector(t)
	ac := c.ForAgent("go-engineer")

	entry := assistantEntry(&logstream.Usage{
		InputTokens:       100,
		OutputTokens:      50,
		CacheReadTokens:   30,
		CacheCreateTokens: 20,
	}, "claude-3-5-sonnet")

	ac.OnEntry(entry)

	tests := []struct {
		name      string
		direction string
		cacheType string
		want      float64
	}{
		{"input tokens", "input", "none", 100},
		{"output tokens", "output", "none", 50},
		{"cache read tokens", "input", "read", 30},
		{"cache create tokens", "input", "create", 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := counterValue(t, c.tokensTotal, "go-engineer", tt.direction, tt.cacheType)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("turns incremented", func(t *testing.T) {
		got := counterValue(t, c.turnsTotal, "go-engineer", "claude-3-5-sonnet")
		if got != 1 {
			t.Errorf("got %v, want 1", got)
		}
	})
}

func TestOnEntry_ToolUseBlocks(t *testing.T) {
	c := newTestCollector(t)
	ac := c.ForAgent("go-engineer")

	entry := assistantEntry(nil, "", toolBlock("Bash"), toolBlock("Edit"))
	ac.OnEntry(entry)

	t.Run("Bash counter", func(t *testing.T) {
		got := counterValue(t, c.toolUseTotal, "go-engineer", "Bash")
		if got != 1 {
			t.Errorf("got %v, want 1", got)
		}
	})
	t.Run("Edit counter", func(t *testing.T) {
		got := counterValue(t, c.toolUseTotal, "go-engineer", "Edit")
		if got != 1 {
			t.Errorf("got %v, want 1", got)
		}
	})
}

func TestOnEntry_NonAssistantEntry(t *testing.T) {
	c := newTestCollector(t)
	ac := c.ForAgent("go-engineer")

	entry := logstream.Entry{
		Type: "user",
		Usage: &logstream.Usage{
			InputTokens:  999,
			OutputTokens: 999,
		},
	}
	ac.OnEntry(entry)

	// No token or turn counters should be non-zero.
	for _, dir := range []string{"input", "output"} {
		for _, ct := range []string{"none", "read", "create"} {
			got := counterValue(t, c.tokensTotal, "go-engineer", dir, ct)
			if got != 0 {
				t.Errorf("tokensTotal{%s,%s} = %v, want 0", dir, ct, got)
			}
		}
	}
}

func TestOnEntry_NilUsageNoIncrements(t *testing.T) {
	c := newTestCollector(t)
	ac := c.ForAgent("go-engineer")

	// assistant entry with nil usage — should not panic or increment tokens/turns.
	entry := assistantEntry(nil, "claude-3-opus")
	ac.OnEntry(entry) // must not panic

	for _, dir := range []string{"input", "output"} {
		for _, ct := range []string{"none", "read", "create"} {
			got := counterValue(t, c.tokensTotal, "go-engineer", dir, ct)
			if got != 0 {
				t.Errorf("tokensTotal{%s,%s} = %v after nil usage, want 0", dir, ct, got)
			}
		}
	}
}

func TestOnEntry_MultipleEntriesAccumulate(t *testing.T) {
	c := newTestCollector(t)
	ac := c.ForAgent("go-engineer")

	for i := 0; i < 3; i++ {
		ac.OnEntry(assistantEntry(&logstream.Usage{
			InputTokens:  10,
			OutputTokens: 5,
		}, "claude-3-5-sonnet"))
	}

	got := counterValue(t, c.tokensTotal, "go-engineer", "input", "none")
	if got != 30 {
		t.Errorf("input tokens = %v, want 30", got)
	}
	got = counterValue(t, c.tokensTotal, "go-engineer", "output", "none")
	if got != 15 {
		t.Errorf("output tokens = %v, want 15", got)
	}
	got = counterValue(t, c.turnsTotal, "go-engineer", "claude-3-5-sonnet")
	if got != 3 {
		t.Errorf("turns = %v, want 3", got)
	}
}

func TestOnReset_DoesNotAffectCounters(t *testing.T) {
	c := newTestCollector(t)
	ac := c.ForAgent("go-engineer")

	ac.OnEntry(assistantEntry(&logstream.Usage{InputTokens: 100, OutputTokens: 50}, "claude-3-5-sonnet"))
	ac.OnReset()

	// Counters must be unchanged after reset.
	if got := counterValue(t, c.tokensTotal, "go-engineer", "input", "none"); got != 100 {
		t.Errorf("input tokens after reset = %v, want 100", got)
	}
	if got := counterValue(t, c.turnsTotal, "go-engineer", "claude-3-5-sonnet"); got != 1 {
		t.Errorf("turns after reset = %v, want 1", got)
	}
}

func TestOnReset_ResetsContextPct(t *testing.T) {
	c := newTestCollector(t)
	ac := c.ForAgent("go-engineer")

	c.contextPct.WithLabelValues("go-engineer").Set(75)
	if got := gaugeValue(t, c.contextPct, "go-engineer"); got != 75 {
		t.Errorf("contextPct before reset = %v, want 75", got)
	}

	ac.OnReset()

	if got := gaugeValue(t, c.contextPct, "go-engineer"); got != 0 {
		t.Errorf("contextPct after reset = %v, want 0", got)
	}
}

func TestForAgent_ScopedByAgent(t *testing.T) {
	c := newTestCollector(t)
	acA := c.ForAgent("agent-a")
	acB := c.ForAgent("agent-b")

	acA.OnEntry(assistantEntry(&logstream.Usage{InputTokens: 10}, "m"))
	acB.OnEntry(assistantEntry(&logstream.Usage{InputTokens: 20}, "m"))

	if got := counterValue(t, c.tokensTotal, "agent-a", "input", "none"); got != 10 {
		t.Errorf("agent-a input = %v, want 10", got)
	}
	if got := counterValue(t, c.tokensTotal, "agent-b", "input", "none"); got != 20 {
		t.Errorf("agent-b input = %v, want 20", got)
	}
}

func TestForAgent_ReturnsScopedCollector(t *testing.T) {
	c := newTestCollector(t)
	ac := c.ForAgent("my-agent")

	if ac.agent != "my-agent" {
		t.Errorf("agent = %q, want %q", ac.agent, "my-agent")
	}
	if ac.collector != c {
		t.Errorf("collector pointer mismatch")
	}
}
