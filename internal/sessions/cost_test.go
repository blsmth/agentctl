package sessions

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/tokens"
)

func TestEstimateCostFromSummary_Sonnet(t *testing.T) {
	tests := []struct {
		name       string
		sum        Summary
		wantApprox float64 // approximate USD
		epsilon    float64
	}{
		{
			name:       "zero tokens",
			sum:        Summary{ModelName: "claude-sonnet-4-5"},
			wantApprox: 0.0,
			epsilon:    0.0001,
		},
		{
			name: "typical session",
			sum: Summary{
				ModelName:         "claude-sonnet-4-5",
				TotalInputTokens:  10000,
				TotalOutputTokens: 2000,
				CacheCreateTokens: 0,
				CacheReadTokens:   0,
			},
			// rawInput = 10000, output = 2000
			// cost = (10000 * 3.0 + 2000 * 15.0) / 1_000_000
			// = (30000 + 30000) / 1_000_000 = 0.06
			wantApprox: 0.06,
			epsilon:    0.0001,
		},
		{
			name: "cache-heavy session",
			sum: Summary{
				ModelName: "claude-sonnet-4-20250514",
				// TotalInputTokens is raw (uncached) input only.
				TotalInputTokens:  10000,
				TotalOutputTokens: 5000,
				CacheCreateTokens: 20000,
				CacheReadTokens:   70000,
			},
			// cost = (10000*3.0 + 5000*15.0 + 20000*3.75 + 70000*0.30) / 1_000_000
			// = (30000 + 75000 + 75000 + 21000) / 1_000_000 = 0.201
			wantApprox: 0.201,
			epsilon:    0.0001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := EstimateCostFromSummary(&tt.sum)
			if math.Abs(got-tt.wantApprox) > tt.epsilon {
				t.Errorf("EstimateCostFromSummary() = %f, want ~%f (epsilon %f)", got, tt.wantApprox, tt.epsilon)
			}
		})
	}
}

func TestEstimateCostFromSummary_Haiku(t *testing.T) {
	tests := []struct {
		name       string
		sum        Summary
		wantApprox float64
		epsilon    float64
	}{
		{
			name: "typical haiku session",
			sum: Summary{
				ModelName:         "claude-haiku-3-5",
				TotalInputTokens:  10000,
				TotalOutputTokens: 2000,
				CacheCreateTokens: 0,
				CacheReadTokens:   0,
			},
			// rawInput = 10000, output = 2000
			// Haiku 3.5: $0.80 in / $4 out
			// cost = (10000 * 0.80 + 2000 * 4.0) / 1_000_000 = 0.016
			wantApprox: 0.016,
			epsilon:    0.0001,
		},
		{
			name: "haiku with cache",
			sum: Summary{
				ModelName: "claude-haiku-3-5-20241022",
				// TotalInputTokens is raw (uncached) input only.
				TotalInputTokens:  10000,
				TotalOutputTokens: 3000,
				CacheCreateTokens: 10000,
				CacheReadTokens:   30000,
			},
			// Haiku 3.5: 0.80 / 4.0 / cache read 0.08 / cache write 1.00
			// cost = (10000*0.80 + 3000*4.0 + 10000*1.00 + 30000*0.08) / 1_000_000 = 0.0324
			wantApprox: 0.0324,
			epsilon:    0.0001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := EstimateCostFromSummary(&tt.sum)
			if math.Abs(got-tt.wantApprox) > tt.epsilon {
				t.Errorf("EstimateCostFromSummary() = %f, want ~%f (epsilon %f)", got, tt.wantApprox, tt.epsilon)
			}
		})
	}
}

