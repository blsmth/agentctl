package statusline

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/render"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/theme"
)

// ansiEscapeRE matches any CSI SGR escape sequence lipgloss emits (e.g.
// "\x1b[38;2;166;227;161m" or the plain reset "\x1b[m").
var ansiEscapeRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// stripANSI removes every ANSI escape sequence from s. Styling
// injects escapes INSIDE substrings that used to be assembled from a single
// fmt.Sprintf (e.g. the label "5h " and the number "24%" are now two
// separately-styled Render() calls, so the raw string no longer contains the
// literal "5h 24%"). Every content assertion in this file routes through this
// helper so it keeps asserting exact text rather than being loosened to
// tolerate escapes.
func stripANSI(s string) string {
	return ansiEscapeRE.ReplaceAllString(s, "")
}

// ansiFg returns the exact truecolor SGR prefix lipgloss emits for a
// "#RRGGBB" theme color, e.g. "#a6e3a1" -> "\x1b[38;2;166;227;161m". Used by
// the threshold tests below to assert a specific color was applied, not just
// that *some* escape is present.
func ansiFg(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	r, _ := strconv.ParseInt(hex[0:2], 16, 64)
	g, _ := strconv.ParseInt(hex[2:4], 16, 64)
	b, _ := strconv.ParseInt(hex[4:6], 16, 64)
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

// ── renderRich: both lines present ──────────────────────────────────────────

func TestRenderRich_FullPayload_BothLinesPresent(t *testing.T) {
	p, err := Decode([]byte(fullPayload))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	snap := &state.AgentState{TurnCount: 12, StartTime: 1}
	lines := renderRich(p, 200, snap)
	if len(lines) != 3 {
		t.Fatalf("renderRich() = %d lines, want 3: %v", len(lines), lines)
	}
	line1 := stripANSI(lines[0])
	line2 := stripANSI(lines[1])
	line3 := stripANSI(lines[2])
	if !strings.Contains(line1, "5h 24%") {
		t.Errorf("line 1 = %q, want to contain '5h 24%%' (23.5 rounded)", line1)
	}
	if !strings.Contains(line1, "7d 41%") {
		t.Errorf("line 1 = %q, want to contain '7d 41%%'", line1)
	}
	if !strings.Contains(line1, "spend 63%") {
		t.Errorf("line 1 = %q, want to contain 'spend 63%%' (62.8 rounded)", line1)
	}
	// fullPayload's model.display_name is "Sonnet 4" -- spec priority is
	// DisplayName over ID, so this is what render.AbbreviateModel sees (and,
	// since it isn't a recognized registry key, falls back to the
	// normalized string itself rather than a nicer "sonnet-4" abbreviation).
	if !strings.Contains(line2, "sonnet 4") {
		t.Errorf("line 2 = %q, want to contain model segment 'sonnet 4' (from display_name)", line2)
	}
	if !strings.Contains(line2, "13% ctx") {
		t.Errorf("line 2 = %q, want to contain '13%% ctx' (12.5 rounds up)", line2)
	}
	if !strings.Contains(line2, "75% cached") {
		t.Errorf("line 2 = %q, want to contain '75%% cached' (hit_ratio 0.75)", line2)
	}
	if !strings.Contains(line2, "12 turns") {
		t.Errorf("line 2 = %q, want to contain '12 turns'", line2)
	}
	if !strings.Contains(line2, "$1.23") {
		t.Errorf("line 2 = %q, want to contain '$1.23'", line2)
	}
	// Line 3: fullPayload's workspace.current_dir, git_worktree, and pr.
	if !strings.Contains(line3, "/home/user/project") {
		t.Errorf("line 3 = %q, want to contain directory '/home/user/project'", line3)
	}
	if !strings.Contains(line3, "issue-1023") {
		t.Errorf("line 3 = %q, want to contain worktree 'issue-1023'", line3)
	}
	if !strings.Contains(line3, "PR #1023") {
		t.Errorf("line 3 = %q, want to contain 'PR #1023'", line3)
	}
	if !strings.Contains(line3, "approved") {
		t.Errorf("line 3 = %q, want to contain review state 'approved'", line3)
	}
}

// TestRenderRich_EmitsColorEscapes is the regression test for a shipped bug: the
// rich statusline shipped with battery bars colored and every other segment
// plain text. An unstyled build must fail this test loudly instead of
// shipping silently again.
func TestRenderRich_EmitsColorEscapes(t *testing.T) {
	p, err := Decode([]byte(fullPayload))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	snap := &state.AgentState{TurnCount: 12, StartTime: 1}
	lines := renderRich(p, 200, snap)
	if len(lines) != 3 {
		t.Fatalf("renderRich() = %d lines, want 3: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "\x1b[") {
		t.Errorf("line 1 = %q, want at least one ANSI escape (rate-limit segments must be styled)", lines[0])
	}
	if !strings.Contains(lines[1], "\x1b[") {
		t.Errorf("line 2 = %q, want at least one ANSI escape", lines[1])
	}
	if !strings.Contains(lines[2], "\x1b[") {
		t.Errorf("line 3 = %q, want at least one ANSI escape (workspace/git segments must be styled)", lines[2])
	}

	// ctx%, cached, and turns must each carry a color, not just the battery
	// bar. Isolate each segment's rendered text and assert it contains an
	// escape sequence of its own.
	th := theme.Current()
	if !strings.Contains(lines[1], ansiFg(th.Green)+"13%") {
		t.Errorf("line 2 = %q, want ctx%% 13%% styled green (%s)", lines[1], ansiFg(th.Green))
	}
	if !strings.Contains(lines[1], ansiFg(th.CacheGood)+"75%") {
		t.Errorf("line 2 = %q, want cached 75%% styled CacheGood (%s)", lines[1], ansiFg(th.CacheGood))
	}
	if !strings.Contains(lines[1], ansiFg(th.TurnNormal)+"12") {
		t.Errorf("line 2 = %q, want turns 12 styled TurnNormal (%s)", lines[1], ansiFg(th.TurnNormal))
	}
}

// ── renderRich: rate_limits nil → exactly one line ──────────────────────────

func TestRenderRich_RateLimitsNil_ExactlyOneLine(t *testing.T) {
	p := &Payload{
		Model: &Model{DisplayName: strPtr("Sonnet 4")},
	}
	lines := renderRich(p, 200, nil)
	if len(lines) != 1 {
		t.Fatalf("renderRich() = %d lines, want exactly 1 (no leading blank line): %v", len(lines), lines)
	}
	if lines[0] == "" {
		t.Error("the one line must not be empty")
	}
}

// ── renderRich: everything absent → noDataLine ──────────────────────────────

func TestRenderRich_EverythingAbsent_NoDataLine(t *testing.T) {
	lines := renderRich(&Payload{}, 200, nil)
	if len(lines) != 1 {
		t.Fatalf("renderRich(empty) = %d lines, want exactly 1: %v", len(lines), lines)
	}
	if lines[0] != noDataLine {
		t.Errorf("renderRich(empty) = %q, want %q", lines[0], noDataLine)
	}
	if lines[0] == "" {
		t.Fatal("line must never be empty")
	}
}

// ── renderRich: cost thresholds ─────────────────────────────────────────────

func TestRenderRichLine2_CostThresholds(t *testing.T) {
	tests := []struct {
		name     string
		cost     float64
		wantSeg  string // "" means the cost segment must be entirely absent
		wantOmit bool
	}{
		{"zero omitted", 0.0, "", true},
		{"just under floor omitted", 0.004, "", true},
		{"at floor rendered", 0.005, "$0.01", false},
		{"typical value rounds", 13.434, "$13.43", false},
		{"negative clamped then omitted", -5.0, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Payload{
				Model: &Model{DisplayName: strPtr("Sonnet 4")},
				Cost:  &Cost{TotalCostUSD: f64Ptr(tt.cost)},
			}
			line, ok := renderRichLine2(p, 200, nil)
			if !ok {
				t.Fatalf("renderRichLine2() ok = false, want true (model segment always present)")
			}
			line = stripANSI(line)
			hasDollar := strings.Contains(line, "$")
			if tt.wantOmit && hasDollar {
				t.Errorf("cost %v: line = %q, want no '$' segment", tt.cost, line)
			}
			if !tt.wantOmit {
				if !strings.Contains(line, tt.wantSeg) {
					t.Errorf("cost %v: line = %q, want to contain %q", tt.cost, line, tt.wantSeg)
				}
			}
		})
	}
}

// ── renderRich: hit_ratio is a fraction, not a percentage ──────────────────

func TestRenderRichLine2_CacheIsFractionNotPercent(t *testing.T) {
	p := &Payload{
		Model:       &Model{DisplayName: strPtr("Sonnet 4")},
		PromptCache: &PromptCache{HitRatio: f64Ptr(0.78)},
	}
	line, ok := renderRichLine2(p, 200, nil)
	if !ok {
		t.Fatal("renderRichLine2() ok = false")
	}
	line = stripANSI(line)
	// A test that would pass whether HitRatio were read as a fraction or a
	// percentage is worthless -- assert the fraction reading (78%), not the
	// percentage misreading (0%).
	if !strings.Contains(line, "78% cached") {
		t.Errorf("line = %q, want to contain '78%% cached' (0.78 fraction * 100)", line)
	}
	if strings.Contains(line, "0% cached") {
		t.Errorf("line = %q, must not contain '0%% cached' (would mean hit_ratio misread as a percentage)", line)
	}
}

// TestRenderRichLine2_CacheThresholdBoundaries pins the cache hit-rate color
// cutoffs recovered from the deleted chrome_bar.go renderMetricsLine: <=29
// CacheBad, <=59 CacheWarn, else CacheGood.
func TestRenderRichLine2_CacheThresholdBoundaries(t *testing.T) {
	th := theme.Current()
	tests := []struct {
		name      string
		hitRatio  float64 // fraction
		wantPct   string
		wantColor string
	}{
		{"29% is bad (at cutoff)", 0.29, "29%", th.CacheBad},
		{"30% is warn (just above cutoff)", 0.30, "30%", th.CacheWarn},
		{"59% is warn (at cutoff)", 0.59, "59%", th.CacheWarn},
		{"60% is good (just above cutoff)", 0.60, "60%", th.CacheGood},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Payload{
				Model:       &Model{DisplayName: strPtr("Sonnet 4")},
				PromptCache: &PromptCache{HitRatio: f64Ptr(tt.hitRatio)},
			}
			line, ok := renderRichLine2(p, 200, nil)
			if !ok {
				t.Fatal("renderRichLine2() ok = false")
			}
			want := ansiFg(tt.wantColor) + tt.wantPct
			if !strings.Contains(line, want) {
				t.Errorf("line = %q, want to contain %q (color %s)", line, want, tt.wantColor)
			}
		})
	}
}

