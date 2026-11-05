package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
)

func stubDiscovery(t *testing.T, agents []agent.Agent, err error) (calls *int, gotDir *string) {
	t.Helper()
	var n int
	var dir string
	old := discoverDispatchAgents
	t.Cleanup(func() { discoverDispatchAgents = old })
	discoverDispatchAgents = func(workDir string) ([]agent.Agent, error) {
		n++
		dir = workDir
		return agents, err
	}
	return &n, &dir
}

func TestValidateAgentName(t *testing.T) {
	known := []agent.Agent{{Name: "python-engineer"}, {Name: "go-engineer"}, {Name: "go-engineer"}}

	tests := []struct {
		name          string
		agents        []agent.Agent
		discErr       error
		input         string
		wantErr       bool
		wantInErr     []string
		wantDiscovery bool
	}{
		{name: "known agent", agents: known, input: "go-engineer", wantDiscovery: true},
		{
			name: "unknown agent lists available sorted and deduped", agents: known, input: "rust-engineer",
			wantErr: true, wantDiscovery: true,
			wantInErr: []string{
				`unknown agent "rust-engineer"`,
				"available agents: Explore, Plan, claude-code-guide, general-purpose, go-engineer, python-engineer, statusline-setup",
				"plugin:name",
			},
		},
		{
			name: "shell metacharacters rejected", agents: known, input: "go-engineer; touch /tmp/pwned",
			wantErr: true, wantDiscovery: true, wantInErr: []string{"unknown agent"},
		},
		{
			name: "no discovered agents still lists built-ins", input: "go-engineer",
			wantErr: true, wantDiscovery: true, wantInErr: []string{"available agents: Explore, Plan"},
		},
		{
			name: "discovery error", discErr: errors.New("boom"), input: "go-engineer",
			wantErr: true, wantDiscovery: true, wantInErr: []string{"discovering agents", "boom"},
		},
		{name: "built-in general-purpose", input: "general-purpose"},
		{name: "built-in Explore", input: "Explore"},
		{name: "built-in Plan", input: "Plan"},
		{name: "built-in claude-code-guide", input: "claude-code-guide"},
		{name: "built-in statusline-setup", input: "statusline-setup"},
		{name: "built-ins are case-sensitive", input: "explore", wantErr: true, wantDiscovery: true, wantInErr: []string{"unknown agent"}},
		{name: "plugin agent passes through", input: "my-plugin:reviewer"},
		{name: "plugin with empty plugin part", input: ":reviewer", wantErr: true, wantDiscovery: true},
		{name: "plugin with empty name part", input: "my-plugin:", wantErr: true, wantDiscovery: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, gotDir := stubDiscovery(t, tt.agents, tt.discErr)
			err := validateAgentName(tt.input, "/some/dir")
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateAgentName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if (*calls > 0) != tt.wantDiscovery {
				t.Errorf("discovery calls = %d, wantDiscovery %v", *calls, tt.wantDiscovery)
			}
			if tt.wantDiscovery && *gotDir != "/some/dir" {
				t.Errorf("discovery called with workDir %q, want /some/dir", *gotDir)
			}
			for _, want := range tt.wantInErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}
}
