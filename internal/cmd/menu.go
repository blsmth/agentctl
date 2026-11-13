package cmd

import (
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
)

// RunMenu shows the main agent picker TUI.
// writeSettings is injected from package main to avoid a circular import.
// createMenu is called when the user selects "create"; it receives the agent
// name to pre-fill (typically "").
func RunMenu(writeSettings WriteAgentSettingsFn, createMenu func(name string)) {
	done := logging.Timing("RunMenu: agent.DiscoverAll")
	cfg := config.Load()
	agents, err := agent.DiscoverFromCwd(cfg)
	done()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	done = logging.Timing("RunMenu: running agents")
	// runningByID maps windowID → instance name. We build it from the snapshot
	// when fresh (avoids a tmux fork) or fall back to a live tmux query.
	runningByID := make(map[string]string)
	snap, _ := state.LoadSnapshot()
	if !snap.IsStale(3 * time.Second) {
		for _, as := range snap.State.Agents {
			if as.WindowID != "" {
				runningByID[as.WindowID] = as.Name
			}
		}
	}
	if len(runningByID) == 0 {
		runningByID = tmux.RunningAgents()
	}
	done()

	// Build menu agent list.
	var menuAgents []tui.MenuAgent
	for _, a := range agents {
		// Collect running instances for this agent.
		var instances []string
		for _, name := range runningByID {
			if tmux.BaseAgentName(name) == a.Name {
				instances = append(instances, name)
			}
		}

		menuAgents = append(menuAgents, tui.MenuAgent{
			Name:        a.Name,
			Description: a.Description,
			Instances:   instances,
			Standalone:  !a.IsPortable(),
		})
	}

	model := tui.NewMenuModel(menuAgents, cfg.DefaultAgent)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.MenuModel).Result()
	if result.Cancelled {
		return
	}

	switch result.Action {
	case "switch":
		// Switch to running agent window by ID when available, fall back to name.
		switchID := ""
		for id, name := range runningByID {
			if name == result.Instance {
				switchID = id
				break
			}
		}
		if switchID != "" {
			if err := tmux.SwitchToWindow(switchID); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		} else {
			if err := tmux.SwitchToAgent(result.Instance); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	case "launch":
		// Clear the terminal so the next TUI replaces this one instead of
		// rendering below it (we're inside the same display-popup).
		fmt.Print("\033[H\033[2J")
		RunLaunchMenu(result.Agent, writeSettings)
	case "create":
		fmt.Print("\033[H\033[2J")
		createMenu("")
	}
}

// RunLaunch launches a fresh interactive agent session for the named agent.
// If name is empty it falls back to the configured default_agent.
// noWorktree is the CLI's --no-worktree escape hatch: when true, the agent
// launches directly in the main checkout instead of an isolated worktree.
// backend is the --backend override: a name selects that backend, "none"
// forces Anthropic, and empty defers to the agent config.
func RunLaunch(name string, noWorktree bool, backend string) {
	if name == "" {
		cfg := config.Load()
		name = usableDefaultAgent(cfg, os.Stderr)
		if name == "" {
			fmt.Fprintln(os.Stderr, "No agent specified and no usable default_agent configured")
			os.Exit(1)
		}
	}
	// Resolve base agent name (strip ~N if someone passes an instance name).
	baseName := tmux.BaseAgentName(name)
	if !agent.ValidLaunchName(baseName) {
		fmt.Fprintf(os.Stderr, "Error: invalid agent name %q\n", baseName)
		os.Exit(1)
	}
	workDir := resolveAgentDir(baseName)
	RunLaunchAgent(baseName, workDir, noWorktree, backend)
}

// RunLaunchAgent launches a fresh interactive agent session in workDir.
// The agent receives the agentctl preamble but no ticket, no autonomous
// execution instructions, and no initial user message — it starts
// interactively and waits for the user.
// noWorktree, when true, passes an explicit false override so this launch
// skips worktree isolation (the CLI's --no-worktree escape hatch); when
// false, UseWorktree is left nil so the launch inherits the mode default.
func RunLaunchAgent(name string, workDir string, noWorktree bool, backend string) {
	req := dispatch.Request{
		AgentName: name,
		Mode:      dispatch.ModeBare,
		WorkDir:   workDir,
		Backend:   backend,
	}
	if noWorktree {
		useWorktree := false
		req.UseWorktree = &useWorktree
	}
	if _, err := dispatch.Launch(req); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// RunLaunchMenu shows the launch TUI for the named agent, offering to resume a
// recent session or start fresh. writeSettings is injected from package main.
func RunLaunchMenu(name string, writeSettings WriteAgentSettingsFn) {
	// Always resolve to base agent name.
	baseName := tmux.BaseAgentName(name)

	// Resolve default working directory from agent definition.
	defaultDir := resolveAgentDir(baseName)

	// Check for recent sessions to offer resume (up to 5).
	recentSessions, _ := sessions.FindSessionsForAgent(baseName, 5)

	// Filter to sessions within the last 24 hours.
	var items []tui.LaunchSession
	for _, s := range recentSessions {
		age := time.Since(s.EndTime)
		if s.EndTime.IsZero() {
			age = time.Since(s.StartTime)
		}
		if age < 24*time.Hour {
			items = append(items, tui.LaunchSession{
				SessionID: s.ID,
				Age:       formatRelativeTime(age),
				Preview:   GetSessionPreview(s),
				Project:   s.Project,
			})
		}
	}

	// Always show the launch TUI so users can change the working directory.
	model := tui.NewLaunchModel(baseName, items, defaultDir)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.LaunchModel).Result()
	if result.Cancelled {
		return
	}

	if result.Resume {
		LaunchWithResume(baseName, result.SessionID, result.Dir, writeSettings)
		return
	}

	// Fresh launch with chosen directory. Inherits the mode default (no
	// --no-worktree escape hatch available from this TUI path).
	RunLaunchAgent(baseName, result.Dir, false, "")
}

