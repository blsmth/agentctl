package statusline

import (
	"math"
	"testing"
)

func f64Ptr(f float64) *float64 { return &f }

const fullPayload = `{
  "session_id": "abc-123",
  "session_name": "my session",
  "transcript_path": "/tmp/transcript.jsonl",
  "cwd": "/home/user/project",
  "version": "1.2.3",
  "model": {"id": "claude-sonnet-4-20250514", "display_name": "Sonnet 4"},
  "workspace": {
    "current_dir": "/home/user/project",
    "project_dir": "/home/user/project",
    "git_worktree": "issue-1023",
    "added_dirs": ["/tmp/extra"]
  },
  "cost": {
    "total_cost_usd": 1.23,
    "total_duration_ms": 4500,
    "total_api_duration_ms": 3200,
    "total_lines_added": 42,
    "total_lines_removed": 7
  },
  "context_window": {
    "total_input_tokens": 1000,
    "total_output_tokens": 200,
    "context_window_size": 200000,
    "used_percentage": 12.5,
    "remaining_percentage": 87.5,
    "current_usage": {
      "input_tokens": 900,
      "output_tokens": 150,
      "cache_creation_input_tokens": 50,
      "cache_read_input_tokens": 25
    }
  },
  "prompt_cache": {
    "warm": true,
    "ttl": 300,
    "expires_at": 1738425600,
    "hit_ratio": 0.75,
    "requests": 10,
    "misses": 2
  },
  "rate_limits": {
    "five_hour": {"used_percentage": 23.5, "resets_at": 1738425600},
    "seven_day": {"used_percentage": 41.2, "resets_at": 1738857600},
    "spend_limit": {"used_percentage": 62.8, "resets_at": 1740787200}
  },
  "agent": {"name": "go-engineer"},
  "pr": {"number": 1023, "url": "https://example.com/pr/1023", "review_state": "approved", "kind": "dispatch"},
  "effort": {"level": "high"},
  "fast_mode": false,
  "thinking": {"enabled": true},
  "vim": {"mode": "normal"}
}`

func TestDecode_FullPayload(t *testing.T) {
	p, err := Decode([]byte(fullPayload))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if p.SessionID == nil || *p.SessionID != "abc-123" {
		t.Errorf("SessionID = %v, want abc-123", p.SessionID)
	}
	if p.Model == nil || p.Model.ID == nil || *p.Model.ID != "claude-sonnet-4-20250514" {
		t.Fatalf("Model.ID = %+v, want claude-sonnet-4-20250514", p.Model)
	}
	if p.Workspace == nil || p.Workspace.GitWorktree == nil || *p.Workspace.GitWorktree != "issue-1023" {
		t.Errorf("Workspace.GitWorktree = %+v", p.Workspace)
	}
	if len(p.Workspace.AddedDirs) != 1 || p.Workspace.AddedDirs[0] != "/tmp/extra" {
		t.Errorf("Workspace.AddedDirs = %v", p.Workspace.AddedDirs)
	}
	if p.Cost == nil || p.Cost.TotalCostUSD == nil || *p.Cost.TotalCostUSD != 1.23 {
		t.Errorf("Cost.TotalCostUSD = %+v", p.Cost)
	}
	if pct, ok := p.ContextWindow.UsedPercentage(); !ok || pct != 12.5 {
		t.Errorf("ContextWindow.UsedPercentage() = (%v, %v), want (12.5, true)", pct, ok)
	}
	if p.PromptCache == nil || p.PromptCache.HitRatio == nil || *p.PromptCache.HitRatio != 0.75 {
		t.Errorf("PromptCache.HitRatio = %+v", p.PromptCache)
	}
	if p.RateLimits == nil || p.RateLimits.FiveHour == nil || *p.RateLimits.FiveHour.UsedPercentage != 23.5 {
		t.Errorf("RateLimits.FiveHour = %+v", p.RateLimits)
	}
	if p.Agent == nil || p.Agent.Name == nil || *p.Agent.Name != "go-engineer" {
		t.Errorf("Agent.Name = %+v", p.Agent)
	}
	if p.PR == nil || p.PR.Number == nil || *p.PR.Number != 1023 {
		t.Errorf("PR.Number = %+v", p.PR)
	}
	if p.FastMode == nil || *p.FastMode != false {
		t.Errorf("FastMode = %+v, want false", p.FastMode)
	}
	if p.Vim == nil || p.Vim.Mode == nil || *p.Vim.Mode != "normal" {
		t.Errorf("Vim.Mode = %+v", p.Vim)
	}
}

