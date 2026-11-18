package fleet

import (
	"testing"

	"github.com/blsmth/agentctl/internal/registry"
	"github.com/blsmth/agentctl/internal/tmux"
)

func TestShellRows(t *testing.T) {
	dir := t.TempDir()
	fakeWindows(t, []tmux.WindowInfo{{ID: "@1", Name: "go-engineer"}, {ID: "@2", Name: "shell"}, {ID: "@3", Name: "improve~1"}}, nil)
	e := registry.Entry{SessionName: "agentctl-p", ProjectDir: dir, RuntimeDir: dir + "/.agentctl"}

	g := loadGroup(e, true)
	if len(g.Rows) != 2 {
		t.Fatalf("rows = %d, want agent + shell", len(g.Rows))
	}
	if g.Rows[0].Shell || !g.Rows[1].Shell || g.Rows[1].WindowID != "@2" || g.Rows[1].WindowIndex != 1 {
		t.Fatalf("unexpected rows: %+v", g.Rows)
	}
	if g.Rows[1].State != "" || g.Rollup != RollupIdle {
		t.Fatalf("shell row has state %q rollup %v", g.Rows[1].State, g.Rollup)
	}

	// Default loads (status, fleet watch, ...) never see shells.
	if g := LoadGroup(e); len(g.Rows) != 1 || g.Rows[0].Shell {
		t.Fatalf("LoadGroup rows = %+v, want only the agent", g.Rows)
	}
	agents, err := LoadAgents(e.SessionName, e.RuntimeDir)
	if err != nil || len(agents) != 1 {
		t.Fatalf("LoadAgents = %v, %v; want only the agent", agents, err)
	}
}

func TestShellRowDoesNotAffectRollup(t *testing.T) {
	rows := []Row{{Shell: true}, {}}
	if got := RollupRows(rows); got != RollupIdle {
		t.Fatalf("rollup = %v", got)
	}
}
