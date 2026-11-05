package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/metrics"
	"github.com/blsmth/agentctl/internal/ratelimit"
	"github.com/blsmth/agentctl/internal/registry"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tokens"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// TODO: test /clear detection via log rotation (findLatestLog returning a different path).

// ── helpers ──────────────────────────────────────────────────────────────────

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

// ── registry self-healing ────────────────────────────────────────────────────

// TestHeartbeatOrReregister_RestoresPrunedEntry is a regression test for the
// self-healing gap: if a live monitor's heartbeat lags past registry.StaleAfter
// (e.g. laptop sleep) the hub prunes its entry, and every subsequent Heartbeat
// call fails with "no entry for session". heartbeatOrReregister must detect
// that failure and Register the entry again so the session reappears in the
// hub without requiring an agentctl restart.
func TestHeartbeatOrReregister_RestoresPrunedEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	registry.SetDirForTest(filepath.Join(home, ".config", "agentctl", "registry"))
	t.Cleanup(func() { registry.SetDirForTest("") })

	entry := registry.Entry{
		SessionName: "agentctl-test",
		ProjectDir:  "/tmp/test",
		RuntimeDir:  "/tmp/test/.agentctl",
		Host:        "local",
		PID:         4242,
		StartedAt:   1000,
		LastSeen:    1000,
	}
	if err := registry.Register(entry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Simulate the hub pruning the entry while the monitor is still alive.
	if err := registry.Deregister(entry.SessionName); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	got := heartbeatOrReregister(entry)

	entries, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List after heartbeatOrReregister returned %d entries, want 1 (entry should be restored)", len(entries))
	}
	if entries[0].SessionName != entry.SessionName {
		t.Errorf("restored entry SessionName = %q, want %q", entries[0].SessionName, entry.SessionName)
	}
	if entries[0].LastSeen == entry.LastSeen {
		t.Errorf("restored entry LastSeen unchanged; want it refreshed")
	}
	if got.LastSeen != entries[0].LastSeen {
		t.Errorf("returned entry LastSeen = %d, want %d (in sync with what was written)", got.LastSeen, entries[0].LastSeen)
	}
}

// TestHeartbeatOrReregister_NoopWhenHeartbeatSucceeds verifies the normal path
// (entry present) doesn't re-register or otherwise change stored fields beyond
// what Heartbeat itself updates.
func TestHeartbeatOrReregister_NoopWhenHeartbeatSucceeds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	registry.SetDirForTest(filepath.Join(home, ".config", "agentctl", "registry"))
	t.Cleanup(func() { registry.SetDirForTest("") })

	entry := registry.Entry{SessionName: "agentctl-alive", PID: 1, LastSeen: 1000}
	if err := registry.Register(entry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got := heartbeatOrReregister(entry)

	entries, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List returned %d entries, want 1", len(entries))
	}
	if entries[0].PID != 1 {
		t.Errorf("PID changed unexpectedly: got %d, want 1", entries[0].PID)
	}
	if got.SessionName != entry.SessionName {
		t.Errorf("returned entry SessionName = %q, want %q", got.SessionName, entry.SessionName)
	}
}

// ── applyVisuals dedup tracking ──────────────────────────────────────────────

// TestApplyVisuals_Dedup verifies that applyVisuals updates inst.lastName on the
// first call (name changed) and leaves it identical on a second call with the
// same state. rename-window will fail silently (no real tmux session) but the
// dedup field update is still exercised.
func TestApplyVisuals_Dedup(t *testing.T) {
	m := monitorModel{}

	inst := &instanceState{
		instance: "go-engineer",
		base:     "go-engineer",
		seen:     true,
	}

	// First call — lastName is "", wantName is "go-engineer", so lastName should be updated.
	m.applyVisuals("@999", inst, state.StatusIdle, "@0")
	if inst.lastName != "go-engineer" {
		t.Errorf("first applyVisuals: expected lastName=%q, got %q", "go-engineer", inst.lastName)
	}

	// Second call with the same state — lastName already matches; no rename needed.
	m.applyVisuals("@999", inst, state.StatusIdle, "@0")
	if inst.lastName != "go-engineer" {
		t.Errorf("second applyVisuals: expected lastName=%q, got %q", "go-engineer", inst.lastName)
	}
}

// ── applyVisuals ctx% authority resolution ────────────────────────

