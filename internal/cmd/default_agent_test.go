package cmd

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
)

func TestUsableDefaultAgent(t *testing.T) {
	const (
		userGo   = "/home/u/.claude/agents/go-engineer.md"
		userLead = "/home/u/.claude/agents/ll-lead.md"
		userAuto = "/home/u/.claude/agents/yolo.md"
	)
	user := []agent.Agent{
		{Name: "go-engineer", FilePath: userGo},
		{Name: "ll-lead", FilePath: userLead},
		{Name: "yolo", FilePath: userAuto},
		{Name: "shadowed", FilePath: "/home/u/.claude/agents/shadowed.md"},
	}
	// cwd discovery sees the user agents plus the repo's own .claude/agents,
	// which adds repo-only and replaces shadowed.
	all := []agent.Agent{
		{Name: "go-engineer", FilePath: userGo},
		{Name: "ll-lead", FilePath: userLead},
		{Name: "yolo", FilePath: userAuto},
		{Name: "shadowed", FilePath: "/repo/.claude/agents/shadowed.md", Scope: agent.ScopeLocal},
		{Name: "repo-only", FilePath: "/repo/.claude/agents/repo-only.md", Scope: agent.ScopeLocal},
	}
	agents := map[string]config.AgentConfig{
		"yolo":     {AutoAccept: true},
		"bypass":   {PermissionMode: "bypassPermissions"},
		"ll-lead":  {PermissionMode: "plan"},
		"Explore":  {PermissionMode: "dontAsk"},
		"p:edits":  {PermissionMode: "acceptEdits"},
		"p:manual": {PermissionMode: "manual"},
	}
	tests := []struct {
		name     string
		def      string // merged default_agent
		global   string // global-layer default_agent
		discErr  error
		userErr  error
		want     string
		wantWarn string // substring of the warning, "" for none
	}{
		// Global-only values (DefaultAgent == GlobalDefaultAgent).
		{name: "unset", def: "", global: "", want: ""},
		{name: "global discovered", def: "go-engineer", global: "go-engineer", want: "go-engineer"},
		{name: "global claude built-in", def: "Plan", global: "Plan", want: "Plan"},
		{name: "global plugin agent", def: "my-plugin:reviewer", global: "my-plugin:reviewer", want: "my-plugin:reviewer"},
		{name: "global malformed plugin agent", def: "a:b:c", global: "a:b:c", want: "", wantWarn: "invalid agent name"},
		{name: "global not discovered", def: "rust-engineer", global: "rust-engineer", want: "", wantWarn: "no such agent"},
		{name: "global repo-only agent still launches", def: "repo-only", global: "repo-only", want: "repo-only"},
		{name: "global auto_accept agent is the user's choice", def: "yolo", global: "yolo", want: "yolo"},
		{name: "global discovery error", def: "go-engineer", global: "go-engineer", discErr: errors.New("boom"), want: "", wantWarn: "boom"},

		// Project values (DefaultAgent != GlobalDefaultAgent).
		{name: "project user-level agent honored", def: "ll-lead", global: "brandon", want: "ll-lead"},
		{name: "project user-level agent honored with no global", def: "ll-lead", global: "", want: "ll-lead"},
		{name: "project built-in honored", def: "Plan", global: "go-engineer", want: "Plan"},
		{name: "project plugin agent honored", def: "p:manual", global: "go-engineer", want: "p:manual"},
		{name: "project injection falls back to global", def: "x$(touch /tmp/p)", global: "go-engineer", want: "go-engineer", wantWarn: "invalid agent name"},
		{name: "project path traversal falls back to global", def: "../../x", global: "go-engineer", want: "go-engineer", wantWarn: "invalid agent name"},
		{name: "project repo-only agent falls back to global", def: "repo-only", global: "go-engineer", want: "go-engineer", wantWarn: "not defined in the user-level agents dir"},
		{name: "project agent shadowed by repo falls back to global", def: "shadowed", global: "go-engineer", want: "go-engineer", wantWarn: "shadowed by a repo-local"},
		{name: "project unknown agent falls back to global", def: "rust-engineer", global: "go-engineer", want: "go-engineer", wantWarn: "not defined in the user-level"},
		{name: "project auto_accept agent falls back to global", def: "yolo", global: "go-engineer", want: "go-engineer", wantWarn: "auto_accept"},
		{name: "project dontAsk built-in falls back to global", def: "Explore", global: "go-engineer", want: "go-engineer", wantWarn: "skips approvals"},
		{name: "project acceptEdits plugin falls back to global", def: "p:edits", global: "go-engineer", want: "go-engineer", wantWarn: "skips approvals"},
		{name: "project user discovery error falls back to global", def: "ll-lead", global: "go-engineer", userErr: errors.New("boom"), want: "go-engineer", wantWarn: "discovering user agents"},
		{name: "project rejected with no global gives empty", def: "repo-only", global: "", want: "", wantWarn: "no global default_agent is set"},
		{name: "project injection with no global gives empty", def: "x$(touch /tmp/p)", global: "", want: "", wantWarn: "invalid agent name"},
		{name: "project rejected and global unusable gives empty", def: "yolo", global: "rust-engineer", want: "", wantWarn: "no such agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubDiscovery(t, all, tt.discErr)
			stubUserDiscovery(t, user, tt.userErr)
			cfg := &config.Config{DefaultAgent: tt.def, GlobalDefaultAgent: tt.global, Agents: agents}
			var w bytes.Buffer
			got := usableDefaultAgent(cfg, &w)
			if got != tt.want {
				t.Errorf("usableDefaultAgent(%q, global %q) = %q, want %q", tt.def, tt.global, got, tt.want)
			}
			if tt.wantWarn == "" {
				if w.Len() != 0 {
					t.Errorf("unexpected warning %q", w.String())
				}
			} else if !strings.Contains(w.String(), tt.wantWarn) {
				t.Errorf("warning = %q, want it to contain %q", w.String(), tt.wantWarn)
			}
		})
	}
}

