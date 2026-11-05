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

// RunJumpNextBlocked finds the first blocked agent across every live
// agentctl session — using the same internal/herd ordering the hub view
// itself uses, so "next blocked" always agrees with what the hub would show
// — and switches to it. Works from any session, not just the hub. No-op
// (with a tmux status message) when nothing is blocked.
func RunJumpNextBlocked() {
	groups, err := herd.Load()
	if err != nil {
		tmux.DisplayMessage(fmt.Sprintf("jump-next-blocked: %v", err))
		return
	}

	row := herd.FirstBlocked(groups)
	if row == nil {
		tmux.DisplayMessage("no blocked agents")
		return
	}

	if err := tmux.SwitchToSessionWindow(row.Session, row.WindowID); err != nil {
		tmux.DisplayMessage(fmt.Sprintf("jump-next-blocked: %v", err))
	}
}