// ── renderRich: cache cascade (payload hit_ratio vs. state.json fallback) ──

// TestRenderRichLine2_CacheCascade is the table-driven cascade test the spec
// asks for: payload hit_ratio present wins; payload absent falls back to
// state.AgentState's cumulative counters; both absent omits the segment;
// InputTokens == 0 omits the fallback (would divide by zero); and a
// computed percentage of exactly 0 is omitted from either source, matching
// the deleted chrome_bar.go's deliberate `if cachePct > 0` guard.
func TestRenderRichLine2_CacheCascade(t *testing.T) {
	tests := []struct {
		name        string
		promptCache *PromptCache
		snap        *state.AgentState
		wantSeg     string // "" means the cached segment must be entirely absent
	}{
		{
			name:        "payload hit_ratio present wins",
			promptCache: &PromptCache{HitRatio: f64Ptr(0.65)},
			snap:        &state.AgentState{InputTokens: 1000, CacheReadTokens: 100}, // would derive 10% -- must not be used
			wantSeg:     "65% cached",
		},
		{
			name:        "payload absent, state.json fallback used",
			promptCache: nil,
			snap:        &state.AgentState{InputTokens: 1000, CacheReadTokens: 650},
			wantSeg:     "65% cached",
		},
		{
			name:        "both absent: no segment",
			promptCache: nil,
			snap:        nil,
			wantSeg:     "",
		},
		{
			name:        "InputTokens == 0: fallback guarded against divide-by-zero",
			promptCache: nil,
			snap:        &state.AgentState{InputTokens: 0, CacheReadTokens: 50},
			wantSeg:     "",
		},
		{
			name:        "payload hit_ratio computes to 0: segment omitted (no data, not 0%)",
			promptCache: &PromptCache{HitRatio: f64Ptr(0)},
			snap:        nil,
			wantSeg:     "",
		},
		{
			name:        "state.json fallback computes to 0: segment omitted",
			promptCache: nil,
			snap:        &state.AgentState{InputTokens: 1000, CacheReadTokens: 0},
			wantSeg:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Payload{
				Model:       &Model{DisplayName: strPtr("Sonnet 4")},
				PromptCache: tt.promptCache,
			}
			line, ok := renderRichLine2(p, 200, tt.snap)
			if !ok {
				t.Fatal("renderRichLine2() ok = false")
			}
			line = stripANSI(line)
			hasCached := strings.Contains(line, "cached")
			if tt.wantSeg == "" {
				if hasCached {
					t.Errorf("line = %q, want no 'cached' segment", line)
				}
				return
			}
			if !strings.Contains(line, tt.wantSeg) {
				t.Errorf("line = %q, want to contain %q", line, tt.wantSeg)
			}
		})
	}
}

// ── renderRich: turn:/total: token segments ─────────────────────────────────

func TestRenderRichLine2_LastTurnTokens(t *testing.T) {
	tests := []struct {
		name    string
		snap    *state.AgentState
		wantSeg string
		wantAbs bool
	}{
		{
			name:    "present when either input or output is non-zero",
			snap:    &state.AgentState{LastTurnInput: 81263, LastTurnOutput: 1121},
			wantSeg: "turn: 81k↑ 1k↓",
		},
		{
			name:    "present with only input",
			snap:    &state.AgentState{LastTurnInput: 5000},
			wantSeg: "turn: 5k↑ 0↓",
		},
		{
			name:    "absent when both zero",
			snap:    &state.AgentState{LastTurnInput: 0, LastTurnOutput: 0},
			wantAbs: true,
		},
		{
			name:    "absent when snap is nil",
			snap:    nil,
			wantAbs: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Payload{Model: &Model{DisplayName: strPtr("Sonnet 4")}}
			line, ok := renderRichLine2(p, 200, tt.snap)
			if !ok {
				t.Fatal("renderRichLine2() ok = false")
			}
			line = stripANSI(line)
			if tt.wantAbs {
				if strings.Contains(line, "turn:") {
					t.Errorf("line = %q, want no 'turn:' segment", line)
				}
				return
			}
			if !strings.Contains(line, tt.wantSeg) {
				t.Errorf("line = %q, want to contain %q", line, tt.wantSeg)
			}
		})
	}
}

