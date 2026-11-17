package cmd

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// completeDeps are the seams RunDispatchComplete uses, replaced in tests.
type completeDeps struct {
	readState   func(windowID string) (windowstate.WindowState, error)
	paneWorkDir func(windowID string) string
	gitBranch   func(workDir string) string
	loadConfig  func() *config.Config
	run         func(t state.Transition, cfg config.AgentConfig)
}

var defaultCompleteDeps = completeDeps{
	readState:   windowstate.Read,
	paneWorkDir: tmux.PaneWorkDir,
	gitBranch:   gitBranchOf,
	loadConfig:  config.Load,
	run:         dispatch.RunComplete,
}

// RunDispatchComplete is the body of the hidden `agentctl dispatch-complete
// <windowID>` command, spawned detached by the Stop hook (and `signal done`).
// It runs dispatch completion synchronously and never fails the process: a
// missing window is logged and ignored.
func RunDispatchComplete(windowID string) {
	runDispatchComplete(windowID, defaultCompleteDeps)
}

func runDispatchComplete(windowID string, d completeDeps) {
	ws, err := d.readState(windowID)
	if err != nil {
		logging.Warn("dispatch-complete: reading windowstate for %s: %v", windowID, err)
		return
	}
	if ws.Mode == "" && ws.AgentName == "" {
		logging.Log("dispatch-complete: no windowstate for %s (gone?), nothing to do", windowID)
		return
	}
	t := buildCompleteTransition(windowID, ws, d)
	cfg := d.loadConfig().ForAgent(t.Agent)
	d.run(t, cfg)
	logging.Log("dispatch-complete: finished window=%s agent=%s", windowID, t.Agent)
}

// buildCompleteTransition reconstructs the done transition the monitor used to
// build, from the window's windowstate.
func buildCompleteTransition(windowID string, ws windowstate.WindowState, d completeDeps) state.Transition {
	agent := tmux.BaseAgentName(ws.AgentName)

	ticket := 0
	if ws.Ticket != "" {
		ticket, _ = strconv.Atoi(ws.Ticket)
	}

	workDir, branch := ws.WorktreeDir, ws.Branch
	if workDir == "" {
		workDir = d.paneWorkDir(windowID)
	}
	if branch == "" {
		branch = d.gitBranch(workDir)
	}

	return state.Transition{
		Agent:        agent,
		WindowID:     windowID,
		From:         state.StatusWorking,
		To:           state.StatusIdle,
		HookHandled:  true,
		Done:         true,
		Mode:         ws.Mode,
		TicketNumber: ticket,
		Branch:       branch,
		WorkDir:      workDir,
	}
}

// gitBranchOf returns the checked-out branch in workDir, or "".
func gitBranchOf(workDir string) string {
	if workDir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", workDir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