func TestDecode_RateLimitsAbsentEntirely(t *testing.T) {
	p, err := Decode([]byte(`{"session_id": "abc"}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p.RateLimits != nil {
		t.Errorf("RateLimits = %+v, want nil (absent)", p.RateLimits)
	}
}

func TestDecode_SingleRateLimitWindowPresent(t *testing.T) {
	p, err := Decode([]byte(`{"rate_limits": {"five_hour": {"used_percentage": 10, "resets_at": 100}}}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p.RateLimits == nil {
		t.Fatal("RateLimits is nil")
	}
	if p.RateLimits.FiveHour == nil {
		t.Fatal("FiveHour is nil, want present")
	}
	if p.RateLimits.SevenDay != nil {
		t.Errorf("SevenDay = %+v, want nil (absent)", p.RateLimits.SevenDay)
	}
	if p.RateLimits.SpendLimit != nil {
		t.Errorf("SpendLimit = %+v, want nil (absent)", p.RateLimits.SpendLimit)
	}
}

func TestDecode_UsedPercentageNull(t *testing.T) {
	p, err := Decode([]byte(`{"context_window": {"used_percentage": null, "context_window_size": 100000}}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p.ContextWindow.UsedPercentagePushed != nil {
		t.Errorf("UsedPercentagePushed = %v, want nil", p.ContextWindow.UsedPercentagePushed)
	}
	// Falls back to current_usage/total_input_tokens cascade; neither present
	// here, so overall ok should be false.
	if _, ok := p.ContextWindow.UsedPercentage(); ok {
		t.Error("UsedPercentage() ok = true, want false (no usable source)")
	}
}

func TestDecode_CurrentUsageNull(t *testing.T) {
	p, err := Decode([]byte(`{
		"context_window": {
			"context_window_size": 100000,
			"total_input_tokens": 5000,
			"current_usage": null
		}
	}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p.ContextWindow.CurrentUsage != nil {
		t.Errorf("CurrentUsage = %+v, want nil", p.ContextWindow.CurrentUsage)
	}
	// Falls back to total_input_tokens: 5000/100000 = 5%.
	pct, ok := p.ContextWindow.UsedPercentage()
	if !ok {
		t.Fatal("UsedPercentage() ok = false, want true (total_input_tokens fallback)")
	}
	if math.Abs(pct-5) > 1e-9 {
		t.Errorf("UsedPercentage() = %v, want 5", pct)
	}
}

func TestDecode_HitRatioNull(t *testing.T) {
	p, err := Decode([]byte(`{"prompt_cache": {"warm": true, "hit_ratio": null}}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p.PromptCache == nil {
		t.Fatal("PromptCache is nil")
	}
	if p.PromptCache.HitRatio != nil {
		t.Errorf("HitRatio = %v, want nil (not 0)", p.PromptCache.HitRatio)
	}
}

func TestDecode_SpendLimitExceeds100_NotClamped(t *testing.T) {
	p, err := Decode([]byte(`{"rate_limits": {"spend_limit": {"used_percentage": 162.8, "resets_at": 100}}}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p.RateLimits.SpendLimit.UsedPercentage == nil {
		t.Fatal("SpendLimit.UsedPercentage is nil")
	}
	// Decode itself must not clamp -- only the coerce* helpers (used at read
	// time) apply clamping, and coerceNonNegative (the one spend_limit reads
	// through) must not clamp to 100 either.
	if *p.RateLimits.SpendLimit.UsedPercentage != 162.8 {
		t.Errorf("SpendLimit.UsedPercentage = %v, want 162.8 (unclamped)", *p.RateLimits.SpendLimit.UsedPercentage)
	}
	got, ok := coerceNonNegative(p.RateLimits.SpendLimit.UsedPercentage)
	if !ok || got != 162.8 {
		t.Errorf("coerceNonNegative(162.8) = (%v, %v), want (162.8, true)", got, ok)
	}
}

func TestDecode_UnknownTopLevelKeys(t *testing.T) {
	_, err := Decode([]byte(`{"session_id": "abc", "some_future_field": {"nested": true}, "another": 42}`))
	if err != nil {
		t.Fatalf("Decode returned error for unknown keys: %v", err)
	}
}

func TestDecode_MalformedJSON(t *testing.T) {
	_, err := Decode([]byte(`{not valid json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestDecode_EmptyInput(t *testing.T) {
	_, err := Decode([]byte(``))
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
}

// ── Absent vs zero (the core bug pointer fields exist to prevent) ──────────

func TestAbsentFiveHour_DistinguishableFromZero(t *testing.T) {
	absent, err := Decode([]byte(`{"rate_limits": {"seven_day": {"used_percentage": 1, "resets_at": 1}}}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	zero, err := Decode([]byte(`{"rate_limits": {"five_hour": {"used_percentage": 0, "resets_at": 1}}}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if absent.RateLimits.FiveHour != nil {
		t.Fatalf("expected FiveHour absent (nil), got %+v", absent.RateLimits.FiveHour)
	}
	if zero.RateLimits.FiveHour == nil {
		t.Fatal("expected FiveHour present with used_percentage 0, got nil")
	}
	if *zero.RateLimits.FiveHour.UsedPercentage != 0 {
		t.Errorf("UsedPercentage = %v, want 0", *zero.RateLimits.FiveHour.UsedPercentage)
	}
}

// ── coercion helpers ─────────────────────────────────────────────────────────

func TestCoerceFinite_RejectsNaNAndInf(t *testing.T) {
	nan := math.NaN()
	posInf := math.Inf(1)
	negInf := math.Inf(-1)

	if _, ok := coerceFinite(&nan); ok {
		t.Error("coerceFinite(NaN) ok = true, want false")
	}
	if _, ok := coerceFinite(&posInf); ok {
		t.Error("coerceFinite(+Inf) ok = true, want false")
	}
	if _, ok := coerceFinite(&negInf); ok {
		t.Error("coerceFinite(-Inf) ok = true, want false")
	}
	if _, ok := coerceFinite(nil); ok {
		t.Error("coerceFinite(nil) ok = true, want false")
	}
}

func TestCoercePercent_Clamps(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{-10, 0},
		{0, 0},
		{50, 50},
		{100, 100},
		{150, 100},
	}
	for _, tt := range tests {
		got, ok := coercePercent(&tt.in)
		if !ok {
			t.Errorf("coercePercent(%v) ok = false", tt.in)
		}
		if got != tt.want {
			t.Errorf("coercePercent(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestCoerceNonNegative_ClampsNegativesOnlyButNeverClampsAt100(t *testing.T) {
	negative := -5.0
	got, ok := coerceNonNegative(&negative)
	if !ok || got != 0 {
		t.Errorf("coerceNonNegative(-5) = (%v, %v), want (0, true)", got, ok)
	}

	high := 162.8
	got, ok = coerceNonNegative(&high)
	if !ok || got != 162.8 {
		t.Errorf("coerceNonNegative(162.8) = (%v, %v), want (162.8, true) -- must not clamp", got, ok)
	}
}

func TestContextWindow_UsedPercentage_NilContextWindow(t *testing.T) {
	var cw *ContextWindow
	if _, ok := cw.UsedPercentage(); ok {
		t.Error("expected ok=false for nil *ContextWindow")
	}
}

func TestContextWindow_UsedPercentage_CascadesToCurrentUsage(t *testing.T) {
	cw := &ContextWindow{
		ContextWindowSize: f64Ptr(1000),
		CurrentUsage: &CurrentUsage{
			InputTokens:              f64Ptr(100),
			CacheCreationInputTokens: f64Ptr(50),
			CacheReadInputTokens:     f64Ptr(50),
		},
	}
	pct, ok := cw.UsedPercentage()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if pct != 20 {
		t.Errorf("UsedPercentage() = %v, want 20 ((100+50+50)/1000*100)", pct)
	}
}

func TestDecode_PromptCacheTTLForms(t *testing.T) {
	tests := []struct {
		name string
		json string
		want *float64
	}{
		{"number", `{"prompt_cache":{"ttl":300}}`, f64Ptr(300)},
		{"numeric string", `{"prompt_cache":{"ttl":"300"}}`, f64Ptr(300)},
		{"fractional string", `{"prompt_cache":{"ttl":"300.5"}}`, f64Ptr(300.5)},
		{"duration minutes", `{"prompt_cache":{"ttl":"5m"}}`, f64Ptr(300)},
		{"duration hours", `{"prompt_cache":{"ttl":"1h"}}`, f64Ptr(3600)},
		{"garbage string", `{"prompt_cache":{"ttl":"soon"}}`, nil},
		{"object", `{"prompt_cache":{"ttl":{"a":1}}}`, nil},
		{"null", `{"prompt_cache":{"ttl":null}}`, nil},
		{"absent", `{"prompt_cache":{"warm":true}}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Decode([]byte(tt.json))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if p.PromptCache == nil {
				t.Fatal("PromptCache is nil")
			}
			got := p.PromptCache.TTL
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Errorf("TTL = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecode_PromptCacheOtherFieldsTolerant(t *testing.T) {
	p, err := Decode([]byte(`{"prompt_cache":{"warm":"yes","hit_ratio":"0.75","requests":"12","misses":"x","expires_at":"1h"}}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	pc := p.PromptCache
	if pc.Warm != nil || pc.Misses != nil {
		t.Errorf("Warm/Misses = %v/%v, want nil", pc.Warm, pc.Misses)
	}
	if pc.HitRatio == nil || *pc.HitRatio != 0.75 || pc.Requests == nil || *pc.Requests != 12 || pc.ExpiresAt == nil || *pc.ExpiresAt != 3600 {
		t.Errorf("unexpected PromptCache: %+v", pc)
	}
}

func TestDecode_MalformedSectionKeepsOthers(t *testing.T) {
	p, err := Decode([]byte(`{
		"prompt_cache": "totally wrong",
		"model": {"id": "m1", "display_name": "Opus"},
		"context_window": {"context_window_size": 200000, "used_percentage": 22},
		"rate_limits": {"five_hour": {"used_percentage": 12, "resets_at": 100}}
	}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p.PromptCache != nil {
		t.Errorf("PromptCache = %+v, want nil (dropped)", p.PromptCache)
	}
	if p.Model == nil || p.Model.ID == nil || *p.Model.ID != "m1" {
		t.Errorf("Model lost: %+v", p.Model)
	}
	if p.ContextWindow == nil || p.ContextWindow.ContextWindowSize == nil {
		t.Errorf("ContextWindow lost: %+v", p.ContextWindow)
	}
	if p.RateLimits == nil || p.RateLimits.FiveHour == nil {
		t.Errorf("RateLimits lost: %+v", p.RateLimits)
	}
}

func TestDecode_SyntaxErrorStillErrors(t *testing.T) {
	for _, in := range []string{`{"model": {`, `{bad`, `[1,2]`} {
		if _, err := Decode([]byte(in)); err == nil {
			t.Errorf("Decode(%q) err = nil, want error", in)
		}
	}
}
