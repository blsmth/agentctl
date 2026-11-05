package hooks

// done_test.go — tests for making windowstate.Done a derived hook fact
// Covers shouldMarkDone (the pure gate that
// decides whether CheckDone marks a window Done, independent of whether it
// has a dispatching parent), the CheckDone integration that flips
// windowstate.Done on the genuine-completion path, and UserPromptSubmit
// clearing Done at the start of a new turn.
//
// All tests are hermetic:
//   - t.Setenv("HOME", t.TempDir()) isolates any code path that might fall
//     back to a real home-directory default.
//   - windowstate.SetDirForTest isolates window state files.
//   - hooks.SetDepsForTest / hooks.SetAutonomousModeFn inject stubs.

import (
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

// ---------------------------------------------------------------------------
// shouldMarkDone: pure decision, table-driven
// ---------------------------------------------------------------------------

func TestShouldMarkDone(t *testing.T) {
	tests := []struct {
		name           string
		mode           string
		stopHookActive bool
		blockReason    string
		autonomous     bool
		want           bool
	}{
		{
			name:       "autonomous + clean -> true",
			mode:       "dispatch",
			autonomous: true,
			want:       true,
		},
		{
			name:       "non-autonomous -> false",
			mode:       "discuss",
			autonomous: false,
			want:       false,
		},
		{
			name:           "stopHookActive -> false",
			mode:           "dispatch",
			stopHookActive: true,
			autonomous:     true,
			want:           false,
		},
		{
			name:        "non-empty blockReason -> false",
			mode:        "dispatch",
			blockReason: "uncommitted changes",
			autonomous:  true,
			want:        false,
		},
		{
			// This is the case that distinguishes shouldMarkDone from
			// shouldEmitDone: an unspawned window (no dispatching parent)
			// still deserves the derived Done fact.
			name:       "empty SpawnedBy is irrelevant -> true",
			mode:       "dispatch",
			autonomous: true,
			want:       true,
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

			got := shouldMarkDone(tt.mode, tt.stopHookActive, tt.blockReason)
			if got != tt.want {
				t.Errorf("shouldMarkDone(%q, %v, %q) = %v, want %v",
					tt.mode, tt.stopHookActive, tt.blockReason, got, tt.want)
			}
		})
	}
}

// TestShouldMarkDone_DoesNotRequireSpawnedBy documents (via the type
// signature) that shouldMarkDone has no spawnedBy parameter at all -- unlike
// shouldEmitDone, it cannot be gated on a dispatching parent existing. This
// test just pins the observable behavior: an otherwise-qualifying turn-end
// is marked done regardless of any notion of "spawned by."
func TestShouldMarkDone_DoesNotRequireSpawnedBy(t *testing.T) {
	SetAutonomousModeFn(func(string) bool { return true })
	t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })

	if !shouldMarkDone("dispatch", false, "") {
		t.Fatal("expected shouldMarkDone to return true with no notion of a spawning parent")
	}
}

// ---------------------------------------------------------------------------
// CheckDone: sets windowstate.Done on the genuine-completion path
// ---------------------------------------------------------------------------

// TestCheckDone_SetsWindowstateDoneOnCompletion verifies that a clean,
// autonomous, non-stop-hook-active turn-end sets windowstate.Done=true, even
// for a window with no SpawnedBy (i.e. no dispatching parent) -- the case
// that distinguishes this from the mailbox "done" auto-emit.
func TestCheckDone_SetsWindowstateDoneOnCompletion(t *testing.T) {
	setupWindowstate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	SetAutonomousModeFn(func(mode string) bool { return mode == "dispatch" })
	t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const windowID = "@70"
	// No seed: SpawnedBy stays "". Only Mode matters for shouldMarkDone.
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.Mode = "dispatch"
	}); err != nil {
		t.Fatalf("seed windowstate: %v", err)
	}

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")

	stdin := `{"stop_hook_active":false,"last_assistant_message":"Opened PR, all tests pass."}`
	runHookCaptured(t, nil, stdin, CheckDone)

	ws, err := windowstate.Read(windowID)
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if !ws.Done {
		t.Error("windowstate.Done = false, want true after a genuine autonomous completion")
	}
}

// TestCheckDone_DoesNotSetDoneWhenGateFails verifies that windowstate.Done
// stays false when shouldMarkDone's gate fails -- here, non-autonomous mode.
func TestCheckDone_DoesNotSetDoneWhenGateFails(t *testing.T) {
	setupWindowstate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	SetAutonomousModeFn(func(mode string) bool { return mode == "dispatch" })
	t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const windowID = "@71"
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.Mode = "discuss" // not autonomous
	}); err != nil {
		t.Fatalf("seed windowstate: %v", err)
	}

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")

	stdin := `{"stop_hook_active":false,"last_assistant_message":"done"}`
	runHookCaptured(t, nil, stdin, CheckDone)

	ws, err := windowstate.Read(windowID)
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.Done {
		t.Error("windowstate.Done = true, want false for a non-autonomous mode")
	}
}

// ---------------------------------------------------------------------------
// UserPromptSubmit: clears windowstate.Done at the start of a new turn
// ---------------------------------------------------------------------------

// TestUserPrompt_ClearsDone verifies that a new user turn clears a
// previously-set Done flag -- Done is a per-turn fact, not sticky across
// turns.
func TestUserPrompt_ClearsDone(t *testing.T) {
	setupWindowstate(t)

	const windowID = "@72"
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.Done = true
	}); err != nil {
		t.Fatalf("seed windowstate: %v", err)
	}

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")

	runHookCaptured(t, nil, `{}`, UserPrompt)

	ws, err := windowstate.Read(windowID)
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.Done {
		t.Error("windowstate.Done = true, want false after UserPromptSubmit clears it for a new turn")
	}
	if ws.State != "working" {
		t.Errorf("windowstate.State = %q, want working", ws.State)
	}
}
