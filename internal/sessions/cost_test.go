package sessions

import (
	"math"
	"testing"
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
			got := EstimateCostFromSummary(&tt.sum)
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
			// cost = (10000 * 0.25 + 2000 * 1.25) / 1_000_000
			// = (2500 + 2500) / 1_000_000 = 0.005
			wantApprox: 0.005,
			epsilon:    0.0001,
		},
		{
			name: "haiku with cache",
			sum: Summary{
				ModelName: "claude-haiku-3-20241022",
				// TotalInputTokens is raw (uncached) input only.
				TotalInputTokens:  10000,
				TotalOutputTokens: 3000,
				CacheCreateTokens: 10000,
				CacheReadTokens:   30000,
			},
			// cost = (10000*0.25 + 3000*1.25 + 10000*0.30 + 30000*0.03) / 1_000_000
			// = (2500 + 3750 + 3000 + 900) / 1_000_000 = 0.01015
			wantApprox: 0.01015,
			epsilon:    0.0001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateCostFromSummary(&tt.sum)
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
			// Opus: $75/1M output
			wantApprox: 75.0,
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
			got := EstimateCostFromSummary(&tt.sum)
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

	got := EstimateCostFromSummary(sum)
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
	// Unknown model should default to Sonnet pricing.
	unknown := Summary{
		ModelName:         "claude-future-model-99",
		TotalInputTokens:  10000,
		TotalOutputTokens: 2000,
		CacheCreateTokens: 0,
		CacheReadTokens:   0,
	}
	sonnet := Summary{
		ModelName:         "claude-sonnet-4-5",
		TotalInputTokens:  10000,
		TotalOutputTokens: 2000,
		CacheCreateTokens: 0,
		CacheReadTokens:   0,
	}

	gotUnknown := EstimateCostFromSummary(&unknown)
	gotSonnet := EstimateCostFromSummary(&sonnet)

	if math.Abs(gotUnknown-gotSonnet) > 0.0001 {
		t.Errorf("unknown model cost %f != sonnet cost %f; expected same (default to Sonnet pricing)", gotUnknown, gotSonnet)
	}
}

func TestEstimateCostFromSummary_Nil(t *testing.T) {
	got := EstimateCostFromSummary(nil)
	if got != 0 {
		t.Errorf("EstimateCostFromSummary(nil) = %f, want 0", got)
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
			name: "unknown model defaults to sonnet",
			tm: TurnMetric{
				InputTokens:  10_000,
				OutputTokens: 2_000,
			},
			model: "claude-future-99",
			// same as sonnet
			wantApprox: 0.06,
			epsilon:    0.0001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TurnCost(tt.tm, tt.model)
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
