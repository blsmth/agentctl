package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/reaper"
	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
)

// RunKillMenu shows the Bubble Tea kill confirmation popup for the named agent
// window. nameOrID may be a window ID ("@N") or an agent name; when empty the
// current tmux window is used.
func RunKillMenu(nameOrID string) {
	// nameOrID comes from #{window_id} via the keybinding, or falls back
	// to resolving from the current tmux window.

	// Try snapshot lookup first (fast path, avoids tmux forks).
	var windowID string
	var displayName string
	snap, _ := state.LoadSnapshot()
	if !snap.IsStale(3*time.Second) && strings.HasPrefix(nameOrID, "@") {
		if as := snap.ByWindowID(nameOrID); as != nil {
			windowID = nameOrID
			displayName = as.Name
		}
	}

	// Fallback: resolve via tmux when snapshot is stale or lookup missed.
	if displayName == "" {
		agentName := nameOrID
		if strings.HasPrefix(nameOrID, "@") {
			agentName = tmux.ResolveAgentByWindowID(nameOrID)
		}
		if agentName == "" || agentName == "shell" {
			agentName = resolveCurrentAgent()
		}
		if agentName == "" || agentName == "shell" {
			fmt.Fprintln(os.Stderr, "Switch to an agent window first")
			os.Exit(1)
		}
		displayName = tmux.StripAgentPrefix(agentName)
		if strings.HasPrefix(nameOrID, "@") {
			windowID = nameOrID
		}
	}

	if displayName == "" || displayName == "shell" {
		fmt.Fprintln(os.Stderr, "Switch to an agent window first")
		os.Exit(1)
	}

	model := tui.NewKillModel(displayName)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.KillModel).Result()
	if !result.Confirmed {
		return
	}

	// Kill by window ID if available (unambiguous); fall back to name-based kill.
	if windowID != "" {
		if err := tmux.KillWindow(windowID); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := tmux.KillAgent(displayName); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
}

// RunKill kills the named agent window (non-interactive, for scripting).
func RunKill(name string) {
	if err := tmux.KillAgent(name); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// RunKillAll kills all running agent windows.
func RunKillAll() {
	killed, err := tmux.KillAllAgents()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: some windows failed to close: %v\n", err)
	}
	if killed == 0 {
		fmt.Println("No agent windows to close.")
	} else {
		fmt.Printf("Closed %d agent window(s).\n", killed)
	}
}

// RunKillAllMenu shows the Bubble Tea kill-all confirmation popup.
func RunKillAllMenu() {
	count := len(tmux.RunningAgents())

	model := tui.NewKillAllModel(count)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.KillAllModel).Result()
	if !result.Confirmed {
		return
	}

	killed, err := tmux.KillAllAgents()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: some windows failed to close: %v\n", err)
	}
	if killed == 0 {
		fmt.Println("No agent windows to close.")
	} else {
		fmt.Printf("Closed %d agent window(s).\n", killed)
	}
}

