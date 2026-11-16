package cmd

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/herd"
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
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "hub-view error: %v\n", err)
		os.Exit(1)
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
