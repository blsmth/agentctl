package tokens_test

import (
	"sync"
	"testing"

	"github.com/blsmth/agentctl/internal/tokens"
)

// resetOverrides ensures each test starts and ends with a clean override
// state. Tests in this file must not run with t.Parallel() since they share
// tokens' package-level override state.
func resetOverrides(t *testing.T) {
	t.Helper()
	tokens.SetOverrides(nil, 0)
	t.Cleanup(func() { tokens.SetOverrides(nil, 0) })
}

// resetReserveOutput ensures each test starts and ends with reserve
// accounting enabled (the documented default), since SetReserveOutput is
// also package-level shared state.
func resetReserveOutput(t *testing.T) {
	t.Helper()
	tokens.SetReserveOutput(true)
	t.Cleanup(func() { tokens.SetReserveOutput(true) })
}

func TestContextWindow(t *testing.T) {
	resetOverrides(t)

	tests := []struct {
		name  string
		model string
		want  int
	}{
		{
			name:  "empty model returns default",
			model: "",
			want:  200_000,
		},
		{
			name:  "standard sonnet returns default",
			model: "claude-sonnet-4-20250514",
			want:  200_000,
		},
		{
			name:  "standard opus returns default",
			model: "claude-opus-4-20250514",
			want:  200_000,
		},
		{
			name:  "1m suffix in lowercase returns 1M",
			model: "claude-sonnet-4-1m",
			want:  1_000_000,
		},
		{
			name:  "1m suffix in uppercase returns 1M",
			model: "claude-sonnet-4-1M",
			want:  1_000_000,
		},
		{
			name:  "1m anywhere in model name returns 1M",
			model: "claude-sonnet-1m-20250514",
			want:  1_000_000,
		},
		{
			name:  "haiku returns default",
			model: "claude-haiku-3-5-20241022",
			want:  200_000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tokens.Lookup(tc.model).ContextWindow
			if got != tc.want {
				t.Errorf("Lookup(%q).ContextWindow = %d, want %d", tc.model, got, tc.want)
			}
		})
	}
}

func TestDefaultContextWindow(t *testing.T) {
	if tokens.DefaultContextWindow != 200_000 {
		t.Errorf("DefaultContextWindow = %d, want 200000", tokens.DefaultContextWindow)
	}
}

func TestLookup_ExactBuiltinMatch(t *testing.T) {
	resetOverrides(t)

	m := tokens.Lookup("claude-sonnet-4")
	if m.Key != "claude-sonnet-4" {
		t.Errorf("Key = %q, want claude-sonnet-4", m.Key)
	}
	if !m.Known {
		t.Error("Known = false, want true for exact built-in match")
	}
	if m.Family != tokens.FamilySonnet {
		t.Errorf("Family = %q, want sonnet", m.Family)
	}
	if m.ContextWindow != 200_000 {
		t.Errorf("ContextWindow = %d, want 200000", m.ContextWindow)
	}
	if m.Pricing.InputPerMTok != 3.0 {
		t.Errorf("InputPerMTok = %v, want 3.0", m.Pricing.InputPerMTok)
	}
}

func TestLookup_PrefixWalk(t *testing.T) {
	resetOverrides(t)

	// Dated model IDs must strip the trailing "-YYYYMMDD" segment via the
	// prefix walk and land on the exact built-in entry, not a coarser one.
	tests := []struct {
		model   string
		wantKey string
	}{
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5"},
		{"claude-haiku-3-5-20241022", "claude-haiku-3-5"},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			m := tokens.Lookup(tc.model)
			if m.Key != tc.wantKey {
				t.Errorf("Key = %q, want %q", m.Key, tc.wantKey)
			}
			if !m.Known {
				t.Error("Known = false, want true for prefix-walked built-in match")
			}
		})
	}
}