func TestUsableDefaultAgentGlobalAutoAccept(t *testing.T) {
	// A project may name the auto-approving agent when the user already made
	// it the global default: that grants the repo nothing new.
	stubDiscovery(t, []agent.Agent{{Name: "yolo", FilePath: "/u/yolo.md"}}, nil)
	stubUserDiscovery(t, []agent.Agent{{Name: "yolo", FilePath: "/u/yolo.md"}}, nil)
	cfg := &config.Config{
		DefaultAgent:       "yolo",
		GlobalDefaultAgent: "yolo",
		Agents:             map[string]config.AgentConfig{"yolo": {AutoAccept: true}},
	}
	var w bytes.Buffer
	if got := usableDefaultAgent(cfg, &w); got != "yolo" {
		t.Errorf("got %q, want yolo (warning %q)", got, w.String())
	}
	if got := projectDefaultRejection(cfg, "yolo"); got != "" {
		t.Errorf("projectDefaultRejection = %q, want accepted when it is the global default", got)
	}
}

// stubUserDiscovery replaces discoverUserAgents for the duration of t.
func stubUserDiscovery(t *testing.T, agents []agent.Agent, err error) {
	t.Helper()
	old := discoverUserAgents
	t.Cleanup(func() { discoverUserAgents = old })
	discoverUserAgents = func() ([]agent.Agent, error) { return agents, err }
}

func TestLessThenRemoveQuotesPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a b;$(touch x)'q", "log.txt")
	cmd := lessThenRemove(path)
	// Swap the pager for echo to see exactly what the shell passes it.
	cmd = strings.Replace(cmd, "less -R ", "printf %s ", 1)
	cmd = strings.Replace(cmd, "; rm -f ", "; : ", 1)
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil {
		t.Fatalf("running %q: %v", cmd, err)
	}
	if string(out) != path {
		t.Errorf("pager got %q, want %q", out, path)
	}
}
