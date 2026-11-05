package chromebar

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/state"
)

// ── test helpers ─────────────────────────────────────────────────────────────

// writeTempJSONL writes the given lines (one per line, with a trailing newline)
// to a temporary file and returns its path. The file is cleaned up when t ends.
func writeTempJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.jsonl")
	if err != nil {
		t.Fatalf("writeTempJSONL: create: %v", err)
	}
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("writeTempJSONL: close: %v", err)
	}
	return f.Name()
}

// appendLines appends lines to an existing file, each followed by a newline.
func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("appendLines: open: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
}

// ── renderChromeHeader tests ─────────────────────────────────────────────────

func TestRenderChromeHeader_States(t *testing.T) {
	tests := []struct {
		name      string
		agent     state.AgentState
		wantTexts []string
	}{
		{
			name:      "working",
			agent:     state.AgentState{Name: "go-engineer", State: "working"},
			wantTexts: []string{"go-engineer", "Working"},
		},
		{
			name:      "idle",
			agent:     state.AgentState{Name: "my-agent", State: "idle"},
			wantTexts: []string{"my-agent", "Idle"},
		},
		{
			name:      "blocked",
			agent:     state.AgentState{Name: "test-agent", State: "blocked"},
			wantTexts: []string{"test-agent", "Blocked"},
		},
		{
			name:      "done",
			agent:     state.AgentState{Name: "done-agent", State: "idle", Done: true},
			wantTexts: []string{"done-agent", "Done"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderChromeHeader(tt.agent, 80)
			for _, want := range tt.wantTexts {
				if !strings.Contains(out, want) {
					t.Errorf("expected %q in header, got: %q", want, out)
				}
			}
		})
	}
}

func TestRenderChromeHeader_FallbackName(t *testing.T) {
	a := state.AgentState{State: "idle"}
	out := renderChromeHeader(a, 80)
	if !strings.Contains(out, "agent") {
		t.Errorf("empty name should fall back to 'agent', got: %q", out)
	}
}

func TestRenderChromeHeader_Width(t *testing.T) {
	tests := []struct {
		name  string
		width int
	}{
		{"width_40", 40},
		{"width_80", 80},
		{"width_120", 120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := state.AgentState{Name: "go-engineer", State: "idle"}
			out := renderChromeHeader(a, tt.width)
			got := lipgloss.Width(out)
			// Header is left-aligned without fill padding; it should be
			// narrower than the terminal width and non-empty.
			if got == 0 {
				t.Errorf("header should not be empty for width %d", tt.width)
			}
			if got > tt.width {
				t.Errorf("header width %d exceeds terminal width %d", got, tt.width)
			}
		})
	}
}

func TestRenderChromeHeader_SingleLine(t *testing.T) {
	a := state.AgentState{Name: "go-engineer", State: "working"}
	out := renderChromeHeader(a, 80)
	if strings.Contains(out, "\n") {
		t.Errorf("header should be single line, got: %q", out)
	}
}

// ── renderChromeFooter tests ─────────────────────────────────────────────────

func TestRenderChromeFooter_Battery(t *testing.T) {
	tests := []struct {
		name   string
		ctxPct int
	}{
		{"0pct", 0},
		{"50pct", 50},
		{"75pct", 75},
		{"100pct", 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := state.AgentState{
				Name:       "test",
				State:      "working",
				ContextPct: tt.ctxPct,
				StartTime:  time.Now().Add(-5 * time.Minute).Unix(),
			}
			out := renderChromeFooter(a, 120)
			if tt.ctxPct > 0 {
				// Should contain battery blocks.
				filled := strings.Count(out, "█")
				empty := strings.Count(out, "░")
				total := filled + empty
				if total != 20 {
					t.Errorf("battery bar should have 20 blocks, got %d", total)
				}
			}
		})
	}
}

func TestRenderChromeFooter_Tokens(t *testing.T) {
	a := state.AgentState{
		Name:         "test",
		State:        "idle",
		InputTokens:  5000,
		OutputTokens: 1000,
		StartTime:    time.Now().Add(-10 * time.Second).Unix(),
	}
	out := renderChromeFooter(a, 120)
	if !strings.Contains(out, "5k") {
		t.Errorf("expected '5k' in footer for 5000 tokens, got: %q", out)
	}
	if !strings.Contains(out, "tokens") {
		t.Errorf("expected 'tokens' label in footer, got: %q", out)
	}
}

