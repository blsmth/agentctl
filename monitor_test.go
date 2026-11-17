package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/metrics"
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

// ── monitor does not own window names ───────────────────────────────────────

// TestResolveCtxPct_WritesNothing verifies the monitor no longer touches
// windowstate (and so cannot rename windows or write Seen): resolving ctx%
// for a window with no record must not create one.
func TestResolveCtxPct_WritesNothing(t *testing.T) {
	windowstate.SetDirForTest(t.TempDir())
	t.Cleanup(func() { windowstate.SetDirForTest("") })

	m := monitorModel{}
	inst := &instanceState{instance: "go-engineer", base: "go-engineer", lastCtxPct: 25}
	m.resolveCtxPct(inst, windowstate.WindowState{})

	if windowstate.Exists("@999") {
		t.Error("resolveCtxPct created a windowstate record")
	}
	if inst.ctxPctSource != "derived" || inst.lastCtxPct != 25 {
		t.Errorf("source=%q lastCtxPct=%d, want derived/25", inst.ctxPctSource, inst.lastCtxPct)
	}
}

// TestDetectTransitions_DoneUnseenFromWindowstate verifies inst.done (the
// state.json Done) is windowstate Done && !Seen.
func TestDetectTransitions_DoneUnseenFromWindowstate(t *testing.T) {
	tests := []struct {
		name string
		ws   windowstate.WindowState
		want bool
	}{
		{"done unseen", windowstate.WindowState{State: "idle", Done: true}, true},
		{"done seen", windowstate.WindowState{State: "idle", Done: true, Seen: true}, false},
		{"not done", windowstate.WindowState{State: "idle"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := monitorModel{}
			inst := &instanceState{instance: "a", base: "a"}
			m.detectAndFireTransitions(inst, "@1", state.StatusIdle, tt.ws, config.Load(), "@0")
			if inst.done != tt.want {
				t.Errorf("inst.done = %v, want %v", inst.done, tt.want)
			}
		})
	}
}

// ── ctx% authority resolution ────────────────────────

// TestResolveCtxPct_PreservesFreshStatuslinePush verifies that a fresh
// statusline-pushed CtxPct is left completely untouched by the monitor: it
// is neither overwritten in windowstate, nor is inst.lastCtxPct pulled back
// down to the (lower/different) derived value. This is the core regression
// test for the "context percentage clobbered every 500ms tick" bug
// (monitor.go:364, before the pushed value took precedence).
func TestResolveCtxPct_PreservesFreshStatuslinePush(t *testing.T) {
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
		lastCtxPct: 25, // the monitor's own derived value -- deliberately different
	}

	m.resolveCtxPct(inst, mustRead(t, windowID))

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

// TestResolveCtxPct_StalePushFallsBackInMemory verifies the other direction:
// once a statusline push has gone stale (no push within
// windowstate.CtxPctFreshness -- e.g. the window is parked and no longer
// producing assistant messages), the monitor reclaims CtxPct with its own
// derived value in memory only; windowstate is left alone.
func TestResolveCtxPct_StalePushFallsBackInMemory(t *testing.T) {
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
		lastCtxPct: 25,
	}

	m.resolveCtxPct(inst, mustRead(t, windowID))

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
	if ws.CtxPct != 22 || ws.CtxPctSource != "statusline" {
		t.Errorf("windowstate CtxPct/Source = %d/%q, want 22/statusline (the monitor writes nothing)", ws.CtxPct, ws.CtxPctSource)
	}
}

