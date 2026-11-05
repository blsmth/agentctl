package hooks

import (
	"os"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// SubagentStart is called by Claude Code's SubagentStart hook (matcher: *).
// Increments @agentctl_subagent_count so the status bar can show "[+N sub]".
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
		ws.SubagentCount++
		count = ws.SubagentCount
	}); err != nil {
		logging.Log("hook-subagent-start: windowstate.Update error: %v", err)
	}

	agentName := os.Getenv("AGENTCTL_AGENT_NAME")
	logging.Log("hook-subagent-start: count=%d window=%s agent=%s type=%s id=%s", count, windowID, agentName, input.AgentType, input.AgentID)
}

// SubagentStop is called by Claude Code's SubagentStop hook.
// Decrements @agentctl_subagent_count when a subagent finishes.
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
		if ws.SubagentCount > 0 {
			ws.SubagentCount--
		}
		count = ws.SubagentCount
	}); err != nil {
		logging.Log("hook-subagent-stop: windowstate.Update error: %v", err)
	}

	agentName := os.Getenv("AGENTCTL_AGENT_NAME")
	logging.Log("hook-subagent-stop: count=%d window=%s agent=%s type=%s id=%s", count, windowID, agentName, input.AgentType, input.AgentID)
}