// TestLookup_Opus48Regression is the regression test for the bug this
// ticket fixes: "claude-opus-4-8" used to prefix-walk down to the
// "claude-opus-4" built-in (200k window, $15/$75 pricing — RETIRED figures)
// instead of matching its own exact built-in entry (1M window, $5/$25).
func TestLookup_Opus48Regression(t *testing.T) {
	resetOverrides(t)

	m := tokens.Lookup("claude-opus-4-8")
	if m.Key != "claude-opus-4-8" {
		t.Errorf("Key = %q, want claude-opus-4-8 (must not prefix-walk to claude-opus-4)", m.Key)
	}
	if !m.Known {
		t.Error("Known = false, want true — claude-opus-4-8 is an exact built-in entry")
	}
	if m.ContextWindow != 1_000_000 {
		t.Errorf("ContextWindow = %d, want 1000000", m.ContextWindow)
	}
	if m.Pricing.InputPerMTok != 5.0 {
		t.Errorf("InputPerMTok = %v, want 5.0", m.Pricing.InputPerMTok)
	}
}

func TestLookup_FamilyFallback(t *testing.T) {
	resetOverrides(t)

	// claude-opus-9 doesn't exist yet — this exercises the family-fallback
	// path (as opposed to claude-opus-5, which is now an exact built-in).
	m := tokens.Lookup("claude-opus-9")
	if m.Known {
		t.Error("Known = true, want false for family fallback")
	}
	if m.Family != tokens.FamilyOpus {
		t.Errorf("Family = %q, want opus", m.Family)
	}
	if m.Short != "opus" {
		t.Errorf("Short = %q, want opus", m.Short)
	}
	if m.Label != "opus-9" {
		t.Errorf("Label = %q, want opus-9", m.Label)
	}
	if m.ContextWindow != 1_000_000 {
		t.Errorf("ContextWindow = %d, want 1000000 (newest opus generation)", m.ContextWindow)
	}
	if m.MaxOutput != 128_000 {
		t.Errorf("MaxOutput = %d, want 128000 (newest opus generation)", m.MaxOutput)
	}
	if m.Pricing.InputPerMTok != 5.0 {
		t.Errorf("InputPerMTok = %v, want 5.0 (newest opus generation)", m.Pricing.InputPerMTok)
	}
}

func TestLookup_TotalUnknown(t *testing.T) {
	resetOverrides(t)

	m := tokens.Lookup("gpt-4")
	if m.Known {
		t.Error("Known = true, want false for total unknown model")
	}
	if m.ContextWindow != tokens.DefaultContextWindow {
		t.Errorf("ContextWindow = %d, want %d", m.ContextWindow, tokens.DefaultContextWindow)
	}
	if m.Short != "gpt-4" {
		t.Errorf("Short = %q, want gpt-4", m.Short)
	}
	if m.Pricing != (tokens.Pricing{}) {
		t.Errorf("Pricing = %+v, want zero value", m.Pricing)
	}
}

func TestLookup_SentinelValues(t *testing.T) {
	resetOverrides(t)

	for _, model := range []string{"", "<synthetic>", "  ", "<SYNTHETIC>"} {
		m := tokens.Lookup(model)
		if m.Family != tokens.FamilyUnknown {
			t.Errorf("Lookup(%q).Family = %q, want unknown", model, m.Family)
		}
		if m.Short != "" {
			t.Errorf("Lookup(%q).Short = %q, want empty", model, m.Short)
		}
		if m.Known {
			t.Errorf("Lookup(%q).Known = true, want false", model)
		}
	}
}

func TestLookup_1MMarker(t *testing.T) {
	resetOverrides(t)

	for _, model := range []string{"claude-sonnet-4-1m", "claude-sonnet-4-1M", "sonnet[1m]"} {
		m := tokens.Lookup(model)
		if m.ContextWindow != 1_000_000 {
			t.Errorf("Lookup(%q).ContextWindow = %d, want 1000000", model, m.ContextWindow)
		}
	}
}

func TestLookup_1MMarkerOnFamilyFallback(t *testing.T) {
	resetOverrides(t)

	m := tokens.Lookup("claude-opus-9-1m")
	if m.ContextWindow != 1_000_000 {
		t.Errorf("ContextWindow = %d, want 1000000", m.ContextWindow)
	}
	if m.Known {
		t.Error("Known = true, want false — this should still be a family fallback")
	}
	if m.Family != tokens.FamilyOpus {
		t.Errorf("Family = %q, want opus", m.Family)
	}
}