func TestRenderRichLine2_CumulativeTokens(t *testing.T) {
	tests := []struct {
		name    string
		snap    *state.AgentState
		wantSeg string
		wantAbs bool
	}{
		{
			name:    "present with non-zero cumulative totals",
			snap:    &state.AgentState{InputTokens: 789213, OutputTokens: 6339},
			wantSeg: "total: 789k↑ 6k↓",
		},
		{
			name:    "all-zero is skipped entirely (deliberate deviation from chrome_bar's 0↑ 0↓ placeholder)",
			snap:    &state.AgentState{InputTokens: 0, OutputTokens: 0},
			wantAbs: true,
		},
		{
			name:    "absent when snap is nil",
			snap:    nil,
			wantAbs: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Payload{Model: &Model{DisplayName: strPtr("Sonnet 4")}}
			line, ok := renderRichLine2(p, 200, tt.snap)
			if !ok {
				t.Fatal("renderRichLine2() ok = false")
			}
			line = stripANSI(line)
			if tt.wantAbs {
				if strings.Contains(line, "total:") {
					t.Errorf("line = %q, want no 'total:' segment", line)
				}
				return
			}
			if !strings.Contains(line, tt.wantSeg) {
				t.Errorf("line = %q, want to contain %q", line, tt.wantSeg)
			}
		})
	}
}

// TestRenderRichLine2_TurnAndTotalStyled asserts the new segments carry ANSI
// escapes -- a plain-substring assertion over styled output is a ratchet
// against ever adding style (see TestRenderRich_EmitsColorEscapes's doc
// comment for the history behind this discipline).
func TestRenderRichLine2_TurnAndTotalStyled(t *testing.T) {
	p := &Payload{Model: &Model{DisplayName: strPtr("Sonnet 4")}}
	snap := &state.AgentState{
		LastTurnInput: 81263, LastTurnOutput: 1121,
		InputTokens: 789213, OutputTokens: 6339,
	}
	line, ok := renderRichLine2(p, 999, snap)
	if !ok {
		t.Fatal("renderRichLine2() ok = false")
	}
	if !strings.Contains(line, "\x1b[") {
		t.Fatalf("line = %q, want at least one ANSI escape", line)
	}
	stripped := stripANSI(line)
	if !strings.Contains(stripped, "turn: 81k↑ 1k↓") {
		t.Errorf("line (stripped) = %q, want 'turn: 81k↑ 1k↓'", stripped)
	}
	if !strings.Contains(stripped, "total: 789k↑ 6k↓") {
		t.Errorf("line (stripped) = %q, want 'total: 789k↑ 6k↓'", stripped)
	}
}

// ── renderRich: turns thresholds ────────────────────────────────────────────

// TestRenderRichLine2_TurnsThresholdBoundaries pins the turn-count color
// cutoffs recovered from the deleted chrome_bar.go renderMetricsLine: 0
// Muted, <=20 TurnNormal, <=50 TurnWarn, else TurnHot.
func TestRenderRichLine2_TurnsThresholdBoundaries(t *testing.T) {
	th := theme.Current()
	tests := []struct {
		name      string
		turns     int
		wantColor string
	}{
		{"20 turns is normal (at cutoff)", 20, th.TurnNormal},
		{"21 turns is warn (just above cutoff)", 21, th.TurnWarn},
		{"50 turns is warn (at cutoff)", 50, th.TurnWarn},
		{"51 turns is hot (just above cutoff)", 51, th.TurnHot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Payload{Model: &Model{DisplayName: strPtr("Sonnet 4")}}
			snap := &state.AgentState{TurnCount: tt.turns}
			line, ok := renderRichLine2(p, 200, snap)
			if !ok {
				t.Fatal("renderRichLine2() ok = false")
			}
			want := ansiFg(tt.wantColor) + fmt.Sprintf("%d", tt.turns)
			if !strings.Contains(line, want) {
				t.Errorf("line = %q, want to contain %q (color %s)", line, want, tt.wantColor)
			}
		})
	}
}

// ── renderRich: ctx% thresholds ──────────────────────────────────────────────

// TestRenderRichLine2_CtxThresholdBoundaries pins the ctx% color cutoffs
// against the CONFIGURED warn/critical thresholds (defaults 75/90), not
// hardcoded values, matching the deleted chrome_bar.go exactly.
func TestRenderRichLine2_CtxThresholdBoundaries(t *testing.T) {
	th := theme.Current()
	tests := []struct {
		name      string
		pct       float64
		wantColor string
	}{
		{"74% is green (just below warn)", 74, th.Green},
		{"75% is yellow (at warn)", 75, th.Yellow},
		{"89% is yellow (just below critical)", 89, th.Yellow},
		{"90% is red (at critical)", 90, th.Red},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Payload{
				Model: &Model{DisplayName: strPtr("Sonnet 4")},
				ContextWindow: &ContextWindow{
					UsedPercentagePushed: f64Ptr(tt.pct),
				},
			}
			line, ok := renderRichLine2(p, 200, nil)
			if !ok {
				t.Fatal("renderRichLine2() ok = false")
			}
			want := ansiFg(tt.wantColor) + fmt.Sprintf("%d%%", int(tt.pct))
			if !strings.Contains(line, want) {
				t.Errorf("line = %q, want to contain %q (color %s)", line, want, tt.wantColor)
			}
		})
	}
}

// TestRenderRichLine2_CtxZero_RendersLabelStyleNotGreen pins the special
// case recovered from the deleted chrome_bar.go: a 0% reading is rendered in
// LabelStyle (Subtle), not colored green, since 0% usually means "no data
// yet" rather than "healthy".
func TestRenderRichLine2_CtxZero_RendersLabelStyleNotGreen(t *testing.T) {
	th := theme.Current()
	p := &Payload{
		Model: &Model{DisplayName: strPtr("Sonnet 4")},
		ContextWindow: &ContextWindow{
			UsedPercentagePushed: f64Ptr(0),
		},
	}
	line, ok := renderRichLine2(p, 200, nil)
	if !ok {
		t.Fatal("renderRichLine2() ok = false")
	}
	greenZero := ansiFg(th.Green) + "0%"
	if strings.Contains(line, greenZero) {
		t.Errorf("line = %q, must not render 0%% in Green", line)
	}
	subtleZero := ansiFg(th.Subtle) + "0%"
	if !strings.Contains(line, subtleZero) {
		t.Errorf("line = %q, want 0%% rendered in LabelStyle (Subtle, %s)", line, ansiFg(th.Subtle))
	}
	if !strings.Contains(stripANSI(line), "0% ctx") {
		t.Errorf("line = %q, want to contain '0%% ctx'", stripANSI(line))
	}
}

// ── renderRich: rate-limit thresholds ───────────────────────────────────────

// TestRenderRichLine1_RateLimitThresholdBoundaries pins the 5h/7d/spend
// color cutoffs recovered from the deleted chrome_bar.go
// renderRateLimitLine: <=75 Green, <=90 Yellow, else Red. five_hour goes
// through coercePercent (clamped to 0..100, unlike spend_limit -- see
// TestRenderRichLine1_SpendLimitNotClamped for the unclamped case), so this
// only exercises values within that range.
func TestRenderRichLine1_RateLimitThresholdBoundaries(t *testing.T) {
	th := theme.Current()
	tests := []struct {
		name      string
		pct       float64
		wantColor string
	}{
		{"75% is green (at cutoff)", 75, th.Green},
		{"76% is yellow (just above cutoff)", 76, th.Yellow},
		{"90% is yellow (at cutoff)", 90, th.Yellow},
		{"91% is red (just above cutoff)", 91, th.Red},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Payload{
				RateLimits: &RateLimits{
					FiveHour: &RateLimitWindow{UsedPercentage: f64Ptr(tt.pct)},
				},
			}
			line, ok := renderRichLine1(p, 200)
			if !ok {
				t.Fatal("renderRichLine1() ok = false")
			}
			want := ansiFg(tt.wantColor) + fmt.Sprintf("%d%%", int(tt.pct))
			if !strings.Contains(line, want) {
				t.Errorf("line = %q, want to contain %q (color %s)", line, want, tt.wantColor)
			}
		})
	}
}

