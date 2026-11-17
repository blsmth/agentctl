package tmux

import (
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestLaunchUnsetEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"no account keeps inherited credentials", map[string]string{"FOO": "1"}, ""},
		{"no account passes backend unset through", map[string]string{EnvUnset: "ANTHROPIC_API_KEY"}, "ANTHROPIC_API_KEY"},
		{
			"account scrubs all three",
			map[string]string{"CLAUDE_CONFIG_DIR": "/h/.claude-p"},
			"ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN",
		},
		{
			"backend-set values survive",
			map[string]string{"CLAUDE_CONFIG_DIR": "/h/.c", "ANTHROPIC_AUTH_TOKEN": "ollama", "ANTHROPIC_API_KEY": ""},
			"CLAUDE_CODE_OAUTH_TOKEN",
		},
		{
			"no duplicates with a nous unset list",
			map[string]string{"CLAUDE_CONFIG_DIR": "/h/.c", EnvUnset: "ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN"},
			"ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := launchUnsetEnv(tt.env); got != tt.want {
				t.Errorf("launchUnsetEnv = %q, want %q", got, tt.want)
			}
			if _, err := unsetLineFor(launchUnsetEnv(tt.env)); err != nil {
				t.Errorf("unsetLineFor: %v", err)
			}
		})
	}
}

func TestLaunchEnvCarriesAccount(t *testing.T) {
	env := map[string]string{"CLAUDE_CONFIG_DIR": "/abs/.claude-personal", "AGENTCTL_ACCOUNT": "personal"}
	args := buildLaunchEnv("w", "/wd", "a", "sid", "/bin/agentctl", "", env)
	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-e" && args[i+1] == "CLAUDE_CONFIG_DIR=/abs/.claude-personal" {
			found = true
		}
	}
	if !found {
		t.Errorf("CLAUDE_CONFIG_DIR not passed: %v", args)
	}
	var ws windowstate.WindowState
	applyLaunchBootstrap(&ws, "w", "a", "", env)
	if ws.Account != "personal" {
		t.Errorf("windowstate Account = %q, want personal", ws.Account)
	}
}
