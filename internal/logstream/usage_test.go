package logstream

import (
	"testing"
)

// makeAssistantEntry builds an Entry with the given token values.
func makeAssistantEntry(inputTokens, outputTokens, cacheCreate, cacheRead int) Entry {
	return Entry{
		Type: "assistant",
		Usage: &Usage{
			InputTokens:       inputTokens,
			OutputTokens:      outputTokens,
			CacheCreateTokens: cacheCreate,
			CacheReadTokens:   cacheRead,
		},
	}
}

func TestUsageAccumulator_TenAssistantEntries(t *testing.T) {
	u := &UsageAccumulator{}
	for i := 1; i <= 10; i++ {
		u.OnEntry(makeAssistantEntry(100, 50, 20, 10))
	}

	// input = 100 + 20 + 10 = 130 per turn × 10 = 1300
	if u.InputTokens != 1300 {
		t.Errorf("InputTokens = %d, want 1300", u.InputTokens)
	}
	if u.OutputTokens != 500 {
		t.Errorf("OutputTokens = %d, want 500", u.OutputTokens)
	}
	if u.CacheReadTokens != 100 {
		t.Errorf("CacheReadTokens = %d, want 100", u.CacheReadTokens)
	}
	if u.CacheCreateTokens != 200 {
		t.Errorf("CacheCreateTokens = %d, want 200", u.CacheCreateTokens)
	}
	if u.TurnCount != 10 {
		t.Errorf("TurnCount = %d, want 10", u.TurnCount)
	}
}

func TestUsageAccumulator_IgnoresNonAssistant(t *testing.T) {
	u := &UsageAccumulator{}

	u.OnEntry(Entry{Type: "user"})
	u.OnEntry(Entry{Type: "agent_setting"})
	u.OnEntry(Entry{Type: "summary"})
	u.OnEntry(makeAssistantEntry(100, 50, 0, 0)) // only this counts
	u.OnEntry(Entry{Type: "assistant"})          // assistant but nil Usage — ignored

	if u.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1 (only assistant+usage entries count)", u.TurnCount)
	}
	if u.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", u.InputTokens)
	}
}

func TestUsageAccumulator_OnReset(t *testing.T) {
	u := &UsageAccumulator{}
	for i := 0; i < 5; i++ {
		u.OnEntry(makeAssistantEntry(100, 50, 10, 5))
	}

	if u.TurnCount == 0 {
		t.Fatal("TurnCount should be > 0 before reset")
	}

	u.OnReset()

	if u.InputTokens != 0 {
		t.Errorf("InputTokens = %d after reset, want 0", u.InputTokens)
	}
	if u.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d after reset, want 0", u.OutputTokens)
	}
	if u.CacheReadTokens != 0 {
		t.Errorf("CacheReadTokens = %d after reset, want 0", u.CacheReadTokens)
	}
	if u.CacheCreateTokens != 0 {
		t.Errorf("CacheCreateTokens = %d after reset, want 0", u.CacheCreateTokens)
	}
	if u.TurnCount != 0 {
		t.Errorf("TurnCount = %d after reset, want 0", u.TurnCount)
	}
	if u.ModelName != "" {
		t.Errorf("ModelName = %q after reset, want empty", u.ModelName)
	}
	if u.LastTurnInput != 0 || u.LastTurnOutput != 0 {
		t.Errorf("LastTurn* non-zero after reset: in=%d out=%d", u.LastTurnInput, u.LastTurnOutput)
	}
}

func TestUsageAccumulator_LastTurnReflectsMostRecent(t *testing.T) {
	u := &UsageAccumulator{}

	// Feed several entries; LastTurn* should reflect only the last one.
	for i := 1; i <= 5; i++ {
		u.OnEntry(makeAssistantEntry(i*100, i*50, i*10, i*5))
	}

	// Last entry: input=500, cacheCreate=50, cacheRead=25
	// LastTurnInput = 500 + 50 + 25 = 575
	wantLastInput := 5*100 + 5*10 + 5*5 // 575
	if u.LastTurnInput != wantLastInput {
		t.Errorf("LastTurnInput = %d, want %d", u.LastTurnInput, wantLastInput)
	}
	if u.LastTurnOutput != 5*50 {
		t.Errorf("LastTurnOutput = %d, want %d", u.LastTurnOutput, 5*50)
	}
	if u.LastTurnCacheRead != 5*5 {
		t.Errorf("LastTurnCacheRead = %d, want %d", u.LastTurnCacheRead, 5*5)
	}
	if u.LastTurnCacheCreate != 5*10 {
		t.Errorf("LastTurnCacheCreate = %d, want %d", u.LastTurnCacheCreate, 5*10)
	}

	// Verify LastTurn* are NOT cumulative (should not equal sum of all turns).
	totalInput := 0
	for i := 1; i <= 5; i++ {
		totalInput += i*100 + i*10 + i*5
	}
	if u.LastTurnInput == totalInput {
		t.Errorf("LastTurnInput = %d equals cumulative total %d — should be last-turn only", u.LastTurnInput, totalInput)
	}
}

