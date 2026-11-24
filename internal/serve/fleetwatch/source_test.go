package fleetwatch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/closedlog"
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

func TestFromFleetLabelsAndHandles(t *testing.T) {
	old := closedSessions
	closedSessions = func() []closedlog.Record { return nil }
	t.Cleanup(func() { closedSessions = old })
	r1 := row("agentctl-p", "@1", "a", "/p", false)
	r1.SessionID, r1.ShortTitle = "abcd1111", "fix hub"
	r2 := row("agentctl-p", "@2", "a", "/p", false)
	r2.SessionID, r2.ShortTitle = "abcd2222", "fix hub"
	r3 := row("agentctl-p", "@3", "a", "/p", false)
	r3.StartTime = 0
	groups := FromFleet([]fleet.Group{{Session: "agentctl-p", Project: "p", Rows: []fleet.Row{r1, r2, r3}}})
	w := groups[0].Windows
	if w[0].Label != "fix hub" || w[1].Label != "fix hub ·2" {
		t.Errorf("labels %q %q", w[0].Label, w[1].Label)
	}
	if w[0].Handle != "abcd1" || w[1].Handle != "abcd2" || w[2].Handle != "" {
		t.Errorf("handles %q %q %q", w[0].Handle, w[1].Handle, w[2].Handle)
	}
}
