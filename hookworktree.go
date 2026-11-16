package main

import (
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
)

// worktreeSettingsAgentConfig returns the agent config used when the
// hook-worktree-create hook plants settings into a worktree Claude Code just
// created. The backend comes from the window's AGENTCTL_BACKEND (set at launch
// from the effective backend; empty means Anthropic), never from the agent's
// configured backend: a --backend none launch of a nous-configured agent must
// not get Nous routing, and a --backend nous launch must.
func worktreeSettingsAgentConfig(cfg *config.Config, agentName string, getenv func(string) string) config.AgentConfig {
	ac := cfg.ForAgent(agentName)
	ac.Backend = getenv(dispatch.EnvBackend)
	return ac
}
