package cmd

import (
	"errors"

	"github.com/blsmth/agentctl/internal/improve"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/reconcile"
	"github.com/blsmth/agentctl/internal/tmux"
)

var errNoSession = errors.New("no running agentctl session for this project")

// RunReconcile is the body of the hidden `agentctl reconcile`: it runs Gone
// detection for windows that vanished from tmux and, with housekeeping, the
// janitor's periodic sweeps. It runs inline in the calling process because
// every spawner (tmux hook, KillWindow, the janitor) already detaches it, and
// improve.HandleTransitionSync must finish before the process exits. With no
// runtime dir (a hook outside agentctl) it does nothing.
func RunReconcile(housekeeping bool) {
	tmux.EnsureUTF8Locale()
	rd := paths.RuntimeDir()
	if rd == "" {
		return
	}
	reconcile.Run(reconcile.DefaultDeps(rd, improve.HandleTransitionSync))
	if housekeeping {
		reconcile.Housekeep(reconcile.DefaultHousekeepingDeps(rd))
	}
}

// RunEnforceChrome is the body of the hidden `agentctl enforce-chrome`, run by
// the client-resized, client-attached and client-session-changed session
// hooks. It first undoes hub zooms (chrome hidden in the hub) on windows the
// hub no longer views, then re-pins chrome pane sizes.
func RunEnforceChrome() {
	tmux.SweepHubZoom()
	tmux.EnforceAllChromeSizes()
}

// RunApplyConfig is the body of `agentctl apply-config`.
func RunApplyConfig() error {
	if !tmux.SessionExists() {
		return errNoSession
	}
	tmux.ApplyConfigLive()
	return nil
}
