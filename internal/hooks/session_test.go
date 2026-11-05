package hooks

// session_test.go — unit tests for SessionEnd window-kill behaviour.
//
// All tests are hermetic:
//   - windowstate.SetDirForTest isolates window state files.
//   - killWindowFunc is replaced with a spy (no real tmux).
//   - runHookCaptured sets env vars and captures stdout/stderr.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/worktree"
)

// ---------------------------------------------------------------------------
// SessionEnd window-kill tests
// ---------------------------------------------------------------------------

// TestSessionEnd_TerminalReasonKillsWindow verifies that SessionEnd calls
// killWindowFunc when the exit reason is terminal (e.g. prompt_input_exit).
func TestSessionEnd_TerminalReasonKillsWindow(t *testing.T) {
	setupWindowstate(t)

	// Write a windowstate entry so FindBySessionID can return windowID.
	const windowID = "@50"
	const sessionID = "sess-terminal"
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.SessionID = sessionID
		ws.State = "working"
	}); err != nil {
		t.Fatalf("setup windowstate: %v", err)
	}

	// Spy on killWindowFunc.
	var killedWindow string
	orig := killWindowFunc
	killWindowFunc = func(id string) { killedWindow = id }
	t.Cleanup(func() { killWindowFunc = orig })

	// Use stubDeps so journal generation is skipped.
	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")

	stdin := `{"reason":"prompt_input_exit","session_id":"` + sessionID + `"}`
	runHookCaptured(t, nil, stdin, SessionEnd)

	if killedWindow != windowID {
		t.Errorf("killWindowFunc called with %q, want %q", killedWindow, windowID)
	}
}

// TestSessionEnd_AllTerminalReasonsKillWindow verifies all known terminal
// reasons trigger a window kill. "other" is deliberately excluded here --
// it's a catch-all reason that also fires on crashes and transient errors,
// so treating it as terminal would kill windows out from under agents that
// are still running. See TestSessionEnd_NonTerminalReasonPreservesWindow.
func TestSessionEnd_AllTerminalReasonsKillWindow(t *testing.T) {
	terminalReasons := []string{
		"prompt_input_exit",
		"logout",
		"bypass_permissions_disabled",
	}

	for _, reason := range terminalReasons {
		reason := reason
		t.Run(reason, func(t *testing.T) {
			setupWindowstate(t)

			const windowID = "@51"
			sessionID := "sess-" + reason
			if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
				ws.SessionID = sessionID
				ws.State = "working"
			}); err != nil {
				t.Fatalf("setup windowstate: %v", err)
			}

			var killedWindow string
			orig := killWindowFunc
			killWindowFunc = func(id string) { killedWindow = id }
			t.Cleanup(func() { killWindowFunc = orig })

			d := stubDeps(t)
			SetDepsForTest(d)
			t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

			t.Setenv("AGENTCTL", "1")
			t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
			t.Setenv("AGENTCTL_WINDOW_ID", windowID)
			t.Setenv("TMUX_PANE", "")

			stdin := `{"reason":"` + reason + `","session_id":"` + sessionID + `"}`
			runHookCaptured(t, nil, stdin, SessionEnd)

			if killedWindow != windowID {
				t.Errorf("reason=%q: killWindowFunc called with %q, want %q", reason, killedWindow, windowID)
			}
		})
	}
}

// TestSessionEnd_NonTerminalReasonPreservesWindow verifies that non-terminal
// reasons (clear, resume) do NOT kill the window. "other" is included here:
// it's a catch-all that also fires on crashes and transient errors, so
// killing the window on "other" would cause cascading session death when
// agents exit unexpectedly.
func TestSessionEnd_NonTerminalReasonPreservesWindow(t *testing.T) {
	nonTerminalReasons := []string{"clear", "resume", "context_window_full", "other"}

	for _, reason := range nonTerminalReasons {
		reason := reason
		t.Run(reason, func(t *testing.T) {
			setupWindowstate(t)

			const windowID = "@52"
			sessionID := "sess-nonterminal-" + reason
			if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
				ws.SessionID = sessionID
				ws.State = "working"
			}); err != nil {
				t.Fatalf("setup windowstate: %v", err)
			}

			killCalled := false
			orig := killWindowFunc
			killWindowFunc = func(id string) { killCalled = true }
			t.Cleanup(func() { killWindowFunc = orig })

			d := stubDeps(t)
			SetDepsForTest(d)
			t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

			t.Setenv("AGENTCTL", "1")
			t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
			t.Setenv("AGENTCTL_WINDOW_ID", windowID)
			t.Setenv("TMUX_PANE", "")

			stdin := `{"reason":"` + reason + `","session_id":"` + sessionID + `"}`
			runHookCaptured(t, nil, stdin, SessionEnd)

			if killCalled {
				t.Errorf("reason=%q: killWindowFunc should NOT be called for non-terminal reason", reason)
			}
		})
	}
}

