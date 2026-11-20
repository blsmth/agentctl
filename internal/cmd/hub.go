package cmd

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/cliout"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/selfexec"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
)

// RunHub summons the fleet hub — creating the agentctl-hub session if it
// doesn't already exist — and switches the caller into it.
func RunHub(client string) {
	// Remember where the hub was summoned from: the hub's new-session (N)
	// prompt defaults to it (hub-view's own cwd is pinned to the hub dir).
	if cwd, err := os.Getwd(); err == nil {
		if err := hubRecordOrigin(cwd); err != nil {
			logging.Warn("hub: %v", err)
		}
	}
	if err := hubSwitch(client); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
}

// RunHubView runs the cross-session aggregator model. This is what actually
// executes *inside* the hub session's sole window — mirrors how `agentctl
// tickets --tui` is launched inside its own dedicated window.
func RunHubView() {
	model := tui.NewFleetModel()
	for {
		final, err := tea.NewProgram(model).Run()
		if err != nil {
			hubClearZoom()
			_ = hubKillActivity()
			fmt.Fprintf(os.Stderr, "hub-view error: %v\n", err)
			cliout.Exit(1)
		}
		hm, ok := final.(tui.FleetModel)
		if !ok || !hm.Reexec() {
			// The hub is gone: give every window it zoomed its chrome bar
			// back and close the activity pane. A hot reload keeps both;
			// the new image re-syncs the zoom and reattaches the pane.
			hubClearZoom()
			_ = hubKillActivity()
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

// hubClearZoom undoes the hub's chrome-hiding zooms; a seam for tests.
var hubClearZoom = tmux.ClearHubZoom

// hubKillActivity closes the hub's activity pane; a seam for tests.
var hubKillActivity = tmux.KillHubActivity

// reexecHub replaces the process with the new binary, carrying the selection
// in the environment. It returns only when exec did not happen, i.e. the
// caller should keep running the old process.
func reexecHub(m tui.FleetModel) {
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
	hubRecordOrigin = tmux.RecordHubOrigin
	hubSendKey      = tmux.SendKeyToHubList
	hubLoad         = fleet.Load
	switchHome      = tmux.SwitchClientToSessionWindow
	hubSwitch       = tmux.SwitchToHubFor
	displayMsg      = tmux.DisplayMessage
)

// RunJumpNextBlocked jumps to the first blocked agent across every live
// agentctl session. When the invoking client is inside the hub
// (clientSession == tmux.HubSessionName) it does not switch sessions: it
// sends `b` to the hub list pane so the TUI selects the row, shows it live
// and focuses it, keeping ordering and selection in one place. From any
// other session it switches to the agent's home window. No-op (with a tmux
// status message) when nothing is blocked.
func RunJumpNextBlocked(clientSession, client string) {
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

	row := fleet.FirstBlocked(groups)
	if row == nil {
		displayMsg("no blocked agents")
		return
	}

	if err := switchHome(client, row.Session, row.WindowID); err != nil {
		displayMsg(fmt.Sprintf("jump-next-blocked: %v", err))
	}
}