func TestRenderChromeFooter_Turns(t *testing.T) {
	a := state.AgentState{
		Name:      "test",
		State:     "idle",
		TurnCount: 42,
		StartTime: time.Now().Add(-10 * time.Second).Unix(),
	}
	out := renderChromeFooter(a, 120)
	if !strings.Contains(out, "42") {
		t.Errorf("expected '42' turns in footer, got: %q", out)
	}
	if !strings.Contains(out, "turns") {
		t.Errorf("expected 'turns' label in footer, got: %q", out)
	}
}

func TestRenderChromeFooter_Duration(t *testing.T) {
	a := state.AgentState{
		Name:      "test",
		State:     "idle",
		StartTime: time.Now().Add(-5 * time.Minute).Unix(),
	}
	out := renderChromeFooter(a, 120)
	if !strings.Contains(out, "m") {
		t.Errorf("expected duration with 'm' for minutes, got: %q", out)
	}
}

func TestRenderChromeFooter_MailboxPending(t *testing.T) {
	a := state.AgentState{
		Name:           "test",
		State:          "idle",
		MailboxPending: 2,
	}
	out := renderChromeFooter(a, 120)
	if !strings.Contains(out, "✉") {
		t.Errorf("expected mailbox glyph in footer, got: %q", out)
	}
	if !strings.Contains(out, "2") {
		t.Errorf("expected pending count '2' in footer, got: %q", out)
	}
}

func TestRenderChromeFooter_NoMailboxGlyphWhenZero(t *testing.T) {
	a := state.AgentState{
		Name:           "test",
		State:          "idle",
		MailboxPending: 0,
	}
	out := renderChromeFooter(a, 120)
	if strings.Contains(out, "✉") {
		t.Errorf("footer with MailboxPending=0 should not show the mailbox glyph, got: %q", out)
	}
}

func TestRenderChromeFooter_EmptyMetrics(t *testing.T) {
	a := state.AgentState{Name: "test", State: "idle"}
	out := renderChromeFooter(a, 80)
	if out == "" {
		t.Error("footer should not be empty even with no metrics")
	}
	if strings.Contains(out, "ctx") {
		t.Errorf("footer with ctxPct=0 should not show 'ctx', got: %q", out)
	}
	if strings.Contains(out, "tokens") {
		t.Errorf("footer with no tokens should not show 'tokens', got: %q", out)
	}
}

func TestRenderChromeFooter_Width(t *testing.T) {
	tests := []struct {
		name  string
		width int
	}{
		{"width_40", 40},
		{"width_80", 80},
		{"width_120", 120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := state.AgentState{
				Name:       "test",
				State:      "working",
				ContextPct: 30,
				StartTime:  time.Now().Add(-10 * time.Second).Unix(),
			}
			out := renderChromeFooter(a, tt.width)
			got := lipgloss.Width(out)
			if got != tt.width {
				t.Errorf("footer width = %d, want %d", got, tt.width)
			}
		})
	}
}

func TestRenderChromeFooter_SingleLine(t *testing.T) {
	a := state.AgentState{
		Name:         "test",
		State:        "working",
		ContextPct:   50,
		InputTokens:  10000,
		OutputTokens: 2000,
		TurnCount:    5,
		StartTime:    time.Now().Add(-3 * time.Minute).Unix(),
	}
	out := renderChromeFooter(a, 120)
	if strings.Contains(out, "\n") {
		t.Errorf("footer should be single line, got: %q", out)
	}
}

// ── Chrome dedup test ────────────────────────────────────────────────────────

func TestChromeDedup(t *testing.T) {
	// Tick deduplicates by caching last-rendered content per pane.
	// Verify that unchanged state produces the same string (so the dedup check works).
	a := state.AgentState{
		Name:         "go-engineer",
		State:        "working",
		ContextPct:   42,
		InputTokens:  5000,
		OutputTokens: 1000,
		TurnCount:    10,
		StartTime:    time.Now().Add(-5 * time.Minute).Unix(),
	}

	h1 := renderChromeHeader(a, 80)
	h2 := renderChromeHeader(a, 80)
	if h1 != h2 {
		t.Errorf("same state should produce identical header strings")
	}

	f1 := renderChromeFooter(a, 80)
	f2 := renderChromeFooter(a, 80)
	if f1 != f2 {
		t.Errorf("same state should produce identical footer strings")
	}
}