func TestEstimateCostFromSummary_Opus(t *testing.T) {
	tests := []struct {
		name       string
		sum        Summary
		wantApprox float64
		epsilon    float64
	}{
		{
			name: "opus raw input",
			sum: Summary{
				ModelName:         "claude-opus-4-20250514",
				TotalInputTokens:  1_000_000,
				TotalOutputTokens: 0,
			},
			// Opus: $15/1M input
			wantApprox: 15.0,
			epsilon:    0.0001,
		},
		{
			name: "opus output",
			sum: Summary{
				ModelName:         "claude-opus-4-5",
				TotalInputTokens:  0,
				TotalOutputTokens: 1_000_000,
			},
			// Opus 4.5: $25/1M output
			wantApprox: 25.0,
			epsilon:    0.0001,
		},
		{
			name: "opus with cache",
			sum: Summary{
				ModelName:         "claude-opus-4-20250514",
				TotalInputTokens:  100_000, // raw only
				TotalOutputTokens: 50_000,
				CacheCreateTokens: 200_000,
				CacheReadTokens:   500_000,
			},
			// cost = (100k*15.0 + 50k*75.0 + 200k*18.75 + 500k*1.50) / 1_000_000
			//      = (1.50 + 3.75 + 3.75 + 0.75) = 9.75
			wantApprox: 9.75,
			epsilon:    0.0001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := EstimateCostFromSummary(&tt.sum)
			if math.Abs(got-tt.wantApprox) > tt.epsilon {
				t.Errorf("EstimateCostFromSummary() = %f, want ~%f (epsilon %f)", got, tt.wantApprox, tt.epsilon)
			}
		})
	}
}

// TestEstimateCostFromSummary_NoCacheDoubleCount verifies that cache tokens are not
// double-counted: CacheCreateTokens and CacheReadTokens should not also appear in
// TotalInputTokens (they are separate fields).
func TestEstimateCostFromSummary_NoCacheDoubleCount(t *testing.T) {
	// If we double-count by adding cache back into raw input, the cost would be higher.
	// Correct: rawInput is TotalInputTokens alone; cache billed separately.
	sum := &Summary{
		ModelName:         "claude-sonnet-4-20250514",
		TotalInputTokens:  10_000, // raw input only
		TotalOutputTokens: 0,
		CacheCreateTokens: 50_000,
		CacheReadTokens:   100_000,
	}
	// Expected: (10k*3.0 + 50k*3.75 + 100k*0.30) / 1M
	//         = (30_000 + 187_500 + 30_000) / 1_000_000 = 0.2475
	const want = 0.2475

	got, _ := EstimateCostFromSummary(sum)
	if math.Abs(got-want) > 0.0001 {
		t.Errorf("EstimateCostFromSummary() = %f, want %f — cache tokens may be double-counted", got, want)
	}

	// Double-counted value would be significantly higher; verify we're NOT there.
	// If double-counted: rawInput = 10k+50k+100k = 160k → 0.48 + 0.1875 + 0.03 = 0.6975
	doubleCountedWrong := 0.6975
	if math.Abs(got-doubleCountedWrong) < 0.01 {
		t.Errorf("EstimateCostFromSummary() = %f matches double-counted value %f — cache is being double-counted", got, doubleCountedWrong)
	}
}

func TestEstimateCostFromSummary_UnknownModel(t *testing.T) {
	// An unpriced model is explicitly unknown; it must never inherit Sonnet rates.
	for _, model := range []string{"qwen3-coder-next", "gemma4-64k:latest", ""} {
		sum := Summary{ModelName: model, TotalInputTokens: 10000, TotalOutputTokens: 2000}
		got, ok := EstimateCostFromSummary(&sum)
		if ok || got != 0 {
			t.Errorf("model %q: got (%v, %v), want (0, false)", model, got, ok)
		}
	}
	// A claude-* id we have never seen still prices via the family fallback.
	sum := Summary{ModelName: "claude-future-opus-99", TotalInputTokens: 1_000_000}
	if got, ok := EstimateCostFromSummary(&sum); !ok || math.Abs(got-4.0) > 1e-9 {
		t.Errorf("family fallback: got (%v, %v), want (4, true)", got, ok)
	}
}

func TestEstimateCostFromSummary_Nil(t *testing.T) {
	got, ok := EstimateCostFromSummary(nil)
	if got != 0 || ok {
		t.Errorf("EstimateCostFromSummary(nil) = (%f, %v), want (0, false)", got, ok)
	}
}

func TestFormatCost(t *testing.T) {
	tests := []struct {
		name string
		usd  float64
		want string
	}{
		{"zero", 0, "$0.00"},
		{"typical", 2.412, "$2.41"},
		{"tiny rounds down", 0.003, "$0.00"},
		{"large", 10.5, "$10.50"},
		{"exact cents", 1.99, "$1.99"},
		{"sub-cent", 0.009, "$0.01"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatCost(tt.usd)
			if got != tt.want {
				t.Errorf("FormatCost(%v) = %q, want %q", tt.usd, got, tt.want)
			}
		})
	}
}

