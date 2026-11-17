package dispatch

import (
	"errors"
	"slices"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestMissingMessagingTools(t *testing.T) {
	tests := []struct {
		name string
		md   string
		want []string
	}{
		{"no tools line", "---\nname: a\n---\nbody", nil},
		{"both present", "---\nname: a\ntools: Read, Bash, SendMessage, ListAgents\n---\n", nil},
		{"neither present", "---\nname: a\ntools: Read, Bash\n---\n", []string{"SendMessage", "ListAgents"}},
		{"one missing", "---\nname: a\ntools: Read, SendMessage\n---\n", []string{"ListAgents"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, err := agent.ParseFrontmatter([]byte(tt.md))
			if err != nil {
				t.Fatal(err)
			}
			if got := missingMessagingTools(fm.Tools); !slices.Equal(got, tt.want) {
				t.Errorf("missingMessagingTools(%q) = %v, want %v", fm.Tools, got, tt.want)
			}
		})
	}
}

func TestSessionNameFor(t *testing.T) {
	tests := []struct {
		name              string
		agent, work, repo string
		worktree          bool
		want              string
	}{
		{"worktree uses slug", "go-engineer", "/r/proj/.worktrees/agent-go-engineer-issue-42", "/r/proj", true, "agent-go-engineer-issue-42"},
		{"bare uses agent-project", "go-engineer", "/r/proj", "/r/proj", false, "go-engineer-proj"},
		{"worktree fell back to repo", "go-engineer", "/r/proj", "/r/proj", true, "go-engineer-proj"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SessionNameFor(tt.agent, tt.work, tt.repo, tt.worktree); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOrchestratorFor(t *testing.T) {
	read := func(m map[string]windowstate.WindowState) func(string) (windowstate.WindowState, error) {
		return func(id string) (windowstate.WindowState, error) {
			ws, ok := m[id]
			if !ok {
				return windowstate.WindowState{}, errors.New("not found")
			}
			return ws, nil
		}
	}
	recs := map[string]windowstate.WindowState{
		"@26": {SessionID: "s26", SessionName: "brandon"},
		"@27": {SessionID: "s27"},
		"@1":  {State: "idle"}, // plain shell window: record, no Claude session
	}
	tests := []struct {
		name, window, env string
		want              Orchestrator
	}{
		{"tracked named", "@26", "", Orchestrator{WindowID: "@26", Name: "brandon"}},
		{"tracked unnamed", "@27", "", Orchestrator{WindowID: "@27"}},
		{"tracked via env only", "@30", "env-sid", Orchestrator{WindowID: "@30"}},
		{"untracked shell window", "@1", "", Orchestrator{}},
		{"unknown window", "@99", "", Orchestrator{}},
		{"outside tmux", "", "", Orchestrator{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := orchestratorFor(tt.window, tt.env, read(recs)); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