func TestUsageAccumulator_ModelName(t *testing.T) {
	u := &UsageAccumulator{}

	e1 := makeAssistantEntry(100, 50, 0, 0)
	e1.Model = "claude-haiku-3"
	u.OnEntry(e1)

	if u.ModelName != "claude-haiku-3" {
		t.Errorf("ModelName = %q, want 'claude-haiku-3'", u.ModelName)
	}

	e2 := makeAssistantEntry(200, 80, 0, 0)
	e2.Model = "claude-sonnet-4-20250514"
	u.OnEntry(e2)

	if u.ModelName != "claude-sonnet-4-20250514" {
		t.Errorf("ModelName = %q after update, want 'claude-sonnet-4-20250514'", u.ModelName)
	}
}

// TestUsageAccumulator_SyntheticModelDoesNotClobberModelName verifies that a
// "<synthetic>" entry doesn't overwrite a previously-observed real model
// name, but its token usage is still accumulated.
func TestUsageAccumulator_SyntheticModelDoesNotClobberModelName(t *testing.T) {
	u := &UsageAccumulator{}

	e1 := makeAssistantEntry(100, 50, 0, 0)
	e1.Model = "claude-sonnet-4-20250514"
	u.OnEntry(e1)

	if u.ModelName != "claude-sonnet-4-20250514" {
		t.Fatalf("ModelName = %q, want claude-sonnet-4-20250514", u.ModelName)
	}

	e2 := makeAssistantEntry(200, 80, 0, 0)
	e2.Model = "<synthetic>"
	u.OnEntry(e2)

	if u.ModelName != "claude-sonnet-4-20250514" {
		t.Errorf("ModelName = %q after synthetic entry, want unchanged claude-sonnet-4-20250514", u.ModelName)
	}
	// Usage from the synthetic entry must still be accumulated.
	if u.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2 (synthetic entry still counts as a turn)", u.TurnCount)
	}
	if u.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300 (100 + 200, synthetic usage accumulated)", u.InputTokens)
	}
	if u.LastTurnInput != 200 {
		t.Errorf("LastTurnInput = %d, want 200 (reflects the synthetic entry's usage)", u.LastTurnInput)
	}
}

// TestUsageAccumulator_SyntheticModelFirstDoesNotSetModelName verifies that
// when the very first entry is synthetic, ModelName stays empty rather than
// becoming the literal string "<synthetic>".
func TestUsageAccumulator_SyntheticModelFirstDoesNotSetModelName(t *testing.T) {
	u := &UsageAccumulator{}

	e := makeAssistantEntry(100, 50, 0, 0)
	e.Model = "<synthetic>"
	u.OnEntry(e)

	if u.ModelName != "" {
		t.Errorf("ModelName = %q, want empty (synthetic model must never be assigned)", u.ModelName)
	}
	if u.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1", u.TurnCount)
	}
}

func TestUsageAccumulator_ViaStream(t *testing.T) {
	// Integration test: feed entries via Stream and verify accumulator totals.
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":10,"cache_read_input_tokens":5}}}`
	}
	// Add a non-assistant entry that should not be counted.
	lines = append(lines, `{"type":"user","message":{"content":[{"type":"text","text":"hi"}]}}`)

	path := writeTempFile(t, lines...)

	u := &UsageAccumulator{}
	s := NewStream(path)
	s.Subscribe(u)
	s.Tick()

	if u.TurnCount != 10 {
		t.Errorf("TurnCount = %d, want 10", u.TurnCount)
	}
	// input per turn = 100 + 10 + 5 = 115; total = 1150
	if u.InputTokens != 1150 {
		t.Errorf("InputTokens = %d, want 1150", u.InputTokens)
	}
	if u.OutputTokens != 500 {
		t.Errorf("OutputTokens = %d, want 500", u.OutputTokens)
	}
}
