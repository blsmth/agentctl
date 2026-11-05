package metrics

import (
	"testing"

	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// newTestCostEstimator returns a CostEstimator on an isolated registry.
// It goes through Collector so we exercise the shared-counter code path.
func newTestCostEstimator(t *testing.T) *CostEstimator {
	t.Helper()
	reg := prometheus.NewRegistry()
	c := NewCollector(reg)
	return c.NewCostEstimator("go-engineer")
}

// costCounterValue reads the current value of the costCounter for the given labels.
func costCounterValue(t *testing.T, ce *CostEstimator, model string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := ce.costCounter.WithLabelValues(ce.agent, model).Write(m); err != nil {
		t.Fatalf("read cost counter %q: %v", model, err)
	}
	return m.GetCounter().GetValue()
}

func mkUsage(input, output, cacheRead, cacheCreate int) *logstream.Usage {
	return &logstream.Usage{
		InputTokens:       input,
		OutputTokens:      output,
		CacheReadTokens:   cacheRead,
		CacheCreateTokens: cacheCreate,
	}
}

func TestCostEstimator_ExactModelMatch(t *testing.T) {
	ce := newTestCostEstimator(t)

	// claude-sonnet-4: input=3.0, output=15.0, cacheRead=0.30, cacheCreate=3.75 per MTok
	// 1_000_000 of each token type → $3 + $15 + $0.30 + $3.75 = $22.05
	ce.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "claude-sonnet-4",
		Usage: mkUsage(1_000_000, 1_000_000, 1_000_000, 1_000_000),
	})

	const want = 22.05
	if ce.TotalCost != want {
		t.Errorf("TotalCost = %v, want %v", ce.TotalCost, want)
	}
	if ce.ByModel["claude-sonnet-4"] != want {
		t.Errorf("ByModel[claude-sonnet-4] = %v, want %v", ce.ByModel["claude-sonnet-4"], want)
	}
	if got := costCounterValue(t, ce, "claude-sonnet-4"); got != want {
		t.Errorf("counter = %v, want %v", got, want)
	}
}

func TestCostEstimator_PrefixMatch(t *testing.T) {
	ce := newTestCostEstimator(t)

	// "claude-sonnet-4-20250514" should match "claude-sonnet-4" prefix.
	// 1_000_000 input only: $3.0
	ce.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "claude-sonnet-4-20250514",
		Usage: mkUsage(1_000_000, 0, 0, 0),
	})

	const want = 3.0
	if ce.TotalCost != want {
		t.Errorf("TotalCost = %v, want %v", ce.TotalCost, want)
	}
	// ByModel key should be the matched prefix, not the full versioned name.
	if ce.ByModel["claude-sonnet-4"] != want {
		t.Errorf("ByModel[claude-sonnet-4] = %v, want %v", ce.ByModel["claude-sonnet-4"], want)
	}
}

// TestCostEstimator_FamilyFallbackAccrues verifies that a model unknown to
// the built-in table (e.g. claude-opus-9, which doesn't exist yet) now
// accrues cost via the tokens package's opus family fallback, instead of
// being silently dropped.
func TestCostEstimator_FamilyFallbackAccrues(t *testing.T) {
	ce := newTestCostEstimator(t)

	// opus family default (newest generation): input=5.0 per MTok.
	ce.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "claude-opus-9",
		Usage: mkUsage(1_000_000, 0, 0, 0),
	})

	const want = 5.0
	if ce.TotalCost != want {
		t.Errorf("TotalCost = %v, want %v (opus family fallback pricing)", ce.TotalCost, want)
	}
	// Family-fallback entries are grouped under the family key ("claude-opus"),
	// not the raw versioned model string — matches today's cardinality
	// behavior for prefix-matched entries.
	if ce.ByModel["claude-opus"] != want {
		t.Errorf("ByModel[claude-opus] = %v, want %v", ce.ByModel["claude-opus"], want)
	}
	if got := costCounterValue(t, ce, "claude-opus"); got != want {
		t.Errorf("counter = %v, want %v", got, want)
	}
}

