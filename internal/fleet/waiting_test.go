package fleet

import (
	"testing"

	"github.com/blsmth/agentctl/internal/state"
)

func wrow(session, id, sid, st string, mut func(*state.AgentState)) Row {
	a := state.AgentState{Name: "go-engineer~" + id[1:], AgentName: "go-engineer", WindowID: id, SessionID: sid, State: st}
	if mut != nil {
		mut(&a)
	}
	return Row{Session: session, AgentState: a}
}

func child(id, st string, mut func(*state.AgentState)) Row {
	return wrow("s1", id, "sid-"+id, st, func(a *state.AgentState) {
		a.SpawnedBy, a.SpawnedBySession = "@1", "p-sess"
		if mut != nil {
			mut(a)
		}
	})
}

func TestTagWaiting(t *testing.T) {
	parent := func() Row { return wrow("s1", "@1", "p-sess", "idle", nil) }
	tests := []struct {
		name    string
		others  []Row
		other   []Row // rows in a second tmux session
		want    int
		blocked bool
	}{
		{name: "no children"},
		{name: "idle and done children", others: []Row{child("@2", "idle", nil), child("@3", "idle", func(a *state.AgentState) { a.Done = true })}},
		{name: "blocked child", others: []Row{child("@2", "blocked", nil)}, want: 1, blocked: true},
		{name: "stale session after resume", others: []Row{child("@2", "working", func(a *state.AgentState) { a.SpawnedBySession = "old" })}},
		{name: "recycled window id", others: []Row{child("@2", "working", func(a *state.AgentState) { a.SpawnedBySession = "" })}},
		{name: "cron child ignored", others: []Row{child("@2", "working", func(a *state.AgentState) { a.ScheduledBy = "nightly" })}},
		{name: "child in other session", other: []Row{child("@9", "working", nil)}, want: 1},
		{name: "shell ignored", others: []Row{{Session: "s1", Shell: true, AgentState: state.AgentState{WindowID: "@5", State: "working", SpawnedBy: "@1", SpawnedBySession: "p-sess"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := []Group{{Session: "s1", Rows: append([]Row{parent()}, tt.others...)}, {Session: "s2", Rows: tt.other}}
			TagWaiting(groups)
			got := groups[0].Rows[0].WaitingOn
			if len(got) != tt.want {
				t.Fatalf("WaitingOn = %v, want %d", got, tt.want)
			}
			if tt.want == 1 && got[0].Blocked != tt.blocked {
				t.Errorf("Blocked = %v, want %v", got[0].Blocked, tt.blocked)
			}
			for _, g := range groups {
				for _, r := range g.Rows[min(1, len(g.Rows)):] {
					if len(r.WaitingOn) != 0 {
						t.Errorf("child %s waiting on %v", r.WindowID, r.WaitingOn)
					}
				}
			}
		})
	}
}

func TestTagWaitingClearsStale(t *testing.T) {
	groups := []Group{{Rows: []Row{wrow("s1", "@1", "p-sess", "idle", nil)}}}
	groups[0].Rows[0].WaitingOn = []WaitChild{{Label: "x"}}
	TagWaiting(groups)
	if len(groups[0].Rows[0].WaitingOn) != 0 {
		t.Fatal("stale WaitingOn kept")
	}
}

func TestWaitingLine(t *testing.T) {
	if got := WaitingLine(Row{}); got != "" {
		t.Errorf("empty = %q", got)
	}
	r := Row{WaitingOn: []WaitChild{{Label: "go-engineer:3"}, {Label: "code-reviewer:2", Blocked: true}}}
	if got, want := WaitingLine(r), "waiting on go-engineer:3 ●, code-reviewer:2 ⏸"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	r.WaitingOn = append(r.WaitingOn, WaitChild{Label: "c"}, WaitChild{Label: "d"}, WaitChild{Label: "e"})
	if got, want := WaitingLine(r), "waiting on go-engineer:3 ●, code-reviewer:2 ⏸, c ●, +2"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
