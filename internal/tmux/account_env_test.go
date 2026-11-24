package tmux

import (
	"slices"
	"strings"
	"testing"
	"time"

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
			if got := withoutIdentity(launchUnsetEnv(tt.env)); got != tt.want {
				t.Errorf("launchUnsetEnv (identity names dropped) = %q, want %q", got, tt.want)
			}
			if _, err := unsetLineFor(launchUnsetEnv(tt.env)); err != nil {
				t.Errorf("unsetLineFor: %v", err)
			}
		})
	}
}

// withoutIdentity drops the launchIdentityEnv names from a launchUnsetEnv
// result, leaving the account and backend part these tests pin.
func withoutIdentity(names string) string {
	var kept []string
	for _, n := range strings.Fields(names) {
		if !slices.Contains(launchIdentityEnv, n) {
			kept = append(kept, n)
		}
	}
	return strings.Join(kept, " ")
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
	if !strings.Contains(content, "unset CLAUDE_CONFIG_DIR ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN ") {
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

// Identity variables a launch does not set are unset, never inherited from
// the tmux global/session env; ones it sets (even to "") survive as -e
// values. AGENTCTL_WINDOW_ID is never set by a launch, so it is always unset.
func TestLaunchUnsetEnvIdentity(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantUnset []string
		wantKept  []string
	}{
		{"nothing set unsets every identity var", nil, launchIdentityEnv, nil},
		{
			"worktree launch keeps its own branch and worktree",
			map[string]string{"AGENTCTL_BRANCH": "agent/x/issue-1", "AGENTCTL_WORKTREE": "/r/.worktrees/x", "AGENTCTL_MODE": "dispatch"},
			[]string{"AGENTCTL_WINDOW_ID", "AGENTCTL_TICKET", "AGENTCTL_SPAWNED_BY"},
			[]string{"AGENTCTL_BRANCH", "AGENTCTL_WORKTREE", "AGENTCTL_MODE"},
		},
		{
			"an explicit empty value is a deliberate -e and survives",
			map[string]string{"AGENTCTL_SPAWNED_BY": ""},
			[]string{"AGENTCTL_WINDOW_ID", "AGENTCTL_BRANCH"},
			[]string{"AGENTCTL_SPAWNED_BY"},
		},
		{
			"listed once when EnvUnset already names it",
			map[string]string{EnvUnset: "AGENTCTL_BRANCH"},
			[]string{"AGENTCTL_BRANCH", "AGENTCTL_WINDOW_ID"},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Fields(launchUnsetEnv(tt.env))
			for _, n := range tt.wantUnset {
				if c := slices.Index(got, n); c < 0 {
					t.Errorf("%s not unset: %q", n, got)
				} else if slices.Contains(got[c+1:], n) {
					t.Errorf("%s listed twice: %q", n, got)
				}
			}
			for _, n := range tt.wantKept {
				if slices.Contains(got, n) {
					t.Errorf("%s unset although the launch sets it: %q", n, got)
				}
			}
		})
	}
}

func TestApplyLaunchBootstrapStartedAt(t *testing.T) {
	var ws windowstate.WindowState
	before := time.Now().Unix()
	applyLaunchBootstrap(&ws, "w", "a", "", nil)
	if ws.StartedAt < before || ws.StartedAt > time.Now().Unix() {
		t.Errorf("StartedAt = %d, want about now", ws.StartedAt)
	}
}
