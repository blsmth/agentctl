package dispatch

import (
	"path/filepath"
	"strconv"
	"strings"
)

// EnvSessionName carries the claude `-n` session name from a launcher to
// tmux.LaunchAgent, which records it in windowstate so restore can reuse it.
const EnvSessionName = "AGENTCTL_CLAUDE_NAME"

// SessionNameFor returns the name passed to claude via -n. A worktree launch
// uses the worktree slug (e.g. agent-go-engineer-issue-42), which is already
// unique per dispatch.
//
// Any other launch uses <agent>-<project>-<N>, where N is the agentctl
// instance number from the window name (instance is "brandon" for the first
// window, which counts as 1, or "brandon:30"). Instance numbers are already
// unique per agent among running windows and are known before the window is
// created (the tmux window index is not), so concurrent windows of one agent
// in one project never ask claude for the same name; claude silently renames
// duplicates, which would leave the recorded name stale. An empty instance
// yields the legacy <agent>-<project> form (resume of an unrecorded session,
// improvement sessions).
func SessionNameFor(agent, instance, workDir, repoDir string, useWorktree bool) string {
	if useWorktree && workDir != "" && workDir != repoDir {
		return filepath.Base(workDir)
	}
	name := agent
	if base := filepath.Base(repoDir); repoDir != "" && base != "." && base != string(filepath.Separator) {
		name = agent + "-" + base
	}
	if n := instanceNumber(instance); n > 0 {
		name += "-" + strconv.Itoa(n)
	}
	return name
}

// instanceNumber extracts N from an instance name "<agent>:N". A bare agent
// name is instance 1; an empty instance returns 0 (no number).
func instanceNumber(instance string) int {
	if instance == "" {
		return 0
	}
	if i := strings.LastIndex(instance, ":"); i >= 0 {
		if n, err := strconv.Atoi(instance[i+1:]); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

// RestoreSessionName picks the -n name for a restored or resumed window.
// The recorded name is kept, so /rename names and worktree slugs survive,
// unless it would collide: it is the legacy <agent>-<project> form that every
// pre-numbering window of one agent shared, or another restored window
// already took it. Then it is regenerated from the window's instance name.
// taken holds names already given to restored windows; the result is added.
// An empty recorded name stays empty (the original launch passed no -n).
func RestoreSessionName(recorded, agent, instance, workDir string, taken map[string]bool) string {
	if recorded == "" {
		return ""
	}
	name := recorded
	legacy := SessionNameFor(agent, "", workDir, workDir, false)
	if recorded == legacy || taken[recorded] {
		name = SessionNameFor(agent, instance, workDir, workDir, false)
	}
	taken[name] = true
	return name
}