// TestSessionEnd_EmptyWindowIDNoKill verifies that no kill attempt is made
// when the window ID cannot be resolved (e.g. session not linked yet).
func TestSessionEnd_EmptyWindowIDNoKill(t *testing.T) {
	setupWindowstate(t)

	killCalled := false
	orig := killWindowFunc
	killWindowFunc = func(id string) { killCalled = true }
	t.Cleanup(func() { killWindowFunc = orig })

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_ID", "")
	t.Setenv("TMUX_PANE", "")

	// session_id is unknown — FindBySessionID returns "".
	stdin := `{"reason":"prompt_input_exit","session_id":"unknown-session-xyz"}`
	runHookCaptured(t, nil, stdin, SessionEnd)

	if killCalled {
		t.Error("killWindowFunc should NOT be called when windowID is empty")
	}
}

// TestSessionEnd_KillHappensAfterJournal verifies that the window state is
// cleared to idle before the kill occurs (ordering guarantee: journal reads
// windowstate first, then kill fires). We verify this indirectly by confirming
// the windowstate is idle after SessionEnd completes.
func TestSessionEnd_KillHappensAfterWindowstateClear(t *testing.T) {
	setupWindowstate(t)

	const windowID = "@53"
	const sessionID = "sess-order"
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.SessionID = sessionID
		ws.State = "working"
	}); err != nil {
		t.Fatalf("setup windowstate: %v", err)
	}

	var stateAtKillTime string
	orig := killWindowFunc
	killWindowFunc = func(id string) {
		// Capture the state at the moment kill is called.
		ws, _ := windowstate.Read(id)
		stateAtKillTime = ws.State
	}
	t.Cleanup(func() { killWindowFunc = orig })

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")

	stdin := `{"reason":"prompt_input_exit","session_id":"` + sessionID + `"}`
	runHookCaptured(t, nil, stdin, SessionEnd)

	if stateAtKillTime != "idle" {
		t.Errorf("windowstate.State at kill time = %q, want idle (kill must happen after state clear)", stateAtKillTime)
	}
}

// ---------------------------------------------------------------------------
// SessionEnd self-reclaim tests
//
// reclaimSessionWorktreeFunc and worktreePruneFunc are stubbed in every test
// here -- the real implementations shell out to git/gh, which is exactly
// what these tests must not depend on. See internal/cleanup's own tests for
// coverage of the actual classify/reclaim logic these seams wrap.
// ---------------------------------------------------------------------------

// setAutoReclaimWorktreeConfig writes a project-local config setting
// auto_reclaim_worktree for agentName and registers cleanup to restore the
// previous config path.
func setAutoReclaimWorktreeConfig(t *testing.T, agentName string, val *bool) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	config.SetConfigPathForTest(cfgPath)
	t.Cleanup(func() { config.SetConfigPathForTest("") })

	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			agentName: {AutoReclaimWorktree: val},
		},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

// stubWorktreePrune replaces worktreePruneFunc with a spy and returns a
// pointer the caller can check after SessionEnd runs.
func stubWorktreePrune(t *testing.T) *bool {
	t.Helper()
	called := false
	orig := worktreePruneFunc
	worktreePruneFunc = func(repoDir string) error {
		called = true
		return nil
	}
	t.Cleanup(func() { worktreePruneFunc = orig })
	return &called
}

