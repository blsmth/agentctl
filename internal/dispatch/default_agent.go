package dispatch

import (
	"fmt"
	"slices"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
)

// approvalModes are the permission_mode values that keep claude asking before
// it acts. Every other value (auto, acceptEdits, dontAsk, bypassPermissions,
// or anything unrecognized) approves at least some actions on its own.
var approvalModes = []string{"", "default", "manual", "plan"}

// DefaultAgentDiscovery is how ResolveDefaultAgent finds agents. A nil field
// uses the live discovery; callers (and tests) override it to stub.
type DefaultAgentDiscovery struct {
	// User lists the user-level registry only (no project-local agents).
	User func() ([]agent.Agent, error)
	// Visible lists the agents a dispatch targeting workDir sees.
	Visible func(workDir string) ([]agent.Agent, error)
}

func (d DefaultAgentDiscovery) user() ([]agent.Agent, error) {
	if d.User != nil {
		return d.User()
	}
	return agent.DiscoverAll(config.Load(), nil)
}

func (d DefaultAgentDiscovery) visible(workDir string) ([]agent.Agent, error) {
	if d.Visible != nil {
		return d.Visible(workDir)
	}
	return DiscoverAgents(workDir)
}

// ResolveDefaultAgent returns the agent a quick launch in workDir should use,
// or "" when there is none it can safely launch. Each rejection is reported
// through warn (which may be nil) rather than launched: default_agent drives
// the quick-launch, so it must never be handed to a launch unchecked.
//
// A default_agent that came from the project-local config (it differs from
// cfg.GlobalDefaultAgent) is untrusted, since a cloned repo commits it. It is
// honored only when ProjectDefaultRejection finds nothing wrong; otherwise
// the global default_agent is used instead.
func ResolveDefaultAgent(cfg *config.Config, workDir string, d DefaultAgentDiscovery, warn func(string)) string {
	if warn == nil {
		warn = func(string) {}
	}
	name := cfg.DefaultAgent
	if name != "" && name != cfg.GlobalDefaultAgent {
		why := d.projectRejection(cfg, workDir, name)
		if why == "" {
			return name
		}
		fallback := "no global default_agent is set"
		if cfg.GlobalDefaultAgent != "" {
			fallback = fmt.Sprintf("using global default_agent %q", cfg.GlobalDefaultAgent)
		}
		warn(fmt.Sprintf("ignoring project default_agent %q: %s; %s", name, why, fallback))
		name = cfg.GlobalDefaultAgent
	}
	if name == "" {
		return ""
	}
	if why := d.rejection(workDir, name); why != "" {
		warn(fmt.Sprintf("ignoring default_agent %q: %s", name, why))
		return ""
	}
	return name
}

// DefaultAgentRejection returns why name cannot be launched as the default
// agent, or "" when it is a valid agent name that discovery actually finds,
// a Claude Code built-in, or a valid plugin:agent reference.
func (d DefaultAgentDiscovery) rejection(workDir, name string) string {
	if !agent.ValidLaunchName(name) {
		return "invalid agent name"
	}
	// Built-ins and plugin agents have no .md for discovery to find; they
	// pass through by name exactly as `dispatch --agent` accepts them.
	if slices.Contains(claudeBuiltinAgents, name) || agent.ValidPluginName(name) {
		return ""
	}
	agents, err := d.visible(workDir)
	if err != nil {
		return fmt.Sprintf("discovering agents: %v", err)
	}
	for _, a := range agents {
		if a.Name == name {
			return ""
		}
	}
	return "no such agent"
}

// ProjectDefaultRejection returns why a project-local default_agent must not
// be honored, or "" when it is safe. Safe means the name is valid, names an
// agent the repo does not control (a built-in, a plugin agent, or one in the
// user-level registry that no repo-local definition shadows), and that agent
// does not auto-approve unless it is also the global default.
func ProjectDefaultRejection(cfg *config.Config, workDir, name string, d DefaultAgentDiscovery) string {
	return d.projectRejection(cfg, workDir, name)
}

func (d DefaultAgentDiscovery) projectRejection(cfg *config.Config, workDir, name string) string {
	if !agent.ValidLaunchName(name) {
		return "invalid agent name"
	}
	if !slices.Contains(claudeBuiltinAgents, name) && !agent.ValidPluginName(name) {
		if why := d.repoControlledRejection(workDir, name); why != "" {
			return why
		}
	}
	if name == cfg.GlobalDefaultAgent {
		return ""
	}
	ac := cfg.ForAgent(name)
	if ac.AutoAccept {
		return "agent has auto_accept enabled"
	}
	if !slices.Contains(approvalModes, ac.PermissionMode) {
		return fmt.Sprintf("agent has permission_mode %q, which skips approvals", ac.PermissionMode)
	}
	return ""
}

// repoControlledRejection returns "" when name resolves to the same file in
// the user-level registry and in workDir discovery, and why not otherwise. A
// name missing from the registry, or that a repo-local .claude/agents file
// shadows, would launch a definition the repo wrote.
func (d DefaultAgentDiscovery) repoControlledRejection(workDir, name string) string {
	user, err := d.user()
	if err != nil {
		return fmt.Sprintf("discovering user agents: %v", err)
	}
	userPath, ok := agentFilePath(user, name)
	if !ok {
		return "not defined in the user-level agents dir"
	}
	all, err := d.visible(workDir)
	if err != nil {
		return fmt.Sprintf("discovering agents: %v", err)
	}
	if path, ok := agentFilePath(all, name); !ok || path != userPath {
		return "shadowed by a repo-local agent definition"
	}
	return ""
}

// agentFilePath returns the FilePath of the first agent in agents named name.
func agentFilePath(agents []agent.Agent, name string) (string, bool) {
	for _, a := range agents {
		if a.Name == name {
			return a.FilePath, true
		}
	}
	return "", false
}