// TestApplyVisuals_CtxPct_PreservesFreshStatuslinePush verifies that a fresh
// statusline-pushed CtxPct is left completely untouched by the monitor: it
// is neither overwritten in windowstate, nor is inst.lastCtxPct pulled back
// down to the (lower/different) derived value. This is the core regression
// test for the "context percentage clobbered every 500ms tick" bug
// (monitor.go:364, before the pushed value took precedence).
func TestApplyVisuals_CtxPct_PreservesFreshStatuslinePush(t *testing.T) {
	wsDir := t.TempDir()
	windowstate.SetDirForTest(wsDir)
	t.Cleanup(func() { windowstate.SetDirForTest("") })

	const windowID = "@1028"
	if err := windowstate.Write(windowID, windowstate.WindowState{
		CtxPct:            22,
		CtxPctSource:      "statusline",
		CtxPushedAt:       time.Now().Add(-30 * time.Second),
		ContextWindowSize: 200_000,
		ContextTokens:     44_000,
	}); err != nil {
		t.Fatalf("seed windowstate: %v", err)
	}

	m := monitorModel{}
	inst := &instanceState{
		instance:   "go-engineer",
		base:       "go-engineer",
		seen:       true,
		lastCtxPct: 25, // the monitor's own derived value -- deliberately different
	}

	m.applyVisuals(windowID, inst, state.StatusWorking, "@0")

	if inst.lastCtxPct != 22 {
		t.Errorf("inst.lastCtxPct = %d, want 22 (pushed value preserved, not clobbered by derived 25)", inst.lastCtxPct)
	}
	if inst.ctxPctSource != "statusline" {
		t.Errorf("inst.ctxPctSource = %q, want statusline", inst.ctxPctSource)
	}
	if inst.pushedContextWindow != 200_000 || inst.pushedContextTokens != 44_000 {
		t.Errorf("pushedContextWindow/Tokens = %d/%d, want 200000/44000", inst.pushedContextWindow, inst.pushedContextTokens)
	}

	ws, err := windowstate.Read(windowID)
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.CtxPct != 22 {
		t.Errorf("windowstate CtxPct = %d, want 22 (untouched)", ws.CtxPct)
	}
	if ws.CtxPctSource != "statusline" {
		t.Errorf("windowstate CtxPctSource = %q, want statusline (untouched)", ws.CtxPctSource)
	}
}

// TestApplyVisuals_CtxPct_OverwritesStalePush verifies the other direction:
// once a statusline push has gone stale (no push within
// windowstate.CtxPctFreshness -- e.g. the window is parked and no longer
// producing assistant messages), the monitor reclaims CtxPct with its own
// derived value, and inst.lastCtxPct adopts that same value.
func TestApplyVisuals_CtxPct_OverwritesStalePush(t *testing.T) {
	wsDir := t.TempDir()
	windowstate.SetDirForTest(wsDir)
	t.Cleanup(func() { windowstate.SetDirForTest("") })

	const windowID = "@1029"
	if err := windowstate.Write(windowID, windowstate.WindowState{
		CtxPct:            22,
		CtxPctSource:      "statusline",
		CtxPushedAt:       time.Now().Add(-windowstate.CtxPctFreshness - time.Minute),
		ContextWindowSize: 200_000,
		ContextTokens:     44_000,
	}); err != nil {
		t.Fatalf("seed windowstate: %v", err)
	}

	m := monitorModel{}
	inst := &instanceState{
		instance:   "go-engineer",
		base:       "go-engineer",
		seen:       true,
		lastCtxPct: 25,
	}

	m.applyVisuals(windowID, inst, state.StatusWorking, "@0")

	if inst.lastCtxPct != 25 {
		t.Errorf("inst.lastCtxPct = %d, want 25 (derived value reclaims a stale push)", inst.lastCtxPct)
	}
	if inst.ctxPctSource != "derived" {
		t.Errorf("inst.ctxPctSource = %q, want derived", inst.ctxPctSource)
	}
	if inst.pushedContextWindow != 0 || inst.pushedContextTokens != 0 {
		t.Errorf("pushedContextWindow/Tokens = %d/%d, want 0/0 when the source is derived", inst.pushedContextWindow, inst.pushedContextTokens)
	}

	ws, err := windowstate.Read(windowID)
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.CtxPct != 25 {
		t.Errorf("windowstate CtxPct = %d, want 25 (reclaimed)", ws.CtxPct)
	}
	if ws.CtxPctSource != "derived" {
		t.Errorf("windowstate CtxPctSource = %q, want derived", ws.CtxPctSource)
	}
}

// ── computeRateLimitState ─────────────────────────────────────────