// ── renderRich: spend_limit is not clamped ──────────────────────────────────

func TestRenderRichLine1_SpendLimitNotClamped(t *testing.T) {
	p := &Payload{
		RateLimits: &RateLimits{
			SpendLimit: &RateLimitWindow{UsedPercentage: f64Ptr(150)},
		},
	}
	line, ok := renderRichLine1(p, 200)
	if !ok {
		t.Fatal("renderRichLine1() ok = false")
	}
	line = stripANSI(line)
	if !strings.Contains(line, "spend 150%") {
		t.Errorf("line = %q, want to contain 'spend 150%%' (not clamped to 100)", line)
	}
}

// ── renderRichLine1: width handling ─────────────────────────────────────────

// TestRenderRichLine1_NarrowWidthDropsSpendThenSevenDay is the regression
// test for line 1 never reacting to COLUMNS at all: it must drop segments
// right-to-left (spend first, then 7d) exactly like line 2, keeping the
// first present segment (5h here) as an unconditional floor. Uses a payload
// with no reset time so the mandatory floor segment's width is short and
// deterministic, rather than depending on render.ResetTime's current-time-
// relative text.
func TestRenderRichLine1_NarrowWidthDropsSpendThenSevenDay(t *testing.T) {
	p := &Payload{
		RateLimits: &RateLimits{
			FiveHour:   &RateLimitWindow{UsedPercentage: f64Ptr(24)},
			SevenDay:   &RateLimitWindow{UsedPercentage: f64Ptr(41)},
			SpendLimit: &RateLimitWindow{UsedPercentage: f64Ptr(63)},
		},
	}

	full, ok := renderRichLine1(p, 999)
	if !ok {
		t.Fatal("renderRichLine1(cols=999) ok = false")
	}
	fullStripped := stripANSI(full)
	if !strings.Contains(fullStripped, "5h") || !strings.Contains(fullStripped, "7d") || !strings.Contains(fullStripped, "spend") {
		t.Fatalf("test setup invalid, full line missing an expected segment: %q", fullStripped)
	}
	fullWidth := lipgloss.Width(full)

	// Narrow enough to force exactly one drop (spend), wide enough to keep
	// 5h + 7d.
	fiveAndSevenOnly, ok := renderRichLine1(p, fullWidth-1)
	if !ok {
		t.Fatal("renderRichLine1() ok = false")
	}
	fiveAndSevenStripped := stripANSI(fiveAndSevenOnly)
	if strings.Contains(fiveAndSevenStripped, "spend") {
		t.Errorf("line = %q, want spend dropped first at a slightly narrower width", fiveAndSevenStripped)
	}
	if !strings.Contains(fiveAndSevenStripped, "7d") {
		t.Errorf("line = %q, want 7d retained (only spend should have dropped yet)", fiveAndSevenStripped)
	}
	if got := lipgloss.Width(fiveAndSevenOnly); got > fullWidth-1 {
		t.Errorf("lipgloss.Width(line) = %d, want <= %d", got, fullWidth-1)
	}

	// Narrow enough to force both drops -- only the mandatory first (5h)
	// segment remains.
	floorOnly, ok := renderRichLine1(p, lipgloss.Width(fiveAndSevenOnly)-1)
	if !ok {
		t.Fatal("renderRichLine1() ok = false")
	}
	floorStripped := stripANSI(floorOnly)
	if strings.Contains(floorStripped, "spend") || strings.Contains(floorStripped, "7d") {
		t.Errorf("line = %q, want both spend and 7d dropped at this width", floorStripped)
	}
	if !strings.Contains(floorStripped, "5h") {
		t.Errorf("line = %q, want the mandatory 5h segment retained as the floor", floorStripped)
	}
}

// TestRenderRich_AllLinesRespectColumns is a general regression covering the
// PR review's core complaint: line 1 used to be emitted at a fixed width
// regardless of COLUMNS while line 2 alone reacted. Every emitted line must
// fit within cols once both the mandatory 5h (line 1) and model+ctx (line 2)
// floor segments themselves fit -- cols below either floor is covered
// separately (TestRenderRichLine1_NarrowWidthDropsSpendThenSevenDay,
// TestRenderRichLine2_ModelFamilyFallbackWhenMandatoryPairOverflows), since
// a floor segment that is itself longer than cols cannot shrink further.
func TestRenderRich_AllLinesRespectColumns(t *testing.T) {
	p, err := Decode([]byte(fullPayload))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	snap := &state.AgentState{TurnCount: 12, StartTime: 1}

	// 45 is the narrowest width the fullPayload fixture's line 1 can reach:
	// its mandatory (never-dropped) floor segment is the 5h battery + label
	// + percentage + reset time, which for this fixture's frozen
	// (long-past) resets_at renders "resets now" -- at cols=40 that floor
	// alone would exceed cols, which isn't a styling regression, it's the
	// same un-droppable-floor behavior renderRichLine1 had before styling was added.
	// 40/60/100/200 (the widths verified manually against a
	// live binary) are covered by
	// TestRenderRich_GarbageColumnsNeverPanicsOrZeroWidth and the narrow-
	// width drop tests above for payloads whose floor segments fit.
	for _, cols := range []int{45, 60, 80, 100, 200} {
		lines := renderRich(p, cols, snap)
		for i, l := range lines {
			if got := lipgloss.Width(l); got > cols {
				t.Errorf("cols=%d line[%d]: lipgloss.Width = %d, want <= %d: %q", cols, i, got, cols, l)
			}
		}
	}
}

// ── renderRich: ctx% used verbatim, never recomputed ────────────────────────

func TestRenderRichLine2_ContextPercentUsedVerbatim(t *testing.T) {
	// Construct a payload where the pushed used_percentage (22) disagrees
	// with what current_usage/context_window_size would derive (25) --
	// input=25000+cache_creation=15000+cache_read=10000=50000 / 200000 = 25%.
	p := &Payload{
		Model: &Model{DisplayName: strPtr("Sonnet 4")},
		ContextWindow: &ContextWindow{
			UsedPercentagePushed: f64Ptr(22),
			ContextWindowSize:    f64Ptr(200_000),
			CurrentUsage: &CurrentUsage{
				InputTokens:              f64Ptr(25_000),
				CacheCreationInputTokens: f64Ptr(15_000),
				CacheReadInputTokens:     f64Ptr(10_000),
			},
		},
	}
	line, ok := renderRichLine2(p, 200, nil)
	if !ok {
		t.Fatal("renderRichLine2() ok = false")
	}
	line = stripANSI(line)
	if !strings.Contains(line, "22% ctx") {
		t.Errorf("line = %q, want to contain pinned pushed value '22%% ctx', not the recomputed 25%%", line)
	}
	if strings.Contains(line, "25% ctx") {
		t.Errorf("line = %q, must not contain the recomputed value '25%% ctx'", line)
	}
	// The tokens segment's denominator/numerator still derive from
	// current_usage -- this is a SEPARATE segment from ctx%, not a
	// recomputation of it.
	if !strings.Contains(line, "50k/200k") {
		t.Errorf("line = %q, want to contain tokens segment '50k/200k'", line)
	}
}

// ── renderRichLine2: width handling ──────────────────────────────────────────

