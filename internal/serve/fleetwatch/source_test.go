package fleetwatch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
)

func row(session, id, name, dir string, shell bool) fleet.Row {
	return fleet.Row{Session: session, Project: "p", Shell: shell,
		AgentState: state.AgentState{Name: name, WindowID: id, WindowIndex: 1, WorkDir: dir}}
}

func TestFromFleetShells(t *testing.T) {
	groups := FromFleet([]fleet.Group{
		{Session: "agentctl-p", Project: "p", Rows: []fleet.Row{row("agentctl-p", "@1", "a", "/p", false), row("agentctl-p", "@2", "shell", "/p/sub", true)}},
		{Session: "agentctl-q", Project: "q", Rows: []fleet.Row{row("agentctl-q", "@3", "shell", "/q", true)}},
		{Session: "agentctl-r", Project: "r", Rows: []fleet.Row{row("agentctl-r", "@4", "a", "/r", false)}},
		{Session: tmux.HubSessionName, Rows: []fleet.Row{row(tmux.HubSessionName, "@5", "shell", "/h", true)}},
	})
	p, q, r, hub := groups[0], groups[1], groups[2], groups[3]
	if len(p.Windows) != 1 || p.Windows[0].WindowID != "@1" || len(p.Rows) != 1 {
		t.Errorf("windows %+v rows %d", p.Windows, len(p.Rows))
	}
	if len(p.Shells) != 1 || p.Shells[0] != (Shell{WindowID: "@2", Index: 1, Dir: "/p/sub"}) {
		t.Errorf("shells %+v", p.Shells)
	}
	if len(q.Windows) != 0 || len(q.Shells) != 1 {
		t.Errorf("shell-only group: %+v", q)
	}
	if r.Shells == nil || len(r.Shells) != 0 {
		t.Errorf("empty shells = %#v", r.Shells)
	}
	if len(hub.Shells) != 0 {
		t.Errorf("hub lists shells: %+v", hub.Shells)
	}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"shells":[]`) {
		t.Errorf("json %s", b)
	}
}