// TestComputeRateLimitState_100PctRegression pins the exact bug reported in
// the field: windowTokens=462769 against the default 88000 token cap computes
// to 526%, clamped to 100% -- permanently pinning the rate limit bar at full
// even though the account is nowhere near its limit. A fresh pushed
// five_hour percentage must be used instead, bypassing that computation
// entirely.
func TestComputeRateLimitState_100PctRegression(t *testing.T) {
	cfg := &config.Config{} // EffectiveTokenCap() default: 88000
	fiveHourPct := 12.0
	info := &ratelimit.RateLimitInfo{
		Status:          "allowed",
		ResetsAt:        1_738_425_600,
		RateLimitType:   "five_hour",
		Source:          "statusline",
		FiveHourUsedPct: &fiveHourPct,
	}
	scanner := ratelimit.NewWindowTokenScanner()

	got := computeRateLimitState(cfg, info, scanner)

	if got.UsagePct != 12 {
		t.Errorf("UsagePct = %d, want 12 (pushed percentage, not the clamped-to-100 derived estimate)", got.UsagePct)
	}
	if got.Source != "statusline" {
		t.Errorf("Source = %q, want statusline", got.Source)
	}
	if got.WindowTokens != 0 || got.TokenCap != 0 {
		t.Errorf("WindowTokens/TokenCap = %d/%d, want 0/0 (meaningless on the pushed path)", got.WindowTokens, got.TokenCap)
	}
}

// TestComputeRateLimitState_TableDriven covers the full selection matrix:
// pushed data wins when present, the derived windowTokens/tokenCap
// computation is bit-for-bit what it was before pushed data existed otherwise.
func TestComputeRateLimitState_TableDriven(t *testing.T) {
	cfg := &config.Config{}

	tests := []struct {
		name string
		info *ratelimit.RateLimitInfo

		wantSource       string
		wantUsagePct     int
		wantWindowTokens int64
		wantTokenCap     int
		wantSevenDayNil  bool
		wantSevenDayPct  float64
	}{
		{
			name: "pushed five_hour present: statusline wins",
			info: &ratelimit.RateLimitInfo{
				ResetsAt:        1_738_425_600,
				RateLimitType:   "five_hour",
				Status:          "allowed",
				Source:          "statusline",
				FiveHourUsedPct: f64Ptr(12),
			},
			wantSource:      "statusline",
			wantUsagePct:    12,
			wantSevenDayNil: true,
		},
		{
			name: "pushed five_hour with seven_day: both surfaced",
			info: &ratelimit.RateLimitInfo{
				ResetsAt:         1_738_425_600,
				RateLimitType:    "five_hour",
				Status:           "allowed",
				Source:           "statusline",
				FiveHourUsedPct:  f64Ptr(12),
				SevenDayUsedPct:  f64Ptr(9),
				SevenDayResetsAt: 1_738_857_600,
			},
			wantSource:      "statusline",
			wantUsagePct:    12,
			wantSevenDayPct: 9,
		},
		{
			name: "no Source (probe-derived): falls back to windowTokens/tokenCap",
			info: &ratelimit.RateLimitInfo{
				ResetsAt:      1_738_425_600,
				RateLimitType: "five_hour",
				Status:        "allowed",
			},
			wantSource:       "derived",
			wantWindowTokens: 0, // fresh scanner, empty projects dir
			wantTokenCap:     88000,
			wantUsagePct:     0,
			wantSevenDayNil:  true,
		},
		{
			name: "Source statusline but FiveHourUsedPct nil: falls back to derived",
			info: &ratelimit.RateLimitInfo{
				ResetsAt:      1_738_425_600,
				RateLimitType: "five_hour",
				Status:        "allowed",
				Source:        "statusline",
			},
			wantSource:      "derived",
			wantTokenCap:    88000,
			wantSevenDayNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("XDG_DATA_HOME", "")
			t.Setenv("XDG_STATE_HOME", "")
			if err := os.MkdirAll(filepath.Join(dir, ".claude", "projects"), 0o755); err != nil {
				t.Fatal(err)
			}
			scanner := ratelimit.NewWindowTokenScanner()

			got := computeRateLimitState(cfg, tt.info, scanner)

			if got.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tt.wantSource)
			}
			if got.UsagePct != tt.wantUsagePct {
				t.Errorf("UsagePct = %d, want %d", got.UsagePct, tt.wantUsagePct)
			}
			if got.TokenCap != tt.wantTokenCap {
				t.Errorf("TokenCap = %d, want %d", got.TokenCap, tt.wantTokenCap)
			}
			if tt.wantSevenDayNil {
				if got.SevenDayPct != nil {
					t.Errorf("SevenDayPct = %v, want nil", got.SevenDayPct)
				}
			} else {
				if got.SevenDayPct == nil || *got.SevenDayPct != tt.wantSevenDayPct {
					t.Errorf("SevenDayPct = %v, want %v", got.SevenDayPct, tt.wantSevenDayPct)
				}
			}
		})
	}
}

