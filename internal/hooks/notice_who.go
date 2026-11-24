package hooks

import (
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// sessionIndexFn loads the machine-wide session index that handles are
// unique within. A seam so tests never read tmux or the ledger.
var sessionIndexFn = fleet.LoadIndex

// noticeWho names the window's session for a person or an orchestrator:
// `go-engineer "esc fix in hub" (a3f9)`. agent is the fallback agent name when
// the record has none.
func noticeWho(windowID string, ws windowstate.WindowState, agent string) string {
	r := fleet.RowFromRecord(windowID, ws)
	if r.AgentName == "" || r.AgentName == windowID {
		r.AgentName = agent
	}
	return fleet.Who(sessionIndexFn(), r)
}

// noticeLocation is where an agent can SendMessage the window: "@N.%P", or
// "@N" when the main pane is not recorded. Notices keep it because the
// recipient needs it to address the session; the session is named by
// noticeWho first.
func noticeLocation(windowID string, ws windowstate.WindowState) string {
	if ws.MainPane != "" {
		return windowID + "." + ws.MainPane
	}
	return windowID
}

// alertHandle is the window's session handle for a notification subtitle,
// "" before it has a session ID.
func alertHandle(ws windowstate.WindowState) string {
	if ws.SessionID == "" {
		return ""
	}
	return sessionIndexFn().HandleFor(ws.SessionID)
}