func TestLookup_ConfigOverridePrecedence(t *testing.T) {
	resetOverrides(t)

	// Override at an exact key beats the built-in exact entry.
	tokens.SetOverrides(map[string]tokens.ModelOverride{
		"claude-sonnet-4": {ContextWindow: 999_000},
	}, 0)
	m := tokens.Lookup("claude-sonnet-4")
	if m.ContextWindow != 999_000 {
		t.Errorf("ContextWindow = %d, want 999000 (override should beat built-in at same specificity)", m.ContextWindow)
	}
	if m.Source != "config" {
		t.Errorf("Source = %q, want config", m.Source)
	}

	// A built-in EXACT match must not be beaten by an override that only
	// matches a shorter prefix. claude-opus-4 is an exact built-in entry;
	// an override for the shorter prefix "claude-opus" must not win.
	tokens.SetOverrides(map[string]tokens.ModelOverride{
		"claude-opus": {ContextWindow: 111_000},
	}, 0)
	m2 := tokens.Lookup("claude-opus-4")
	if m2.Source != "built-in" {
		t.Errorf("Source = %q, want built-in (exact built-in match must beat a coarser override prefix)", m2.Source)
	}
	if m2.ContextWindow != 200_000 {
		t.Errorf("ContextWindow = %d, want 200000 (unaffected by the coarser override)", m2.ContextWindow)
	}
}

func TestLookup_PartialOverride(t *testing.T) {
	resetOverrides(t)

	// Only context_window is overridden; pricing should still come from the
	// built-in entry, not be zeroed out.
	tokens.SetOverrides(map[string]tokens.ModelOverride{
		"claude-sonnet-4": {ContextWindow: 500_000},
	}, 0)

	m := tokens.Lookup("claude-sonnet-4")
	if m.ContextWindow != 500_000 {
		t.Errorf("ContextWindow = %d, want 500000", m.ContextWindow)
	}
	if m.Pricing.InputPerMTok != 3.0 {
		t.Errorf("InputPerMTok = %v, want 3.0 (from built-in, not zeroed)", m.Pricing.InputPerMTok)
	}
}

func TestLookup_PartialOverrideOnFamilyFallback(t *testing.T) {
	resetOverrides(t)

	// Override a model that would otherwise resolve via family fallback.
	tokens.SetOverrides(map[string]tokens.ModelOverride{
		"claude-opus-9": {ContextWindow: 2_000_000},
	}, 0)

	m := tokens.Lookup("claude-opus-9")
	if m.ContextWindow != 2_000_000 {
		t.Errorf("ContextWindow = %d, want 2000000", m.ContextWindow)
	}
	if m.Pricing.InputPerMTok != 5.0 {
		t.Errorf("InputPerMTok = %v, want 5.0 (newest opus family default, not zeroed)", m.Pricing.InputPerMTok)
	}
	if m.Source != "config" {
		t.Errorf("Source = %q, want config", m.Source)
	}
}

func TestSetOverrides_ResetsToBuiltinBehavior(t *testing.T) {
	resetOverrides(t)

	tokens.SetOverrides(map[string]tokens.ModelOverride{
		"claude-sonnet-4": {ContextWindow: 999_000},
	}, 0)
	if got := tokens.Lookup("claude-sonnet-4").ContextWindow; got != 999_000 {
		t.Fatalf("precondition failed: ContextWindow = %d, want 999000", got)
	}

	tokens.SetOverrides(nil, 0)
	if got := tokens.Lookup("claude-sonnet-4").ContextWindow; got != 200_000 {
		t.Errorf("ContextWindow after reset = %d, want 200000", got)
	}
}

func TestSetOverrides_DefaultWindow(t *testing.T) {
	resetOverrides(t)

	tokens.SetOverrides(nil, 750_000)
	m := tokens.Lookup("some-totally-unknown-model")
	if m.ContextWindow != 750_000 {
		t.Errorf("ContextWindow = %d, want 750000", m.ContextWindow)
	}
}