// TestSessionEnd_NonTerminalReasonNoReclaim verifies that a non-terminal exit
// reason (e.g. /clear) never attempts a worktree reclaim -- the window and
// its worktree must both stay alive.
func TestSessionEnd_NonTerminalReasonNoReclaim(t *testing.T) {
	setupWindowstate(t)

	const windowID = "@60"
	const sessionID = "sess-nonterm-reclaim"
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.SessionID = sessionID
		ws.State = "working"
		ws.WorktreeDir = "/repo/.worktrees/agent-go-engineer-issue-1"
		ws.Branch = "agent/go-engineer/issue-1"
	}); err != nil {
		t.Fatalf("setup windowstate: %v", err)
	}

	reclaimCalled := false
	origReclaim := reclaimSessionWorktreeFunc
	reclaimSessionWorktreeFunc = func(_ string, _ windowstate.WindowState) bool {
		reclaimCalled = true
		return true
	}
	t.Cleanup(func() { reclaimSessionWorktreeFunc = origReclaim })

	stubWorktreePrune(t)

	killCalled := false
	origKill := killWindowFunc
	killWindowFunc = func(id string) { killCalled = true }
	t.Cleanup(func() { killWindowFunc = origKill })

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_INSTANCE_NAME", "")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")

	stdin := `{"reason":"clear","session_id":"` + sessionID + `"}`
	runHookCaptured(t, nil, stdin, SessionEnd)

	if reclaimCalled {
		t.Error("reclaimSessionWorktreeFunc must not be called for a non-terminal reason")
	}
	if killCalled {
		t.Error("killWindowFunc must not be called for a non-terminal reason")
	}
}

// TestSessionEnd_EmptyWorktreeDirNoReclaim verifies that a window with no
// recorded WorktreeDir (e.g. a dispatch that fell back to the shared repo
// dir) never attempts a reclaim, and falls through to the normal kill path.
func TestSessionEnd_EmptyWorktreeDirNoReclaim(t *testing.T) {
	setupWindowstate(t)

	const windowID = "@61"
	const sessionID = "sess-emptywt"
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.SessionID = sessionID
		ws.State = "working"
		// WorktreeDir intentionally left empty.
	}); err != nil {
		t.Fatalf("setup windowstate: %v", err)
	}

	reclaimCalled := false
	origReclaim := reclaimSessionWorktreeFunc
	reclaimSessionWorktreeFunc = func(_ string, _ windowstate.WindowState) bool {
		reclaimCalled = true
		return true
	}
	t.Cleanup(func() { reclaimSessionWorktreeFunc = origReclaim })

	pruneCalled := stubWorktreePrune(t)

	killCalled := false
	origKill := killWindowFunc
	killWindowFunc = func(id string) { killCalled = true }
	t.Cleanup(func() { killWindowFunc = origKill })

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_INSTANCE_NAME", "")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")

	stdin := `{"reason":"prompt_input_exit","session_id":"` + sessionID + `"}`
	runHookCaptured(t, nil, stdin, SessionEnd)

	if reclaimCalled {
		t.Error("reclaimSessionWorktreeFunc must not be called when WorktreeDir is empty")
	}
	if !killCalled {
		t.Error("killWindowFunc must be called when there is no worktree to reclaim")
	}
	if *pruneCalled {
		t.Error("worktree.Prune must not run when there is no worktree recorded for this window")
	}
}

// TestSessionEnd_SkipVerdictFallsThroughToKill verifies that when the
// classify verdict is "skip" (reclaimSessionWorktreeFunc returns false),
// SessionEnd still kills the window via the normal path.
func TestSessionEnd_SkipVerdictFallsThroughToKill(t *testing.T) {
	setupWindowstate(t)

	const windowID = "@62"
	const sessionID = "sess-skip"
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.SessionID = sessionID
		ws.State = "working"
		ws.WorktreeDir = "/repo/.worktrees/agent-go-engineer-issue-2"
		ws.Branch = "agent/go-engineer/issue-2"
	}); err != nil {
		t.Fatalf("setup windowstate: %v", err)
	}

	origReclaim := reclaimSessionWorktreeFunc
	reclaimSessionWorktreeFunc = func(_ string, _ windowstate.WindowState) bool {
		return false // Classify said skip.
	}
	t.Cleanup(func() { reclaimSessionWorktreeFunc = origReclaim })

	stubWorktreePrune(t)

	killCalled := false
	origKill := killWindowFunc
	killWindowFunc = func(id string) { killCalled = true }
	t.Cleanup(func() { killWindowFunc = origKill })

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_INSTANCE_NAME", "")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")

	stdin := `{"reason":"prompt_input_exit","session_id":"` + sessionID + `"}`
	runHookCaptured(t, nil, stdin, SessionEnd)

	if !killCalled {
		t.Error("killWindowFunc must be called when the reclaim verdict is skip")
	}
}