// formatRelativeTime formats a duration as a compact human-readable relative
// time string (e.g. "just now", "5m ago", "2h ago", "3d ago").
func formatRelativeTime(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// discoverUserAgents lists the agents in the user-level registry
// (~/.claude/agents) only, without any project-local .claude/agents. It is a
// package-level var so tests can stub discovery.
var discoverUserAgents = func() ([]agent.Agent, error) {
	return agent.DiscoverAll(config.Load(), nil)
}

// approvalModes are the permission_mode values that keep claude asking before
// it acts. Every other value (auto, acceptEdits, dontAsk, bypassPermissions,
// or anything unrecognized) approves at least some actions on its own.
var approvalModes = []string{"", "default", "manual", "plan"}

// usableDefaultAgent returns the agent prefix+A should launch, or "" when
// there is none it can safely launch. Every rejection is reported on w and
// in the log rather than launched: default_agent drives the quick-launch, so
// it must never be handed to a launch unchecked.
//
// A default_agent that came from the project-local config (it differs from
// cfg.GlobalDefaultAgent) is untrusted, since a cloned repo commits it. It is
// honored only when projectDefaultRejection finds nothing wrong; otherwise
// the global default_agent is used instead.
func usableDefaultAgent(cfg *config.Config, w io.Writer) string {
	name := cfg.DefaultAgent
	if name != "" && name != cfg.GlobalDefaultAgent {
		why := projectDefaultRejection(cfg, name)
		if why == "" {
			return name
		}
		fallback := "no global default_agent is set"
		if cfg.GlobalDefaultAgent != "" {
			fallback = fmt.Sprintf("using global default_agent %q", cfg.GlobalDefaultAgent)
		}
		warnDefaultAgent(w, fmt.Sprintf("ignoring project default_agent %q: %s; %s", name, why, fallback))
		name = cfg.GlobalDefaultAgent
	}
	if name == "" {
		return ""
	}
	if why := defaultAgentRejection(name); why != "" {
		warnDefaultAgent(w, fmt.Sprintf("ignoring default_agent %q: %s", name, why))
		return ""
	}
	return name
}

// defaultAgentRejection returns why name cannot be launched as the default
// agent, or "" when it is a valid agent name that discovery actually finds,
// a Claude Code built-in, or a valid plugin:agent reference.
func defaultAgentRejection(name string) string {
	if !agent.ValidLaunchName(name) {
		return "invalid agent name"
	}
	// Built-ins and plugin agents have no .md for discovery to find; they
	// pass through by name exactly as `dispatch --agent` accepts them.
	if slices.Contains(claudeBuiltinAgents, name) || agent.ValidPluginName(name) {
		return ""
	}
	agents, err := discoverDispatchAgents("")
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

// projectDefaultRejection returns why a project-local default_agent must not
// be honored, or "" when it is safe. Safe means the name is valid, names an
// agent the repo does not control (a built-in, a plugin agent, or one in the
// user-level registry that no repo-local definition shadows), and that agent
// does not auto-approve unless it is also the global default.
func projectDefaultRejection(cfg *config.Config, name string) string {
	if !agent.ValidLaunchName(name) {
		return "invalid agent name"
	}
	if !slices.Contains(claudeBuiltinAgents, name) && !agent.ValidPluginName(name) {
		if why := repoControlledRejection(name); why != "" {
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
// the user-level registry and in cwd discovery, and why not otherwise. A name
// that is missing from the registry, or that a repo-local .claude/agents file
// shadows, would launch a definition the repo wrote.
func repoControlledRejection(name string) string {
	user, err := discoverUserAgents()
	if err != nil {
		return fmt.Sprintf("discovering user agents: %v", err)
	}
	userPath, ok := agentPath(user, name)
	if !ok {
		return "not defined in the user-level agents dir"
	}
	all, err := discoverDispatchAgents("")
	if err != nil {
		return fmt.Sprintf("discovering agents: %v", err)
	}
	if path, ok := agentPath(all, name); !ok || path != userPath {
		return "shadowed by a repo-local agent definition"
	}
	return ""
}

// agentPath returns the FilePath of the first agent in agents named name.
func agentPath(agents []agent.Agent, name string) (string, bool) {
	for _, a := range agents {
		if a.Name == name {
			return a.FilePath, true
		}
	}
	return "", false
}

// warnDefaultAgent reports msg on w and in the log.
func warnDefaultAgent(w io.Writer, msg string) {
	msg = "agentctl: warning: " + msg
	fmt.Fprintln(w, msg)
	logging.Warn("%s", msg)
}