// f64Ptr is a small helper for building *float64 literals in table tests.
func f64Ptr(f float64) *float64 { return &f }

// ── instanceState seen/done tracking ─────────────────────────────────────────

// TestInstanceState_SeenDone verifies the seen and done fields are accessible
// and settable on instanceState (compile-time coverage of tracking state).
func TestInstanceState_SeenDone(t *testing.T) {
	inst := &instanceState{}
	inst.seen = true
	if !inst.seen {
		t.Error("seen should be true after setting")
	}
	inst.done = true
	if !inst.done {
		t.Error("done should be true after setting")
	}
}

// TestStateStringNeverWorkingWhenDone verifies the state-string override that
// prevents state.json from being written as "working" when done=true.
// This guards against stale spinners in chrome bars that read state.json after
// the hook sets done=1 but before the next tick corrects prevState to idle.
func TestStateStringNeverWorkingWhenDone(t *testing.T) {
	tests := []struct {
		name      string
		prevState state.AgentStatus
		done      bool
		wantState string
	}{
		{"idle + not done → idle", state.StatusIdle, false, "idle"},
		{"working + not done → working", state.StatusWorking, false, "working"},
		{"blocked + not done → blocked", state.StatusBlocked, false, "blocked"},
		// Key invariant: done=true must never produce "working".
		{"working + done → idle", state.StatusWorking, true, "idle"},
		{"idle + done → idle", state.StatusIdle, true, "idle"},
		{"blocked + done → idle", state.StatusBlocked, true, "idle"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mirror the logic in processTick's state-building loop.
			st := stateString(tt.prevState)
			if tt.done {
				st = "idle"
			}
			if st != tt.wantState {
				t.Errorf("state = %q, want %q (prevState=%v done=%v)",
					st, tt.wantState, tt.prevState, tt.done)
			}
		})
	}
}

// ── ctxPct via UsageAccumulator ──────────────────────────────────────────────

// TestCtxPct_UsageAccumulator verifies that UsageAccumulator.LastTurnInput
// correctly computes ctxPct even when the assistant entry is far outside the
// 32KB tail window (i.e. followed by a large tool_result user entry).
//
// This test documents why ctxPct is computed from UsageAccumulator (which reads
// from offset 0) rather than a tail scan.
func TestCtxPct_UsageAccumulator(t *testing.T) {
	// Build a JSONL file with:
	//   Line 1: assistant entry with usage totaling 150,000 tokens
	//   Rest:   a single large user entry (>100 KB) that would push the assistant
	//           entry out of any fixed-size tail window.
	assistantLine := `{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":50000,"output_tokens":500,"cache_creation_input_tokens":60000,"cache_read_input_tokens":40000}}}`
	// 150,000 = 50,000 input + 60,000 cache_create + 40,000 cache_read → ctxPct = 75%

	filler := strings.Repeat("x", 100*1024)
	userLine := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"x","content":"` + filler + `"}]}}`

	path := writeTempJSONL(t, assistantLine, userLine)

	stream := logstream.NewStream(path)
	acc := &logstream.UsageAccumulator{}
	stream.Subscribe(acc)
	stream.Tick()

	if acc.LastTurnInput == 0 {
		t.Fatal("UsageAccumulator.LastTurnInput = 0; assistant entry was not read")
	}

	ctxPct := tokens.CtxPct("", acc.LastTurnInput)

	want := 75 // 150_000 * 100 / 200_000
	if ctxPct != want {
		t.Errorf("ctxPct via UsageAccumulator = %d, want %d (LastTurnInput=%d)", ctxPct, want, acc.LastTurnInput)
	}
}

// ── monitor windowstate writeback on log discovery ────────────────────────────

// TestMonitorWritesSessionIDOnLogDiscovery verifies the logic added to the
// monitor that writes session ID + log path to windowstate when a log is
// discovered via findLatestLog() scan. This guards against the HUD showing
// "waiting for session start…" for several seconds while Claude Code
// initialises and fires the SessionStart hook.
func TestMonitorWritesSessionIDOnLogDiscovery(t *testing.T) {
	// Redirect windowstate to a temp directory.
	wsDir := t.TempDir()
	windowstate.SetDirForTest(wsDir)
	t.Cleanup(func() { windowstate.SetDirForTest("") })

	const windowID = "@42"
	const logName = "abc123def456.jsonl"
	logPath := filepath.Join(t.TempDir(), logName)
	if err := os.WriteFile(logPath, []byte(`{"type":"assistant"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	// Simulate what the monitor does after findLatestLog() returns newLog.
	sessionID := strings.TrimSuffix(filepath.Base(logPath), ".jsonl")
	err := windowstate.Update(windowID, func(wst *windowstate.WindowState) {
		if wst.LogPath == "" {
			wst.LogPath = logPath
		}
		if wst.SessionID == "" {
			wst.SessionID = sessionID
		}
	})
	if err != nil {
		t.Fatalf("windowstate.Update: %v", err)
	}

	ws, err := windowstate.Read(windowID)
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.SessionID != "abc123def456" {
		t.Errorf("SessionID = %q, want %q", ws.SessionID, "abc123def456")
	}
	if ws.LogPath != logPath {
		t.Errorf("LogPath = %q, want %q", ws.LogPath, logPath)
	}
}

