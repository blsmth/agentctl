package statusline

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/ratelimit"
	"github.com/blsmth/agentctl/internal/signal"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// blockTmuxDeps stubs signal's tmux dependency so ResolveWindowID never
// shells out to a real tmux binary and, with no env vars set, resolves to
// "" (unresolvable window).
func blockTmuxDeps(t *testing.T) {
	t.Helper()
	orig := signal.DefaultDeps()
	signal.SetDepsForTest(&signal.Deps{
		TmuxDisplay:  func(string, string) string { return "" },
		RenameWindow: func(string, string) error { return nil },
	})
	t.Cleanup(func() { signal.SetDepsForTest(orig) })
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "")
}

// ── exit contract (§9: malformed stdin, empty stdin, unresolvable window) ──

func TestRunWithIO_MalformedStdin_PrintsOneLine(t *testing.T) {
	blockTmuxDeps(t)
	windowstate.SetDirForTest(t.TempDir())

	var out bytes.Buffer
	RunWithIO(strings.NewReader(`{not valid json`), &out, false)

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("expected exactly one non-empty line, got %q", out.String())
	}
}

func TestRunWithIO_EmptyStdin_PrintsOneLine(t *testing.T) {
	blockTmuxDeps(t)
	windowstate.SetDirForTest(t.TempDir())

	var out bytes.Buffer
	RunWithIO(strings.NewReader(``), &out, false)

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("expected exactly one non-empty line, got %q", out.String())
	}
}

func TestRunWithIO_UnresolvableWindow_StillPrintsOneLine(t *testing.T) {
	blockTmuxDeps(t)
	windowstate.SetDirForTest(t.TempDir())

	// Default format is "rich": a model-only payload has no rate
	// limits (line 1 omitted) but does have a model (line 2's first
	// segment), so the expected output is just the abbreviated model name --
	// but it must still be exactly one non-empty line even though the
	// window can't be resolved (no turns/duration to look up), which is the
	// actual contract under test here.
	payload := `{"model": {"display_name": "Sonnet 4"}}`
	var out bytes.Buffer
	RunWithIO(strings.NewReader(payload), &out, false)

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("expected exactly one non-empty line, got %q", out.String())
	}
}

// ── renderLine / format dispatch ──────────────────────────────────

func TestRenderLine_Full_Fallback(t *testing.T) {
	p := &Payload{}
	if got := renderLine(p, "full"); got != fallbackLine {
		t.Errorf("renderLine(empty, full) = %q, want %q", got, fallbackLine)
	}
}

func TestRenderLine_Full_ComposesModelCtxAgent(t *testing.T) {
	displayName := "Sonnet 4"
	agentName := "go-engineer"
	p := &Payload{
		Model: &Model{DisplayName: &displayName},
		ContextWindow: &ContextWindow{
			UsedPercentagePushed: f64Ptr(42),
		},
		Agent: &Agent{Name: &agentName},
	}
	got := renderLine(p, "full")
	if !strings.Contains(got, "Sonnet 4") {
		t.Errorf("renderLine() = %q, want to contain model display name", got)
	}
	if !strings.Contains(got, "42%") {
		t.Errorf("renderLine() = %q, want to contain ctx percentage", got)
	}
	if !strings.Contains(got, "go-engineer") {
		t.Errorf("renderLine() = %q, want to contain agent name", got)
	}
}

func TestRenderLine_Minimal_AlwaysDash(t *testing.T) {
	displayName := "Sonnet 4"
	p := &Payload{Model: &Model{DisplayName: &displayName}}
	if got := renderLine(p, "minimal"); got != noDataLine {
		t.Errorf("renderLine(minimal) = %q, want %q", got, noDataLine)
	}
}

