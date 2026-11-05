// Package signal provides window ID resolution and state-signaling logic for
// agentctl-managed tmux windows. It is called from the "agentctl signal"
// CLI shim and from the hooks package, eliminating the previous duplication.
package signal

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// Deps holds injectable external dependencies for signal operations.
// Production code uses DefaultDeps(). Tests inject stubs via SetDepsForTest.
type Deps struct {
	// TmuxDisplay resolves a tmux format string targeting a specific pane/window.
	// target may be a pane ID (e.g. "%3") or a window target (e.g. "agentctl:@5").
	TmuxDisplay func(target, format string) string

	// RenameWindow renames the tmux window identified by target.
	RenameWindow func(target, name string) error
}

// DefaultDeps returns the production dependency set backed by real tmux calls.
func DefaultDeps() *Deps {
	return &Deps{
		TmuxDisplay:  tmux.DisplayTarget,
		RenameWindow: tmuxRenameWindow,
	}
}

// deps is the active dependency set. Replaced in tests via SetDepsForTest.
var deps = DefaultDeps()

// SetDepsForTest overrides the package-level deps for the duration of a test.
// Call t.Cleanup(func() { signal.SetDepsForTest(signal.DefaultDeps()) }) to restore.
func SetDepsForTest(d *Deps) {
	deps = d
}

// ResolveWindowID returns the tmux window ID for the current process.
// Resolution order (most → least reliable):
//  1. AGENTCTL_SESSION_ID → windowstate lookup (UUID, never reused across sessions)
//  2. TMUX_PANE → tmux display-message #{window_id} (always accurate within a pane)
//  3. AGENTCTL_WINDOW_ID env (written at session start, may be stale after restarts)
func ResolveWindowID() string {
	// 1. Session ID lookup — unambiguous UUID, never reused across sessions.
	if sessionID := os.Getenv("AGENTCTL_SESSION_ID"); sessionID != "" {
		if id := windowstate.FindBySessionID(sessionID); id != "" {
			logging.Log("signal: resolved window_id=%s via AGENTCTL_SESSION_ID=%s", id, sessionID)
			return id
		}
	}

	// 2. TMUX_PANE — set by tmux itself, always accurate within the pane.
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		if id := deps.TmuxDisplay(pane, "#{window_id}"); id != "" {
			logging.Log("signal: resolved window_id=%s via TMUX_PANE=%s", id, pane)
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
	target := fmt.Sprintf("%s:%s", tmux.SessionName, windowID)
	return deps.TmuxDisplay(target, "#{window_name}")
}

// ResolveAgentName returns the agent name for the given window. It checks
// AGENTCTL_INSTANCE_NAME, then AGENTCTL_AGENT_NAME, then strips the status
// prefix from the live tmux window name.
func ResolveAgentName(windowID string) string {
	if name := os.Getenv("AGENTCTL_INSTANCE_NAME"); name != "" {
		logging.Log("signal: agent_name=%q (from AGENTCTL_INSTANCE_NAME)", name)
		return name
	}
	if name := os.Getenv("AGENTCTL_AGENT_NAME"); name != "" {
		logging.Log("signal: agent_name=%q (from AGENTCTL_AGENT_NAME)", name)
		return name
	}
	name := tmux.StripAgentPrefix(WindowName(windowID))
	logging.Log("signal: resolved agent_name=%q from window name", name)
	return name
}

// Apply updates the windowstate for windowID to reflect state, then renames
// the tmux window to match. agentName is used for the window rename; pass an
// empty string to have Apply resolve it via ResolveAgentName.
//
// Valid state values: "done", "idle", "blocked", "working".
func Apply(windowID, state, agentName string) error {
	if agentName == "" {
		agentName = ResolveAgentName(windowID)
	}

	logging.Log("signal: applying state=%s on window=%s agent=%s", state, windowID, agentName)

	// Update persistent windowstate (read by the monitor on its next tick).
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		switch state {
		case "done":
			ws.State = "idle"
			ws.Done = true
		case "working":
			ws.State = "working"
			ws.Done = false
		default:
			ws.State = state
			ws.Done = false
		}
	}); err != nil {
		return fmt.Errorf("updating window state: %w", err)
	}
	logging.Log("signal: windowstate updated")

	// Rename the tmux window immediately so the tab bar reflects the new state
	// without waiting for the monitor poll interval.
	target := fmt.Sprintf("%s:%s", tmux.SessionName, windowID)
	switch state {
	case "blocked":
		if err := deps.RenameWindow(target, fmt.Sprintf("⏸ %s", agentName)); err != nil {
			logging.Log("signal: rename-window error: %v", err)
		} else {
			logging.Log("signal: renamed window to ⏸ %s", agentName)
		}
	case "idle", "done":
		if err := deps.RenameWindow(target, agentName); err != nil {
			logging.Log("signal: rename-window error: %v", err)
		} else {
			logging.Log("signal: renamed window to %s", agentName)
		}
		// "working" state: the monitor drives the spinner; no rename needed here.
	}

	logging.Log("signal: done — window=%s state=%s agent=%s", windowID, state, agentName)
	return nil
}

// tmuxRenameWindow renames a tmux window by its session:window target.
func tmuxRenameWindow(target, name string) error {
	// rename-window format-expands the name; it is literal here.
	return exec.Command("tmux", "rename-window", "-t", target, tmux.FormatEscape(name)).Run()
}