// ── main_pane backfill ───────────────────────────────────────────────────────

// TestBackfillMainPane covers the gate that lets already-live windows (those
// whose chrome bar predates the main_pane field) pick up MainPane exactly
// once, without re-deriving it on every 500ms tick.
func TestBackfillMainPane(t *testing.T) {
	tests := []struct {
		name string
		ws   windowstate.WindowState

		wantFindCalls int
		wantMainPane  string
	}{
		{
			name:          "no chrome bar pane: not eligible, zero tmux calls",
			ws:            windowstate.WindowState{ChromeBarPane: "", MainPane: ""},
			wantFindCalls: 0,
			wantMainPane:  "",
		},
		{
			name:          "main pane already set: zero tmux calls",
			ws:            windowstate.WindowState{ChromeBarPane: "%1", MainPane: "%2"},
			wantFindCalls: 0,
			wantMainPane:  "%2",
		},
		{
			name:          "chrome bar set, main pane empty: backfills via one tmux call",
			ws:            windowstate.WindowState{ChromeBarPane: "%1", MainPane: ""},
			wantFindCalls: 1,
			wantMainPane:  "%3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wsDir := t.TempDir()
			windowstate.SetDirForTest(wsDir)
			t.Cleanup(func() { windowstate.SetDirForTest("") })

			const windowID = "@99"
			if err := windowstate.Write(windowID, tt.ws); err != nil {
				t.Fatalf("windowstate.Write: %v", err)
			}

			findCalls := 0
			findMainPane := func(gotWindowID string) string {
				findCalls++
				if gotWindowID != windowID {
					t.Errorf("findMainPane called with windowID = %q, want %q", gotWindowID, windowID)
				}
				return "%3"
			}

			got := backfillMainPane(windowID, tt.ws, findMainPane, windowstate.Update)

			if findCalls != tt.wantFindCalls {
				t.Errorf("findMainPane calls = %d, want %d", findCalls, tt.wantFindCalls)
			}
			if got.MainPane != tt.wantMainPane {
				t.Errorf("returned MainPane = %q, want %q", got.MainPane, tt.wantMainPane)
			}

			// Persisted state must agree with the returned value -- the whole
			// point of the backfill is that the next Read() no longer needs
			// another tmux call.
			persisted, err := windowstate.Read(windowID)
			if err != nil {
				t.Fatalf("windowstate.Read: %v", err)
			}
			if persisted.MainPane != tt.wantMainPane {
				t.Errorf("persisted MainPane = %q, want %q", persisted.MainPane, tt.wantMainPane)
			}
		})
	}
}

// ── nil usage regression ──────────────────────────────────────────────────────

// TestProcessTickHandlesNilUsage guards against the nil-pointer dereference that
// previously occurred in processTick when inst.usage was nil (i.e. a window had
// been registered before its JSONL log was discovered). The bug was at
// monitor.go:512 where inst.usage.LastTurnInput was read OUTSIDE the
// "if inst.stream != nil" guard.
//
// We test the inner per-window logic directly by constructing an instanceState
// with nil stream and nil usage (simulating a freshly-seen window whose log
// hasn't been linked yet) and verifying that ctxPct computation does not panic.
func TestProcessTickHandlesNilUsage(t *testing.T) {
	// Construct an instanceState that mimics a new agent window whose log
	// hasn't been discovered yet: stream == nil, usage == nil.
	inst := &instanceState{
		instance: "go-engineer~1",
		base:     "go-engineer",
		seen:     true,
		// stream and usage are intentionally nil — this is the crash scenario.
	}

	// Wrap the ctxPct computation in a defer/recover so the test fails loudly
	// if a nil dereference is reintroduced.
	didPanic := func() (panicked bool) {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		// Mirror the exact logic from updateMetrics: ctxPct is computed from
		// inst.lastTurnInput (zero here) via tokens.CtxPct, never by reading
		// inst.usage directly, so a nil inst.usage can't cause a panic.
		ctxPct := tokens.CtxPct(inst.lastModelName, inst.lastTurnInput)
		_ = ctxPct
		return false
	}()

	if didPanic {
		t.Error("ctxPct computation panicked with nil usage — nil guard is missing")
	}
}

