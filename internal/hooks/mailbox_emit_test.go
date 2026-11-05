package hooks

// mailbox_emit_test.go — tests for Phase 3 of agent messaging: auto-emitting
// "done" (CheckDone / Stop hook) and "blocked" (Notify / Notification hook)
// mailbox messages to a dispatched agent's parent, with zero agent
// cooperation. See docs/agent-messaging-design.md §4.
//
// All tests are hermetic:
//   - t.Setenv("HOME", t.TempDir()) isolates any code path that might fall
//     back to a real home-directory default (mailbox.resolveDir does this
//     when SetDirForTest hasn't been called).
//   - mailbox.SetDirForTest isolates the on-disk mailbox store.
//   - windowstate.SetDirForTest isolates window state files.
//   - hooks.SetDepsForTest / hooks.SetAutonomousModeFn inject stubs.

import (
	"errors"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// ---------------------------------------------------------------------------
// shouldEmitDone: pure decision, table-driven
// ---------------------------------------------------------------------------

func TestShouldEmitDone(t *testing.T) {
	tests := []struct {
		name           string
		spawnedBy      string
		mode           string
		stopHookActive bool
		blockReason    string
		autonomous     bool
		want           bool
	}{
		{
			name:       "spawned + autonomous + checks passed -> emit",
			spawnedBy:  "@36",
			mode:       "dispatch",
			autonomous: true,
			want:       true,
		},
		{
			name:       "no spawning parent -> no emit",
			spawnedBy:  "",
			mode:       "dispatch",
			autonomous: true,
			want:       false,
		},
		{
			name:           "stop_hook_active -> no emit",
			spawnedBy:      "@36",
			mode:           "dispatch",
			stopHookActive: true,
			autonomous:     true,
			want:           false,
		},
		{
			name:        "checkDoneReason blocked (exit-2 path) -> no emit",
			spawnedBy:   "@36",
			mode:        "dispatch",
			blockReason: "uncommitted changes",
			autonomous:  true,
			want:        false,
		},
		{
			name:       "interactive/non-autonomous mode -> no emit",
			spawnedBy:  "@36",
			mode:       "discuss",
			autonomous: false,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetAutonomousModeFn(func(mode string) bool {
				if mode != tt.mode {
					t.Errorf("autonomousModeFn called with mode=%q, want %q", mode, tt.mode)
				}
				return tt.autonomous
			})
			t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })

			got := shouldEmitDone(tt.spawnedBy, tt.mode, tt.stopHookActive, tt.blockReason)
			if got != tt.want {
				t.Errorf("shouldEmitDone(%q, %q, %v, %q) = %v, want %v",
					tt.spawnedBy, tt.mode, tt.stopHookActive, tt.blockReason, got, tt.want)
			}
		})
	}
}

// TestShouldEmitDone_EmptySpawnedByReturnsFalse verifies spawnedBy=="" always
// returns false regardless of autonomousModeFn's answer. Note: shouldEmitDone
// is now defined as shouldMarkDone(...) && spawnedBy != "" (see shouldMarkDone,
// which is also consulted directly by CheckDone for the Done fact), so
// autonomousModeFn IS consulted here -- there's no longer a short-circuit on
// spawnedBy alone, since the shared predicate can't know about spawnedBy.
func TestShouldEmitDone_EmptySpawnedByReturnsFalse(t *testing.T) {
	SetAutonomousModeFn(func(string) bool {
		return true
	})
	t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })

	if shouldEmitDone("", "dispatch", false, "") {
		t.Fatal("expected false when spawnedBy is empty")
	}
}

// ---------------------------------------------------------------------------
// shouldEmitBlocked: pure decision, table-driven
// ---------------------------------------------------------------------------