func TestRenderRichLine2_NarrowWidthDropsRightToLeft(t *testing.T) {
	p := &Payload{
		Model: &Model{ID: strPtr("claude-sonnet-4-20250514")},
		ContextWindow: &ContextWindow{
			UsedPercentagePushed: f64Ptr(42),
			ContextWindowSize:    f64Ptr(200_000),
			CurrentUsage: &CurrentUsage{
				InputTokens: f64Ptr(80_000),
			},
		},
		PromptCache: &PromptCache{HitRatio: f64Ptr(0.5)},
		Cost:        &Cost{TotalCostUSD: f64Ptr(13.43)},
	}
	snap := &state.AgentState{TurnCount: 5, StartTime: 1}

	// The mandatory minKeep=2 segments (model + battery-backed ctx%) have a
	// fixed minimum width on their own -- compute it directly so this test
	// doesn't hardcode a column count that happens to already be narrower
	// than what model+ctx alone require (the battery bar is a fixed 22
	// visible columns).
	mandatory := render.AbbreviateModel("claude-sonnet-4-20250514") + " · " + render.Battery(42) + " 42% ctx"
	minWidth := lipgloss.Width(mandatory)
	cols := minWidth + 10 // enough room to keep the mandatory two, not enough for everything else

	full, ok := renderRichLine2(p, 999, snap)
	if !ok {
		t.Fatal("renderRichLine2(cols=999) ok = false")
	}
	fullWidth := lipgloss.Width(full)
	if fullWidth <= cols {
		t.Fatalf("test setup invalid: full-width line (%d) already fits in cols (%d)", fullWidth, cols)
	}

	line, ok := renderRichLine2(p, cols, snap)
	if !ok {
		t.Fatal("renderRichLine2() ok = false")
	}
	if got := lipgloss.Width(line); got > cols {
		t.Errorf("lipgloss.Width(line) = %d, want <= %d: %q", got, cols, line)
	}
	line = stripANSI(line)
	if !strings.Contains(line, "sonnet-4") {
		t.Errorf("narrow line = %q, model must never be dropped (minKeep=2)", line)
	}
	if !strings.Contains(line, "42% ctx") {
		t.Errorf("narrow line = %q, ctx%% must never be dropped (minKeep=2)", line)
	}
	if strings.Contains(line, "turns") {
		t.Errorf("narrow line = %q, droppable 'turns' segment should have been trimmed", line)
	}
}

// TestRenderRichLine2_ModelFamilyFallbackWhenMandatoryPairOverflows is the
// regression test for the reviewed off-by-one: when the two always-kept
// segments (model + ctx) alone exceed cols, the model segment must fall back
// from render.AbbreviateModel's full Label (e.g. "sonnet-4") to
// render.AbbreviateModelFamily's bare family badge ("sonnet") rather than
// leave the pair permanently over budget.
func TestRenderRichLine2_ModelFamilyFallbackWhenMandatoryPairOverflows(t *testing.T) {
	const modelID = "claude-sonnet-4-20250514"
	p := &Payload{
		Model:         &Model{ID: strPtr(modelID)},
		ContextWindow: &ContextWindow{UsedPercentagePushed: f64Ptr(42)},
	}

	fullBadge := render.AbbreviateModel(modelID)
	familyBadge := render.AbbreviateModelFamily(modelID)
	if fullBadge == familyBadge {
		t.Fatalf("test setup invalid: full badge (%q) and family badge (%q) must differ", fullBadge, familyBadge)
	}
	ctxSeg := render.Battery(42) + " 42% ctx"
	widthWithFull := lipgloss.Width(fullBadge + " · " + ctxSeg)
	widthWithFamily := lipgloss.Width(familyBadge + " · " + ctxSeg)

	// Below widthWithFull (but still >= widthWithFamily): the full badge
	// alone would overflow, so the family badge must be used instead.
	cols := widthWithFull - 1
	if cols < widthWithFamily {
		t.Fatalf("test setup invalid: cols (%d) narrower than even the family badge (%d)", cols, widthWithFamily)
	}
	line, ok := renderRichLine2(p, cols, nil)
	if !ok {
		t.Fatal("renderRichLine2() ok = false")
	}
	if got := lipgloss.Width(line); got > cols {
		t.Errorf("lipgloss.Width(line) = %d, want <= %d: %q", got, cols, line)
	}
	if strings.Contains(line, fullBadge) {
		t.Errorf("line = %q, want the full badge %q replaced by the family badge %q", line, fullBadge, familyBadge)
	}
	if !strings.Contains(line, familyBadge) {
		t.Errorf("line = %q, want to contain family badge %q", line, familyBadge)
	}

	// At/above widthWithFull, the full badge fits and must be kept.
	line2, ok := renderRichLine2(p, widthWithFull, nil)
	if !ok {
		t.Fatal("renderRichLine2() ok = false")
	}
	if !strings.Contains(line2, fullBadge) {
		t.Errorf("line = %q, want full badge %q retained when it fits (cols=%d)", line2, fullBadge, widthWithFull)
	}
}