// TestSessionEnd_ReclaimableVerdictSkipsDoubleKill verifies that when
// reclaimSessionWorktreeFunc reports a reclaim actually ran (cleanup.Reclaim
// already killed the window as one of its steps), SessionEnd does NOT also
// call killWindowFunc.
func TestSessionEnd_ReclaimableVerdictSkipsDoubleKill(t *testing.T) {
	setupWindowstate(t)

	const windowID = "@63"
	const sessionID = "sess-reclaim"
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.SessionID = sessionID
		ws.State = "working"
		ws.WorktreeDir = "/repo/.worktrees/agent-go-engineer-issue-3"
		ws.Branch = "agent/go-engineer/issue-3"
	}); err != nil {
		t.Fatalf("setup windowstate: %v", err)
	}

	reclaimCalled := false
	origReclaim := reclaimSessionWorktreeFunc
	reclaimSessionWorktreeFunc = func(_ string, _ windowstate.WindowState) bool {
		reclaimCalled = true
		return true // Reclaim ran and already killed the window.
	}
	t.Cleanup(func() { reclaimSessionWorktreeFunc = origReclaim })

	pruneCalled := stubWorktreePrune(t)

	killCalled := false
	origKill := killWindowFunc
	killWindowFunc = func(id string) { killCalled = true }
	t.Cleanup(func() { killWindowFunc = origKill })

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_INSTANCE_NAME", "")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")

	stdin := `{"reason":"prompt_input_exit","session_id":"` + sessionID + `"}`
	runHookCaptured(t, nil, stdin, SessionEnd)

	if !reclaimCalled {
		t.Fatal("reclaimSessionWorktreeFunc was not called")
	}
	if killCalled {
		t.Error("killWindowFunc must NOT be called when Reclaim already ran (would kill the window twice)")
	}
	if !*pruneCalled {
		t.Error("worktree.Prune should still run after a successful reclaim")
	}
}

// TestSessionEnd_AutoReclaimWorktreeDisabledSkipsReclaim verifies the config
// gate: when auto_reclaim_worktree is explicitly false for the agent, no
// reclaim attempt is made at all, even though a WorktreeDir is recorded.
func TestSessionEnd_AutoReclaimWorktreeDisabledSkipsReclaim(t *testing.T) {
	setupWindowstate(t)

	falseVal := false
	setAutoReclaimWorktreeConfig(t, "go-engineer", &falseVal)

	const windowID = "@64"
	const sessionID = "sess-disabled"
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.SessionID = sessionID
		ws.State = "working"
		ws.WorktreeDir = "/repo/.worktrees/agent-go-engineer-issue-4"
		ws.Branch = "agent/go-engineer/issue-4"
	}); err != nil {
		t.Fatalf("setup windowstate: %v", err)
	}

	reclaimCalled := false
	origReclaim := reclaimSessionWorktreeFunc
	reclaimSessionWorktreeFunc = func(_ string, _ windowstate.WindowState) bool {
		reclaimCalled = true
		return true
	}
	t.Cleanup(func() { reclaimSessionWorktreeFunc = origReclaim })

	stubWorktreePrune(t)

	killCalled := false
	origKill := killWindowFunc
	killWindowFunc = func(id string) { killCalled = true }
	t.Cleanup(func() { killWindowFunc = origKill })

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_INSTANCE_NAME", "")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")

	stdin := `{"reason":"prompt_input_exit","session_id":"` + sessionID + `"}`
	runHookCaptured(t, nil, stdin, SessionEnd)

	if reclaimCalled {
		t.Error("reclaimSessionWorktreeFunc must not be called when auto_reclaim_worktree is disabled")
	}
	if !killCalled {
		t.Error("killWindowFunc must be called when reclaim is skipped due to config")
	}
}