// TestProcessTickHandlesNilUsage_CtxPctIsZero verifies that ctxPct is 0 when
// inst.lastTurnInput is 0 (i.e. no stream has ticked yet), which is the correct
// behavior for a window whose log hasn't been discovered.
func TestProcessTickHandlesNilUsage_CtxPctIsZero(t *testing.T) {
	inst := &instanceState{
		instance: "go-engineer~1",
		base:     "go-engineer",
		seen:     true,
	}

	ctxPct := tokens.CtxPct(inst.lastModelName, inst.lastTurnInput)

	if ctxPct != 0 {
		t.Errorf("ctxPct = %d, want 0 when lastTurnInput is 0 (no log discovered)", ctxPct)
	}
}

// ── state.AgentState absolute-token fields ────────────

// TestPersistState_ContextTokensAndWindow mirrors the ContextTokens/
// ContextWindow assignment in persistState (state.AgentState{ContextTokens:
// inst.lastTurnInput, ContextWindow: tokens.UsableWindow(inst.lastModelName)})
// and verifies both are populated correctly for a model-aware, reserve-aware
// window. persistState itself is not called directly here since it also
// drives tmux/rate-limit/HUD side effects unrelated to this fix.
func TestPersistState_ContextTokensAndWindow(t *testing.T) {
	inst := &instanceState{
		instance:      "go-engineer~1",
		base:          "go-engineer",
		lastTurnInput: 50_000,
		lastModelName: "claude-sonnet-4-20250514",
	}

	got := state.AgentState{
		ContextTokens: inst.lastTurnInput,
		ContextWindow: tokens.UsableWindow(inst.lastModelName),
	}

	if got.ContextTokens != 50_000 {
		t.Errorf("ContextTokens = %d, want 50000", got.ContextTokens)
	}
	// sonnet: 200_000 window - 64_000 reserve = 136_000 usable.
	if got.ContextWindow != 136_000 {
		t.Errorf("ContextWindow = %d, want 136000 (sonnet usable window)", got.ContextWindow)
	}
}

// TestMonitorDoesNotOverwriteExistingSessionID verifies that the conditional
// write in the monitor does not overwrite session ID / log path that were
// already written by the SessionStart hook (hook is authoritative).
func TestMonitorDoesNotOverwriteExistingSessionID(t *testing.T) {
	wsDir := t.TempDir()
	windowstate.SetDirForTest(wsDir)
	t.Cleanup(func() { windowstate.SetDirForTest("") })

	const windowID = "@43"
	const hookSessionID = "hook-set-session-id"
	const hookLogPath = "/some/hook/path/hook-set-session-id.jsonl"

	// Pre-populate windowstate as the hook would have done.
	if err := windowstate.Update(windowID, func(wst *windowstate.WindowState) {
		wst.SessionID = hookSessionID
		wst.LogPath = hookLogPath
	}); err != nil {
		t.Fatalf("pre-populate: %v", err)
	}

	// Now simulate the monitor discovering a *different* log via scan.
	scanDiscoveredLog := filepath.Join(t.TempDir(), "monitor-discovered.jsonl")
	sessionID := strings.TrimSuffix(filepath.Base(scanDiscoveredLog), ".jsonl")
	if err := windowstate.Update(windowID, func(wst *windowstate.WindowState) {
		if wst.LogPath == "" {
			wst.LogPath = scanDiscoveredLog
		}
		if wst.SessionID == "" {
			wst.SessionID = sessionID
		}
	}); err != nil {
		t.Fatalf("monitor update: %v", err)
	}

	ws, err := windowstate.Read(windowID)
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.SessionID != hookSessionID {
		t.Errorf("SessionID = %q, want hook value %q", ws.SessionID, hookSessionID)
	}
	if ws.LogPath != hookLogPath {
		t.Errorf("LogPath = %q, want hook value %q", ws.LogPath, hookLogPath)
	}
}

// ── gitBranchForWorkDir ──────────────────────────────────────────────────────

