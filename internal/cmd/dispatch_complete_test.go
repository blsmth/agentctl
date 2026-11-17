package cmd

import (
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestRunDispatchComplete(t *testing.T) {
	tests := []struct {
		name       string
		ws         windowstate.WindowState
		wantRun    bool
		wantDir    string
		wantBranch string
		wantAgent  string
		wantTicket int
	}{
		{
			name:       "worktree fields from windowstate",
			ws:         windowstate.WindowState{AgentName: "go-engineer:2", Mode: "dispatch", Ticket: "42", WorktreeDir: "/wt", Branch: "agent/x/issue-42"},
			wantRun:    true,
			wantDir:    "/wt",
			wantBranch: "agent/x/issue-42",
			wantAgent:  "go-engineer",
			wantTicket: 42,
		},
		{
			name:       "falls back to pane cwd and git",
			ws:         windowstate.WindowState{AgentName: "go-engineer", Mode: "adhoc"},
			wantRun:    true,
			wantDir:    "/pane",
			wantBranch: "git-branch",
			wantAgent:  "go-engineer",
		},
		{name: "no windowstate", ws: windowstate.WindowState{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got state.Transition
			ran := false
			d := completeDeps{
				readState:   func(string) (windowstate.WindowState, error) { return tt.ws, nil },
				paneWorkDir: func(string) string { return "/pane" },
				gitBranch:   func(string) string { return "git-branch" },
				loadConfig:  func() *config.Config { return &config.Config{} },
				run:         func(tr state.Transition, _ config.AgentConfig) { ran = true; got = tr },
			}
			runDispatchComplete("@7", d)
			if ran != tt.wantRun {
				t.Fatalf("ran = %v, want %v", ran, tt.wantRun)
			}
			if !ran {
				return
			}
			if got.WorkDir != tt.wantDir || got.Branch != tt.wantBranch || got.Agent != tt.wantAgent ||
				got.TicketNumber != tt.wantTicket || got.WindowID != "@7" || got.Mode != tt.ws.Mode ||
				!got.Done || got.To != state.StatusIdle {
				t.Errorf("transition = %+v", got)
			}
		})
	}
}