func TestShouldEmitBlocked(t *testing.T) {
	tests := []struct {
		name             string
		spawnedBy        string
		notificationType string
		prevState        string
		want             bool
	}{
		{"permission_prompt + spawned + prev idle -> emit", "@36", "permission_prompt", "idle", true},
		{"permission_prompt + spawned + prev working -> emit", "@36", "permission_prompt", "working", true},
		{"permission_prompt + spawned + prev empty -> emit (first-ever notification)", "@36", "permission_prompt", "", true},
		{"permission_prompt + spawned + prev blocked -> no emit (dedup, already blocked)", "@36", "permission_prompt", "blocked", false},
		{"idle_prompt + spawned + prev idle -> no emit", "@36", "idle_prompt", "idle", false},
		{"idle_prompt + spawned + prev blocked -> no emit", "@36", "idle_prompt", "blocked", false},
		{"permission_prompt + unspawned + prev idle -> no emit", "", "permission_prompt", "idle", false},
		{"idle_prompt + unspawned -> no emit", "", "idle_prompt", "idle", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldEmitBlocked(tt.spawnedBy, tt.notificationType, tt.prevState)
			if got != tt.want {
				t.Errorf("shouldEmitBlocked(%q, %q, %q) = %v, want %v", tt.spawnedBy, tt.notificationType, tt.prevState, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CheckDone: end-to-end auto-emit of "done"
// ---------------------------------------------------------------------------

// setupMailbox isolates the mailbox store (and $HOME, for any code path that
// might fall back to it) for the duration of a test.
func setupMailbox(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	dir := t.TempDir()
	mailbox.SetDirForTest(dir)
	t.Cleanup(func() { mailbox.SetDirForTest("") })
}

// seedSpawnedWindow writes a windowstate record with SpawnedBy/SpawnedBySession
// and Mode set, mirroring what dispatch.Launch records at dispatch time.
func seedSpawnedWindow(t *testing.T, windowID, spawnedBy, spawnedBySession, mode string) {
	t.Helper()
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.SpawnedBy = spawnedBy
		ws.SpawnedBySession = spawnedBySession
		ws.Mode = mode
	}); err != nil {
		t.Fatalf("seed windowstate: %v", err)
	}
}

// TestCheckDone_EmitsDoneWhenSpawnedAndAutonomous verifies the full path: a
// dispatch-mode window with SpawnedBy set gets a real "done" message enqueued
// to (SpawnedBy, SpawnedBySession) with the last assistant message as body,
// using the real mailbox package (not a stub) so the on-disk write is
// actually exercised.
func TestCheckDone_EmitsDoneWhenSpawnedAndAutonomous(t *testing.T) {
	setupWindowstate(t)
	setupMailbox(t)

	SetAutonomousModeFn(func(mode string) bool { return mode == "dispatch" })
	t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })

	d := stubDeps(t)
	d.EnqueueMailboxMessage = mailbox.Enqueue // exercise the real mailbox package
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const childWindow = "@41"
	const parentWindow = "@36"
	const parentSession = "00000000-0000-4000-8000-000000000001"
	seedSpawnedWindow(t, childWindow, parentWindow, parentSession, "dispatch")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", childWindow)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")

	stdin := `{"stop_hook_active":false,"last_assistant_message":"Opened PR #948, all tests pass."}`
	runHookCaptured(t, nil, stdin, CheckDone)

	pending, err := mailbox.Pending(parentWindow)
	if err != nil {
		t.Fatalf("mailbox.Pending(%s): %v", parentWindow, err)
	}
	if len(pending) != 1 {
		t.Fatalf("mailbox.Pending(%s) = %d messages, want 1", parentWindow, len(pending))
	}

	msg := pending[0]
	if msg.Kind != mailbox.KindDone {
		t.Errorf("Kind = %q, want %q", msg.Kind, mailbox.KindDone)
	}
	if msg.To != parentWindow {
		t.Errorf("To = %q, want %q (SpawnedBy)", msg.To, parentWindow)
	}
	if msg.ToSession != parentSession {
		t.Errorf("ToSession = %q, want %q (SpawnedBySession)", msg.ToSession, parentSession)
	}
	if msg.From != childWindow {
		t.Errorf("From = %q, want %q", msg.From, childWindow)
	}
	if !strings.Contains(msg.Body, "Opened PR #948") {
		t.Errorf("Body %q does not contain the last assistant message", msg.Body)
	}
	if msg.Hops != 0 {
		t.Errorf("Hops = %d, want 0 (a freshly-originated auto-message, not a reply)", msg.Hops)
	}
}