func TestTurnCost(t *testing.T) {
	tests := []struct {
		name       string
		tm         TurnMetric
		model      string
		wantApprox float64
		epsilon    float64
	}{
		{
			name:       "zero tokens",
			tm:         TurnMetric{},
			model:      "claude-sonnet-4-5",
			wantApprox: 0.0,
			epsilon:    0.0001,
		},
		{
			name: "sonnet turn",
			tm: TurnMetric{
				InputTokens:  10_000,
				OutputTokens: 2_000,
			},
			model: "claude-sonnet-4-5",
			// cost = (10000*3.0 + 2000*15.0) / 1_000_000 = 0.06
			wantApprox: 0.06,
			epsilon:    0.0001,
		},
		{
			name: "opus turn with cache",
			tm: TurnMetric{
				InputTokens:  100_000,
				OutputTokens: 10_000,
				CacheRead:    50_000,
				CacheCreate:  20_000,
			},
			model: "claude-opus-4-20250514",
			// cost = (100k*15.0 + 10k*75.0 + 50k*1.50 + 20k*18.75) / 1_000_000
			//      = (1.50 + 0.75 + 0.075 + 0.375) = 2.70
			wantApprox: 2.70,
			epsilon:    0.0001,
		},
		{
			name: "opus 5.5 is $4/$20/$0.20/$5",
			tm: TurnMetric{
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
				CacheRead:    1_000_000,
				CacheCreate:  1_000_000,
			},
			model:      "claude-opus-5-5",
			wantApprox: 4 + 20 + 0.20 + 5,
			epsilon:    0.0001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := TurnCost(tt.tm, tt.model)
			if math.Abs(got-tt.wantApprox) > tt.epsilon {
				t.Errorf("TurnCost() = %f, want ~%f (epsilon %f)", got, tt.wantApprox, tt.epsilon)
			}
		})
	}
}

func TestSampleTurnMetrics(t *testing.T) {
	// Build a slice of 100 metrics.
	var metrics []TurnMetric
	for i := 1; i <= 100; i++ {
		metrics = append(metrics, TurnMetric{Turn: i})
	}

	t.Run("short session shows all turns", func(t *testing.T) {
		short := metrics[:10]
		got := SampleTurnMetrics(short, 15)
		if len(got) != 10 {
			t.Errorf("SampleTurnMetrics(10 turns, max=15) = %d rows, want 10", len(got))
		}
	})

	t.Run("long session capped at maxRows", func(t *testing.T) {
		got := SampleTurnMetrics(metrics, 15)
		if len(got) > 15 {
			t.Errorf("SampleTurnMetrics(100 turns, max=15) = %d rows, want <= 15", len(got))
		}
	})

	t.Run("long session includes first and last turn", func(t *testing.T) {
		got := SampleTurnMetrics(metrics, 15)
		if len(got) == 0 {
			t.Fatal("SampleTurnMetrics returned empty slice")
		}
		if got[0].Turn != 1 {
			t.Errorf("first sampled turn = %d, want 1", got[0].Turn)
		}
		if got[len(got)-1].Turn != 100 {
			t.Errorf("last sampled turn = %d, want 100", got[len(got)-1].Turn)
		}
	})

	t.Run("empty slice returns nil", func(t *testing.T) {
		got := SampleTurnMetrics(nil, 15)
		if got != nil {
			t.Errorf("SampleTurnMetrics(nil) = %v, want nil", got)
		}
	})

	t.Run("default maxRows when zero", func(t *testing.T) {
		got := SampleTurnMetrics(metrics, 0)
		if len(got) > 15 {
			t.Errorf("SampleTurnMetrics(100, 0) = %d, want <= 15 (default)", len(got))
		}
	})
}