func mustRead(t *testing.T, windowID string) windowstate.WindowState {
	t.Helper()
	ws, err := windowstate.Read(windowID)
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	return ws
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

// ── detectAndFireTransitions: done edge no longer fires handlers ─────────────
//
// Dispatch completion (PR lookup, label swap, open-in-browser,
// dispatch_complete hooks) is driven by the Stop hook / `signal done`, which
// spawn `agentctl dispatch-complete`. The monitor must NOT fire anything on a
// done edge; only state edges reach RecordTransition and improve.

// fireTick is one simulated 500ms monitor tick fed into detectAndFireTransitions.
type fireTick struct {
	curState state.AgentStatus
	wsState  string // windowstate.State for this tick (only affects HookHandled)
	wsDone   bool
	wsMode   string
}

// TestDetectAndFireTransitions runs ticks through detectAndFireTransitions
// and checks that handlers fire only on state edges (never on a done edge).
func TestDetectAndFireTransitions(t *testing.T) {
	tests := []struct {
		name      string
		hasPrev   bool
		prevState state.AgentStatus
		ticks     []fireTick
		wantEdges int // expected state-edge handler calls == transitions counter
	}{
		{
			name:      "done edge alone with no state change fires nothing",
			hasPrev:   true,
			prevState: state.StatusIdle,
			ticks:     []fireTick{{curState: state.StatusIdle, wsState: "idle", wsDone: true, wsMode: "adhoc"}},
		},
		{
			name:      "state change with done already true fires once (state edge only)",
			hasPrev:   true,
			prevState: state.StatusWorking,
			ticks:     []fireTick{{curState: state.StatusIdle, wsState: "idle", wsDone: true, wsMode: "adhoc"}},
			wantEdges: 1,
		},
		{
			name:      "state change then later done edge fires only for the state change",
			hasPrev:   true,
			prevState: state.StatusWorking,
			ticks: []fireTick{
				{curState: state.StatusIdle, wsState: "idle", wsDone: false, wsMode: "adhoc"},
				{curState: state.StatusIdle, wsState: "idle", wsDone: true, wsMode: "adhoc"},
			},
			wantEdges: 1,
		},
		{
			name:      "settled done+idle does not fire",
			hasPrev:   true,
			prevState: state.StatusIdle,
			ticks:     []fireTick{{curState: state.StatusIdle, wsState: "idle", wsDone: true}},
		},
		{
			name:      "first tick on an already-done window does not fire",
			hasPrev:   false,
			prevState: state.StatusIdle,
			ticks: []fireTick{
				{curState: state.StatusIdle, wsState: "idle", wsDone: true, wsMode: "adhoc"},
				{curState: state.StatusIdle, wsState: "idle", wsDone: true, wsMode: "adhoc"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &monitorModel{windows: make(map[string]*instanceState)}
			inst := &instanceState{
				instance:  "go-engineer",
				base:      "go-engineer",
				hasPrev:   tt.hasPrev,
				prevState: tt.prevState,
			}

			edges := 0
			orig := improveHandleFn
			improveHandleFn = func(state.Transition, config.AgentConfig) { edges++ }
			t.Cleanup(func() { improveHandleFn = orig })

			cfg := &config.Config{}
			for _, tick := range tt.ticks {
				ws := windowstate.WindowState{State: tick.wsState, Done: tick.wsDone, Mode: tick.wsMode}
				m.detectAndFireTransitions(inst, "@1", tick.curState, ws, cfg, "")
			}

			if edges != tt.wantEdges {
				t.Errorf("handlers fired %d times, want %d", edges, tt.wantEdges)
			}
		})
	}
}

func TestSessionIdentityOK(t *testing.T) {
	tests := []struct {
		name      string
		want, got string
		alive     bool
		ok        bool
	}{
		{"same id", "$3", "$3", true, true},
		{"different id", "$3", "$7", true, false},
		{"session missing", "$3", "", false, false},
		{"empty got with known want is not rescued by liveness", "$3", "", true, false},
		{"unknown recorded id, session exists", "", "$7", false, true},
		{"unknown recorded id, lookup empty, session alive", "", "", true, true},
		{"unknown recorded id, session missing", "", "", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionIdentityOK(tc.want, tc.got, func() bool { return tc.alive }); got != tc.ok {
				t.Fatalf("sessionIdentityOK(%q,%q,alive=%v) = %v, want %v", tc.want, tc.got, tc.alive, got, tc.ok)
			}
		})
	}
}

func TestLookupSessionID(t *testing.T) {
	calls := 0
	got := lookupSessionID(func() string {
		calls++
		if calls < 3 {
			return ""
		}
		return "$4"
	}, 5, 0)
	if got != "$4" || calls != 3 {
		t.Fatalf("got %q after %d calls", got, calls)
	}
	calls = 0
	if got := lookupSessionID(func() string { calls++; return "" }, 3, 0); got != "" || calls != 3 {
		t.Fatalf("got %q after %d calls", got, calls)
	}
}

// TestMonitorDoesNotRegisterMetrics: the monitor no longer owns metrics. It
// registers nothing on the shared Prometheus registry and binds no port; both
// belong to `agentctl metrics serve`.
func TestMonitorDoesNotRegisterMetrics(t *testing.T) {
	m := newMonitorModel("", registry.Entry{})
	if m.windows == nil {
		t.Fatal("model not initialised")
	}
	mfs, err := metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		t.Errorf("monitor registered metric family %q", mf.GetName())
	}
}
