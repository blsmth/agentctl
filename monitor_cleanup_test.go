package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestReconcileWindows_GoneUntrackedWindowsCleanedUpWithoutHandlers(t *testing.T) {
	for _, name := range []string{"shell", "improve~x"} {
		t.Run(name, func(t *testing.T) {
			m := &monitorModel{windows: map[string]*instanceState{
				"@9": {instance: name, base: name, hasPrev: true, prevState: state.StatusWorking},
			}}
			orig := handleCompleteFn
			t.Cleanup(func() { handleCompleteFn = orig })
			calls := 0
			handleCompleteFn = func(state.Transition, config.AgentConfig) { calls++ }

			m.reconcileWindows(&config.Config{}, nil, 0)

			if calls != 0 {
				t.Errorf("handler fired %d times for %s, want 0", calls, name)
			}
			if _, ok := m.windows["@9"]; ok {
				t.Errorf("gone %s window still in m.windows", name)
			}
		})
	}
}

func TestPruneTick_FirstTickPrunes(t *testing.T) {
	origM, origS := pruneMailboxFn, pruneSessionLogsFn
	t.Cleanup(func() { pruneMailboxFn, pruneSessionLogsFn = origM, origS })
	mailboxCh := make(chan struct{}, 4)
	logsCh := make(chan struct{}, 4)
	pruneMailboxFn = func(time.Duration) (int, error) { mailboxCh <- struct{}{}; return 0, nil }
	pruneSessionLogsFn = func(time.Duration, []string) (int, error) { logsCh <- struct{}{}; return 0, nil }

	wait := func(ch chan struct{}, want bool, what string) {
		t.Helper()
		select {
		case <-ch:
			if !want {
				t.Errorf("%s ran unexpectedly", what)
			}
		case <-time.After(300 * time.Millisecond):
			if want {
				t.Errorf("%s did not run", what)
			}
		}
	}

	m := &monitorModel{windows: map[string]*instanceState{}, firstTick: true}
	m.pruneTick()
	wait(mailboxCh, true, "first-tick mailbox prune")
	wait(logsCh, true, "first-tick session log prune")

	m.firstTick = false
	m.pruneTick()
	wait(mailboxCh, false, "second-tick mailbox prune")
	wait(logsCh, false, "second-tick session log prune")
}

func TestProtectedSessionLogs(t *testing.T) {
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(t.TempDir())
	dir := paths.SessionLogDir()

	m := &monitorModel{windows: map[string]*instanceState{
		"@1": {logPath: "/home/u/.claude/projects/p/aaa.jsonl"},
		"@2": {},
	}}
	read := func(id string) (windowstate.WindowState, error) {
		if id == "@2" {
			return windowstate.WindowState{SessionID: "bbb", LogPath: "/x/ccc.jsonl"}, nil
		}
		return windowstate.WindowState{}, os.ErrNotExist
	}
	got := m.protectedSessionLogs(read)
	for _, id := range []string{"aaa", "bbb", "ccc"} {
		if !slices.Contains(got, filepath.Join(dir, id+".log")) {
			t.Errorf("protected %v missing %s.log", got, id)
		}
	}
	for _, p := range got {
		if filepath.Dir(p) != dir {
			t.Errorf("protected path %q is not under the session log dir %q", p, dir)
		}
	}
}

func TestDetectAndFireTransitions_SteadyTickMakesNoLookups(t *testing.T) {
	origW, origB, origH := paneWorkDirFn, gitBranchFn, handleCompleteFn
	t.Cleanup(func() { paneWorkDirFn, gitBranchFn, handleCompleteFn = origW, origB, origH })
	lookups := 0
	paneWorkDirFn = func(string) string { lookups++; return "/w" }
	gitBranchFn = func(string) string { lookups++; return "b" }
	var got state.Transition
	handleCompleteFn = func(tr state.Transition, _ config.AgentConfig) { got = tr }

	m := &monitorModel{windows: map[string]*instanceState{}}
	inst := &instanceState{instance: "a", base: "a", hasPrev: true, prevState: state.StatusIdle}
	ws := windowstate.WindowState{State: "idle", Mode: "dispatch"}

	for range 3 {
		m.detectAndFireTransitions(inst, "@1", state.StatusIdle, ws, &config.Config{}, "")
	}
	if lookups != 0 {
		t.Fatalf("steady ticks made %d lookups, want 0", lookups)
	}

	m.detectAndFireTransitions(inst, "@1", state.StatusWorking, ws, &config.Config{}, "")
	if lookups != 2 || got.WorkDir != "/w" || got.Branch != "b" {
		t.Errorf("edge: lookups=%d transition=%+v, want 2 lookups with workdir and branch", lookups, got)
	}
}
