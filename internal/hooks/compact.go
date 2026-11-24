package hooks

import (
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// PostCompact is called by Claude Code's PostCompact hook.
// Clears the cached ctx% so the monitor refreshes immediately after compaction.
func PostCompact() {
	if !guardAgentctl() {
		return
	}

	// Consume stdin.
	decodeInput(&struct{}{}) // non-fatal: ignore return value

	agentName := resolveAgentName()

	windowID := resolveHookWindowID()
	var snap windowstate.WindowState
	if windowID != "" {
		if ws, err := windowstate.Read(windowID); err == nil {
			snap = ws
		}
		// Clear the cached ctx% so the monitor re-reads from JSONL on next tick.
		// CtxPct is not stored in windowstate (it's derived from JSONL by the monitor),
		// so no windowstate update is needed here -- the monitor recomputes it each tick.
		logging.Log("hook-post-compact: ctx_pct will be reset by monitor on next tick window=%s agent=%s", windowID, agentName)
	}

	logging.Log("hook-post-compact: firing alert agent=%s", agentName)
	title, titled := alertTitle(snap, agentName, windowID)
	sendAlert(agentName, "post_compact", notify.Options{
		Title:    title,
		Subtitle: alertSubtitle("Context compacted", titled, agentName, paths.DirName()),
		Message:  "Context compacted, continuing",
		Env: map[string]string{
			"AGENTCTL_AGENT_NAME":        agentName,
			"AGENTCTL_NOTIFICATION_TYPE": "post_compact",
			"AGENTCTL_WINDOW_ID":         windowID,
		},
	})
}
