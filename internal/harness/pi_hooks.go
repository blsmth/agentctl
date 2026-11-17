package harness

import (
	"encoding/json"
	"fmt"

	"github.com/blsmth/agentctl/internal/transcript"
)

// piPayload is what the bridge extension (pi/agentctl-pi.ts) writes to a
// hook's stdin. Every field is optional; which ones are set depends on the
// event.
type piPayload struct {
	SessionID   string          `json:"session_id"`
	SessionFile string          `json:"session_file"`
	Cwd         string          `json:"cwd"`
	Reason      string          `json:"reason"`
	Prompt      string          `json:"prompt"`
	ToolName    string          `json:"tool_name"`
	ToolCallID  string          `json:"tool_call_id"`
	Input       json.RawMessage `json:"input"`
	Content     string          `json:"content"`
	IsError     bool            `json:"is_error"`
	LastText    string          `json:"last_assistant_message"`
	StopActive  bool            `json:"stop_hook_active"`
}

// piEndReasons maps pi's session_shutdown reasons onto Claude Code's
// SessionEnd reasons, which hook-session-end acts on: only a terminal
// reason (quit) reclaims the worktree and closes the window.
var piEndReasons = map[string]string{
	"quit":   "prompt_input_exit",
	"new":    "clear",
	"resume": "resume",
	"fork":   "resume",
	"reload": "reload",
}

// DecodeHook turns a bridge payload into the canonical hook payload.
func (piHarness) DecodeHook(hook string, raw []byte) (Event, error) {
	var p piPayload
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return Event{}, fmt.Errorf("pi %s payload: %w", hook, err)
		}
	}
	ev := Event{
		SessionID:      p.SessionID,
		Cwd:            p.Cwd,
		TranscriptPath: p.SessionFile,
	}
	switch hook {
	case "hook-session-end":
		ev.Reason = piEndReasons[p.Reason]
		if ev.Reason == "" {
			ev.Reason = "other"
		}
	case "hook-user-prompt":
		ev.Prompt = p.Prompt
	case "hook-pre-tool", "hook-validate-bash":
		ev.ToolName, ev.ToolInput = piTool(p)
	case "hook-post-tool":
		ev.ToolName, ev.ToolInput = piTool(p)
		ev.ToolResponse = piToolResponse(p)
	case "hook-post-tool-failure":
		ev.ToolName, ev.ToolInput = piTool(p)
		ev.Error = p.Content
	case "hook-check-done":
		ev.LastAssistantMessage = p.LastText
		ev.StopHookActive = p.StopActive
	}
	canon, err := json.Marshal(ev)
	if err != nil {
		return Event{}, fmt.Errorf("pi %s payload: %w", hook, err)
	}
	ev.Raw = canon
	return ev, nil
}

// piTool returns the Claude Code tool name and input for a pi tool call.
func piTool(p piPayload) (string, json.RawMessage) {
	var args map[string]any
	if len(p.Input) > 0 {
		_ = json.Unmarshal(p.Input, &args)
	}
	in, err := json.Marshal(transcript.ToolInput(p.ToolName, args))
	if err != nil {
		in = nil
	}
	return transcript.ToolName(p.ToolName), in
}

// piToolResponse shapes a pi tool result's text like Claude Code's: Bash
// output under "stdout" (what audit summaries read), anything else under
// "output".
func piToolResponse(p piPayload) json.RawMessage {
	key := "output"
	if p.ToolName == "bash" {
		key = "stdout"
	}
	b, err := json.Marshal(map[string]string{key: p.Content})
	if err != nil {
		return nil
	}
	return b
}

// EncodeDecision renders what the bridge reads back: {"block", "reason"}
// for the bash gate and {"context"} for text to inject. pi gets no
// SessionStart orientation (its system prompt already carries it).
func (piHarness) EncodeDecision(d Decision) []byte {
	switch d.Kind {
	case Allow:
		return marshalLine(map[string]any{"block": false})
	case Deny:
		return marshalLine(map[string]any{"block": true, "reason": d.Reason})
	case Context:
		return marshalLine(map[string]string{"context": d.Text})
	}
	return nil
}
