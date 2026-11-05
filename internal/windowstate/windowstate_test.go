package windowstate_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/windowstate"
)

// initTemp sets a temp directory as the window state dir and returns a cleanup func.
func initTemp(t *testing.T) func() {
	t.Helper()
	tmp := t.TempDir()
	// Directly set via Init by hijacking instance naming.
	// We expose a test-only setter for the dir.
	windowstate.SetDirForTest(filepath.Join(tmp, "windows"))
	return func() { windowstate.SetDirForTest("") }
}

func TestReadWriteRoundTrip(t *testing.T) {
	defer initTemp(t)()

	wid := "@42"
	want := windowstate.WindowState{
		State:         "working",
		LogPath:       "/tmp/abc-123.jsonl",
		SessionID:     "abc-123",
		Done:          false,
		LastToolTime:  1711814400,
		Ticket:        "99",
		TicketTitle:   "fix flicker bug",
		Mode:          "dispatch",
		SubagentCount: 2,
		Error:         "",
		LastError:     "Read: file not found",
	}

	if err := windowstate.Write(wid, want); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := windowstate.Read(wid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if got.State != want.State {
		t.Errorf("State: got %q want %q", got.State, want.State)
	}
	if got.LogPath != want.LogPath {
		t.Errorf("LogPath: got %q want %q", got.LogPath, want.LogPath)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("SessionID: got %q want %q", got.SessionID, want.SessionID)
	}
	if got.Done != want.Done {
		t.Errorf("Done: got %v want %v", got.Done, want.Done)
	}
	if got.LastToolTime != want.LastToolTime {
		t.Errorf("LastToolTime: got %d want %d", got.LastToolTime, want.LastToolTime)
	}
	if got.Ticket != want.Ticket {
		t.Errorf("Ticket: got %q want %q", got.Ticket, want.Ticket)
	}
	if got.TicketTitle != want.TicketTitle {
		t.Errorf("TicketTitle: got %q want %q", got.TicketTitle, want.TicketTitle)
	}
	if got.Mode != want.Mode {
		t.Errorf("Mode: got %q want %q", got.Mode, want.Mode)
	}
	if got.SubagentCount != want.SubagentCount {
		t.Errorf("SubagentCount: got %d want %d", got.SubagentCount, want.SubagentCount)
	}
	if got.LastError != want.LastError {
		t.Errorf("LastError: got %q want %q", got.LastError, want.LastError)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set by Write")
	}
}

func TestSpawnedByRoundTrip(t *testing.T) {
	defer initTemp(t)()

	wid := "@41"
	want := windowstate.WindowState{
		State:            "working",
		SpawnedBy:        "@36",
		SpawnedBySession: "00000000-0000-4000-8000-000000000001",
	}

	if err := windowstate.Write(wid, want); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := windowstate.Read(wid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.SpawnedBy != want.SpawnedBy {
		t.Errorf("SpawnedBy: got %q want %q", got.SpawnedBy, want.SpawnedBy)
	}
	if got.SpawnedBySession != want.SpawnedBySession {
		t.Errorf("SpawnedBySession: got %q want %q", got.SpawnedBySession, want.SpawnedBySession)
	}
}

func TestSpawnedByOmittedWhenEmpty(t *testing.T) {
	defer initTemp(t)()

	wid := "@42"
	if err := windowstate.Write(wid, windowstate.WindowState{State: "working"}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(windowstate.Dir(), "@42.json"))
	if err != nil {
		t.Fatalf("reading raw file: %v", err)
	}
	if strings.Contains(string(raw), "spawned_by") {
		t.Errorf("expected spawned_by fields to be omitted from JSON when empty, got: %s", raw)
	}
}

func TestReadMissing(t *testing.T) {
	defer initTemp(t)()

	ws, err := windowstate.Read("@nonexistent")
	if err != nil {
		t.Fatalf("Read missing: expected nil error, got %v", err)
	}
	if ws.State != "" || ws.LogPath != "" || ws.Done {
		t.Errorf("Read missing: expected zero value, got %+v", ws)
	}
}

func TestRemove(t *testing.T) {
	defer initTemp(t)()

	wid := "@99"
	if err := windowstate.Write(wid, windowstate.WindowState{State: "idle"}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Verify it was written.
	ws, err := windowstate.Read(wid)
	if err != nil || ws.State != "idle" {
		t.Fatalf("Read after Write: err=%v ws=%+v", err, ws)
	}

	windowstate.Remove(wid)

	// Should return zero value after removal.
	ws, err = windowstate.Read(wid)
	if err != nil {
		t.Fatalf("Read after Remove: %v", err)
	}
	if ws.State != "" {
		t.Errorf("Read after Remove: expected zero value, got state=%q", ws.State)
	}
}

func TestUpdateBasic(t *testing.T) {
	defer initTemp(t)()

	wid := "@10"
	// Initial write.
	if err := windowstate.Write(wid, windowstate.WindowState{State: "idle", Ticket: "42"}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Update only State; Ticket should be preserved.
	if err := windowstate.Update(wid, func(ws *windowstate.WindowState) {
		ws.State = "working"
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := windowstate.Read(wid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.State != "working" {
		t.Errorf("State: got %q want %q", got.State, "working")
	}
	if got.Ticket != "42" {
		t.Errorf("Ticket should be preserved: got %q want %q", got.Ticket, "42")
	}
}

func TestUpdateAtomicity(t *testing.T) {
	defer initTemp(t)()

	wid := "@5"
	// Seed subagent_count = 0.
	if err := windowstate.Write(wid, windowstate.WindowState{}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if err := windowstate.Update(wid, func(ws *windowstate.WindowState) {
				ws.SubagentCount++
			}); err != nil {
				t.Errorf("Update: %v", err)
			}
		}()
	}
	wg.Wait()

	got, err := windowstate.Read(wid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.SubagentCount != goroutines {
		t.Errorf("SubagentCount after %d concurrent increments: got %d want %d",
			goroutines, got.SubagentCount, goroutines)
	}
}

// TestRawWriteRaceCanLoseFields documents the mechanism behind the
// disappearing-spawned_by bug: a raw, unlocked Write() call racing against a
// concurrent, lock-respecting Update() can be silently clobbered if the
// Update() call's Read() happened before the Write() landed. This is why
// tmux.LaunchAgent's bootstrap was switched from a raw Write() to an
// Update(), which the companion TestConcurrentUpdatesNeverLoseFields proves
// closes this exact race.
func TestRawWriteRaceCanLoseFields(t *testing.T) {
	defer initTemp(t)()
	wid := "@1"

	proceed := make(chan struct{})
	done := make(chan struct{})

	// Simulates the monitor's per-tick windowstate.Update() (applyVisuals),
	// which runs for every known window every tick, including a window that
	// just appeared this same tick. It acquires the lock, reads the
	// (currently nonexistent) file, and then blocks mid-critical-section --
	// modeling a tick whose Read() raced ahead of the bootstrap write below.
	go func() {
		defer close(done)
		windowstate.Update(wid, func(ws *windowstate.WindowState) { //nolint:errcheck
			<-proceed
			ws.CtxPct = 42
		})
	}()

	// Give the goroutine above time to acquire the lock and start its Read()
	// before the racing bootstrap write below lands.
	time.Sleep(50 * time.Millisecond)

	// Simulates the OLD bootstrap bug: a raw, unlocked Write() that fully
	// replaces the struct. Because Write() takes no lock, it can (and here,
	// deliberately does) land while the goroutine above still holds the
	// flock and is mid-critical-section.
	if err := windowstate.Write(wid, windowstate.WindowState{SpawnedBy: "@105"}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	close(proceed)
	<-done

	got, err := windowstate.Read(wid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// The goroutine's stale pre-Write Read() clobbers SpawnedBy when it
	// finally writes back -- this is the historical bug, reproduced.
	if got.SpawnedBy != "" {
		t.Fatalf("expected the lost-update race to drop SpawnedBy, but it survived as %q", got.SpawnedBy)
	}
	if got.CtxPct != 42 {
		t.Errorf("CtxPct = %d, want 42 (should still be set by the racing Update)", got.CtxPct)
	}
}

// TestConcurrentUpdatesNeverLoseFields proves the fix: when BOTH writers use
// Update() (as tmux.LaunchAgent's bootstrap now does, instead of a raw
// Write()), they share the same flock, so the second writer to run always
// starts from whatever the first left in place -- no lost fields, regardless
// of which one happens to run first.
func TestConcurrentUpdatesNeverLoseFields(t *testing.T) {
	defer initTemp(t)()
	wid := "@1"

	proceedA := make(chan struct{})
	doneA := make(chan struct{})
	doneB := make(chan struct{})

	// Goroutine A: holds the lock and blocks mid-critical-section, exactly
	// as in TestRawWriteRaceCanLoseFields.
	go func() {
		defer close(doneA)
		windowstate.Update(wid, func(ws *windowstate.WindowState) { //nolint:errcheck
			<-proceedA
			ws.CtxPct = 42
		})
	}()
	time.Sleep(50 * time.Millisecond) // let A acquire the lock and block in its fn

	// Goroutine B: the FIXED bootstrap -- Update(), not raw Write(). Since it
	// takes the same flock, this call blocks (real OS-level flock contention)
	// until A releases, so it can never observe a stale state to clobber.
	go func() {
		defer close(doneB)
		if err := windowstate.Update(wid, func(ws *windowstate.WindowState) {
			ws.SpawnedBy = "@105"
		}); err != nil {
			t.Errorf("Update: %v", err)
		}
	}()
	time.Sleep(50 * time.Millisecond) // let B block waiting for A's lock

	close(proceedA) // release A; it writes {CtxPct: 42} and unlocks
	<-doneA
	<-doneB // B then acquires, reads {CtxPct: 42}, adds SpawnedBy, writes back

	got, err := windowstate.Read(wid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.SpawnedBy != "@105" {
		t.Errorf("SpawnedBy = %q, want %q -- Update()-vs-Update() must never lose a field", got.SpawnedBy, "@105")
	}
	if got.CtxPct != 42 {
		t.Errorf("CtxPct = %d, want 42", got.CtxPct)
	}
}

func TestFindBySessionID(t *testing.T) {
	defer initTemp(t)()

	// Write several windows.
	for _, tc := range []struct {
		wid       string
		sessionID string
	}{
		{"@1", "session-aaa"},
		{"@2", "session-bbb"},
		{"@3", "session-ccc"},
	} {
		if err := windowstate.Write(tc.wid, windowstate.WindowState{SessionID: tc.sessionID}); err != nil {
			t.Fatalf("Write %s: %v", tc.wid, err)
		}
	}

	found := windowstate.FindBySessionID("session-bbb")
	if found != "@2" {
		t.Errorf("FindBySessionID: got %q want %q", found, "@2")
	}

	notFound := windowstate.FindBySessionID("session-zzz")
	if notFound != "" {
		t.Errorf("FindBySessionID missing: got %q want empty", notFound)
	}
}

func TestAll(t *testing.T) {
	defer initTemp(t)()

	for _, tc := range []struct {
		wid string
		ws  windowstate.WindowState
	}{
		{"@1", windowstate.WindowState{State: "working", Branch: "agent/go-engineer/issue-1"}},
		{"@2", windowstate.WindowState{State: "idle", WorktreeDir: "/repo/.worktrees/foo"}},
	} {
		if err := windowstate.Write(tc.wid, tc.ws); err != nil {
			t.Fatalf("Write %s: %v", tc.wid, err)
		}
	}

	all := windowstate.All()
	if len(all) != 2 {
		t.Fatalf("All() returned %d entries, want 2 (got: %+v)", len(all), all)
	}
	if all["@1"].Branch != "agent/go-engineer/issue-1" {
		t.Errorf("All()[@1].Branch = %q, want agent/go-engineer/issue-1", all["@1"].Branch)
	}
	if all["@2"].WorktreeDir != "/repo/.worktrees/foo" {
		t.Errorf("All()[@2].WorktreeDir = %q, want /repo/.worktrees/foo", all["@2"].WorktreeDir)
	}
}

func TestAll_EmptyDirReturnsEmptyMapNotError(t *testing.T) {
	defer initTemp(t)()

	all := windowstate.All()
	if len(all) != 0 {
		t.Fatalf("All() on an empty dir returned %d entries, want 0", len(all))
	}
}

func TestBranchAndWorktreeDirRoundTrip(t *testing.T) {
	defer initTemp(t)()

	wid := "@50"
	want := windowstate.WindowState{
		State:       "working",
		Branch:      "agent/go-engineer/issue-42",
		WorktreeDir: "/repo/.worktrees/agent-go-engineer-issue-42",
	}
	if err := windowstate.Write(wid, want); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := windowstate.Read(wid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Branch != want.Branch {
		t.Errorf("Branch: got %q want %q", got.Branch, want.Branch)
	}
	if got.WorktreeDir != want.WorktreeDir {
		t.Errorf("WorktreeDir: got %q want %q", got.WorktreeDir, want.WorktreeDir)
	}
}

func TestRemoveAll(t *testing.T) {
	cleanup := initTemp(t)
	defer cleanup()

	// Create a couple of state files.
	_ = windowstate.Write("@0", windowstate.WindowState{ChromeBarPane: "%1"})
	_ = windowstate.Write("@1", windowstate.WindowState{ChromeBarPane: "%3"})

	// Verify they exist.
	entries, _ := os.ReadDir(windowstate.Dir())
	if len(entries) == 0 {
		t.Fatal("expected state files before RemoveAll")
	}

	// RemoveAll should clear them.
	if err := windowstate.RemoveAll(); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	entries, _ = os.ReadDir(windowstate.Dir())
	if len(entries) != 0 {
		t.Fatalf("expected empty dir after RemoveAll, got %d files", len(entries))
	}
}

func TestReadLogPathConsistencyGuard(t *testing.T) {
	defer initTemp(t)()

	tests := []struct {
		name        string
		logPath     string
		sessionID   string
		wantLogPath string
	}{
		{
			name:        "matching basename preserved",
			logPath:     "/Users/alice/.claude/projects/-Users-alice-src-agentctl/00000000-0000-4000-8000-000000000001.jsonl",
			sessionID:   "00000000-0000-4000-8000-000000000001",
			wantLogPath: "/Users/alice/.claude/projects/-Users-alice-src-agentctl/00000000-0000-4000-8000-000000000001.jsonl",
		},
		{
			// Regression case for window @89: a resumed session legitimately
			// logs to the directory where it was originally created, not the
			// window's current cwd. Only the basename/session_id relationship
			// matters -- the directory must never be considered.
			name:        "cross-project resume with matching basename is valid",
			logPath:     "/Users/alice/.claude/projects/-Users-alice-src-side-project/fca7ce23.jsonl",
			sessionID:   "fca7ce23",
			wantLogPath: "/Users/alice/.claude/projects/-Users-alice-src-side-project/fca7ce23.jsonl",
		},
		{
			// Regression case for window @93: session_id and log_path
			// diverged (stale log_path from a different repo entirely).
			name:        "mismatched basename ignored",
			logPath:     "/Users/alice/.claude/projects/-Users-alice-src-acme-repo-a--worktrees-agent-go-engineer-adhoc-04abce4a/00000000-0000-4000-8000-000000000002.jsonl",
			sessionID:   "00000000-0000-4000-8000-000000000001",
			wantLogPath: "",
		},
		{
			name:        "empty session_id no false positive",
			logPath:     "/Users/alice/.claude/projects/foo/bar.jsonl",
			sessionID:   "",
			wantLogPath: "/Users/alice/.claude/projects/foo/bar.jsonl",
		},
		{
			name:        "empty log_path no false positive",
			logPath:     "",
			sessionID:   "00000000-0000-4000-8000-000000000001",
			wantLogPath: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wid := "@" + tc.name
			if err := windowstate.Write(wid, windowstate.WindowState{
				LogPath:   tc.logPath,
				SessionID: tc.sessionID,
			}); err != nil {
				t.Fatalf("Write: %v", err)
			}

			got, err := windowstate.Read(wid)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if got.LogPath != tc.wantLogPath {
				t.Errorf("LogPath: got %q want %q", got.LogPath, tc.wantLogPath)
			}
			// SessionID must never be touched by the consistency guard.
			if got.SessionID != tc.sessionID {
				t.Errorf("SessionID: got %q want %q", got.SessionID, tc.sessionID)
			}
		})
	}
}

func TestReadLogPathGuardDoesNotRewriteFile(t *testing.T) {
	defer initTemp(t)()

	wid := "@corrupt"
	if err := windowstate.Write(wid, windowstate.WindowState{
		LogPath:   "/tmp/other-session.jsonl",
		SessionID: "this-session",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Read repeatedly -- the on-disk record must remain untouched (no
	// write-amplification on the read path). We verify this indirectly: the
	// raw file content should still contain the original (inconsistent)
	// log_path even though Read() ignores it in the returned value.
	for i := 0; i < 3; i++ {
		got, err := windowstate.Read(wid)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got.LogPath != "" {
			t.Errorf("Read #%d: expected ignored LogPath, got %q", i, got.LogPath)
		}
	}

	raw, err := os.ReadFile(filepath.Join(windowstate.Dir(), "@corrupt.json"))
	if err != nil {
		t.Fatalf("reading raw file: %v", err)
	}
	if !strings.Contains(string(raw), "other-session.jsonl") {
		t.Errorf("expected on-disk record to remain unmodified by Read, got: %s", raw)
	}
}

func TestWriteSetsUpdatedAt(t *testing.T) {
	defer initTemp(t)()

	before := time.Now()
	if err := windowstate.Write("@7", windowstate.WindowState{State: "idle"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	after := time.Now()

	ws, err := windowstate.Read("@7")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if ws.UpdatedAt.Before(before) || ws.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt %v not between %v and %v", ws.UpdatedAt, before, after)
	}
}

// ── CtxFresh ──────────────────────────────────────────────────────

func TestCtxFresh(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)

	tests := []struct {
		name string
		ws   windowstate.WindowState
		want bool
	}{
		{
			name: "derived source: never fresh",
			ws:   windowstate.WindowState{CtxPctSource: "derived", CtxPushedAt: now},
			want: false,
		},
		{
			name: "statusline source but zero CtxPushedAt: never fresh",
			ws:   windowstate.WindowState{CtxPctSource: "statusline"},
			want: false,
		},
		{
			name: "statusline source, pushed 1 minute ago: fresh",
			ws:   windowstate.WindowState{CtxPctSource: "statusline", CtxPushedAt: now.Add(-1 * time.Minute)},
			want: true,
		},
		{
			name: "statusline source, exactly at freshness boundary: fresh",
			ws:   windowstate.WindowState{CtxPctSource: "statusline", CtxPushedAt: now.Add(-windowstate.CtxPctFreshness)},
			want: true,
		},
		{
			name: "statusline source, stale beyond freshness window: not fresh",
			ws:   windowstate.WindowState{CtxPctSource: "statusline", CtxPushedAt: now.Add(-windowstate.CtxPctFreshness - time.Minute)},
			want: false,
		},
		{
			name: "empty source: never fresh",
			ws:   windowstate.WindowState{CtxPushedAt: now},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ws.CtxFresh(now); got != tt.want {
				t.Errorf("CtxFresh() = %v, want %v", got, tt.want)
			}
		})
	}
}