func TestRenderLine_Complementary(t *testing.T) {
	tests := []struct {
		name string
		p    *Payload
		want string
	}{
		{
			name: "all absent → dash",
			p:    &Payload{},
			want: noDataLine,
		},
		{
			name: "all present",
			p: &Payload{
				RateLimits: &RateLimits{
					FiveHour: &RateLimitWindow{UsedPercentage: f64Ptr(12)},
					SevenDay: &RateLimitWindow{UsedPercentage: f64Ptr(9)},
				},
				Cost: &Cost{TotalCostUSD: f64Ptr(13.43)},
			},
			want: "5h 12% · 7d 9% · $13.43",
		},
		{
			name: "seven_day absent: omitted cleanly",
			p: &Payload{
				RateLimits: &RateLimits{
					FiveHour: &RateLimitWindow{UsedPercentage: f64Ptr(12)},
				},
				Cost: &Cost{TotalCostUSD: f64Ptr(13.43)},
			},
			want: "5h 12% · $13.43",
		},
		{
			name: "rate_limits entirely absent: cost only",
			p: &Payload{
				Cost: &Cost{TotalCostUSD: f64Ptr(1.5)},
			},
			want: "$1.50",
		},
		{
			name: "cost absent: rate limits only",
			p: &Payload{
				RateLimits: &RateLimits{
					FiveHour: &RateLimitWindow{UsedPercentage: f64Ptr(12)},
				},
			},
			want: "5h 12%",
		},
		{
			name: "spend_limit is not rendered (not part of complementary format)",
			p: &Payload{
				RateLimits: &RateLimits{
					SpendLimit: &RateLimitWindow{UsedPercentage: f64Ptr(162.8)},
				},
			},
			want: noDataLine,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderLine(tt.p, "complementary"); got != tt.want {
				t.Errorf("renderLine(complementary) = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderLine_UnknownFormat_FallsBackToComplementary(t *testing.T) {
	p := &Payload{Cost: &Cost{TotalCostUSD: f64Ptr(1.5)}}
	got := renderLine(p, "some-future-format-agentctl-does-not-know-yet")
	want := renderComplementary(p)
	if got != want {
		t.Errorf("renderLine(unknown format) = %q, want %q (complementary fallback)", got, want)
	}
}

// ── windowstate persistence ──────────────────────────────────────────────────

func TestRunWithIO_PersistsWindowState(t *testing.T) {
	dir := t.TempDir()
	windowstate.SetDirForTest(dir)

	orig := signal.DefaultDeps()
	signal.SetDepsForTest(&signal.Deps{
		TmuxDisplay:  func(string, string) string { return "@42" },
		RenameWindow: func(string, string) error { return nil },
	})
	t.Cleanup(func() { signal.SetDepsForTest(orig) })
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("TMUX_PANE", "%1")

	payload := `{
		"model": {"id": "claude-sonnet-4-20250514"},
		"cost": {"total_cost_usd": 3.5},
		"context_window": {
			"used_percentage": 67.4,
			"context_window_size": 200000,
			"current_usage": {
				"input_tokens": 100000,
				"output_tokens": 500,
				"cache_creation_input_tokens": 20000,
				"cache_read_input_tokens": 14800
			}
		}
	}`
	var out bytes.Buffer
	before := time.Now()
	RunWithIO(strings.NewReader(payload), &out, false)
	after := time.Now()

	ws, err := windowstate.Read("@42")
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want claude-sonnet-4-20250514", ws.Model)
	}
	if ws.CostUSD != 3.5 {
		t.Errorf("CostUSD = %v, want 3.5", ws.CostUSD)
	}
	if ws.CtxPct != 67 {
		t.Errorf("CtxPct = %d, want 67 (rounded)", ws.CtxPct)
	}
	// The statusline must mark itself as the source and stamp when it
	// pushed, so the monitor can tell a fresh push from a stale one.
	if ws.CtxPctSource != "statusline" {
		t.Errorf("CtxPctSource = %q, want statusline", ws.CtxPctSource)
	}
	if ws.CtxPushedAt.Before(before) || ws.CtxPushedAt.After(after) {
		t.Errorf("CtxPushedAt = %v, want between %v and %v", ws.CtxPushedAt, before, after)
	}
	// ContextWindowSize/ContextTokens mirror the exact numerator/denominator
	// used_percentage itself is computed from -- 100000 + 20000 + 14800 =
	// 134800 (input-only, excluding output_tokens).
	if ws.ContextWindowSize != 200000 {
		t.Errorf("ContextWindowSize = %d, want 200000", ws.ContextWindowSize)
	}
	if ws.ContextTokens != 134800 {
		t.Errorf("ContextTokens = %d, want 134800", ws.ContextTokens)
	}
}

func TestRunWithIO_DoesNotTouchOwnedWindowStateFields(t *testing.T) {
	dir := t.TempDir()
	windowstate.SetDirForTest(dir)

	orig := signal.DefaultDeps()
	signal.SetDepsForTest(&signal.Deps{
		TmuxDisplay:  func(string, string) string { return "@7" },
		RenameWindow: func(string, string) error { return nil },
	})
	t.Cleanup(func() { signal.SetDepsForTest(orig) })
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("TMUX_PANE", "%1")

	// Seed fields owned by other writers.
	if err := windowstate.Write("@7", windowstate.WindowState{
		State:       "working",
		LogPath:     "/some/sess-1.jsonl",
		SessionID:   "sess-1",
		Branch:      "agent/go-engineer/issue-1023",
		WorktreeDir: "/repo/.worktrees/issue-1023",
		Done:        false,
	}); err != nil {
		t.Fatalf("seeding windowstate: %v", err)
	}

	var out bytes.Buffer
	RunWithIO(strings.NewReader(`{"context_window": {"used_percentage": 10}}`), &out, false)

	ws, err := windowstate.Read("@7")
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.State != "working" {
		t.Errorf("State = %q, want unchanged \"working\"", ws.State)
	}
	if ws.Branch != "agent/go-engineer/issue-1023" {
		t.Errorf("Branch = %q, want unchanged", ws.Branch)
	}
	if ws.WorktreeDir != "/repo/.worktrees/issue-1023" {
		t.Errorf("WorktreeDir = %q, want unchanged", ws.WorktreeDir)
	}
	if ws.LogPath != "/some/sess-1.jsonl" {
		t.Errorf("LogPath = %q, want unchanged", ws.LogPath)
	}
	if ws.Done {
		t.Error("Done = true, want unchanged false")
	}
}

// ── shared rate-limits file persistence ─────────────────────────────────────

func TestRunWithIO_PersistsRateLimits(t *testing.T) {
	blockTmuxDeps(t)
	windowstate.SetDirForTest(t.TempDir())

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	payload := `{
		"rate_limits": {
			"five_hour": {"used_percentage": 23.5, "resets_at": 1738425600},
			"spend_limit": {"used_percentage": 162.8, "resets_at": 1740787200}
		}
	}`
	var out bytes.Buffer
	RunWithIO(strings.NewReader(payload), &out, false)

	pr, err := ratelimit.LoadPushed()
	if err != nil {
		t.Fatalf("LoadPushed: %v", err)
	}
	if pr == nil {
		t.Fatal("expected non-nil PushedRateLimits")
	}
	if pr.FiveHour == nil || pr.FiveHour.UsedPercentage != 23.5 {
		t.Errorf("FiveHour = %+v, want UsedPercentage 23.5", pr.FiveHour)
	}
	if pr.SevenDay != nil {
		t.Errorf("SevenDay = %+v, want nil (absent from payload)", pr.SevenDay)
	}
	if pr.SpendLimit == nil || pr.SpendLimit.UsedPercentage != 162.8 {
		t.Errorf("SpendLimit = %+v, want UsedPercentage 162.8 (unclamped)", pr.SpendLimit)
	}
	if pr.Source != "statusline" {
		t.Errorf("Source = %q, want statusline", pr.Source)
	}
}

func TestRunWithIO_RateLimitsAbsent_DoesNotWriteFile(t *testing.T) {
	blockTmuxDeps(t)
	windowstate.SetDirForTest(t.TempDir())

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	var out bytes.Buffer
	RunWithIO(strings.NewReader(`{"session_id": "abc"}`), &out, false)

	path := filepath.Join(home, ".config", "agentctl", "ratelimits.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected no ratelimits.json to be written, stat err = %v", err)
	}
}
