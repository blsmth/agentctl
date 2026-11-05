package cmd

import (
	"fmt"
	"os"

	"github.com/blsmth/agentctl/internal/signal"
)

// RunSignal is the thin CLI shim for "agentctl signal <done|start|state ...>".
// Business logic lives in internal/signal.
func RunSignal(subcommand, state string) {
	windowID := signal.ResolveWindowID()
	if windowID == "" {
		fmt.Fprintln(os.Stderr, "cannot determine window ID")
		os.Exit(1)
	}
	agentName := signal.ResolveAgentName(windowID)
	if err := signal.Apply(windowID, state, agentName); err != nil {
		fmt.Fprintf(os.Stderr, "signal %s: %v\n", subcommand, err)
		os.Exit(1)
	}
}
