package hooks

// computeblockreason_test.go — tests for the fix scoping CheckDone's
// completion gate (checkDoneReason: uncommitted changes / build failure) to
// autonomous (dispatch-like) windows only. Before this fix, an
// interactive/bare agent working in a repo with uncommitted files got
// exit-2'd on every turn-end and could never stop.
//
// computeBlockReason is the pure(ish) gating decision extracted from
// CheckDone specifically so this can be tested without exercising os.Exit(2)
// -- see the doc comment on computeBlockReason in control.go.
//
// All tests are hermetic:
//   - t.Setenv("HOME", t.TempDir()) isolates any code path that might fall
//     back to a real home-directory default (CI has no real ~/.claude).
//   - hooks.SetDepsForTest injects RunCommand/StatFile stubs.
//   - hooks.SetAutonomousModeFn injects the mode-autonomy predicate.

import (
	"errors"
	"os"
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestComputeBlockReason(t *testing.T) {
	tests := []struct {
		name          string
		mode          string
		autonomous    bool
		dirtyGit      bool
		wantBlocked   bool
		wantGitCalled bool
	}{
		{
			name:          "non-autonomous mode + dirty git -> not blocked, checkDoneReason not invoked",
			mode:          "bare",
			autonomous:    false,
			dirtyGit:      true,
			wantBlocked:   false,
			wantGitCalled: false,
		},
		{
			name:          "autonomous mode + dirty git -> still blocks",
			mode:          "dispatch",
			autonomous:    true,
			dirtyGit:      true,
			wantBlocked:   true,
			wantGitCalled: true,
		},
		{
			name:          "autonomous mode + clean git -> not blocked, checkDoneReason invoked",
			mode:          "dispatch",
			autonomous:    true,
			dirtyGit:      false,
			wantBlocked:   false,
			wantGitCalled: true,
		},
		{
			name:          "empty mode -> treated as non-autonomous, not blocked",
			mode:          "",
			autonomous:    false,
			dirtyGit:      true,
			wantBlocked:   false,
			wantGitCalled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("XDG_DATA_HOME", "")
			t.Setenv("XDG_STATE_HOME", "")

			SetAutonomousModeFn(func(mode string) bool {
				if mode != tt.mode {
					t.Errorf("autonomousModeFn called with mode=%q, want %q", mode, tt.mode)
				}
				return tt.autonomous
			})
			t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })

			gitCalled := false
			d := stubDeps(t)
			d.RunCommand = func(name string, args ...string) ([]byte, error) {
				if name == "git" {
					gitCalled = true
					if tt.dirtyGit {
						return []byte("M  internal/foo.go\n"), nil
					}
					return []byte(""), nil
				}
				return nil, nil
			}
			// No go.mod -> build check skipped, isolating the test to the git check.
			d.StatFile = func(name string) (os.FileInfo, error) {
				return nil, errors.New("not found")
			}
			SetDepsForTest(d)
			t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

			got := computeBlockReason(tt.mode, "@1", "test-agent")

			if gitCalled != tt.wantGitCalled {
				t.Errorf("git status invoked = %v, want %v", gitCalled, tt.wantGitCalled)
			}
			if blocked := got != ""; blocked != tt.wantBlocked {
				t.Errorf("computeBlockReason(%q, ...) blocked = %v (reason %q), want %v",
					tt.mode, blocked, got, tt.wantBlocked)
			}
		})
	}
}

// TestComputeBlockReason_SkipsGateEntirelyWhenNonAutonomous is a stronger,
// integration-shaped assertion: with a stub Deps whose RunCommand fails the
// test if invoked with "git" at all, computeBlockReason for a non-autonomous
// mode must never reach checkDoneReason.
func TestComputeBlockReason_SkipsGateEntirelyWhenNonAutonomous(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	SetAutonomousModeFn(func(mode string) bool { return false })
	t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })

	d := stubDeps(t)
	d.RunCommand = func(name string, args ...string) ([]byte, error) {
		if name == "git" {
			t.Fatalf("git status should never be invoked for a non-autonomous mode")
		}
		return nil, nil
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	if got := computeBlockReason("bare", "@41", "lead"); got != "" {
		t.Errorf("computeBlockReason = %q, want empty (non-autonomous must never block)", got)
	}
}

// TestCheckDone_AutonomousCleanBuildMarksDone is the end-to-end version of
// the "autonomous mode + clean git + passing build -> windowstate.Done ==
// true" case, exercised through CheckDone (not just computeBlockReason) so
// the fix is verified against the real hook entry point too.
func TestCheckDone_AutonomousCleanBuildMarksDone(t *testing.T) {
	setupWindowstate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	SetAutonomousModeFn(func(mode string) bool { return mode == "dispatch" })
	t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })

	d := stubDeps(t)
	d.RunCommand = func(name string, args ...string) ([]byte, error) {
		// Clean git status, passing build.
		return []byte(""), nil
	}
	d.StatFile = func(name string) (os.FileInfo, error) {
		if name == "go.mod" {
			return nil, nil // go.mod exists -> build check runs (and passes)
		}
		return nil, os.ErrNotExist
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const windowID = "@80"
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

	stdin := `{"stop_hook_active":false,"last_assistant_message":"Opened PR, build passes."}`
	runHookCaptured(t, nil, stdin, CheckDone)

	ws, err := windowstate.Read(windowID)
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if !ws.Done {
		t.Error("windowstate.Done = false, want true for autonomous mode + clean git + passing build")
	}
}