// ── chromeFormatDuration tests ───────────────────────────────────────────────

func TestChromeFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m 30s"},
		{3661 * time.Second, "1h 1m"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := chromeFormatDuration(tt.d)
			if got != tt.want {
				t.Errorf("chromeFormatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// ── chromeFormatTokensK tests ────────────────────────────────────────────────

func TestChromeFormatTokensK(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{500, "500"},
		{1000, "1k"},
		{142381, "142k"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := chromeFormatTokensK(tt.n)
			if got != tt.want {
				t.Errorf("chromeFormatTokensK(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

// ── Turn counting test ───────────────────────────────────────────────────────

func TestUsageStream_TurnCount(t *testing.T) {
	// Write 3 assistant entries — stream should count 3 turns (no line skipping).
	path := writeTempJSONL(t,
		`{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		`{"type":"assistant","message":{"usage":{"input_tokens":200,"output_tokens":80,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		`{"type":"assistant","message":{"usage":{"input_tokens":300,"output_tokens":120,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	)

	u := &logstream.UsageAccumulator{}
	s := logstream.NewStream(path)
	s.Subscribe(u)
	s.Tick()

	if u.TurnCount != 3 {
		t.Errorf("turn count = %d, want 3", u.TurnCount)
	}
}

func TestUsageStream_TurnCountIncremental(t *testing.T) {
	path := writeTempJSONL(t,
		`{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	)

	u := &logstream.UsageAccumulator{}
	s := logstream.NewStream(path)
	s.Subscribe(u)

	s.Tick()
	if u.TurnCount != 1 {
		t.Errorf("initial turn count = %d, want 1", u.TurnCount)
	}

	// Append 2 more entries — stream should count both (no first-line skip).
	appendLines(t, path,
		`{"type":"assistant","message":{"usage":{"input_tokens":200,"output_tokens":80,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		`{"type":"assistant","message":{"usage":{"input_tokens":300,"output_tokens":120,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	)

	s.Tick()
	if u.TurnCount != 3 {
		t.Errorf("incremental turn count = %d, want 3 (all entries counted, no skip)", u.TurnCount)
	}
}

// ── Snapshot field population ────────────────────────────────────────────────

func TestSnapshotNewFields(t *testing.T) {
	// Verify that the new AgentState fields round-trip through Save/Load.
	dir := t.TempDir()
	state.SetStatePathForTest(dir + "/state.json")

	now := time.Now().Unix()
	s := &state.State{
		Agents: []state.AgentState{
			{
				Name:         "go-engineer",
				AgentName:    "go-engineer",
				WindowID:     "@1",
				State:        "working",
				Done:         true,
				InputTokens:  50000,
				OutputTokens: 10000,
				TurnCount:    15,
				StartTime:    now,
				HeaderPane:   "%5",
				FooterPane:   "%6",
				ContextPct:   42,
			},
		},
		Timestamp: time.Now(),
	}

	if err := state.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := state.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	a := loaded.Agents[0]
	if !a.Done {
		t.Error("Done should be true")
	}
	if a.InputTokens != 50000 {
		t.Errorf("InputTokens = %d, want 50000", a.InputTokens)
	}
	if a.OutputTokens != 10000 {
		t.Errorf("OutputTokens = %d, want 10000", a.OutputTokens)
	}
	if a.TurnCount != 15 {
		t.Errorf("TurnCount = %d, want 15", a.TurnCount)
	}
	if a.StartTime != now {
		t.Errorf("StartTime = %d, want %d", a.StartTime, now)
	}
	if a.HeaderPane != "%5" {
		t.Errorf("HeaderPane = %q, want %%5", a.HeaderPane)
	}
	if a.FooterPane != "%6" {
		t.Errorf("FooterPane = %q, want %%6", a.FooterPane)
	}
}
