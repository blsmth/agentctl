package cmd

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
)

// CmdHud runs the HUD Bubble Tea app for the given tmux window ID.
// It runs inline (the HUD IS the pane content) — no popup wrapper.
func CmdHud(windowID string) {
	model := tui.NewHudModel(windowID)
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "hud error: %v\n", err)
		os.Exit(1)
	}
}

// CmdChromeBar runs the 3-row bottom chrome bar as a Bubble Tea program.
// It composes the context-metrics line, keybind-hints line, and agent-tabs line
// into a single pane, eliminating the two tmux pane-border lines that would
// otherwise separate three independent panes.
func CmdChromeBar(windowID string) {
	model := tui.NewChromeBarModel(windowID, state.FilePath())
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "chrome-bar error: %v\n", err)
		os.Exit(1)
	}
}

// CmdChromeBarPane creates a 3-row chrome bar pane at the bottom of the given
// window. Called by the after-new-window tmux hook so every new window gets
// the chrome bar automatically.
func CmdChromeBarPane(windowID string) {
	if err := tmux.LaunchChromeBar(windowID); err != nil {
		// Non-fatal: the window may not exist yet or the bar is already
		// present. This runs under tmux's `run-shell -b`, which discards
		// stderr, so it must also be logged via logging.Error to land in the
		// agentctl log at a visible level -- include the resolved session
		// name, window ID, and runtime dir, since a wrong tmux.SessionName
		// (e.g. derived from a runtime dir anchored in a worktree rather than
		// the repo root) is the actual diagnostic when this fails silently.
		msg := fmt.Sprintf("chrome-bar-pane: session=%s window=%s runtime_dir=%s: %v",
			tmux.SessionName, windowID, paths.RuntimeDir(), err)
		fmt.Fprintln(os.Stderr, msg)
		logging.Error("%s", msg)
	}
}

// CmdHudToggle determines the active window ID and toggles the HUD pane.
// windowID may be empty; when empty the current tmux window is used.
func CmdHudToggle(windowID string) {
	if windowID == "" {
		windowID = tmux.CurrentWindowID()
	}
	if windowID == "" {
		tmux.DisplayMessage("No agent window found")
		return
	}
	if err := tmux.ToggleHud(windowID); err != nil {
		tmux.DisplayMessage(fmt.Sprintf("hud toggle error: %v", err))
	}
}