// TestCheckDone_NoEmitWhenNotSpawned verifies that a window with no
// SpawnedBy (no dispatching parent) never enqueues anything, even when the
// mode is autonomous and checks pass.
func TestCheckDone_NoEmitWhenNotSpawned(t *testing.T) {
	setupWindowstate(t)
	setupMailbox(t)

	SetAutonomousModeFn(func(string) bool { return true })
	t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })

	enqueueCalled := false
	d := stubDeps(t)
	d.EnqueueMailboxMessage = func(m mailbox.Message) (string, error) {
		enqueueCalled = true
		return "id", nil
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const windowID = "@50"
	// No seed: SpawnedBy stays "".

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")

	stdin := `{"stop_hook_active":false,"last_assistant_message":"done"}`
	runHookCaptured(t, nil, stdin, CheckDone)

	if enqueueCalled {
		t.Error("EnqueueMailboxMessage was called for a window with no SpawnedBy")
	}
}

// TestCheckDone_NoEmitWhenStopHookActive verifies that a blocked-stop retry
// (stop_hook_active=true) never enqueues a done message, even for a spawned,
// autonomous window.
func TestCheckDone_NoEmitWhenStopHookActive(t *testing.T) {
	setupWindowstate(t)
	setupMailbox(t)

	SetAutonomousModeFn(func(string) bool { return true })
	t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })

	enqueueCalled := false
	d := stubDeps(t)
	d.EnqueueMailboxMessage = func(m mailbox.Message) (string, error) {
		enqueueCalled = true
		return "id", nil
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const windowID = "@51"
	seedSpawnedWindow(t, windowID, "@30", "session-abc", "dispatch")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")

	stdin := `{"stop_hook_active":true,"last_assistant_message":"done"}`
	runHookCaptured(t, nil, stdin, CheckDone)

	if enqueueCalled {
		t.Error("EnqueueMailboxMessage was called when stop_hook_active=true")
	}
}

// TestCheckDone_NoEmitWhenNonAutonomousMode verifies that an interactive
// window (e.g. mode=discuss, or a bare window with no recorded mode) never
// gets an unsolicited done message even when SpawnedBy happens to be set.
func TestCheckDone_NoEmitWhenNonAutonomousMode(t *testing.T) {
	setupWindowstate(t)
	setupMailbox(t)

	// Mirrors dispatch.IsAutonomous's real behavior: only dispatch/adhoc/triage
	// are autonomous.
	SetAutonomousModeFn(func(mode string) bool {
		return mode == "dispatch" || mode == "adhoc" || mode == "triage"
	})
	t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })

	enqueueCalled := false
	d := stubDeps(t)
	d.EnqueueMailboxMessage = func(m mailbox.Message) (string, error) {
		enqueueCalled = true
		return "id", nil
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const windowID = "@52"
	seedSpawnedWindow(t, windowID, "@30", "session-abc", "discuss")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")

	stdin := `{"stop_hook_active":false,"last_assistant_message":"done"}`
	runHookCaptured(t, nil, stdin, CheckDone)

	if enqueueCalled {
		t.Error("EnqueueMailboxMessage was called for a non-autonomous (interactive) mode")
	}
}

// TestCheckDone_EnqueueFailureDoesNotChangeExitBehavior verifies that a
// mailbox enqueue failure is swallowed: CheckDone must still set state=idle
// and must not panic or otherwise alter its behavior. Hooks must never fail
// the turn over a local mailbox write error.
func TestCheckDone_EnqueueFailureDoesNotChangeExitBehavior(t *testing.T) {
	setupWindowstate(t)
	setupMailbox(t)

	SetAutonomousModeFn(func(string) bool { return true })
	t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })

	d := stubDeps(t)
	d.EnqueueMailboxMessage = func(m mailbox.Message) (string, error) {
		return "", errors.New("disk full")
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const windowID = "@53"
	seedSpawnedWindow(t, windowID, "@30", "session-abc", "dispatch")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")

	stdin := `{"stop_hook_active":false,"last_assistant_message":"done"}`
	// Must not panic despite the enqueue failure.
	runHookCaptured(t, nil, stdin, CheckDone)

	ws, err := windowstate.Read(windowID)
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.State != "idle" {
		t.Errorf("windowstate.State = %q, want idle (enqueue failure must not affect the stop path)", ws.State)
	}
}

