package hooks

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/windowstate"
)

// stopEnv prepares env + windowstate for a CheckDone run on windowID.
func stopEnv(t *testing.T, windowID, mode string) {
	t.Helper()
	setupWindowstate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	SetAutonomousModeFn(func(m string) bool { return m == "dispatch" || m == "adhoc" })
	t.Cleanup(func() { SetAutonomousModeFn(func(string) bool { return false }) })
	SetDepsForTest(stubDeps(t))
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) { ws.Mode = mode }); err != nil {
		t.Fatal(err)
	}
}

func setSpawner(t *testing.T, fn func(string) error) {
	t.Helper()
	SetSpawnCompleteFn(fn)
	t.Cleanup(func() { SetSpawnCompleteFn(func(string) error { return nil }) })
}

const stopStdin = `{"stop_hook_active":false,"last_assistant_message":"done"}`

func TestCheckDone_SpawnsCompletionOnceAndRearms(t *testing.T) {
	const w = "@80"
	stopEnv(t, w, "dispatch")
	var spawns atomic.Int32
	setSpawner(t, func(id string) error {
		if id != w {
			t.Errorf("spawn window = %q, want %q", id, w)
		}
		spawns.Add(1)
		return nil
	})

	runHookCaptured(t, nil, stopStdin, CheckDone)
	runHookCaptured(t, nil, stopStdin, CheckDone)
	if got := spawns.Load(); got != 1 {
		t.Fatalf("spawns after two Done transitions = %d, want 1", got)
	}
	ws, _ := windowstate.Read(w)
	if ws.CompleteFiredAt.IsZero() || !ws.Done {
		t.Fatalf("want Done and CompleteFiredAt stamped, got %+v", ws)
	}

	runHookCaptured(t, nil, "{}", UserPrompt) // re-arm
	ws, _ = windowstate.Read(w)
	if ws.Done || !ws.CompleteFiredAt.IsZero() {
		t.Fatalf("UserPrompt must clear Done and CompleteFiredAt, got %+v", ws)
	}
	runHookCaptured(t, nil, stopStdin, CheckDone)
	if got := spawns.Load(); got != 2 {
		t.Fatalf("spawns after re-arm = %d, want 2", got)
	}
}

func TestCheckDone_NoSpawnCases(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		stdin string
	}{
		{"interactive mode", "discuss", stopStdin},
		{"stop_hook_active", "dispatch", `{"stop_hook_active":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stopEnv(t, "@81", tt.mode)
			var spawns atomic.Int32
			setSpawner(t, func(string) error { spawns.Add(1); return nil })
			runHookCaptured(t, nil, tt.stdin, CheckDone)
			if spawns.Load() != 0 {
				t.Fatal("spawned unexpectedly")
			}
		})
	}
}

func TestCheckDone_SlowSpawnerDoesNotBlockOnGhOrGit(t *testing.T) {
	const w = "@82"
	stopEnv(t, w, "adhoc")
	d := stubDeps(t)
	var cmds []string
	d.RunCommand = func(name string, args ...string) ([]byte, error) {
		cmds = append(cmds, name)
		return nil, nil
	}
	SetDepsForTest(d)
	setSpawner(t, func(string) error { return nil }) // returns immediately, as a detached Start does

	start := time.Now()
	runHookCaptured(t, nil, stopStdin, CheckDone)
	if time.Since(start) > 2*time.Second {
		t.Fatal("hook was slow")
	}
	for _, c := range cmds {
		if c == "gh" {
			t.Fatalf("hook ran gh: %v", cmds)
		}
	}
}

func TestCheckDone_SpawnFailureReleasesClaim(t *testing.T) {
	const w = "@83"
	stopEnv(t, w, "dispatch")
	calls := 0
	setSpawner(t, func(string) error {
		calls++
		if calls == 1 {
			return errors.New("no executable")
		}
		return nil
	})
	runHookCaptured(t, nil, stopStdin, CheckDone)
	ws, _ := windowstate.Read(w)
	if !ws.CompleteFiredAt.IsZero() {
		t.Fatal("claim must be released after spawn failure")
	}
	runHookCaptured(t, nil, stopStdin, CheckDone)
	if calls != 2 {
		t.Fatalf("later Stop must retry, spawn calls = %d", calls)
	}
}