func TestStatuslineColumns(t *testing.T) {
	tests := []struct {
		name string
		val  string
		set  bool
		want int
	}{
		{"unset falls back to 80", "", false, 80},
		{"garbage falls back to 80", "abc", true, 80},
		{"zero falls back to 80", "0", true, 80},
		{"negative falls back to 80", "-5", true, 80},
		{"valid value used", "120", true, 120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("COLUMNS", tt.val)
			} else {
				orig, hadOrig := os.LookupEnv("COLUMNS")
				_ = os.Unsetenv("COLUMNS")
				t.Cleanup(func() {
					if hadOrig {
						_ = os.Setenv("COLUMNS", orig)
					}
				})
			}
			if got := statuslineColumns(); got != tt.want {
				t.Errorf("statuslineColumns() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestRenderRichLine2_NeverPanicsOrZeroWidth exercises statuslineColumns'
// garbage-input fallback end-to-end through renderRich, pinning "never
// panics, never emits a 0-width line".
func TestRenderRich_GarbageColumnsNeverPanicsOrZeroWidth(t *testing.T) {
	p := &Payload{Model: &Model{DisplayName: strPtr("Sonnet 4")}}
	for _, cols := range []int{0, -5} {
		lines := renderRich(p, cols, nil)
		// cols<=0 passed directly to renderRich should still be handled
		// gracefully by the trimming loop (no segments get dropped below
		// minKeep, so the line is still emitted).
		if len(lines) == 0 {
			t.Fatalf("renderRich(cols=%d) returned no lines", cols)
		}
		for _, l := range lines {
			if lipgloss.Width(l) == 0 {
				t.Errorf("renderRich(cols=%d) produced a 0-width line", cols)
			}
		}
	}
}

// ── renderRichLine2: no padding ──────────────────────────────────────────────

func TestRenderRichLine2_NeverPaddedToColumns(t *testing.T) {
	p := &Payload{Model: &Model{DisplayName: strPtr("Sonnet 4")}}
	line, ok := renderRichLine2(p, 200, nil)
	if !ok {
		t.Fatal("renderRichLine2() ok = false")
	}
	if strings.HasSuffix(line, " ") {
		t.Errorf("line has trailing whitespace (should not be padded to COLUMNS): %q", line)
	}
	if got := lipgloss.Width(line); got >= 200 {
		t.Errorf("lipgloss.Width(line) = %d, should be far less than cols=200 (not padded)", got)
	}
}

func TestRenderRichLine1_NeverPaddedToColumns(t *testing.T) {
	p := &Payload{
		RateLimits: &RateLimits{FiveHour: &RateLimitWindow{UsedPercentage: f64Ptr(11)}},
	}
	line, ok := renderRichLine1(p, 200)
	if !ok {
		t.Fatal("renderRichLine1() ok = false")
	}
	if strings.HasSuffix(line, " ") {
		t.Errorf("line has trailing whitespace (should not be padded): %q", line)
	}
}

// ── renderRich: state.json unavailable ──────────────────────────────────────

func TestRenderRichLine2_NilSnapshot_OmitsTurnsAndDuration(t *testing.T) {
	p := &Payload{
		Model: &Model{ID: strPtr("claude-sonnet-4-20250514")},
		Cost:  &Cost{TotalCostUSD: f64Ptr(1.5)},
	}
	line, ok := renderRichLine2(p, 200, nil)
	if !ok {
		t.Fatal("renderRichLine2() ok = false")
	}
	line = stripANSI(line)
	if strings.Contains(line, "turns") {
		t.Errorf("line = %q, want no 'turns' segment when snap is nil", line)
	}
	// Other segments (model, cost) must still render.
	if !strings.Contains(line, "sonnet-4") {
		t.Errorf("line = %q, want model segment unaffected by nil snapshot", line)
	}
	if !strings.Contains(line, "$1.50") {
		t.Errorf("line = %q, want cost segment unaffected by nil snapshot", line)
	}
}

func TestLookupAgentState_EmptyWindowID(t *testing.T) {
	if got := lookupAgentState(""); got != nil {
		t.Errorf("lookupAgentState(\"\") = %+v, want nil", got)
	}
}

func TestLookupAgentState_NoSnapshotFile(t *testing.T) {
	state.SetStatePathForTest("/nonexistent/path/state.json")
	if got := lookupAgentState("@1"); got != nil {
		t.Errorf("lookupAgentState() = %+v, want nil when state.json is unavailable", got)
	}
}

// strPtr returns a pointer to s -- payload_test.go already has f64Ptr.
func strPtr(s string) *string { return &s }

// ── renderRichAligned: right-alignment ──────────────────────────────────────

// TestRenderRichAligned_LeftIsByteIdenticalToRenderRich is the important
// regression test: "left" (and unset, and any garbage value routed through
// EffectiveStatuslineAlign upstream) must produce output byte-identical to
// pre-alignment renderRich, with zero padding added.
func TestRenderRichAligned_LeftIsByteIdenticalToRenderRich(t *testing.T) {
	p, err := Decode([]byte(fullPayload))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	snap := &state.AgentState{TurnCount: 12, StartTime: 1}

	for _, align := range []string{"left", "", "garbage-value", "RIGHT", "right "} {
		t.Run(align, func(t *testing.T) {
			for _, cols := range []int{45, 60, 80, 100, 200} {
				want := renderRich(p, cols, snap)
				got := renderRichAligned(p, cols, snap, align)
				if len(got) != len(want) {
					t.Fatalf("align=%q cols=%d: got %d lines, want %d: %v vs %v", align, cols, len(got), len(want), got, want)
				}
				for i := range want {
					if got[i] != want[i] {
						t.Errorf("align=%q cols=%d line[%d]: got %q, want byte-identical %q", align, cols, i, got[i], want[i])
					}
				}
			}
		})
	}
}

// TestRenderRichAligned_RightPadsEveryLineToTheSameWidth pins the core
// right-alignment invariant: every emitted line's lipgloss.Width equals
// cols-rightAlignReserve, at several representative COLUMNS values.
func TestRenderRichAligned_RightPadsEveryLineToTheSameWidth(t *testing.T) {
	p, err := Decode([]byte(fullPayload))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	snap := &state.AgentState{TurnCount: 12, StartTime: 1}

	for _, cols := range []int{60, 100, 200} {
		lines := renderRichAligned(p, cols, snap, "right")
		wantWidth := cols - rightAlignReserve
		for i, l := range lines {
			if got := lipgloss.Width(l); got != wantWidth {
				t.Errorf("cols=%d line[%d]: lipgloss.Width = %d, want %d (cols-rightAlignReserve): %q", cols, i, got, wantWidth, l)
			}
		}
	}
}

// TestRenderRichAligned_RightNeverExceedsBudget mirrors the existing "all
// lines respect columns" regression, but for the reduced right-aligned
// budget (cols-rightAlignReserve).
func TestRenderRichAligned_RightNeverExceedsBudget(t *testing.T) {
	p, err := Decode([]byte(fullPayload))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	snap := &state.AgentState{TurnCount: 12, StartTime: 1}

	// 45 is the narrowest cols where the fullPayload fixture's line 1
	// mandatory floor segment fits under cols itself (see
	// TestRenderRich_AllLinesRespectColumns) -- once rightAlignReserve is
	// subtracted, the right-aligned budget needs a further
	// rightAlignReserve columns of headroom to still clear that same floor.
	for _, cols := range []int{45 + rightAlignReserve, 60, 80, 100, 200} {
		lines := renderRichAligned(p, cols, snap, "right")
		budget := cols - rightAlignReserve
		for i, l := range lines {
			if got := lipgloss.Width(l); got > budget {
				t.Errorf("cols=%d line[%d]: lipgloss.Width = %d, want <= %d (cols-rightAlignReserve): %q", cols, i, got, budget, l)
			}
		}
	}
}

// TestRenderRichAligned_NarrowBudgetDoesNotPanic drives the documented
// pre-existing floor (line 1's mandatory 5h segment can exceed a very
// narrow budget) through the right-align path specifically: padding must
// clamp at zero rather than call strings.Repeat with a negative count, and
// the over-budget line must still be emitted, just unpadded.
func TestRenderRichAligned_NarrowBudgetDoesNotPanic(t *testing.T) {
	p := &Payload{
		RateLimits: &RateLimits{
			FiveHour: &RateLimitWindow{
				UsedPercentage: f64Ptr(24),
				ResetsAt:       f64Ptr(1),
			},
		},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("renderRichAligned panicked at cols=30: %v", r)
		}
	}()

	const cols = 30 // narrower than the mandatory 5h floor segment
	unaligned := renderRich(p, cols-rightAlignReserve, nil)
	if len(unaligned) != 1 {
		t.Fatalf("test setup invalid: renderRich(cols=%d) = %d lines, want 1", cols-rightAlignReserve, len(unaligned))
	}
	if lipgloss.Width(unaligned[0]) <= cols-rightAlignReserve {
		t.Fatalf("test setup invalid: mandatory floor segment (%d) does not exceed budget (%d), narrow the fixture further", lipgloss.Width(unaligned[0]), cols-rightAlignReserve)
	}

	lines := renderRichAligned(p, cols, nil, "right")
	if len(lines) != 1 {
		t.Fatalf("renderRichAligned(cols=%d, right) = %d lines, want 1", cols, len(lines))
	}
	// Emitted unpadded (i.e. identical to the unaligned/trimmed line), not
	// panicked, and not silently dropped.
	if lines[0] != unaligned[0] {
		t.Errorf("renderRichAligned(cols=%d, right) = %q, want unpadded line %q (over budget, clamp at zero)", cols, lines[0], unaligned[0])
	}
}

// TestRenderRichAligned_ContentUnchangedByAlignment asserts that, once ANSI
// escapes AND leading padding spaces are stripped, "left" and "right"
// produce identical content -- alignment must never alter the data, only
// where it sits.
func TestRenderRichAligned_ContentUnchangedByAlignment(t *testing.T) {
	p, err := Decode([]byte(fullPayload))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	snap := &state.AgentState{TurnCount: 12, StartTime: 1}

	// Wide enough that NEITHER budget (cols for left, cols-rightAlignReserve
	// for right) triggers segment trimming -- otherwise the two would
	// legitimately keep a different number of segments (right has less
	// room by design) and this test would be comparing apples to oranges
	// instead of isolating alignment's effect on data.
	const cols = 999
	left := renderRichAligned(p, cols, snap, "left")
	right := renderRichAligned(p, cols, snap, "right")
	if len(left) != len(right) {
		t.Fatalf("left produced %d lines, right produced %d: %v vs %v", len(left), len(right), left, right)
	}
	for i := range left {
		// Trim both the pad rune (right's leading margin) and plain spaces
		// (in case a future change re-introduces space-based padding) so
		// this test isolates alignment's effect on data regardless of which
		// character the padding happens to use.
		leftContent := strings.TrimLeft(stripANSI(left[i]), " "+string(padRune))
		rightContent := strings.TrimLeft(stripANSI(right[i]), " "+string(padRune))
		if leftContent != rightContent {
			t.Errorf("line[%d]: left content %q != right content %q (alignment must not alter data)", i, leftContent, rightContent)
		}
	}
}

// TestPadRuneIsOneColumnWide pins the load-bearing assumption behind every
// width calculation in padLeftTo: padRune must measure as exactly 1 column
// via lipgloss.Width. If a future lipgloss/terminal-width-table update (or
// a copy-paste swap to a different Unicode block) changed that to 0 or 2,
// every padding computation in this file would be silently wrong -- this
// must fail loudly in CI, not be discovered visually.
func TestPadRuneIsOneColumnWide(t *testing.T) {
	if got := lipgloss.Width(string(padRune)); got != 1 {
		t.Fatalf("lipgloss.Width(padRune) = %d, want 1 -- every padding calculation in padLeftTo assumes this", got)
	}
}

// TestPadLeftTo covers the clamp-at-zero and lipgloss.Width behaviors
// directly, including a styled (ANSI-containing) input where len() would
// compute the wrong padding.
func TestPadLeftTo(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		width     int
		wantWidth int
	}{
		{"pads to width", "abc", 10, 10},
		{"exact width no padding", "abcde", 5, 5},
		{"already over width left unpadded", "abcdefghij", 5, 10},
		{"negative width clamps to unpadded", "abc", -1, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := padLeftTo(tt.line, tt.width)
			if gotWidth := lipgloss.Width(got); gotWidth != tt.wantWidth {
				t.Errorf("padLeftTo(%q, %d) width = %d, want %d", tt.line, tt.width, gotWidth, tt.wantWidth)
			}
			if !strings.HasSuffix(got, tt.line) {
				t.Errorf("padLeftTo(%q, %d) = %q, want to end with the original line", tt.line, tt.width, got)
			}
			// The padding itself must be padRune, never a plain space --
			// see padRune's doc comment for why a space is silently
			// stripped by Claude Code before rendering.
			padded := strings.TrimSuffix(got, tt.line)
			if strings.ContainsRune(padded, ' ') {
				t.Errorf("padLeftTo(%q, %d) padding %q contains a plain space, want only padRune", tt.line, tt.width, padded)
			}
			for _, r := range padded {
				if r != padRune {
					t.Errorf("padLeftTo(%q, %d) padding %q contains rune %q, want only padRune %q", tt.line, tt.width, padded, r, padRune)
				}
			}
		})
	}

	// A styled line's padding must be computed from lipgloss.Width, not
	// len() -- len() would count every byte of the ANSI escape sequence.
	stylePrefix := ansiFg("#a6e3a1")
	stylerLine := stylePrefix + "5%" + "\x1b[m"
	if lipgloss.Width(stylerLine) != 2 {
		t.Fatalf("test setup invalid: expected styled line to have display width 2, got %d", lipgloss.Width(stylerLine))
	}
	padded := padLeftTo(stylerLine, 10)
	if got := lipgloss.Width(padded); got != 10 {
		t.Errorf("padLeftTo(styled, 10) width = %d, want 10 (len() would have overcounted the ANSI bytes)", got)
	}
}

// ── renderRichLine3: workspace/git ──────────────────────────────────────────

func TestRenderRichLine3_DirFromWorkspaceCurrentDir(t *testing.T) {
	p := &Payload{
		Workspace: &Workspace{CurrentDir: strPtr("/tmp/some/project")},
		Cwd:       strPtr("/tmp/other/cwd"),
	}
	line, ok := renderRichLine3(p, 200)
	if !ok {
		t.Fatal("renderRichLine3() ok = false")
	}
	line = stripANSI(line)
	if !strings.Contains(line, "/tmp/some/project") {
		t.Errorf("line = %q, want workspace.current_dir preferred over cwd", line)
	}
	if strings.Contains(line, "/tmp/other/cwd") {
		t.Errorf("line = %q, must not contain cwd when current_dir is present", line)
	}
}

func TestRenderRichLine3_DirFallsBackToCwd(t *testing.T) {
	p := &Payload{Cwd: strPtr("/tmp/fallback/cwd")}
	line, ok := renderRichLine3(p, 200)
	if !ok {
		t.Fatal("renderRichLine3() ok = false")
	}
	line = stripANSI(line)
	if !strings.Contains(line, "/tmp/fallback/cwd") {
		t.Errorf("line = %q, want cwd used as fallback", line)
	}
}

func TestRenderRichLine3_NoDirAvailable_NotOK(t *testing.T) {
	p := &Payload{
		PR: &PR{Number: f64Ptr(42)},
	}
	if _, ok := renderRichLine3(p, 200); ok {
		t.Error("renderRichLine3() ok = true, want false when neither current_dir nor cwd is present")
	}
}

func TestRenderRichLine3_HomeRelativeTilde(t *testing.T) {
	orig := homeDirForDisplay
	t.Cleanup(func() { homeDirForDisplay = orig })
	homeDirForDisplay = func() (string, error) { return "/home/user", nil }

	p := &Payload{Cwd: strPtr("/home/user/project")}
	line, ok := renderRichLine3(p, 200)
	if !ok {
		t.Fatal("renderRichLine3() ok = false")
	}
	line = stripANSI(line)
	if !strings.Contains(line, "~/project") {
		t.Errorf("line = %q, want home-relative tilde rendering '~/project'", line)
	}
}

func TestRenderRichLine3_LongDirTruncated(t *testing.T) {
	orig := homeDirForDisplay
	t.Cleanup(func() { homeDirForDisplay = orig })
	homeDirForDisplay = func() (string, error) { return "", os.ErrNotExist }

	long := "/very/deeply/nested/path/that/keeps/going/and/going/until/it/exceeds/forty/columns/agentctl/internal"
	p := &Payload{Cwd: strPtr(long)}
	line, ok := renderRichLine3(p, 999)
	if !ok {
		t.Fatal("renderRichLine3() ok = false")
	}
	line = stripANSI(line)
	if !strings.Contains(line, "…/") {
		t.Errorf("line = %q, want ellipsis-truncated directory", line)
	}
	if strings.Contains(line, long) {
		t.Errorf("line = %q, must not contain the full untruncated path", line)
	}
}

func TestRenderRichLine3_WorktreePresent(t *testing.T) {
	p := &Payload{
		Cwd:       strPtr("/tmp/project"),
		Workspace: &Workspace{GitWorktree: strPtr("issue-1041")},
	}
	line, ok := renderRichLine3(p, 200)
	if !ok {
		t.Fatal("renderRichLine3() ok = false")
	}
	line = stripANSI(line)
	if !strings.Contains(line, "issue-1041") {
		t.Errorf("line = %q, want worktree name 'issue-1041'", line)
	}
}

func TestRenderRichLine3_WorktreeAbsent(t *testing.T) {
	p := &Payload{Cwd: strPtr("/tmp/project")}
	line, ok := renderRichLine3(p, 200)
	if !ok {
		t.Fatal("renderRichLine3() ok = false")
	}
	// No workspace at all -- nothing worktree-shaped should appear beyond
	// the directory segment itself.
	if strings.Count(stripANSI(line), "/tmp/project") != 1 {
		t.Errorf("line = %q, want exactly the directory segment and nothing else", line)
	}
}

func TestRenderRichLine3_PRWithReviewState(t *testing.T) {
	p := &Payload{
		Cwd: strPtr("/tmp/project"),
		PR:  &PR{Number: f64Ptr(1042), ReviewState: strPtr("approved")},
	}
	line, ok := renderRichLine3(p, 200)
	if !ok {
		t.Fatal("renderRichLine3() ok = false")
	}
	line = stripANSI(line)
	if !strings.Contains(line, "PR #1042") {
		t.Errorf("line = %q, want 'PR #1042'", line)
	}
	if !strings.Contains(line, "approved") {
		t.Errorf("line = %q, want review state 'approved'", line)
	}
}

func TestRenderRichLine3_PRWithoutReviewState(t *testing.T) {
	p := &Payload{
		Cwd: strPtr("/tmp/project"),
		PR:  &PR{Number: f64Ptr(1042)},
	}
	line, ok := renderRichLine3(p, 200)
	if !ok {
		t.Fatal("renderRichLine3() ok = false")
	}
	line = stripANSI(line)
	if !strings.Contains(line, "PR #1042") {
		t.Errorf("line = %q, want 'PR #1042'", line)
	}
}

func TestRenderRichLine3_EmitsColorEscapes(t *testing.T) {
	p := &Payload{
		Cwd:       strPtr("/tmp/project"),
		Workspace: &Workspace{GitWorktree: strPtr("issue-1041")},
		PR:        &PR{Number: f64Ptr(1042), ReviewState: strPtr("approved")},
	}
	line, ok := renderRichLine3(p, 200)
	if !ok {
		t.Fatal("renderRichLine3() ok = false")
	}
	if !strings.Contains(line, "\x1b[") {
		t.Errorf("line = %q, want at least one ANSI escape", line)
	}
}

func TestRenderRichLine3_NarrowWidthDropsRightToLeft(t *testing.T) {
	p := &Payload{
		Cwd:       strPtr("/tmp/project"),
		Workspace: &Workspace{GitWorktree: strPtr("issue-1041")},
		PR:        &PR{Number: f64Ptr(1042), ReviewState: strPtr("approved")},
	}
	full, ok := renderRichLine3(p, 999)
	if !ok {
		t.Fatal("renderRichLine3(cols=999) ok = false")
	}
	fullWidth := lipgloss.Width(full)
	dirOnlyWidth := lipgloss.Width(render.ValueStyle().Render("/tmp/project"))

	// Narrow enough to drop the PR segment, wide enough to keep the
	// worktree segment.
	cols := fullWidth - 1
	if cols < dirOnlyWidth {
		t.Fatalf("test setup invalid: cols (%d) narrower than the mandatory dir segment (%d)", cols, dirOnlyWidth)
	}
	line, ok := renderRichLine3(p, cols)
	if !ok {
		t.Fatal("renderRichLine3() ok = false")
	}
	if got := lipgloss.Width(line); got > cols {
		t.Errorf("lipgloss.Width(line) = %d, want <= %d", got, cols)
	}
	stripped := stripANSI(line)
	if strings.Contains(stripped, "PR #1042") {
		t.Errorf("line = %q, want PR dropped first at a slightly narrower width", stripped)
	}
	if !strings.Contains(stripped, "issue-1041") {
		t.Errorf("line = %q, want worktree retained (only PR should have dropped yet)", stripped)
	}

	// Narrowest: only the mandatory directory segment remains.
	floor, ok := renderRichLine3(p, dirOnlyWidth)
	if !ok {
		t.Fatal("renderRichLine3() ok = false")
	}
	floorStripped := stripANSI(floor)
	if strings.Contains(floorStripped, "issue-1041") || strings.Contains(floorStripped, "PR #1042") {
		t.Errorf("line = %q, want both worktree and PR dropped at this width", floorStripped)
	}
	if !strings.Contains(floorStripped, "/tmp/project") {
		t.Errorf("line = %q, want the mandatory directory segment retained as the floor", floorStripped)
	}
}

// ── width/trimming at representative COLUMNS values ─────────────────────────

// TestRenderRich_ThreeLines_RespectColumnsAtRepresentativeWidths exercises
// all three lines together at the COLUMNS values the manual
// verification steps use (40, 60, 100, 200), pinning that every emitted
// line's lipgloss.Width never exceeds cols, and that narrowing cols actually
// changes at least one line's width (a column that doesn't react to
// COLUMNS at all is the regression this guards against). Deliberately
// NOT fullPayload: its five_hour.resets_at is a frozen past timestamp that
// renders as "resets now", which makes line 1's mandatory (never-dropped)
// floor segment itself exceed cols=40 -- a pre-existing, documented
// property of the floor (see TestRenderRich_AllLinesRespectColumns), not a
// bug in the width-reaction logic this test is pinning. Omitting resets_at
// here keeps the floor segment short enough to fit at all four widths.
func TestRenderRich_ThreeLines_RespectColumnsAtRepresentativeWidths(t *testing.T) {
	p := &Payload{
		Model:     &Model{DisplayName: strPtr("Sonnet 4")},
		Cwd:       strPtr("/home/user/project"),
		Workspace: &Workspace{CurrentDir: strPtr("/home/user/project"), GitWorktree: strPtr("issue-1041")},
		ContextWindow: &ContextWindow{
			UsedPercentagePushed: f64Ptr(13),
			ContextWindowSize:    f64Ptr(200_000),
			CurrentUsage:         &CurrentUsage{InputTokens: f64Ptr(25_000)},
		},
		PromptCache: &PromptCache{HitRatio: f64Ptr(0.75)},
		Cost:        &Cost{TotalCostUSD: f64Ptr(1.23)},
		RateLimits: &RateLimits{
			FiveHour: &RateLimitWindow{UsedPercentage: f64Ptr(24)},
			SevenDay: &RateLimitWindow{UsedPercentage: f64Ptr(41)},
		},
		PR: &PR{Number: f64Ptr(1042), ReviewState: strPtr("approved")},
	}
	snap := &state.AgentState{
		TurnCount:      12,
		StartTime:      1,
		LastTurnInput:  81263,
		LastTurnOutput: 1121,
		InputTokens:    789213,
		OutputTokens:   6339,
	}

	widths := []int{40, 60, 100, 200}
	perLineWidths := make(map[int][]int) // lineIndex -> widths seen across cols
	for _, cols := range widths {
		lines := renderRich(p, cols, snap)
		for i, l := range lines {
			got := lipgloss.Width(l)
			if got > cols {
				t.Errorf("cols=%d line[%d]: lipgloss.Width = %d, want <= %d: %q", cols, i, got, cols, l)
			}
			perLineWidths[i] = append(perLineWidths[i], got)
		}
	}

	// At least one line must actually react to COLUMNS by dropping
	// segments -- a line that stays exactly the same width at cols=40 and
	// cols=200 despite carrying more than enough droppable content is the
	// kind of bug this test exists to catch (this payload's line 2 and
	// line 3 each carry several droppable segments; line 1 legitimately
	// fits in full at every width tested here and is not required to
	// shrink).
	changed := false
	for _, ws := range perLineWidths {
		if ws[0] != ws[len(ws)-1] {
			changed = true
			break
		}
	}
	if !changed {
		t.Errorf("no line's width changed across COLUMNS values %v -- expected at least one line to react by dropping segments", widths)
	}
}