// ---------------------------------------------------------------------------
// Notify: end-to-end auto-emit of "blocked"
// ---------------------------------------------------------------------------

// TestNotify_EmitsBlockedWhenSpawned verifies that a permission_prompt
// notification on a spawned window enqueues a real "blocked" message
// addressed to (SpawnedBy, SpawnedBySession).
func TestNotify_EmitsBlockedWhenSpawned(t *testing.T) {
	setupWindowstate(t)
	setupMailbox(t)

	d := stubDeps(t)
	d.EnqueueMailboxMessage = mailbox.Enqueue // exercise the real mailbox package
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const childWindow = "@60"
	const parentWindow = "@30"
	const parentSession = "session-xyz"
	seedSpawnedWindow(t, childWindow, parentWindow, parentSession, "dispatch")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_ID", childWindow)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"notification_type":"permission_prompt","message":"needs approval for rm -rf"}`
	runHookCaptured(t, nil, stdin, Notify)

	pending, err := mailbox.Pending(parentWindow)
	if err != nil {
		t.Fatalf("mailbox.Pending(%s): %v", parentWindow, err)
	}
	if len(pending) != 1 {
		t.Fatalf("mailbox.Pending(%s) = %d messages, want 1", parentWindow, len(pending))
	}

	msg := pending[0]
	if msg.Kind != mailbox.KindBlocked {
		t.Errorf("Kind = %q, want %q", msg.Kind, mailbox.KindBlocked)
	}
	if msg.To != parentWindow {
		t.Errorf("To = %q, want %q (SpawnedBy)", msg.To, parentWindow)
	}
	if msg.ToSession != parentSession {
		t.Errorf("ToSession = %q, want %q (SpawnedBySession)", msg.ToSession, parentSession)
	}
	if !strings.Contains(msg.Body, "needs approval for rm -rf") {
		t.Errorf("Body %q does not contain the notification message", msg.Body)
	}
}

// TestNotify_DedupesRepeatedPermissionPrompt is the regression test for the
// real incident this fix addresses: a single stuck window fired 18
// byte-identical "blocked" messages because the Notification hook fires on
// every invocation while a permission prompt is pending, and the old gate
// (shouldEmitBlocked with no prevState) had no notion of a state
// *transition*. Two consecutive permission_prompt Notify invocations for the
// same window -- exactly what happens while Claude waits on the same
// pending prompt -- must enqueue exactly ONE mailbox message, not two.
func TestNotify_DedupesRepeatedPermissionPrompt(t *testing.T) {
	setupWindowstate(t)
	setupMailbox(t)

	d := stubDeps(t)
	d.EnqueueMailboxMessage = mailbox.Enqueue // exercise the real mailbox package
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const childWindow = "@156"
	const parentWindow = "@155"
	const parentSession = "session-lead"
	seedSpawnedWindow(t, childWindow, parentWindow, parentSession, "dispatch")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "lead:2")
	t.Setenv("AGENTCTL_WINDOW_ID", childWindow)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"notification_type":"permission_prompt","message":"Claude needs your permission"}`

	// First invocation: window transitions idle/"" -> blocked. Must emit.
	runHookCaptured(t, nil, stdin, Notify)
	// Second (and, in the real incident, many more) invocation while the
	// same prompt is still pending: window is already "blocked" coming in.
	// Must NOT emit again.
	runHookCaptured(t, nil, stdin, Notify)
	runHookCaptured(t, nil, stdin, Notify)

	pending, err := mailbox.Pending(parentWindow)
	if err != nil {
		t.Fatalf("mailbox.Pending(%s): %v", parentWindow, err)
	}
	if len(pending) != 1 {
		t.Fatalf("mailbox.Pending(%s) = %d messages after 3 identical Notify invocations, want exactly 1", parentWindow, len(pending))
	}
}

// TestNotify_ReemitsAfterUnblocking verifies blocked -> unblocked -> blocked
// fires a second "blocked" message: transition gating must not permanently
// silence a window after its first block, only suppress re-firing while it
// stays blocked.
func TestNotify_ReemitsAfterUnblocking(t *testing.T) {
	setupWindowstate(t)
	setupMailbox(t)

	d := stubDeps(t)
	d.EnqueueMailboxMessage = mailbox.Enqueue // exercise the real mailbox package
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const childWindow = "@156"
	const parentWindow = "@155"
	const parentSession = "session-lead"
	seedSpawnedWindow(t, childWindow, parentWindow, parentSession, "dispatch")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "lead:2")
	t.Setenv("AGENTCTL_WINDOW_ID", childWindow)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"notification_type":"permission_prompt","message":"Claude needs your permission"}`

	// First block: idle/"" -> blocked. Emits.
	runHookCaptured(t, nil, stdin, Notify)

	// Simulate the agent getting unstuck: a new user turn (or the agent's
	// own continuation) sets the window back to "working", exactly as
	// UserPrompt does at the start of the next turn.
	if err := windowstate.Update(childWindow, func(w *windowstate.WindowState) {
		w.State = "working"
	}); err != nil {
		t.Fatalf("windowstate.Update(working): %v", err)
	}

	// Blocked again on a fresh prompt: working -> blocked. Must emit again.
	runHookCaptured(t, nil, stdin, Notify)

	pending, err := mailbox.Pending(parentWindow)
	if err != nil {
		t.Fatalf("mailbox.Pending(%s): %v", parentWindow, err)
	}
	if len(pending) != 2 {
		t.Fatalf("mailbox.Pending(%s) = %d messages after blocked->working->blocked, want exactly 2", parentWindow, len(pending))
	}
}

