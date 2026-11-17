package dispatch

import "path/filepath"

// EnvSessionName carries the claude `-n` session name from a launcher to
// tmux.LaunchAgent, which records it in windowstate so restore can reuse it.
const EnvSessionName = "AGENTCTL_CLAUDE_NAME"

// SessionNameFor returns the stable name passed to claude via -n. A worktree
// launch uses the worktree slug (e.g. agent-go-engineer-issue-42), which is
// already unique per dispatch; anything else uses <agent>-<project basename>.
func SessionNameFor(agent, workDir, repoDir string, useWorktree bool) string {
	if useWorktree && workDir != "" && workDir != repoDir {
		return filepath.Base(workDir)
	}
	if base := filepath.Base(repoDir); repoDir != "" && base != "." && base != string(filepath.Separator) {
		return agent + "-" + base
	}
	return agent
}