// RunQuit kills the agentctl tmux session (non-interactive, for scripting).
// After the session is destroyed it runs a synchronous reaper sweep to clean up
// any orphaned Claude processes that the monitor (which dies with the session)
// would no longer be able to reap.
func RunQuit() {
	// Capture the tmux server pid before the session is gone: afterwards it
	// can no longer be resolved, and the reaper only kills descendants of it.
	serverPID := reaper.ServerPID(tmux.SessionName)
	if err := tmux.KillSession(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	reaper.ReapOrphansUnderServer(serverPID, paths.SettingsDir())
}

// RunQuitMenu shows the Bubble Tea quit confirmation popup.
func RunQuitMenu() {
	// Collect running instance names for the quit confirmation dialog.
	var agents []string
	snap, _ := state.LoadSnapshot()
	if !snap.IsStale(3 * time.Second) {
		for i := range snap.State.Agents {
			agents = append(agents, snap.State.Agents[i].Name)
		}
	}
	if len(agents) == 0 {
		for _, name := range tmux.RunningAgents() {
			agents = append(agents, name)
		}
	}

	model := tui.NewQuitModel(agents)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.QuitModel).Result()
	if !result.Confirmed {
		return
	}

	// Capture the tmux server pid before the session is gone: afterwards it
	// can no longer be resolved, and the reaper only kills descendants of it.
	serverPID := reaper.ServerPID(tmux.SessionName)
	if err := tmux.KillSession(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	reaper.ReapOrphansUnderServer(serverPID, paths.SettingsDir())
}

// RunRestartMenu shows the Bubble Tea restart confirmation popup.
// On confirm it relaunches the running agentctl binary (no rebuild), then kills
// this session.
func RunRestartMenu() {
	// Collect running instance names for the restart confirmation dialog.
	var agents []string
	snap, _ := state.LoadSnapshot()
	if !snap.IsStale(3 * time.Second) {
		for i := range snap.State.Agents {
			agents = append(agents, snap.State.Agents[i].Name)
		}
	}
	if len(agents) == 0 {
		for _, name := range tmux.RunningAgents() {
			agents = append(agents, name)
		}
	}

	model := tui.NewRestartModel(agents)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.RestartModel).Result()
	if !result.Confirmed {
		return
	}

	// Re-exec the binary that is running right now. Restart never rebuilds:
	// building from "the current Go module" would compile whatever project the
	// user happens to be in. Developers use `just restart` / `just install`.
	exe, err := selfpath.Resolve(os.Executable)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot locate agentctl binary: %v\n", err)
		os.Exit(1)
	}

	scriptPath, err := writeRestartScript(exe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not create restart script: %v\n", err)
		os.Exit(1)
	}

	// nohup detaches the child from this process's controlling terminal and
	// session, so it survives the upcoming kill-session.
	nohupCmd := exec.Command("nohup", scriptPath)
	nohupCmd.Stdout = nil
	nohupCmd.Stderr = nil
	if err := nohupCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Could not launch restart process: %v\n", err)
		os.Exit(1)
	}
	// Release detaches the child from this process so it runs independently.
	_ = nohupCmd.Process.Release()

	// Kill the current tmux session. The detached script will relaunch agentctl,
	// which will restore all agents from the saved state file.
	if err := tmux.KillSession(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// restartScript returns the shell script that waits briefly for the session to
// die and then relaunches exe. The path is single-quoted so spaces and shell
// metacharacters are inert. It never invokes a compiler.
func restartScript(exe string) string {
	return fmt.Sprintf("#!/bin/sh\nsleep 1\nexec %s\n", shellQuote(exe))
}

// shellQuote single-quotes s for POSIX sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeRestartScript writes restartScript(exe) to an executable temp file and
// returns its path. Every filesystem error is reported; a half-written script
// is removed.
func writeRestartScript(exe string) (string, error) {
	f, err := os.CreateTemp("", "agentctl-restart-*.sh")
	if err != nil {
		return "", fmt.Errorf("creating script: %w", err)
	}
	name := f.Name()
	fail := func(err error) (string, error) {
		_ = os.Remove(name)
		return "", err
	}
	if _, err := f.WriteString(restartScript(exe)); err != nil {
		_ = f.Close()
		return fail(fmt.Errorf("writing script: %w", err))
	}
	if err := f.Close(); err != nil {
		return fail(fmt.Errorf("closing script: %w", err))
	}
	if err := os.Chmod(name, 0o755); err != nil {
		return fail(fmt.Errorf("chmod script: %w", err))
	}
	return name, nil
}

// RunRestart kills the current session (non-interactive, for scripting). It
// does not relaunch; use RunRestartMenu for a full restart.
// After the session is destroyed it runs a synchronous reaper sweep to clean up
// any orphaned Claude processes that the monitor would no longer be able to reap.
func RunRestart() {
	// Capture the tmux server pid before the session is gone: afterwards it
	// can no longer be resolved, and the reaper only kills descendants of it.
	serverPID := reaper.ServerPID(tmux.SessionName)
	if err := tmux.KillSession(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	reaper.ReapOrphansUnderServer(serverPID, paths.SettingsDir())
}
