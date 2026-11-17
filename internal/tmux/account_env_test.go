package tmux

import (
	"strings"
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
			"default-dir account unsets CLAUDE_CONFIG_DIR and scrubs credentials",
			map[string]string{EnvUnset: "CLAUDE_CONFIG_DIR", "AGENTCTL_ACCOUNT": "ll"},
			"CLAUDE_CONFIG_DIR ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN",
		},
		{
			"default-dir account merged with a nous unset list",
			map[string]string{EnvUnset: "CLAUDE_CONFIG_DIR ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN"},
			"CLAUDE_CONFIG_DIR ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN",
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

// A default-dir (ll) launch must remove an inherited CLAUDE_CONFIG_DIR in
// the launch script, never pass one through tmux -e.
func TestDefaultDirLaunchUnsetsConfigDir(t *testing.T) {
	env := map[string]string{EnvUnset: "CLAUDE_CONFIG_DIR", "AGENTCTL_ACCOUNT": "ll"}
	for _, a := range buildLaunchEnv("w", "/wd", "a", "sid", "/bin/agentctl", "", env) {
		if strings.HasPrefix(a, "CLAUDE_CONFIG_DIR=") {
			t.Errorf("CLAUDE_CONFIG_DIR passed to tmux: %q", a)
		}
	}
	dir := t.TempDir()
	_, content, err := writeLaunchScript(dir, "claude --agent x", launchLogPath(dir, "w"), launchUnsetEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "unset CLAUDE_CONFIG_DIR ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN\n") {
		t.Errorf("launch script does not unset CLAUDE_CONFIG_DIR and credentials:\n%s", content)
	}
	var ws windowstate.WindowState
	applyLaunchBootstrap(&ws, "w", "a", "", env)
	if ws.Account != "ll" {
		t.Errorf("windowstate Account = %q, want ll", ws.Account)
	}
}

func TestAddRemoveUnsetEnv(t *testing.T) {
	env := map[string]string{}
	AddUnsetEnv(env, "A", "B")
	AddUnsetEnv(env, "B", "C")
	if env[EnvUnset] != "A B C" {
		t.Errorf("after add: %q", env[EnvUnset])
	}
	RemoveUnsetEnv(env, "B")
	if env[EnvUnset] != "A C" {
		t.Errorf("after remove: %q", env[EnvUnset])
	}
	RemoveUnsetEnv(env, "A", "C")
	if _, ok := env[EnvUnset]; ok {
		t.Errorf("empty list must delete the key: %v", env)
	}
	AddUnsetEnv(env)
	if _, ok := env[EnvUnset]; ok {
		t.Errorf("adding nothing must not create the key: %v", env)
	}
}