func TestParseSummary_TurnMetrics(t *testing.T) {
	s := Session{LogFile: "testdata/multiturn.jsonl"}
	sum, err := ParseSummary(s)
	if err != nil {
		t.Fatalf("ParseSummary error: %v", err)
	}

	if len(sum.TurnMetrics) != 2 {
		t.Fatalf("TurnMetrics count = %d, want 2", len(sum.TurnMetrics))
	}

	// Turn 1: input=100, output=50, cache_create=200, cache_read=50.
	tm1 := sum.TurnMetrics[0]
	if tm1.Turn != 1 {
		t.Errorf("TurnMetrics[0].Turn = %d, want 1", tm1.Turn)
	}
	if tm1.InputTokens != 100 {
		t.Errorf("TurnMetrics[0].InputTokens = %d, want 100", tm1.InputTokens)
	}
	if tm1.OutputTokens != 50 {
		t.Errorf("TurnMetrics[0].OutputTokens = %d, want 50", tm1.OutputTokens)
	}
	if tm1.CacheCreate != 200 {
		t.Errorf("TurnMetrics[0].CacheCreate = %d, want 200", tm1.CacheCreate)
	}
	if tm1.CacheRead != 50 {
		t.Errorf("TurnMetrics[0].CacheRead = %d, want 50", tm1.CacheRead)
	}

	// Turn 2: input=500, output=100, cache_create=0, cache_read=300.
	tm2 := sum.TurnMetrics[1]
	if tm2.Turn != 2 {
		t.Errorf("TurnMetrics[1].Turn = %d, want 2", tm2.Turn)
	}
	if tm2.InputTokens != 500 {
		t.Errorf("TurnMetrics[1].InputTokens = %d, want 500", tm2.InputTokens)
	}
	if tm2.OutputTokens != 100 {
		t.Errorf("TurnMetrics[1].OutputTokens = %d, want 100", tm2.OutputTokens)
	}
	if tm2.CacheCreate != 0 {
		t.Errorf("TurnMetrics[1].CacheCreate = %d, want 0", tm2.CacheCreate)
	}
	if tm2.CacheRead != 300 {
		t.Errorf("TurnMetrics[1].CacheRead = %d, want 300", tm2.CacheRead)
	}

	// Timestamps should be populated.
	if tm1.Timestamp.IsZero() {
		t.Errorf("TurnMetrics[0].Timestamp is zero, want non-zero")
	}
	if tm2.Timestamp.IsZero() {
		t.Errorf("TurnMetrics[1].Timestamp is zero, want non-zero")
	}
}

func TestTurnCost_UnknownModel(t *testing.T) {
	tm := TurnMetric{InputTokens: 10_000, OutputTokens: 2_000}
	for _, model := range []string{"claude-future-99", "qwen3-coder-next", ""} {
		if got, ok := TurnCost(tm, model); ok || got != 0 {
			t.Errorf("TurnCost(%q) = (%v, %v), want (0, false): must not default to Sonnet", model, got, ok)
		}
	}
}

// TestCostOfSummary_Haiku55PerTurn: the long-context tier is decided per
// request, so the session total must equal the per-turn sum, which differs from
// pricing the aggregate totals.
func TestCostOfSummary_Haiku55PerTurn(t *testing.T) {
	small := TurnMetric{Turn: 1, InputTokens: 1000, OutputTokens: 500, CacheRead: 50_000}
	big := TurnMetric{Turn: 2, InputTokens: 1000, OutputTokens: 500, CacheRead: 150_000}
	sum := &Summary{ModelName: "claude-haiku-5-5", TurnMetrics: []TurnMetric{small, big}}
	for _, tm := range sum.TurnMetrics {
		sum.TotalInputTokens += tm.InputTokens
		sum.TotalOutputTokens += tm.OutputTokens
		sum.CacheReadTokens += tm.CacheRead
	}
	cSmall, _ := TurnCost(small, sum.ModelName)
	cBig, _ := TurnCost(big, sum.ModelName)
	if cBig <= cSmall {
		t.Fatalf("over-threshold request should cost more: small=%v big=%v", cSmall, cBig)
	}
	// Under 100K: 0.10/0.50/0.01; over: 0.50/2.50/0.05 per MTok.
	if want := (1000*0.10 + 500*0.50 + 50_000*0.01) / 1e6; math.Abs(cSmall-want) > 1e-12 {
		t.Errorf("under tier = %v, want %v", cSmall, want)
	}
	if want := (1000*0.50 + 500*2.50 + 150_000*0.05) / 1e6; math.Abs(cBig-want) > 1e-12 {
		t.Errorf("over tier = %v, want %v", cBig, want)
	}

	got := CostOfSummary(sum, BillingSub)
	if !got.Known || math.Abs(got.USD-(cSmall+cBig)) > 1e-12 {
		t.Errorf("session = (%v, %v), want per-turn sum %v", got.USD, got.Known, cSmall+cBig)
	}
	// Pricing the aggregate would be wrong (prompt 202K > 100K => all tier rates).
	agg, _ := EstimateCostFromSummary(&Summary{ModelName: sum.ModelName,
		TotalInputTokens: sum.TotalInputTokens, TotalOutputTokens: sum.TotalOutputTokens, CacheReadTokens: sum.CacheReadTokens})
	if math.Abs(agg-got.USD) < 1e-12 {
		t.Errorf("aggregate pricing equals per-turn sum (%v); test does not exercise the tier", agg)
	}
}