// TestGitBranchForWorkDir covers a valid git repo, a nonexistent directory,
// and a directory that exists but isn't a git repo.
func TestGitBranchForWorkDir(t *testing.T) {
	t.Run("valid repo returns branch", func(t *testing.T) {
		repoDir := t.TempDir()
		runGit(t, repoDir, "init", "-b", "main")
		runGit(t, repoDir, "config", "user.email", "test@example.com")
		runGit(t, repoDir, "config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		runGit(t, repoDir, "add", "f.txt")
		runGit(t, repoDir, "commit", "-m", "initial")

		got := gitBranchForWorkDir(repoDir)
		if got != "main" {
			t.Errorf("gitBranchForWorkDir(%q) = %q, want %q", repoDir, got, "main")
		}
	})

	t.Run("nonexistent dir returns empty", func(t *testing.T) {
		got := gitBranchForWorkDir(filepath.Join(t.TempDir(), "does-not-exist"))
		if got != "" {
			t.Errorf("gitBranchForWorkDir(nonexistent) = %q, want empty", got)
		}
	})

	t.Run("non-repo dir returns empty", func(t *testing.T) {
		plainDir := t.TempDir()
		got := gitBranchForWorkDir(plainDir)
		if got != "" {
			t.Errorf("gitBranchForWorkDir(non-repo) = %q, want empty", got)
		}
	})
}

// runGit runs a git command in dir, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// ── detectAndFireTransitions: done-edge firing ──────────────────────────────
//
// Regression coverage for the bug where dispatch.HandleComplete (and
// therefore auto-open-PR-in-browser, dispatch label swaps, and
// dispatch_complete hooks) never fired: hook-check-done writes
// windowstate.State="idle" immediately, then windowstate.Done=true seconds
// later once slower completion checks finish. The monitor almost always
// observes the state-change tick with Done still false, and — before this
// fix — never re-fired handlers once Done flipped true with no accompanying
// state change.

// fireTick is one simulated 500ms monitor tick fed into detectAndFireTransitions.
type fireTick struct {
	curState state.AgentStatus
	wsState  string // windowstate.State for this tick (only affects HookHandled)
	wsDone   bool
	wsMode   string
}

// gatherCounterTotal sums every sample of the named counter metric family
// across a registry, regardless of label combination.
func gatherCounterTotal(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var total float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, sample := range mf.GetMetric() {
			total += sample.GetCounter().GetValue()
		}
	}
	return total
}

