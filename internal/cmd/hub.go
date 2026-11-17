package cmd

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/selfexec"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
)

// RunHub summons the herd hub — creating the agentctl-hub session if it
// doesn't already exist — and switches the caller into it.
func RunHub() {
	if err := tmux.SwitchToHub(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// RunHubView runs the cross-session aggregator model. This is what actually
// executes *inside* the hub session's sole window — mirrors how `agentctl
// tickets --tui` is launched inside its own dedicated window.
func RunHubView() {
	model := tui.NewHerdModel()
	for {
		final, err := tea.NewProgram(model).Run()
		if err != nil {
			fmt.Fprintf(os.Stderr, "hub-view error: %v\n", err)
			os.Exit(1)
		}
		hm, ok := final.(tui.HerdModel)
		if !ok || !hm.Reexec() {
			return
		}
		// Hot reload: exec only after Run returned so the terminal is
		// restored (alt screen and mouse off). The nested viewer is a separate
		// tmux client and is left untouched.
		reexecHub(hm)
		// Exec failed: keep running the old image with the same state, and
		// stop watching the binary so we don't spin.
		model = hm.WithoutReload()
	}
}

// hubExec is the exec seam for tests.
var hubExec = selfexec.ReexecEnv

// reexecHub replaces the process with the new binary, carrying the selection
// in the environment. It returns only when exec did not happen, i.e. the
// caller should keep running the old process.
func reexecHub(m tui.HerdModel) {
	exe, err := selfexec.Capture()
	if err != nil {
		logging.Warn("hub: re-exec skipped: %v", err)
		return
	}
	extra := m.ReexecEnv()
	logging.Log("hub: binary changed, re-executing %s", exe.Path)
	if err := hubExec(exe.Path, extra...); err != nil {
		logging.Warn("hub: re-exec %s failed, keeping old image: %v", exe.Path, err)
	}
}

// Seams for tests.
var (
	hubSendKey = tmux.SendKeyToHubList
	hubLoad    = herd.Load
	switchHome = tmux.SwitchToSessionWindow
	displayMsg = tmux.DisplayMessage
)

// RunJumpNextBlocked jumps to the first blocked agent across every live
// agentctl session. When the invoking client is inside the hub
// (clientSession == tmux.HubSessionName) it does not switch sessions: it
// sends `b` to the hub list pane so the TUI selects the row, shows it live
// and focuses it, keeping ordering and selection in one place. From any
// other session it switches to the agent's home window. No-op (with a tmux
// status message) when nothing is blocked.
func RunJumpNextBlocked(clientSession string) {
	if clientSession == tmux.HubSessionName {
		if err := hubSendKey("b"); err != nil {
			displayMsg(fmt.Sprintf("jump-next-blocked: %v", err))
		}
		return
	}

	groups, err := hubLoad()
	if err != nil {
		displayMsg(fmt.Sprintf("jump-next-blocked: %v", err))
		return
	}

	row := herd.FirstBlocked(groups)
	if row == nil {
		displayMsg("no blocked agents")
		return
	}

	if err := switchHome(row.Session, row.WindowID); err != nil {
		displayMsg(fmt.Sprintf("jump-next-blocked: %v", err))
	}
}
