package fleet

import (
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// LoadIndex is the session Index of every live window on the machine plus the
// closed ledger. Failures degrade to what could be read.
func LoadIndex() *Index {
	groups, _ := Load()
	recs, _ := closedlog.Load(time.Now().Add(-closedlog.MaxAge))
	return NewIndex(groups, recs)
}

// RowFromRecord is the Row of a window record, for callers that hold a
// windowstate record rather than a fleet row (hooks).
func RowFromRecord(windowID string, ws windowstate.WindowState) Row {
	a := agentFromWindow(0, tmux.WindowInfo{ID: windowID}, "", map[string]windowstate.WindowState{windowID: ws})
	return Row{AgentState: a}
}

// Who names a session for a person: the agent, its label and its handle,
// e.g. `go-engineer "esc fix in hub" (a3f9)`. Parts that are empty are left
// out; a label that already starts with the agent name ("go-engineer · started
// 09:05") is not repeated. ix may be nil, the handle is then the minimum
// prefix.
func Who(ix *Index, r Row) string {
	agent := r.AgentName
	if agent == "" {
		agent = RowDisplayName(r)
	}
	label := Label(r, Scope{})
	who := agent
	switch {
	case label == "" || label == agent:
	case strings.HasPrefix(label, agent):
		who = label
	default:
		who += ` "` + label + `"`
	}
	if h := ix.HandleFor(r.SessionID); h != "" {
		who += " (" + h + ")"
	}
	return who
}
