// Package signal provides window ID resolution and state-signaling logic for
// agentctl-managed tmux windows. It is called from the "agentctl signal"
// CLI shim and from the hooks package, eliminating the previous duplication.
package signal

import (
	"fmt"
	"os"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/winname"
)

// Deps holds injectable external dependencies for signal operations.
// Production code uses DefaultDeps(). Tests inject stubs via SetDepsForTest.
type Deps struct {
	// TmuxDisplay resolves a tmux format string targeting a specific pane/window.
	// target may be a pane ID (e.g. "%3") or a window target (e.g. "agentctl:@5").
	TmuxDisplay func(target, format string) string
}

// DefaultDeps returns the production dependency set backed by real tmux calls.
func DefaultDeps() *Deps {
	return &Deps{
		TmuxDisplay: tmux.DisplayTarget,
	}
}

// deps is the active dependency set. Replaced in tests via SetDepsForTest.
var deps = DefaultDeps()

// SetDepsForTest overrides the package-level deps for the duration of a test.
// Call t.Cleanup(func() { signal.SetDepsForTest(signal.DefaultDeps()) }) to restore.
func SetDepsForTest(d *Deps) {
	deps = d
}

// autonomousModeFn and spawnCompleteFn are seams wired by main: this package
// cannot import internal/dispatch (which imports it). Defaults are the safe
// answers: nothing is autonomous, and nothing is spawned.
var (
	autonomousModeFn = func(string) bool { return false }
	spawnCompleteFn  = func(string) error { return nil }
)

// SetAutonomousModeFn overrides the mode-autonomy predicate.
func SetAutonomousModeFn(fn func(mode string) bool) { autonomousModeFn = fn }

// SetSpawnCompleteFn overrides the detached completion spawner.
func SetSpawnCompleteFn(fn func(windowID string) error) { spawnCompleteFn = fn }

// ResolveWindowID returns the tmux window ID for the current process.
// Resolution order (most → least reliable):
//  1. AGENTCTL_SESSION_ID → windowstate lookup (UUID, never reused across sessions)
//  2. TMUX_PANE → tmux display-message #{window_id} (always accurate within a pane)
//  3. AGENTCTL_WINDOW_ID env (written at session start, may be stale after restarts)
func ResolveWindowID() string {
	// 1. Session ID lookup — unambiguous UUID, never reused across sessions.
	if sessionID := os.Getenv("AGENTCTL_SESSION_ID"); sessionID != "" {
		if id := windowstate.FindBySessionID(sessionID); id != "" {
			logging.Debug("signal: resolved window_id=%s via AGENTCTL_SESSION_ID=%s", id, sessionID)
			return id
		}
		logging.Log("signal: no window found for AGENTCTL_SESSION_ID=%s; falling back", sessionID)
	}

	// 2. TMUX_PANE — set by tmux itself, always accurate within the pane.
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		if id := deps.TmuxDisplay(pane, "#{window_id}"); id != "" {
			logging.Debug("signal: resolved window_id=%s via TMUX_PANE=%s", id, pane)
			return id
		}
	}

	// 3. Fallback: AGENTCTL_WINDOW_ID from env (may be stale).
	id := os.Getenv("AGENTCTL_WINDOW_ID")
	if id != "" {
		logging.Log("signal: window_id=%s (from AGENTCTL_WINDOW_ID env)", id)
	}
	return id
}

// WindowName returns the current tmux window name for windowID.
// Returns an empty string if tmux cannot be reached.
func WindowName(windowID string) string {
	target := tmux.ExactSessionTarget(tmux.SessionName) + windowID
	return deps.TmuxDisplay(target, "#{window_name}")
}

// ResolveAgentName returns the agent name for the given window. It checks
// AGENTCTL_INSTANCE_NAME, then AGENTCTL_AGENT_NAME, then strips the status
// prefix from the live tmux window name.
func ResolveAgentName(windowID string) string {
	if name := os.Getenv("AGENTCTL_INSTANCE_NAME"); name != "" {
		logging.Debug("signal: agent_name=%q (from AGENTCTL_INSTANCE_NAME)", name)
		return name
	}
	if name := os.Getenv("AGENTCTL_AGENT_NAME"); name != "" {
		logging.Debug("signal: agent_name=%q (from AGENTCTL_AGENT_NAME)", name)
		return name
	}
	name := tmux.StripAgentPrefix(WindowName(windowID))
	logging.Debug("signal: resolved agent_name=%q from window name", name)
	return name
}

// Apply updates the windowstate for windowID to reflect state; winname renames
// the tmux window to match inside the same transaction. agentName seeds
// WindowState.Instance when the record has none; pass an empty string to have
// Apply resolve it via ResolveAgentName.
//
// Valid state values: "done", "idle", "blocked", "working".
func Apply(windowID, state, agentName string) error {
	if agentName == "" {
		agentName = ResolveAgentName(windowID)
	}

	logging.Log("signal: applying state=%s on window=%s agent=%s", state, windowID, agentName)

	// Update persistent windowstate. For "done" the same transaction claims
	// dispatch completion so exactly one detached completion process runs.
	var claimed bool
	now := time.Now()
	if _, _, err := winname.Transact(windowID, func(ws *windowstate.WindowState) {
		if ws.Instance == "" {
			ws.Instance = agentName
		}
		switch state {
		case "done":
			ws.State = "idle"
			claimed = windowstate.ClaimComplete(ws, autonomousModeFn(ws.Mode), now)
		case "working":
			ws.State = "working"
			windowstate.ClearDone(ws)
		default:
			ws.State = state
			windowstate.ClearDone(ws)
		}
	}); err != nil {
		return fmt.Errorf("updating window state: %w", err)
	}
	if claimed {
		if err := spawnCompleteFn(windowID); err != nil {
			logging.Warn("signal: spawning dispatch-complete window=%s: %v", windowID, err)
			if rerr := windowstate.ReleaseComplete(windowID, now); rerr != nil {
				logging.Warn("signal: releasing completion claim window=%s: %v", windowID, rerr)
			}
		}
	}
	logging.Log("signal: windowstate updated")

	logging.Log("signal: done — window=%s state=%s agent=%s", windowID, state, agentName)
	return nil
}
