package cmd

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/cliout"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/selfexec"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
)

// reloadable is a pane model that can ask to be re-exec'd after it quits.
type reloadable interface {
	tea.Model
	Reexec() bool
}

// paneExec is the exec seam for tests (selfexec.Reexec in production).
var paneExec = selfexec.Reexec

// paneCapture is the identity seam for tests.
var paneCapture = selfexec.Capture

// runWithReload runs a long-lived pane program. When the model quits asking
// for a re-exec (binary changed) it execs the new binary only after Run has
// returned, so the terminal is restored first. If the exec cannot happen the
// old image keeps running with hot reload off: exiting would leave the
// window without its pane, and nothing respawns it (windowstate still
// records the dead pane, so LaunchChromeBar would refuse to recreate it).
func runWithReload(name string, model reloadable, run func(tea.Model) (tea.Model, error), disable func(tea.Model) reloadable) {
	for {
		final, err := run(model)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s error: %v\n", name, err)
			cliout.Exit(1)
		}
		rm, ok := final.(reloadable)
		if !ok || !rm.Reexec() {
			return
		}
		if reexecPane(name) {
			return // only reached when paneExec is stubbed
		}
		model = disable(rm)
	}
}

// reexecPane execs the new binary. It returns true only if exec returned
// successfully (test seams); false means the caller should keep running.
func reexecPane(name string) bool {
	exe, err := paneCapture()
	if err != nil {
		logging.Warn("%s: re-exec skipped: %v", name, err)
		return false
	}
	logging.Log("%s: binary changed, re-executing %s", name, exe.Path)
	if err := paneExec(exe.Path); err != nil {
		logging.Warn("%s: re-exec %s failed, keeping old image: %v", name, exe.Path, err)
		return false
	}
	return true
}

func runProgram(m tea.Model) (tea.Model, error) { return tea.NewProgram(m).Run() }

// CmdHud runs the HUD Bubble Tea app for the given tmux window ID.
// It runs inline (the HUD IS the pane content) — no popup wrapper.
func CmdHud(windowID string) {
	runWithReload("hud", tui.NewHudModel(windowID), runProgram, func(m tea.Model) reloadable {
		return m.(tui.HudModel).WithoutReload()
	})
}

// CmdChromeBar runs the 3-row bottom chrome bar as a Bubble Tea program.
// It composes the context-metrics line, keybind-hints line, and agent-tabs line
// into a single pane, eliminating the two tmux pane-border lines that would
// otherwise separate three independent panes.
func CmdChromeBar(windowID string) {
	runWithReload("chrome-bar", tui.NewChromeBarModel(windowID, paths.RuntimeDir()), runProgram, func(m tea.Model) reloadable {
		return m.(tui.ChromeBarModel).WithoutReload()
	})
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
