package dispatch

import (
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
)

func TestProjectDefaultRejectionShadowed(t *testing.T) {
	const userPath = "/home/u/.claude/agents/go-engineer.md"
	user := []agent.Agent{{Name: "go-engineer", FilePath: userPath}}
	tests := []struct {
		name    string
		visible []agent.Agent
		wantErr bool
	}{
		{"same file", []agent.Agent{{Name: "go-engineer", FilePath: userPath}}, false},
		// serve's discovery lists the cwd's agents first, then the project's.
		{"project definition appended after global", []agent.Agent{
			{Name: "go-engineer", FilePath: userPath},
			{Name: "go-engineer", FilePath: "/code/proj/.claude/agents/go-engineer.md", Scope: agent.ScopeLocal},
		}, true},
		{"project definition first", []agent.Agent{
			{Name: "go-engineer", FilePath: "/code/proj/.claude/agents/go-engineer.md", Scope: agent.ScopeLocal},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DefaultAgentDiscovery{
				User:    func() ([]agent.Agent, error) { return user, nil },
				Visible: func(string) ([]agent.Agent, error) { return tt.visible, nil },
			}
			got := ProjectDefaultRejection(&config.Config{}, "/code/proj", "go-engineer", d)
			if (got != "") != tt.wantErr {
				t.Errorf("rejection = %q, wantErr %v", got, tt.wantErr)
			}
		})
	}
}