func TestAbbreviate(t *testing.T) {
	resetOverrides(t)

	tests := []struct {
		model string
		want  string
	}{
		{"claude-opus-4", "opus-4"},
		{"claude-sonnet-4", "sonnet-4"},
		{"claude-opus-4-8", "opus-4.8"},              // known model
		{"claude-sonnet-4-5-20250929", "sonnet-4.5"}, // dated model, prefix-walked
		{"claude-opus-9", "opus-9"},                  // unknown-but-family model
		{"claude-opus-5", "opus-5"},
		{"gpt-4", "gpt-4"},
		{"", ""},
		{"<synthetic>", ""},
	}
	for _, tc := range tests {
		if got := tokens.Abbreviate(tc.model); got != tc.want {
			t.Errorf("Abbreviate(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

func TestRegistry_SortedAndIncludesOverrides(t *testing.T) {
	resetOverrides(t)

	tokens.SetOverrides(map[string]tokens.ModelOverride{
		"claude-opus-9": {ContextWindow: 2_000_000},
	}, 0)

	reg := tokens.Registry()
	if len(reg) == 0 {
		t.Fatal("Registry() returned empty slice")
	}
	// Sorted by family ascending, then Label descending (newest first)
	// within a family — e.g. opus-5 must sort above opus-4.8 above opus-4.
	for i := 1; i < len(reg); i++ {
		prev, cur := reg[i-1], reg[i]
		if prev.Family != cur.Family {
			if prev.Family > cur.Family {
				t.Fatalf("Registry() not sorted by family: %q before %q", prev.Family, cur.Family)
			}
			continue
		}
		if prev.Label < cur.Label {
			t.Fatalf("Registry() not sorted by Label descending within family %q: %q before %q", prev.Family, prev.Label, cur.Label)
		}
	}
	found := false
	for _, m := range reg {
		if m.Key == "claude-opus-9" {
			found = true
			if m.ContextWindow != 2_000_000 {
				t.Errorf("claude-opus-9.ContextWindow = %d, want 2000000", m.ContextWindow)
			}
			if m.Source != "config" {
				t.Errorf("claude-opus-9.Source = %q, want config", m.Source)
			}
		}
	}
	if !found {
		t.Error("Registry() missing config-overridden claude-opus-9")
	}
}

func TestLookup_ConcurrentSetOverrides(t *testing.T) {
	resetOverrides(t)

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			tokens.SetOverrides(map[string]tokens.ModelOverride{
				"claude-sonnet-4": {ContextWindow: 500_000 + i},
			}, 0)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = tokens.Lookup("claude-sonnet-4")
			_ = tokens.Lookup("claude-opus-5")
			_ = tokens.Lookup("gpt-4")
		}
	}()

	// A second Lookup-only goroutine hitting different unrecognized models,
	// to exercise the family-fallback and unknown-model paths under
	// concurrent access from two goroutines simultaneously.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = tokens.Lookup("totally-unknown-model-a")
			_ = tokens.Lookup("totally-unknown-model-b")
			_ = tokens.Lookup("claude-opus-99")
		}
	}()

	wg.Wait()
}

// ── Phase 2: UsableWindow ────────────────────────────────────────────────────