func TestCostOfSummary_UnknownExcluded(t *testing.T) {
	sum := &Summary{ModelName: "qwen3-coder-next", TurnMetrics: []TurnMetric{{Turn: 1, InputTokens: 100, OutputTokens: 100}}}
	got := CostOfSummary(sum, BillingUnknown)
	if got.Known || got.USD != 0 {
		t.Errorf("unknown model: got (%v, known=%v), want (0, false)", got.USD, got.Known)
	}
}

func TestCostOfSummary_OllamaFree(t *testing.T) {
	sum := &Summary{ModelName: "qwen3.6:35b-a3b", TurnMetrics: []TurnMetric{{Turn: 1, InputTokens: 1_000_000, OutputTokens: 1_000_000}}}
	got := CostOfSummary(sum, BillingFree)
	if !got.Known || got.USD != 0 || got.Billing != BillingFree {
		t.Errorf("free session: got %+v, want known $0 free", got)
	}
}

func TestCostOfSummary_Opus55(t *testing.T) {
	sum := &Summary{ModelName: "claude-opus-5-5", TurnMetrics: []TurnMetric{
		{Turn: 1, InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheRead: 1_000_000, CacheCreate: 1_000_000},
	}}
	got := CostOfSummary(sum, BillingSub)
	if want := 4.0 + 20 + 0.20 + 5; math.Abs(got.USD-want) > 1e-9 {
		t.Errorf("opus 5.5 = %v, want %v (not the old 15/75/1.5/18.75)", got.USD, want)
	}
}

func TestCostOfSummary_PerModelBreakdown(t *testing.T) {
	sum := &Summary{ModelName: "claude-sonnet-5-5", TurnMetrics: []TurnMetric{
		{Turn: 1, Model: "claude-opus-5-5", InputTokens: 1_000_000},
		{Turn: 2, Model: "claude-sonnet-5-5", InputTokens: 1_000_000},
		{Turn: 3, InputTokens: 1_000_000}, // no per-turn model: session model
	}}
	got := CostOfSummary(sum, BillingSub)
	if len(got.ByModel) != 2 || got.ByModel[0].Model != "claude-opus-5-5" || got.ByModel[1].Turns != 2 {
		t.Fatalf("breakdown = %+v", got.ByModel)
	}
	if math.Abs(got.USD-8.0) > 1e-9 {
		t.Errorf("total = %v, want 8", got.USD)
	}
}

func TestClassifyBilling(t *testing.T) {
	backends := map[string]config.BackendConfig{
		"local": {Kind: config.BackendKindOllama},
		"nous":  {Kind: config.BackendKindNous},
	}
	tokens.SetCatalogDir("")
	tests := []struct {
		model, backend string
		want           Billing
	}{
		{"claude-opus-5-5", "", BillingSub},
		{"qwen3.6:35b-a3b", "local", BillingFree},
		{"qwen3.6:35b-a3b", "", BillingFree}, // legacy: no backend recorded, ollama configured
		{"mystery/model", "", BillingUnknown},
		{"openai/gpt-6-astra", "nous", BillingMetered},
		{"claude-opus-5-5", "gone", BillingSub}, // backend removed from config: model decides
	}
	for _, tt := range tests {
		if got := ClassifyBilling(tt.model, tt.backend, backends); got != tt.want {
			t.Errorf("ClassifyBilling(%q, %q) = %q, want %q", tt.model, tt.backend, got, tt.want)
		}
	}
}

func TestClassifyBilling_NoOllamaConfigured(t *testing.T) {
	tokens.SetCatalogDir("")
	if got := ClassifyBilling("qwen3.6:35b-a3b", "", nil); got != BillingUnknown {
		t.Errorf("got %q, want unknown without an ollama backend", got)
	}
}

