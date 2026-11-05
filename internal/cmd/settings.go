package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
	"github.com/blsmth/agentctl/internal/worktree"
)

// CmdSettingsMenu resolves the current agent and runs the Bubble Tea settings popup.
func CmdSettingsMenu() {
	agentName := resolveCurrentAgentName()
	if agentName == "" {
		tmux.DisplayMessage("Switch to an agent window first")
		return
	}
	agentName = tmux.BaseAgentName(agentName)
	CmdSettingsPopup(agentName)
}

// CmdSettingsPopup runs the Bubble Tea settings popup inside a tmux display-popup.
func CmdSettingsPopup(windowIDOrName string) {
	// Resolve window ID (e.g. "@3") to agent name, or fall back to treating as name.
	var agentName string
	if strings.HasPrefix(windowIDOrName, "@") {
		// Try snapshot first.
		snap, _ := state.LoadSnapshot()
		if !snap.IsStale(3 * time.Second) {
			if as := snap.ByWindowID(windowIDOrName); as != nil {
				agentName = as.AgentName
			}
		}
		if agentName == "" {
			agentName = tmux.ResolveAgentByWindowID(windowIDOrName)
		}
	} else {
		agentName = tmux.BaseAgentName(tmux.StripAgentPrefix(windowIDOrName))
	}
	if agentName == "" || agentName == "shell" {
		fmt.Fprintf(os.Stderr, "Switch to an agent window first\n")
		os.Exit(1)
	}

	cfg := config.Load()
	ac := cfg.ForAgent(agentName)

	model := tui.NewSettingsModel(agentName, ac.Alerts, ac.AutoImprove, ac.CrossContext, ac.AlertConfig.Blocked, ac.AlertConfig.Idle, ac.AlertConfig.Gone, ac.AutoAccept, ac.WorkJournal)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.SettingsModel).Result()
	if result.Cancelled {
		return
	}

	// Apply all toggled settings.
	if alerts, ok := result.Toggles["alerts"]; ok {
		ac.Alerts = alerts
	}
	if autoImprove, ok := result.Toggles["auto_improve"]; ok {
		ac.AutoImprove = autoImprove
	}
	if crossContext, ok := result.Toggles["cross_context"]; ok {
		ac.CrossContext = crossContext
	}
	if autoAccept, ok := result.Toggles["auto_accept"]; ok {
		ac.AutoAccept = autoAccept
	}
	if v, ok := result.Toggles["work_journal"]; ok {
		ac.WorkJournal = v
	}
	if v, ok := result.Toggles["alert_blocked"]; ok {
		ac.AlertConfig.Blocked = v
	}
	if v, ok := result.Toggles["alert_idle"]; ok {
		ac.AlertConfig.Idle = v
	}
	if v, ok := result.Toggles["alert_gone"]; ok {
		ac.AlertConfig.Gone = v
	}
	if err := cfg.SetAgent(agentName, ac); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}
}