func TestUsableWindow(t *testing.T) {
	resetOverrides(t)
	resetReserveOutput(t)

	tests := []struct {
		name  string
		model string
		want  int
	}{
		{
			name:  "opus family fallback: window minus reserve",
			model: "claude-opus-9", // resolves via family fallback: newest gen, 1M window, 128k reserve
			want:  872_000,
		},
		{
			name:  "sonnet built-in: window minus reserve",
			model: "claude-sonnet-4",
			want:  136_000, // 200_000 - 64_000
		},
		{
			name:  "unknown model: default window, zero reserve",
			model: "gpt-4",
			want:  200_000,
		},
		{
			name:  "empty/sentinel model: default window, zero reserve",
			model: "",
			want:  200_000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokens.UsableWindow(tt.model)
			if got != tt.want {
				t.Errorf("UsableWindow(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}

// TestUsableWindow_ReserveOutputDisabled verifies that SetReserveOutput(false)
// makes UsableWindow return the full ContextWindow, ignoring MaxOutput.
func TestUsableWindow_ReserveOutputDisabled(t *testing.T) {
	resetOverrides(t)
	resetReserveOutput(t)

	tokens.SetReserveOutput(false)

	got := tokens.UsableWindow("claude-opus-4")
	want := tokens.Lookup("claude-opus-4").ContextWindow
	if got != want {
		t.Errorf("UsableWindow with reserve disabled = %d, want %d (full ContextWindow)", got, want)
	}
}

// TestUsableWindow_FiftyPercentFloor verifies that a bogus MaxOutput
// override (larger than half the context window) cannot collapse the usable
// budget below 50% of the total window.
func TestUsableWindow_FiftyPercentFloor(t *testing.T) {
	resetOverrides(t)
	resetReserveOutput(t)

	tokens.SetOverrides(map[string]tokens.ModelOverride{
		"claude-bogus-model": {
			ContextWindow: 100_000,
			MaxOutput:     90_000, // would leave only 10_000 usable without the floor
		},
	}, 0)

	got := tokens.UsableWindow("claude-bogus-model")
	want := 50_000 // 50% of 100_000
	if got != want {
		t.Errorf("UsableWindow with bogus MaxOutput override = %d, want %d (50%% floor)", got, want)
	}
}

// ── Phase 2: CtxPct ──────────────────────────────────────────────────────────

func TestCtxPct(t *testing.T) {
	resetOverrides(t)
	resetReserveOutput(t)

	tests := []struct {
		name  string
		model string
		used  int
		want  int
	}{
		{"zero used", "claude-sonnet-4", 0, 0},
		{"negative used", "claude-sonnet-4", -100, 0},
		{"exact 100%", "gpt-4", 200_000, 100}, // unknown model: usable == 200_000, no reserve
		{"over-100% clamps", "gpt-4", 999_999, 100},
		{"partial usage", "gpt-4", 100_000, 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokens.CtxPct(tt.model, tt.used)
			if got != tt.want {
				t.Errorf("CtxPct(%q, %d) = %d, want %d", tt.model, tt.used, got, tt.want)
			}
		})
	}
}

// TestCtxPct_NeverPanics is a light fuzz-style guard: CtxPct must never
// panic (divide-by-zero or otherwise) across a spread of models and used
// values, including negative and huge inputs.
func TestCtxPct_NeverPanics(t *testing.T) {
	resetOverrides(t)
	resetReserveOutput(t)

	models := []string{"", "<synthetic>", "claude-opus-4", "claude-sonnet-4-1m", "totally-unknown"}
	used := []int{-1_000_000, -1, 0, 1, 200_000, 1_000_000_000}

	for _, model := range models {
		for _, u := range used {
			got := tokens.CtxPct(model, u)
			if got < 0 || got > 100 {
				t.Errorf("CtxPct(%q, %d) = %d, out of [0,100] range", model, u, got)
			}
		}
	}
}

// ── Full model registry (ticket: fix stale/missing model entries) ──────────

// TestLookup_RealWorldModels asserts exact resolution for every model string
// observed in real Claude Code session logs on this machine, plus two dated
// IDs (claude-sonnet-4-5-20250929, claude-haiku-3-5-20241022) to prove dated
// model IDs still prefix-walk correctly onto their exact built-in entry.
func TestLookup_RealWorldModels(t *testing.T) {
	resetOverrides(t)

	tests := []struct {
		model         string
		wantKey       string
		wantLabel     string
		wantWindow    int
		wantMaxOutput int
		wantIn        float64
		wantOut       float64
		wantSource    string
	}{
		{
			model: "claude-opus-4-8", wantKey: "claude-opus-4-8", wantLabel: "opus-4.8",
			wantWindow: 1_000_000, wantMaxOutput: 128_000, wantIn: 5.00, wantOut: 25.00, wantSource: "built-in",
		},
		{
			model: "claude-opus-5", wantKey: "claude-opus-5", wantLabel: "opus-5",
			wantWindow: 1_000_000, wantMaxOutput: 128_000, wantIn: 5.00, wantOut: 25.00, wantSource: "built-in",
		},
		{
			model: "claude-sonnet-4-6", wantKey: "claude-sonnet-4-6", wantLabel: "sonnet-4.6",
			wantWindow: 1_000_000, wantMaxOutput: 128_000, wantIn: 3.00, wantOut: 15.00, wantSource: "built-in",
		},
		{
			model: "claude-sonnet-5", wantKey: "claude-sonnet-5", wantLabel: "sonnet-5",
			wantWindow: 1_000_000, wantMaxOutput: 128_000, wantIn: 2.00, wantOut: 10.00, wantSource: "built-in",
		},
		{
			model: "claude-haiku-4-5-20251001", wantKey: "claude-haiku-4-5", wantLabel: "haiku-4.5",
			wantWindow: 200_000, wantMaxOutput: 64_000, wantIn: 1.00, wantOut: 5.00, wantSource: "built-in",
		},
		{
			model: "claude-opus-4-7", wantKey: "claude-opus-4-7", wantLabel: "opus-4.7",
			wantWindow: 1_000_000, wantMaxOutput: 128_000, wantIn: 5.00, wantOut: 25.00, wantSource: "built-in",
		},
		{
			model: "claude-sonnet-4-5-20250929", wantKey: "claude-sonnet-4-5", wantLabel: "sonnet-4.5",
			wantWindow: 200_000, wantMaxOutput: 64_000, wantIn: 3.00, wantOut: 15.00, wantSource: "built-in",
		},
		{
			model: "claude-haiku-3-5-20241022", wantKey: "claude-haiku-3-5", wantLabel: "haiku-3.5",
			wantWindow: 200_000, wantMaxOutput: 32_000, wantIn: 0.80, wantOut: 4.00, wantSource: "built-in",
		},
	}

	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			m := tokens.Lookup(tc.model)
			if m.Key != tc.wantKey {
				t.Errorf("Key = %q, want %q", m.Key, tc.wantKey)
			}
			if m.Label != tc.wantLabel {
				t.Errorf("Label = %q, want %q", m.Label, tc.wantLabel)
			}
			if m.ContextWindow != tc.wantWindow {
				t.Errorf("ContextWindow = %d, want %d", m.ContextWindow, tc.wantWindow)
			}
			if m.MaxOutput != tc.wantMaxOutput {
				t.Errorf("MaxOutput = %d, want %d", m.MaxOutput, tc.wantMaxOutput)
			}
			if m.Pricing.InputPerMTok != tc.wantIn {
				t.Errorf("InputPerMTok = %v, want %v", m.Pricing.InputPerMTok, tc.wantIn)
			}
			if m.Pricing.OutputPerMTok != tc.wantOut {
				t.Errorf("OutputPerMTok = %v, want %v", m.Pricing.OutputPerMTok, tc.wantOut)
			}
			if m.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", m.Source, tc.wantSource)
			}
		})
	}
}

