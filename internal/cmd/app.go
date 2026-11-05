package cmd

import (
	"os"
	"path/filepath"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/tmux"
)

// App holds the shared dependencies that are common across agentctl command
// implementations. Create one with [NewApp] after the standard init sequence
// (paths.Init, config.Init, etc.) has run in main.
type App struct {
	// Config is the loaded global configuration. Callers may call config.Load()
	// again if they need a fresh snapshot; this field is the value at NewApp
	// construction time.
	Config *config.Config

	// RepoDir is the current working directory at startup, used as the default
	// repository root for worktree operations and relative path resolution.
	RepoDir string

	// AgentDir is the path to the user's Claude agent definitions directory
	// (~/.claude/agents/). Agents are discovered from here as well as from
	// project-local .claude/agents/ subdirectories.
	AgentDir string

	// SessionName is the tmux session name that agentctl manages for the
	// current working directory. Populated from tmux.SessionName after
	// tmux.SetSessionName() has been called.
	SessionName string
}

// NewApp constructs a fully initialised App from the current environment.
// It must be called after paths.Init, config.Init, and tmux.SetSessionName
// have all run.
func NewApp() *App {
	cfg := config.Load()

	repoDir, _ := os.Getwd()
	if repoDir == "" {
		repoDir, _ = os.UserHomeDir()
	}

	home, _ := os.UserHomeDir()
	agentDir := filepath.Join(home, ".claude", "agents")

	return &App{
		Config:      cfg,
		RepoDir:     repoDir,
		AgentDir:    agentDir,
		SessionName: tmux.SessionName,
	}
}

// DiscoverAll returns all agent definitions visible from the current
// environment: global agents from AgentDir plus any project-local agents
// found under the directories configured in cfg.
func (a *App) DiscoverAll() ([]agent.Agent, error) {
	return agent.DiscoverAll(a.Config, agent.ProjectDirs(a.Config, a.RepoDir))
}
