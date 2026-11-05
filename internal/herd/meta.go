package herd

import (
	"time"

	"github.com/blsmth/agentctl/internal/windowstate"
)

// Meta is the per-window detail that lives in windowstate but never reaches
// state.json: how the window was launched, its dispatch metadata, and the
// spawned-by link back to whatever dispatched it.
//
// state.json is the monitor's rendering-oriented snapshot; windowstate is the
// hook-written record of how the window came to exist. A status query needs
// both, so Meta is loaded alongside a Group's Rows rather than folded into
// state.AgentState (which would mean widening what every monitor tick writes).
type Meta struct {
	WindowID         string
	Mode             string // "dispatch", "adhoc", "bare", "custom"
	Ticket           string
	TicketTitle      string
	Branch           string
	WorktreeDir      string
	LogPath          string
	SessionID        string
	SpawnedBy        string // raw windowstate value — see ParentTrusted before showing it
	SpawnedBySession string
	UpdatedAt        time.Time
}

// ParentTrusted reports whether SpawnedBy actually identifies the window that
// dispatched this one.
//
// It is only trustworthy for dispatched windows, and SpawnedBySession is the
// discriminator. The reason is in how the two values are produced:
//
//   - A dispatched launch runs with AGENTCTL_SESSION_ID set, so
//     signal.ResolveWindowID resolves the parent via its first and reliable
//     step: session UUID → windowstate lookup. Both fields get written.
//
//   - A bare/menu launch has neither AGENTCTL_SESSION_ID nor TMUX_PANE, so
//     resolution falls through to the last-resort step: the AGENTCTL_WINDOW_ID
//     environment variable. That variable is set with `tmux set-environment
//     -t <session>`, and tmux has no window-scoped environment — it is
//     session-global and last-writer-wins. Every bare window therefore
//     inherits, and reports, the *previously created* window as its parent.
//     SpawnedBySession stays empty because there was no session UUID to record.
//
// So: non-empty SpawnedBySession means a real dispatcher; empty means the
// SpawnedBy value is an artifact of that env leak and must not be displayed as
// a parent. Callers render "-" rather than a plausible-looking wrong answer.
func (m Meta) ParentTrusted() bool { return m.SpawnedBySession != "" }

// Parent returns the dispatching window ID, or "" when the recorded value is
// not trustworthy. Prefer this over reading SpawnedBy directly.
func (m Meta) Parent() string {
	if !m.ParentTrusted() {
		return ""
	}
	return m.SpawnedBy
}

// Dispatched reports whether this window was launched by another agent, as
// opposed to interactively from the menu or hub.
func (m Meta) Dispatched() bool { return m.Mode != "" && m.Mode != "bare" }

// LoadMeta reads every windowstate record in a group's runtime directory,
// keyed by window ID. Returns an empty (non-nil) map when the group has no
// runtime dir or the directory is unreadable — a group whose windowstate is
// missing still renders from state.json, just without launch metadata.
func LoadMeta(g Group) map[string]Meta {
	out := make(map[string]Meta)
	for id, ws := range windowstate.AllFrom(windowstate.WindowsDirFor(g.RuntimeDir)) {
		out[id] = Meta{
			WindowID:         id,
			Mode:             ws.Mode,
			Ticket:           ws.Ticket,
			TicketTitle:      ws.TicketTitle,
			Branch:           ws.Branch,
			WorktreeDir:      ws.WorktreeDir,
			LogPath:          ws.LogPath,
			SessionID:        ws.SessionID,
			SpawnedBy:        ws.SpawnedBy,
			SpawnedBySession: ws.SpawnedBySession,
			UpdatedAt:        ws.UpdatedAt,
		}
	}
	return out
}