// TestLookup_UnknownFutureModelFamilyFallback verifies that a model newer
// than anything in the built-in table falls back to the newest known
// generation for its family, with a derived (not bare-family) Label.
func TestLookup_UnknownFutureModelFamilyFallback(t *testing.T) {
	resetOverrides(t)

	m := tokens.Lookup("claude-opus-9")
	if m.Known {
		t.Error("Known = true, want false for family fallback")
	}
	if m.Label != "opus-9" {
		t.Errorf("Label = %q, want opus-9", m.Label)
	}
	if m.ContextWindow != 1_000_000 {
		t.Errorf("ContextWindow = %d, want 1000000 (newest opus generation)", m.ContextWindow)
	}
	if m.Pricing.InputPerMTok != 5.0 {
		t.Errorf("InputPerMTok = %v, want 5.0 (newest opus generation)", m.Pricing.InputPerMTok)
	}
}

// TestAbbreviate_Extended covers the Abbreviate cases called out explicitly
// in the ticket: known model, dated model, unknown-but-family model,
// <synthetic>, and empty string.
func TestAbbreviate_Extended(t *testing.T) {
	resetOverrides(t)

	tests := []struct {
		name  string
		model string
		want  string
	}{
		{"known model", "claude-opus-4-8", "opus-4.8"},
		{"dated model", "claude-sonnet-4-5-20250929", "sonnet-4.5"},
		{"unknown-but-family model", "claude-opus-9", "opus-9"},
		{"synthetic sentinel", "<synthetic>", ""},
		{"empty string", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tokens.Abbreviate(tc.model); got != tc.want {
				t.Errorf("Abbreviate(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}

// TestRegistry_IncludesAllRealWorldModels confirms every real-world model
// string's resolved key is present in Registry() as a built-in entry.
func TestRegistry_IncludesAllRealWorldModels(t *testing.T) {
	resetOverrides(t)

	wantKeys := []string{
		"claude-opus-4-8",
		"claude-opus-5",
		"claude-sonnet-4-6",
		"claude-sonnet-5",
		"claude-haiku-4-5",
		"claude-opus-4-7",
	}

	reg := tokens.Registry()
	present := make(map[string]bool, len(reg))
	for _, m := range reg {
		present[m.Key] = true
	}
	for _, k := range wantKeys {
		if !present[k] {
			t.Errorf("Registry() missing built-in key %q", k)
		}
	}
}