func TestRecordBackend(t *testing.T) {
	SetSessionDataDirForTest(t.TempDir())
	t.Cleanup(func() { SetSessionDataDirForTest("") })
	if got := RecordedBackend("s1"); got != "" {
		t.Fatalf("unrecorded = %q", got)
	}
	if err := RecordBackend("s1", "local"); err != nil {
		t.Fatal(err)
	}
	if got := RecordedBackend("s1"); got != "local" {
		t.Errorf("RecordedBackend = %q, want local", got)
	}
	if err := RecordBackend("../x", "local"); err != nil || RecordedBackend("../x") != "" {
		t.Error("path-like session ids must be ignored")
	}
}

// writeJSONL writes lines to a temp session log and returns a Session for it.
func writeJSONL(t *testing.T, lines ...string) Session {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Session{ID: "sess", Agent: "a", LogFile: path}
}

func assistantLine(id, model string, in, out, cr, cw int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-11-10T10:00:00Z","message":{"id":%q,"model":%q,"content":[{"type":"text","text":"x"}],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`,
		id, model, in, out, cr, cw)
}

// TestParseSummary_DedupsByMessageID: Claude Code writes one line per content
// block with the same message id and usage; counting each line overcounts.
func TestParseSummary_DedupsByMessageID(t *testing.T) {
	s := writeJSONL(t,
		assistantLine("m1", "claude-opus-5-5", 10, 100, 1000, 0),
		assistantLine("m1", "claude-opus-5-5", 10, 100, 1000, 0),
		assistantLine("m1", "claude-opus-5-5", 10, 100, 1000, 0),
		assistantLine("m2", "claude-opus-5-5", 20, 200, 2000, 50),
		assistantLine("m2", "claude-opus-5-5", 20, 200, 2000, 50),
		assistantLine("", "<synthetic>", 0, 0, 0, 0),
	)
	sum, err := ParseSummary(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.TurnMetrics) != 3 { // m1, m2, and the id-less synthetic entry
		t.Errorf("TurnMetrics = %d, want 3", len(sum.TurnMetrics))
	}
	if sum.TotalInputTokens != 30 || sum.TotalOutputTokens != 300 || sum.CacheReadTokens != 3000 || sum.CacheCreateTokens != 50 {
		t.Errorf("totals = in %d out %d cr %d cw %d", sum.TotalInputTokens, sum.TotalOutputTokens, sum.CacheReadTokens, sum.CacheCreateTokens)
	}
	if sum.TurnCount != 3 {
		t.Errorf("TurnCount = %d, want 3", sum.TurnCount)
	}
	if sum.ModelName != "claude-opus-5-5" {
		t.Errorf("ModelName = %q (synthetic must not win)", sum.ModelName)
	}
}

// TestCostOfSummary_AstraCalibration reproduces a real gpt-6-astra review
// session's usage (split over turns) at Nous catalog prices: Nous billed ~$7.
func TestCostOfSummary_AstraCalibration(t *testing.T) {
	dir := t.TempDir()
	doc := `{"data":[{"id":"openai/gpt-6-astra","pricing":{"prompt":"0.0000100000","completion":"0.0000500000","input_cache_read":"0.0000010000","input_cache_write":"0.0000125000"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "nous-models-x.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	tokens.SetCatalogDir(dir)
	t.Cleanup(func() { tokens.SetCatalogDir("") })

	s := writeJSONL(t,
		assistantLine("a", "openai/gpt-6-astra", 100, 5000, 2_000_000, 100_000),
		assistantLine("a", "openai/gpt-6-astra", 100, 5000, 2_000_000, 100_000),
		assistantLine("b", "openai/gpt-6-astra", 17, 5323, 2_552_942, 71_823),
	)
	sum, err := ParseSummary(s)
	if err != nil {
		t.Fatal(err)
	}
	got := CostOfSummary(sum, ClassifyBilling(sum.ModelName, "nous", map[string]config.BackendConfig{"nous": {Kind: config.BackendKindNous}}))
	if got.Billing != BillingMetered || !got.Known {
		t.Fatalf("got %+v", got)
	}
	if math.Abs(got.USD-7.22) > 0.01 {
		t.Errorf("astra session = $%.4f, want $7.22 +-0.01", got.USD)
	}
}
