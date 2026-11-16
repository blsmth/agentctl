package main

import (
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

func TestWorktreeSettingsAgentConfigBackend(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{"a": {Backend: "nous"}}}
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"backend none launch of a nous-configured agent", map[string]string{}, ""},
		{"empty AGENTCTL_BACKEND", map[string]string{"AGENTCTL_BACKEND": ""}, ""},
		{"nous launch", map[string]string{"AGENTCTL_BACKEND": "nous"}, "nous"},
		{"other backend launch", map[string]string{"AGENTCTL_BACKEND": "fenrir"}, "fenrir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ac := worktreeSettingsAgentConfig(cfg, "a", func(k string) string { return tt.env[k] })
			if ac.Backend != tt.want {
				t.Errorf("Backend = %q, want %q", ac.Backend, tt.want)
			}
		})
	}
}