// CmdPermissionsPopup runs the Bubble Tea permissions popup inside a tmux
// display-popup. It resolves the current window name from tmux (works inside
// display-popup context).
func CmdPermissionsPopup() {
	windowName := tmux.CurrentWindowName()
	logging.Log("permissions-menu: windowName=%q", windowName)

	agentName := tmux.StripAgentPrefix(windowName)
	agentName = tmux.BaseAgentName(agentName)
	logging.Log("permissions-menu: agentName=%q", agentName)
	if agentName == "" || agentName == "shell" {
		fmt.Fprintf(os.Stderr, "Switch to an agent window first\n")
		os.Exit(1)
	}

	// Get the agent's working directory from its tmux pane.
	workDir := tmux.PaneWorkDir(windowName)
	logging.Log("permissions-menu: workDir=%q", workDir)
	if workDir == "" {
		fmt.Fprintf(os.Stderr, "Could not determine agent working directory\n")
		os.Exit(1)
	}

	// Find the project root; outside a git repo, use workDir itself.
	projectRoot, err := worktree.Toplevel(workDir)
	if err != nil {
		projectRoot = workDir
	}
	settingsPath := claude.ProjectSettingsPath(projectRoot)

	// Load current project settings.
	settings, err := claude.LoadSettings(settingsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading settings: %v\n", err)
		os.Exit(1)
	}

	// Load global settings (read-only context).
	home, _ := os.UserHomeDir()
	globalPath := filepath.Join(home, ".claude", "settings.json")
	globalSettings, _ := claude.LoadSettings(globalPath)
	var globalAllow []string
	if globalSettings != nil {
		globalAllow = globalSettings.Permissions.Allow
	}

	model := tui.NewPermissionsModel(agentName, projectRoot, settingsPath, settings.Permissions.Allow, globalAllow)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.PermissionsModel).Result()
	if result.Cancelled {
		return
	}

	// Rebuild the allow list from scratch:
	// 1. Start with enabled preset patterns
	// 2. Add surviving standalone permissions
	// 3. Add covered permissions (granular ones subsumed by presets)
	//
	// This approach is clean — we don't try to diff against the original,
	// we just reconstruct from the TUI's final state.
	var newAllow []string
	seen := make(map[string]bool)

	// Add preset patterns for enabled presets.
	presets := tui.DefaultPresets()
	for _, preset := range presets {
		enabled, ok := result.Presets[preset.Key]
		if !ok || !enabled {
			continue
		}
		for _, pattern := range preset.Patterns {
			if !seen[pattern] {
				newAllow = append(newAllow, pattern)
				seen[pattern] = true
			}
		}
	}

	// Add surviving standalone permissions.
	for _, perm := range result.Standalone {
		if !seen[perm] {
			newAllow = append(newAllow, perm)
			seen[perm] = true
		}
	}

	// Preserve covered permissions (ones subsumed by presets) unless cleanup was requested.
	if !result.CleanedUp {
		for _, perm := range settings.Permissions.Allow {
			if seen[perm] {
				continue
			}
			// Check if this was a "covered" perm — i.e. subsumed by an enabled preset.
			for _, preset := range presets {
				enabled := result.Presets[preset.Key]
				if !enabled {
					continue
				}
				for _, pattern := range preset.Patterns {
					if claude.PermissionCovers(pattern, perm) && !seen[perm] {
						newAllow = append(newAllow, perm)
						seen[perm] = true
					}
				}
			}
		}
	}

	settings.Permissions.Allow = newAllow

	if err := claude.SaveSettings(settingsPath, settings); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving settings: %v\n", err)
		os.Exit(1)
	}
	logging.Log("permissions: saved %s", settingsPath)
}

// CmdToggle toggles a named boolean setting for the given agent and reports
// the new value via a tmux display-message.
func CmdToggle(agentName string, setting string) {
	cfg := config.Load()

	var newVal bool
	var err error
	var label string

	switch setting {
	case "alerts":
		newVal, err = cfg.ToggleAlert(agentName)
		label = "alerts"
	case "auto_improve":
		newVal, err = cfg.ToggleAutoImprove(agentName)
		label = "auto-improve"
	case "cross_context":
		newVal, err = cfg.ToggleCrossContext(agentName)
		label = "cross-context"
	case "auto_accept":
		newVal, err = cfg.ToggleAutoAccept(agentName)
		label = "auto-accept"
	case "work_journal":
		newVal, err = cfg.ToggleWorkJournal(agentName)
		label = "work-journal"
	default:
		tmux.DisplayMessage(fmt.Sprintf("Unknown setting: %s", setting))
		return
	}

	if err != nil {
		tmux.DisplayMessage(fmt.Sprintf("Error: %v", err))
		return
	}
	onOff := "off"
	if newVal {
		onOff = "on"
	}
	tmux.DisplayMessage(fmt.Sprintf("%s: %s %s", agentName, label, onOff))
}

// resolveCurrentAgentName returns the base agent name for the current tmux
// window, or an empty string if the active window is not an agent window.
func resolveCurrentAgentName() string {
	name := tmux.CurrentWindowName()
	if name == "" || name == "shell" {
		return ""
	}
	return tmux.StripAgentPrefix(name)
}
