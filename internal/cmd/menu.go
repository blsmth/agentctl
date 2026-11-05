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
func RunLaunch(name string, noWorktree bool) {
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
	RunLaunchAgent(baseName, workDir, noWorktree)
}

// RunLaunchAgent launches a fresh interactive agent session in workDir.
// The agent receives the agentctl preamble but no ticket, no autonomous
// execution instructions, and no initial user message — it starts
// interactively and waits for the user.
// noWorktree, when true, passes an explicit false override so this launch
// skips worktree isolation (the CLI's --no-worktree escape hatch); when
// false, UseWorktree is left nil so the launch inherits the mode default.
func RunLaunchAgent(name string, workDir string, noWorktree bool) {
	req := dispatch.Request{
		AgentName: name,
		Mode:      dispatch.ModeBare,
		WorkDir:   workDir,
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
	RunLaunchAgent(baseName, result.Dir, false)
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

// usableDefaultAgent returns cfg.DefaultAgent when it is a valid agent name
// that discovery actually finds (or a Claude Code built-in, or a valid
// plugin:agent reference), and "" otherwise.
// A rejected value is reported on w and in the log rather than launched:
// default_agent drives the prefix+A quick-launch, so it must never be handed
// to a launch unchecked.
func usableDefaultAgent(cfg *config.Config, w io.Writer) string {
	name := cfg.DefaultAgent
	if name == "" {
		return ""
	}
	reject := func(why string) string {
		msg := fmt.Sprintf("agentctl: warning: ignoring default_agent %q: %s", name, why)
		fmt.Fprintln(w, msg)
		logging.Warn("%s", msg)
		return ""
	}
	if !agent.ValidLaunchName(name) {
		return reject("invalid agent name")
	}
	// Built-ins and plugin agents have no .md for discovery to find; they
	// pass through by name exactly as `dispatch --agent` accepts them.
	if slices.Contains(claudeBuiltinAgents, name) || agent.ValidPluginName(name) {
		return name
	}
	agents, err := discoverDispatchAgents("")
	if err != nil {
		return reject(fmt.Sprintf("discovering agents: %v", err))
	}
	for _, a := range agents {
		if a.Name == name {
			return name
		}
	}
	return reject("no such agent")
}