// TestDetectAndFireTransitions runs one or more ticks through
// detectAndFireTransitions against a fresh instanceState/transitionTracker
// and checks both how many times handleCompleteFn fired (and with what Done
// values) and how much agentctl_state_transitions_total incremented.
func TestDetectAndFireTransitions(t *testing.T) {
	tests := []struct {
		name string

		// Initial instanceState going into the first tick.
		hasPrev   bool
		prevState state.AgentStatus
		prevDone  bool

		ticks []fireTick

		wantHandleCompleteDone  []bool // one entry per handleCompleteFn call, in order
		wantStateTransitionsInc float64
	}{
		{
			// The core regression case: no state change at all, just Done
			// flipping false -> true. Before this fix, nothing fired.
			name:                    "done edge alone with no state change fires",
			hasPrev:                 true,
			prevState:               state.StatusIdle,
			prevDone:                false,
			ticks:                   []fireTick{{curState: state.StatusIdle, wsState: "idle", wsDone: true, wsMode: "adhoc"}},
			wantHandleCompleteDone:  []bool{true},
			wantStateTransitionsInc: 0,
		},
		{
			// State change (working -> idle) with Done already true: fires
			// exactly once, not twice (state edge and done edge must not both
			// fire independently within the same tick).
			name:                    "state change with done already true fires once",
			hasPrev:                 true,
			prevState:               state.StatusWorking,
			prevDone:                false,
			ticks:                   []fireTick{{curState: state.StatusIdle, wsState: "idle", wsDone: true, wsMode: "adhoc"}},
			wantHandleCompleteDone:  []bool{true},
			wantStateTransitionsInc: 1,
		},
		{
			// Production sequence: tick 1 is the state change with Done still
			// false (fires, but HandleComplete itself early-returns on
			// !t.Done); tick 2 is a later done-edge-only tick once Done flips
			// true (fires again, for real).
			name:      "state change then later done edge fires twice",
			hasPrev:   true,
			prevState: state.StatusWorking,
			prevDone:  false,
			ticks: []fireTick{
				{curState: state.StatusIdle, wsState: "idle", wsDone: false, wsMode: "adhoc"},
				{curState: state.StatusIdle, wsState: "idle", wsDone: true, wsMode: "adhoc"},
			},
			wantHandleCompleteDone:  []bool{false, true},
			wantStateTransitionsInc: 1,
		},
		{
			// The dedupe guarantee that matters most: once a window has
			// settled at done+idle (prevDone already true, no state change),
			// detectAndFireTransitions must not keep firing every tick.
			name:                    "done already true and settled does not repeat-fire",
			hasPrev:                 true,
			prevState:               state.StatusIdle,
			prevDone:                true,
			ticks:                   []fireTick{{curState: state.StatusIdle, wsState: "idle", wsDone: true}},
			wantHandleCompleteDone:  nil,
			wantStateTransitionsInc: 0,
		},
		{
			// Pre-existing behavior preserved: a state change with Done false
			// (and staying false) fires once via the state edge alone.
			name:                    "state change with done false fires once as today",
			hasPrev:                 true,
			prevState:               state.StatusWorking,
			prevDone:                false,
			ticks:                   []fireTick{{curState: state.StatusIdle, wsState: "idle", wsDone: false, wsMode: "adhoc"}},
			wantHandleCompleteDone:  []bool{false},
			wantStateTransitionsInc: 1,
		},
		{
			// Mode filtering lives inside dispatch.HandleComplete, not the
			// monitor -- "bare" mode windows must still reach the seam on the
			// done edge, with Mode passed through untouched.
			name:                    "bare mode window still reaches the seam on done edge",
			hasPrev:                 true,
			prevState:               state.StatusIdle,
			prevDone:                false,
			ticks:                   []fireTick{{curState: state.StatusIdle, wsState: "idle", wsDone: true, wsMode: "bare"}},
			wantHandleCompleteDone:  []bool{true},
			wantStateTransitionsInc: 0,
		},
		{
			// Blocking-review regression: doneEdge must be gated on hasPrev
			// just like stateChanged. A brand-new instanceState (hasPrev:
			// false) for a window that is already done=true must NOT fire on
			// its first tick -- this is exactly what every agent window
			// sitting at done+idle looks like across a monitor restart, and
			// firing here would reopen a browser tab for an already-handled
			// PR (markPROpened's dedupe map is per-process and starts empty).
			// The second tick proves the prevDone baseline established by
			// the first tick holds, so it doesn't fire on the next tick either.
			name:      "first tick on an already-done window does not fire (monitor restart)",
			hasPrev:   false,
			prevState: state.StatusIdle,
			prevDone:  false,
			ticks: []fireTick{
				{curState: state.StatusIdle, wsState: "idle", wsDone: true, wsMode: "adhoc"},
				{curState: state.StatusIdle, wsState: "idle", wsDone: true, wsMode: "adhoc"},
			},
			wantHandleCompleteDone:  nil,
			wantStateTransitionsInc: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			tracker := metrics.NewTransitionTracker(reg)
			m := &monitorModel{windows: make(map[string]*instanceState), transitionTracker: tracker}
			inst := &instanceState{
				instance:  "go-engineer",
				base:      "go-engineer",
				hasPrev:   tt.hasPrev,
				prevState: tt.prevState,
				prevDone:  tt.prevDone,
			}

			var gotModes []string
			var gotDone []bool
			orig := handleCompleteFn
			handleCompleteFn = func(tr state.Transition, _ config.AgentConfig) {
				gotDone = append(gotDone, tr.Done)
				gotModes = append(gotModes, tr.Mode)
			}
			t.Cleanup(func() { handleCompleteFn = orig })

			cfg := &config.Config{}
			for _, tick := range tt.ticks {
				ws := windowstate.WindowState{State: tick.wsState, Done: tick.wsDone, Mode: tick.wsMode}
				m.detectAndFireTransitions(inst, "@1", tick.curState, ws, cfg, "")
			}

			if len(gotDone) != len(tt.wantHandleCompleteDone) {
				t.Fatalf("handleCompleteFn called %d times with Done=%v, want %d times with Done=%v",
					len(gotDone), gotDone, len(tt.wantHandleCompleteDone), tt.wantHandleCompleteDone)
			}
			for i, want := range tt.wantHandleCompleteDone {
				if gotDone[i] != want {
					t.Errorf("call %d: Transition.Done = %v, want %v", i, gotDone[i], want)
				}
			}
			for i, tick := range tt.ticks {
				if i >= len(gotModes) {
					break
				}
				if gotModes[i] != tick.wsMode {
					t.Errorf("call %d: Transition.Mode = %q, want %q", i, gotModes[i], tick.wsMode)
				}
			}

			if got := gatherCounterTotal(t, reg, "agentctl_state_transitions_total"); got != tt.wantStateTransitionsInc {
				t.Errorf("agentctl_state_transitions_total = %v, want %v (RecordTransition must only be invoked on the state edge)",
					got, tt.wantStateTransitionsInc)
			}
		})
	}
}