// ---------------------------------------------------------------------------
// reclaimSessionWorktree — real-repo wiring tests
//
// These exercise the actual (unstubbed) function, against a real git repo,
// to prove the wiring from Facts-gathering through cleanup.Classify to
// cleanup.Reclaim is correct end to end. Everything above this section tests
// SessionEnd's control flow via the reclaimSessionWorktreeFunc seam instead.
// ---------------------------------------------------------------------------

func gitRunForHooksTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

// setupFakeRemoteForHooksTest creates a bare "remote" repo with one commit on
// main and returns a local clone whose origin points to the bare repo --
// mirroring internal/worktree's setupFakeRemote fixture (unexported there).
func setupFakeRemoteForHooksTest(t *testing.T) (localRepo string) {
	t.Helper()

	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	local := filepath.Join(tmp, "local")

	gitRunForHooksTest(t, tmp, "init", "--bare", bare)

	staging := filepath.Join(tmp, "staging")
	gitRunForHooksTest(t, tmp, "init", staging)
	gitRunForHooksTest(t, staging, "config", "user.email", "test@example.com")
	gitRunForHooksTest(t, staging, "config", "user.name", "Test")
	gitRunForHooksTest(t, staging, "remote", "add", "origin", bare)

	readme := filepath.Join(staging, "README.md")
	if err := os.WriteFile(readme, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunForHooksTest(t, staging, "add", "README.md")
	gitRunForHooksTest(t, staging, "commit", "-m", "initial commit")
	gitRunForHooksTest(t, staging, "push", "origin", "HEAD:main")

	cmd := exec.Command("git", "clone", bare, local)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, string(out))
	}
	gitRunForHooksTest(t, local, "config", "user.email", "test@example.com")
	gitRunForHooksTest(t, local, "config", "user.name", "Test")

	return local
}

// TestReclaimSessionWorktree_EmptyDispositionReclaims verifies that a
// worktree with zero commits ahead of base (an abandoned dispatch) is
// actually removed by the real reclaimSessionWorktree implementation.
func TestReclaimSessionWorktree_EmptyDispositionReclaims(t *testing.T) {
	repoDir := setupFakeRemoteForHooksTest(t)

	wtPath, err := worktree.Create(repoDir, "reclaim-empty")
	if err != nil {
		t.Fatalf("worktree.Create: %v", err)
	}

	ws := windowstateForTest(wtPath, "reclaim-empty")

	got := reclaimSessionWorktree("@70", ws)
	if !got {
		t.Fatal("reclaimSessionWorktree() = false, want true for a zero-commits-ahead-of-base worktree")
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree directory %s still exists after reclaim, want removed", wtPath)
	}
}

// TestReclaimSessionWorktree_DirtyWorktreeSkips verifies that a worktree
// with uncommitted changes is never reclaimed, matching cleanup.Classify's
// dirty-tree safety rail.
func TestReclaimSessionWorktree_DirtyWorktreeSkips(t *testing.T) {
	repoDir := setupFakeRemoteForHooksTest(t)

	wtPath, err := worktree.Create(repoDir, "reclaim-dirty")
	if err != nil {
		t.Fatalf("worktree.Create: %v", err)
	}
	// Make the worktree dirty.
	if err := os.WriteFile(filepath.Join(wtPath, "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatalf("write scratch file: %v", err)
	}

	ws := windowstateForTest(wtPath, "reclaim-dirty")

	got := reclaimSessionWorktree("@71", ws)
	if got {
		t.Fatal("reclaimSessionWorktree() = true, want false for a dirty worktree")
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("worktree directory %s must survive when dirty: %v", wtPath, err)
	}
}

// windowstateForTest builds a minimal windowstate.WindowState carrying the
// WorktreeDir/Branch fields reclaimSessionWorktree reads.
func windowstateForTest(worktreeDir, branch string) windowstate.WindowState {
	return windowstate.WindowState{WorktreeDir: worktreeDir, Branch: branch}
}
