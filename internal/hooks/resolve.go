package hooks

import (
	"github.com/blsmth/agentctl/internal/signal"
)

// resolveHookWindowID returns the tmux window ID for the hook's own pane.
// Resolution order (most -> least reliable):
//  1. AGENTCTL_SESSION_ID -> windowstate lookup (UUID, never reused)
//  2. TMUX_PANE -> tmux display-message #{window_id}
//  3. AGENTCTL_WINDOW_ID env (may be stale)
//
// Delegates to signal.ResolveWindowID so the logic is not duplicated.
func resolveHookWindowID() string {
	return signal.ResolveWindowID()
}
