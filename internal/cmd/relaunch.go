package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/pidfile"
	"github.com/blsmth/agentctl/internal/tmux"
)

// worktreesSegment is the directory under a project root that holds dispatch
// worktrees (see internal/worktree).
const worktreesSegment = string(filepath.Separator) + ".worktrees" + string(filepath.Separator)

// ResolveRelaunchRoot returns the project root a relaunch should act on.
// The session name and the monitor pidfile are both derived from this one
// value, so they can never disagree about which project is being relaunched.
//
//   - runtimeDirEnv (AGENTCTL_RUNTIME_DIR) set: its parent directory, since
//     the runtime dir is <root>/.agentctl.
//   - otherwise cwd, except that a cwd inside <root>/.worktrees/<slug>/
//     resolves to <root>, the main repo.
func ResolveRelaunchRoot(cwd, runtimeDirEnv string) string {
	if runtimeDirEnv != "" {
		return filepath.Dir(filepath.Clean(runtimeDirEnv))
	}
	cwd = filepath.Clean(cwd)
	if i := strings.Index(cwd+string(filepath.Separator), worktreesSegment); i > 0 {
		return cwd[:i]
	}
	return cwd
}

// RunRelaunchPrep stops the project's monitor and kills its tmux session,
// using the same helpers the session start path uses. It is the first half of
// `just relaunch`.
func RunRelaunchPrep() error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("relaunch-prep: getwd: %w", err)
	}
	root := ResolveRelaunchRoot(cwd, os.Getenv("AGENTCTL_RUNTIME_DIR"))
	session := tmux.SessionNameForProjectRoot(root)
	// Stop the monitor first: if it outlives the session it can observe the
	// replacement (same name) and overwrite state.json with zero agents.
	pidfile.Kill(filepath.Join(root, ".agentctl"), "monitor.pid")
	_ = exec.Command("tmux", "kill-session", "-t", "="+session).Run()
	fmt.Printf("relaunch-prep: root=%s session=%s\n", root, session)
	return nil
}
