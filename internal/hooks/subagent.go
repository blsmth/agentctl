package hooks

import (
	"os"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// SubagentStart is called by Claude Code's SubagentStart hook (matcher: *).
// Records the subagent by ID so CheckDone can tell the window is still working.
func SubagentStart() {
	if !guardAgentctl() {
		return
	}

	var input struct {
		AgentID   string `json:"agent_id"`
		AgentType string `json:"agent_type"`
	}
	decodeInput(&input) // non-fatal: ignore return value

	windowID := resolveHookWindowID()
	if windowID == "" {
		logging.Log("hook-subagent-start: cannot determine window ID, skipping")
		return
	}

	// Increment subagent count atomically via windowstate.Update (flock-protected).
	var count int
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.StartSubagent(input.AgentID, time.Now())
		count = ws.SubagentCount
	}); err != nil {
		logUpdateErr("hook-subagent-start", err)
	}

	agentName := os.Getenv("AGENTCTL_AGENT_NAME")
	logging.Log("hook-subagent-start: count=%d window=%s agent=%s type=%s id=%s", count, windowID, agentName, input.AgentType, input.AgentID)
}

// SubagentStop is called by Claude Code's SubagentStop hook.
// Removes the subagent by ID. State is left alone: the parent's next Stop,
// after Claude Code delivers the result, is what marks the window idle/done.
func SubagentStop() {
	if !guardAgentctl() {
		return
	}

	var input struct {
		AgentID             string `json:"agent_id"`
		AgentType           string `json:"agent_type"`
		AgentTranscriptPath string `json:"agent_transcript_path"`
	}
	decodeInput(&input) // non-fatal: ignore return value

	windowID := resolveHookWindowID()
	if windowID == "" {
		logging.Log("hook-subagent-stop: cannot determine window ID, skipping")
		return
	}

	// Decrement subagent count atomically via windowstate.Update (flock-protected, floor at 0).
	var count int
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.StopSubagent(input.AgentID, time.Now())
		count = ws.SubagentCount
	}); err != nil {
		logUpdateErr("hook-subagent-stop", err)
	}

	agentName := os.Getenv("AGENTCTL_AGENT_NAME")
	logging.Log("hook-subagent-stop: count=%d window=%s agent=%s type=%s id=%s", count, windowID, agentName, input.AgentType, input.AgentID)
}
