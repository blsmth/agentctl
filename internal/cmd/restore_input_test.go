package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func useRuntime(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	state.SetStatePathForTest(filepath.Join(dir, "state.json"))
	windowstate.SetDirForTest(filepath.Join(dir, "windows"))
	return dir
}

func collect() (restoreFunc, *[]dispatch.RestoreRequest) {
	var got []dispatch.RestoreRequest
	return func(r dispatch.RestoreRequest) (string, error) {
		got = append(got, r)
		return "", nil
	}, &got
}

func agentsFor(names ...string) map[string]agent.Agent {
	m := map[string]agent.Agent{}
	for _, n := range names {
		m[n] = agent.Agent{Name: n, Dir: "/proj"}
	}
	return m
}

// No monitor ever ran: only windowstate records exist.
func TestRestoreFromWindowStateOnly(t *testing.T) {
	dir := useRuntime(t)
	for id, idx := range map[string]int{"@5": 2, "@3": 1} {
		name := map[string]string{"@5": "b", "@3": "a"}[id]
		if err := windowstate.Update(id, func(ws *windowstate.WindowState) {
			ws.Instance, ws.AgentName, ws.WindowIndex, ws.WorkDir = name, name, idx, "/w"
		}); err != nil {
			t.Fatal(err)
		}
	}
	agents := state.BuildRestoreAgents(windowstate.All(), nil)
	if err := state.SaveRestoreAgents(dir, agents); err != nil {
		t.Fatal(err)
	}
	saved, err := loadRestoreInput()
	if err != nil || saved == nil {
		t.Fatalf("loadRestoreInput() = %v, %v", saved, err)
	}
	fn, got := collect()
	restoreAgents(saved.Agents, agentsFor("a", "b"), "/home/x", t.TempDir(), config.NewWindowRight, fn)
	if len(*got) != 2 || (*got)[0].InstanceName != "a" || (*got)[1].InstanceName != "b" {
		t.Errorf("restored %+v", *got)
	}
}

func TestLoadRestoreInputFallback(t *testing.T) {
	dir := useRuntime(t)
	if err := state.Save(&state.State{Agents: []state.AgentState{{Name: "mon"}}, Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	t.Run("no restore.json uses state.json", func(t *testing.T) {
		s, err := loadRestoreInput()
		if err != nil || s == nil || s.Agents[0].Name != "mon" {
			t.Fatalf("got %+v, %v", s, err)
		}
	})
	t.Run("restore.json preferred", func(t *testing.T) {
		if err := state.SaveRestoreAgents(dir, []state.AgentState{{Name: "hook"}}); err != nil {
			t.Fatal(err)
		}
		s, err := loadRestoreInput()
		if err != nil || s == nil || s.Agents[0].Name != "hook" {
			t.Fatalf("got %+v, %v", s, err)
		}
	})
}

func TestRestoreStaleAppliesToRestoreFile(t *testing.T) {
	dir := useRuntime(t)
	old := time.Now().Add(-2 * config.DefaultRestoreMaxAge)
	if err := state.SaveRestoreAt(dir, &state.State{Agents: []state.AgentState{{Name: "a"}}, Timestamp: old}); err != nil {
		t.Fatal(err)
	}
	s, err := loadRestoreInput()
	if err != nil || s == nil {
		t.Fatal(s, err)
	}
	if !restoreStale(s.Timestamp, time.Now(), config.DefaultRestoreMaxAge) {
		t.Errorf("restore.json timestamp %v not treated as stale", s.Timestamp)
	}
}

func TestRestoreAgentsOrderAndDedupe(t *testing.T) {
	in := []state.AgentState{
		{Name: "a", AgentName: "a"}, {Name: "b", AgentName: "b"}, {Name: "c", AgentName: "c"},
	}
	for _, tt := range []struct{ pos, want string }{
		{config.NewWindowRight, "abc"}, {config.NewWindowLeft, "cba"},
	} {
		t.Run(tt.pos, func(t *testing.T) {
			fn, got := collect()
			restoreAgents(in, agentsFor("a", "b", "c"), "/home/x", t.TempDir(), tt.pos, fn)
			s := ""
			for _, r := range *got {
				s += r.InstanceName
			}
			if s != tt.want {
				t.Errorf("order %q, want %q", s, tt.want)
			}
		})
	}
}

func TestLoadFreshRestoreInputClears(t *testing.T) {
	now := time.Now()
	maxAge := config.DefaultRestoreMaxAge
	tests := []struct {
		name      string
		agents    []state.AgentState
		ts        time.Time
		wantNil   bool
		wantFiles bool
	}{
		{"stale cleared", []state.AgentState{{Name: "a"}}, now.Add(-2 * maxAge), true, false},
		{"empty cleared", nil, now, true, false},
		{"fresh kept for restore", []state.AgentState{{Name: "a"}}, now, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := useRuntime(t)
			if err := state.SaveRestoreAt(dir, &state.State{Agents: tt.agents, Timestamp: tt.ts}); err != nil {
				t.Fatal(err)
			}
			if err := state.Save(&state.State{Agents: tt.agents, Timestamp: tt.ts}); err != nil {
				t.Fatal(err)
			}
			got := loadFreshRestoreInput(now, maxAge)
			if (got == nil) != tt.wantNil {
				t.Fatalf("got %v, wantNil=%v", got, tt.wantNil)
			}
			for _, f := range []string{state.RestoreFileName, "state.json"} {
				_, err := os.Stat(filepath.Join(dir, f))
				if exists := err == nil; exists != tt.wantFiles {
					t.Errorf("%s exists=%v, want %v", f, exists, tt.wantFiles)
				}
			}
		})
	}
}
