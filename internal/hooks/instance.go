package hooks

import (
	"os"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/tmux"
)

// sessionNameForPane is a seam over tmux.SessionNameForPane so tests can
// exercise resolveInstanceSessionName's fallback cascade without a live
// tmux server.
var sessionNameForPane = tmux.SessionNameForPane

// resolveInstanceSessionName resolves the full tmux session name (e.g.
// "agentctl-acme") of the agentctl instance the calling hook process belongs
// to. This is what lets a notification say *which* of several concurrently
// running agentctl instances it came from.
//
// This is deliberately NOT tmux.SessionName: that package var is only set by
// tmux.SetSessionName, which runs on the full-init path (main.go) and never
// on the hook fast path -- inside a hook process it is permanently stuck at
// its literal default "agentctl", which would label every alert from every
// project identically (exactly the bug this resolver exists to fix).
//
// Fallback cascade, most to least reliable:
//  1. sessionNameForPane($TMUX_PANE) -- a live tmux query for the pane this
//     hook is actually running in. Preferred over the bare
//     `tmux display-message` that tmux.CurrentSessionName uses, because that
//     resolves against the attached *client*, which is not necessarily the
//     session the hook's own pane lives in.
//  2. "agentctl-" + paths.DirName() -- reconstructs the session name from
//     the project directory. paths.DirName() is the basename of the parent
//     of AGENTCTL_RUNTIME_DIR, which hooks inherit; a dispatched agent
//     working in a git worktree still resolves to the parent project name
//     here (see paths.InitForSession's and paths.ownsDir's doc comments),
//     not the worktree slug.
//  3. "" -- neither signal is available; callers must degrade gracefully
//     (bare agent name, bare window ID target).
func resolveInstanceSessionName() string {
	if name := sessionNameForPane(os.Getenv("TMUX_PANE")); name != "" {
		return name
	}
	if dir := paths.DirName(); dir != "" {
		return "agentctl-" + dir
	}
	return ""
}