func TestCostEstimator_UnknownModel_NoPanic_NoCost(t *testing.T) {
	ce := newTestCostEstimator(t)

	// Totally unrecognized model (no built-in match, no family substring):
	// should not panic, should not accumulate cost.
	ce.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "gpt-4",
		Usage: mkUsage(1_000_000, 1_000_000, 0, 0),
	})

	if ce.TotalCost != 0 {
		t.Errorf("TotalCost = %v, want 0 for unknown model", ce.TotalCost)
	}
	if len(ce.ByModel) != 0 {
		t.Errorf("ByModel should be empty for unknown model, got %v", ce.ByModel)
	}
}

func TestCostEstimator_UnknownModel_LoggedOnce(t *testing.T) {
	ce := newTestCostEstimator(t)

	// Call OnEntry multiple times with the same unknown model.
	for i := 0; i < 5; i++ {
		ce.OnEntry(logstream.Entry{
			Type:  "assistant",
			Model: "gpt-4",
			Usage: mkUsage(100, 50, 0, 0),
		})
	}

	// Should be logged exactly once (unknownLogged tracks it).
	if !ce.unknownLogged["gpt-4"] {
		t.Error("expected unknownLogged[gpt-4] to be true")
	}
	if ce.TotalCost != 0 {
		t.Errorf("TotalCost = %v after unknown model entries, want 0", ce.TotalCost)
	}
}

func TestCostEstimator_OnReset(t *testing.T) {
	ce := newTestCostEstimator(t)

	ce.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "claude-sonnet-4",
		Usage: mkUsage(1_000_000, 0, 0, 0),
	})
	if ce.TotalCost == 0 {
		t.Fatal("expected non-zero TotalCost before reset")
	}

	ce.OnReset()

	if ce.TotalCost != 0 {
		t.Errorf("TotalCost after reset = %v, want 0", ce.TotalCost)
	}
	if len(ce.ByModel) != 0 {
		t.Errorf("ByModel after reset = %v, want empty map", ce.ByModel)
	}
}

func TestCostEstimator_OnReset_CounterNotReset(t *testing.T) {
	ce := newTestCostEstimator(t)

	ce.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "claude-sonnet-4",
		Usage: mkUsage(1_000_000, 0, 0, 0),
	})
	preReset := costCounterValue(t, ce, "claude-sonnet-4")
	ce.OnReset()

	// Prometheus counter must not decrease after reset.
	if got := costCounterValue(t, ce, "claude-sonnet-4"); got != preReset {
		t.Errorf("counter after OnReset = %v, want %v (counters must not decrease)", got, preReset)
	}
}

func TestCostEstimator_MultipleEntriesAccumulate(t *testing.T) {
	ce := newTestCostEstimator(t)

	// 3 turns × 1_000_000 input tokens at $3/MTok = $9
	for i := 0; i < 3; i++ {
		ce.OnEntry(logstream.Entry{
			Type:  "assistant",
			Model: "claude-sonnet-4",
			Usage: mkUsage(1_000_000, 0, 0, 0),
		})
	}

	const want = 9.0
	if ce.TotalCost != want {
		t.Errorf("TotalCost = %v, want %v", ce.TotalCost, want)
	}
	if ce.ByModel["claude-sonnet-4"] != want {
		t.Errorf("ByModel[claude-sonnet-4] = %v, want %v", ce.ByModel["claude-sonnet-4"], want)
	}
	if got := costCounterValue(t, ce, "claude-sonnet-4"); got != want {
		t.Errorf("counter = %v, want %v", got, want)
	}
}

func TestCostEstimator_SkipsNonAssistantEntries(t *testing.T) {
	ce := newTestCostEstimator(t)

	ce.OnEntry(logstream.Entry{
		Type:  "user",
		Model: "claude-sonnet-4",
		Usage: mkUsage(1_000_000, 1_000_000, 0, 0),
	})

	if ce.TotalCost != 0 {
		t.Errorf("TotalCost = %v after user entry, want 0", ce.TotalCost)
	}
}

func TestCostEstimator_SkipsNilUsage(t *testing.T) {
	ce := newTestCostEstimator(t)

	ce.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "claude-sonnet-4",
		Usage: nil,
	})

	if ce.TotalCost != 0 {
		t.Errorf("TotalCost = %v after nil usage, want 0", ce.TotalCost)
	}
}