// TestNotify_NoEmitForIdlePromptEvenWhenSpawned verifies idle_prompt never
// enqueues a blocked message, even on a spawned window.
func TestNotify_NoEmitForIdlePromptEvenWhenSpawned(t *testing.T) {
	setupWindowstate(t)
	setupMailbox(t)

	enqueueCalled := false
	d := stubDeps(t)
	d.EnqueueMailboxMessage = func(m mailbox.Message) (string, error) {
		enqueueCalled = true
		return "id", nil
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const windowID = "@61"
	seedSpawnedWindow(t, windowID, "@30", "session-abc", "dispatch")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"notification_type":"idle_prompt","message":"waiting"}`
	runHookCaptured(t, nil, stdin, Notify)

	if enqueueCalled {
		t.Error("EnqueueMailboxMessage was called for idle_prompt")
	}
}

// TestNotify_NoEmitWhenNotSpawned verifies that permission_prompt on an
// unspawned window (no dispatching parent) never enqueues anything.
func TestNotify_NoEmitWhenNotSpawned(t *testing.T) {
	setupWindowstate(t)
	setupMailbox(t)

	enqueueCalled := false
	d := stubDeps(t)
	d.EnqueueMailboxMessage = func(m mailbox.Message) (string, error) {
		enqueueCalled = true
		return "id", nil
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const windowID = "@62"
	// No seed: SpawnedBy stays "".

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"notification_type":"permission_prompt","message":"needs approval"}`
	runHookCaptured(t, nil, stdin, Notify)

	if enqueueCalled {
		t.Error("EnqueueMailboxMessage was called for a window with no SpawnedBy")
	}
}

// TestNotify_EnqueueFailureDoesNotAffectNotifyPath verifies that a mailbox
// enqueue failure during Notify does not prevent the notification path
// (windowstate update, deps.Notify) from completing.
func TestNotify_EnqueueFailureDoesNotAffectNotifyPath(t *testing.T) {
	setupWindowstate(t)
	setupMailbox(t)

	notifyCalled := false
	d := stubDeps(t)
	d.EnqueueMailboxMessage = func(m mailbox.Message) (string, error) {
		return "", errors.New("disk full")
	}
	d.Notify = func(opts notify.Options) {
		notifyCalled = true
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const windowID = "@63"
	seedSpawnedWindow(t, windowID, "@30", "session-abc", "dispatch")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"notification_type":"permission_prompt","message":"needs approval"}`
	// Must not panic despite the enqueue failure.
	runHookCaptured(t, nil, stdin, Notify)

	if !notifyCalled {
		t.Error("deps.Notify was not called after a mailbox enqueue failure")
	}

	ws, err := windowstate.Read(windowID)
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.State != "blocked" {
		t.Errorf("windowstate.State = %q, want blocked (enqueue failure must not affect the notify path)", ws.State)
	}
}