func TestCostEstimator_SkipsEmptyModel(t *testing.T) {
	ce := newTestCostEstimator(t)

	ce.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "",
		Usage: mkUsage(1_000_000, 0, 0, 0),
	})

	if ce.TotalCost != 0 {
		t.Errorf("TotalCost = %v after empty model, want 0", ce.TotalCost)
	}
}

func TestCostEstimator_SkipsSyntheticModel(t *testing.T) {
	ce := newTestCostEstimator(t)

	ce.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "<synthetic>",
		Usage: mkUsage(1_000_000, 0, 0, 0),
	})

	if ce.TotalCost != 0 {
		t.Errorf("TotalCost = %v after synthetic model, want 0", ce.TotalCost)
	}
}

func TestCostEstimator_MultipleModels(t *testing.T) {
	ce := newTestCostEstimator(t)

	// claude-sonnet-4: 1M input = $3
	ce.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "claude-sonnet-4",
		Usage: mkUsage(1_000_000, 0, 0, 0),
	})
	// claude-opus-4: 1M input = $15
	ce.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "claude-opus-4",
		Usage: mkUsage(1_000_000, 0, 0, 0),
	})

	const wantTotal = 18.0
	if ce.TotalCost != wantTotal {
		t.Errorf("TotalCost = %v, want %v", ce.TotalCost, wantTotal)
	}
	if ce.ByModel["claude-sonnet-4"] != 3.0 {
		t.Errorf("ByModel[claude-sonnet-4] = %v, want 3.0", ce.ByModel["claude-sonnet-4"])
	}
	if ce.ByModel["claude-opus-4"] != 15.0 {
		t.Errorf("ByModel[claude-opus-4] = %v, want 15.0", ce.ByModel["claude-opus-4"])
	}
}

func TestCostEstimator_AllTokenTypes(t *testing.T) {
	ce := newTestCostEstimator(t)

	// claude-haiku-3-5: input=0.80, output=4.0, cacheRead=0.08, cacheCreate=1.0 per MTok
	// 500k of each: (0.5×0.80) + (0.5×4.0) + (0.5×0.08) + (0.5×1.0) = 0.40 + 2.0 + 0.04 + 0.50 = 2.94
	ce.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "claude-haiku-3-5",
		Usage: mkUsage(500_000, 500_000, 500_000, 500_000),
	})

	const want = 2.94
	const epsilon = 1e-9
	diff := ce.TotalCost - want
	if diff > epsilon || diff < -epsilon {
		t.Errorf("TotalCost = %v, want %v", ce.TotalCost, want)
	}
}

// TestCostEstimator_MultiAgentSharedRegistry verifies that creating CostEstimators
// for two different agents on the same Collector (and therefore the same Prometheus
// registry) does not panic, and that each agent accumulates cost independently.
func TestCostEstimator_MultiAgentSharedRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := NewCollector(reg)

	// Creating a second CostEstimator on the same registry must not panic.
	ce1 := c.NewCostEstimator("agent-a")
	ce2 := c.NewCostEstimator("agent-b")

	// agent-a: 1M input tokens at claude-sonnet-4 = $3.00
	ce1.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "claude-sonnet-4",
		Usage: mkUsage(1_000_000, 0, 0, 0),
	})

	// agent-b: 1M output tokens at claude-sonnet-4 = $15.00
	ce2.OnEntry(logstream.Entry{
		Type:  "assistant",
		Model: "claude-sonnet-4",
		Usage: mkUsage(0, 1_000_000, 0, 0),
	})

	if ce1.TotalCost != 3.0 {
		t.Errorf("agent-a TotalCost = %v, want 3.0", ce1.TotalCost)
	}
	if ce2.TotalCost != 15.0 {
		t.Errorf("agent-b TotalCost = %v, want 15.0", ce2.TotalCost)
	}

	// Verify Prometheus counters carry the right agent labels.
	if got := costCounterValue(t, ce1, "claude-sonnet-4"); got != 3.0 {
		t.Errorf("agent-a counter = %v, want 3.0", got)
	}
	if got := costCounterValue(t, ce2, "claude-sonnet-4"); got != 15.0 {
		t.Errorf("agent-b counter = %v, want 15.0", got)
	}
}
