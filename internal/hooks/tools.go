package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/toolinput"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// maxToolDetailLen caps toolinput.Summarize's output for the pending-tool
// notification banner. Separate from truncateSummary's 160-char cap (which
// serves the audit table) -- this feeds a one-line notification banner,
// which wants to stay shorter.
const maxToolDetailLen = 120

// PreTool is called by Claude Code's PreToolUse hook (matcher: *).
// It sets @agentctl_state=working on the agent's tmux window so the monitor
// immediately shows the spinner. This fires before every tool use.
func PreTool() {
	if !guardAgentctl() {
		return
	}

	var input struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	decodeInput(&input) // non-fatal: ignore return value

	windowID := resolveHookWindowID()
	if windowID == "" {
		logging.Log("hook-pre-tool: cannot determine window ID, skipping")
		return
	}

	agentName := os.Getenv("AGENTCTL_AGENT_NAME")
	logging.Log("hook/pre-tool: tool=%s window=%s agent=%s", input.ToolName, windowID, agentName)

	doneTimer := logging.Timing("hook/pre-tool: " + input.ToolName)
	defer doneTimer()

	detail := toolinput.Summarize(input.ToolName, input.ToolInput, maxToolDetailLen)
	pendingAt := time.Now()

	// Write state (plus the pending-tool fields the Notification hook reads
	// to describe a permission prompt) in one Update -- visual updates
	// (window renaming, tab style) are owned by the Bubble Tea monitor view.
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.State = "working"
		ws.PendingTool = input.ToolName
		ws.PendingToolDetail = detail
		ws.PendingToolAt = pendingAt
	}); err != nil {
		logging.Log("hook-pre-tool: windowstate.Update error: %v", err)
	}
	logging.Log("hook-pre-tool: set state=working window=%s agent=%s tool=%s", windowID, agentName, input.ToolName)

	// Auto-accept is now handled via broad permission allows in --settings
	// (see dispatch.WriteAgentSettings). Returning hookSpecificOutput JSON
	// here caused "hook error" on every tool call because Claude Code v2.1.86
	// fails Zod schema validation on the output before checking permissionDecision.

	// Fire user lifecycle hooks (async, non-blocking).
	runUserHooks("pre_tool", map[string]string{
		"AGENTCTL_AGENT_NAME": agentName,
		"AGENTCTL_WINDOW_ID":  windowID,
		"AGENTCTL_TOOL_NAME":  input.ToolName,
	})
}

// PostTool is called by Claude Code's PostToolUse hook (matcher: *).
// It confirms Claude is actively working by refreshing @agentctl_last_tool_time.
// The monitor uses this timestamp to detect stuck states: if working/blocked but
// no tool completed in the last 5s, override to idle.
func PostTool() {
	if !guardAgentctl() {
		return
	}

	// Fields beyond ToolName are verified against a real captured PostToolUse
	// payload (captured from a live PostToolUse hook): session_id, transcript_path,
	// cwd, and permission_mode are documented common fields on every hook
	// (docs/claude-code-hooks.md); tool_input, tool_response, tool_use_id, and
	// duration_ms are PostToolUse-specific and confirmed present.
	var input struct {
		ToolName       string          `json:"tool_name"`
		ToolInput      json.RawMessage `json:"tool_input"`
		ToolResponse   json.RawMessage `json:"tool_response"`
		ToolUseID      string          `json:"tool_use_id"`
		SessionID      string          `json:"session_id"`
		TranscriptPath string          `json:"transcript_path"`
		PromptID       string          `json:"prompt_id"`
		PermissionMode string          `json:"permission_mode"`
		Cwd            string          `json:"cwd"`
		DurationMS     int64           `json:"duration_ms"`
	}
	decodeInput(&input) // non-fatal: ignore return value

	windowID := resolveHookWindowID()
	if windowID == "" {
		logging.Log("hook-post-tool: cannot determine window ID, skipping")
		return
	}

	agentName := os.Getenv("AGENTCTL_AGENT_NAME")
	logging.Log("hook/post-tool: tool=%s window=%s agent=%s", input.ToolName, windowID, agentName)

	doneTimer := logging.Timing("hook/post-tool: " + input.ToolName)
	defer doneTimer()

	nowUnix := time.Now().Unix()
	var ws windowstate.WindowState
	if err := windowstate.Update(windowID, func(w *windowstate.WindowState) {
		w.State = "working"
		w.LastToolTime = nowUnix
		clearPendingToolIfMatch(w, input.ToolName)
		ws = *w
	}); err != nil {
		logging.Log("hook-post-tool: windowstate.Update error: %v", err)
	}
	logging.Log("hook-post-tool: set state=working last_tool_time=%d window=%s agent=%s tool=%s", nowUnix, windowID, agentName, input.ToolName)

	appendAuditRecord(audit.Record{
		Event:          audit.EventTool,
		Tool:           input.ToolName,
		ToolUseID:      input.ToolUseID,
		Session:        input.SessionID,
		PromptID:       input.PromptID,
		Transcript:     input.TranscriptPath,
		Agent:          agentName,
		Instance:       os.Getenv("AGENTCTL_INSTANCE_NAME"),
		Window:         windowID,
		Cwd:            input.Cwd,
		PermissionMode: input.PermissionMode,
		DurationMS:     input.DurationMS,
		OK:             true,
		Summary:        summarizeToolResult(input.ToolName, input.ToolResponse, ""),
		Input:          input.ToolInput,
		Result:         input.ToolResponse,
	})

	// Fire user lifecycle hooks (async, non-blocking).
	runUserHooks("post_tool", map[string]string{
		"AGENTCTL_AGENT_NAME": agentName,
		"AGENTCTL_WINDOW_ID":  windowID,
		"AGENTCTL_TOOL_NAME":  input.ToolName,
	})

	// Deliver any pending mailbox messages as additionalContext. PostToolUse
	// fires on every tool call, making it the primary delivery path for a
	// *working* agent (see docs/agent-messaging-design.md §3).
	deliverMailbox("PostToolUse", windowID, ws.SessionID)
}

// PostToolFailure is called by Claude Code's PostToolUseFailure hook (matcher: *).
// Logs tool failures, records the last error, and sets idle state on user interrupt.
func PostToolFailure() {
	if !guardAgentctl() {
		return
	}

	// tool_name, error, and is_interrupt are the documented/verified
	// PostToolUseFailure-specific fields (docs/claude-code-hooks.md).
	// session_id, transcript_path, cwd, and permission_mode are the common
	// fields present on every hook payload. tool_response, exit_code, and
	// duration_ms are claimed by some docs but UNVERIFIED against a real
	// failure payload on this hook -- they are deliberately not decoded here
	// until confirmed; see the spec for this increment.
	var input struct {
		ToolName       string `json:"tool_name"`
		Error          string `json:"error"`
		IsInterrupt    bool   `json:"is_interrupt"`
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		Cwd            string `json:"cwd"`
		PermissionMode string `json:"permission_mode"`
	}
	decodeInput(&input) // non-fatal: ignore return value

	windowID := resolveHookWindowID()
	if windowID == "" {
		logging.Log("hook-post-tool-failure: cannot determine window ID, skipping")
		return
	}

	agentName := os.Getenv("AGENTCTL_AGENT_NAME")

	// Record last error for diagnostics.
	errSummary := fmt.Sprintf("%s: %s", input.ToolName, input.Error)
	if len(errSummary) > 120 {
		errSummary = errSummary[:120]
	}

	if input.IsInterrupt {
		// User interrupted the tool -- agent is no longer working.
		if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
			ws.LastError = errSummary
			ws.State = "idle"
			clearPendingToolIfMatch(ws, input.ToolName)
		}); err != nil {
			logging.Log("hook-post-tool-failure: windowstate.Update error: %v", err)
		}
		logging.Log("hook-post-tool-failure: interrupt, set state=idle window=%s agent=%s tool=%s", windowID, agentName, input.ToolName)
	} else {
		if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
			ws.LastError = errSummary
			clearPendingToolIfMatch(ws, input.ToolName)
		}); err != nil {
			logging.Log("hook-post-tool-failure: windowstate.Update error: %v", err)
		}
		logging.Log("hook-post-tool-failure: tool failure window=%s agent=%s tool=%s error=%s", windowID, agentName, input.ToolName, input.Error)
	}

	appendAuditRecord(audit.Record{
		Event:          audit.EventToolFailure,
		Tool:           input.ToolName,
		Session:        input.SessionID,
		Transcript:     input.TranscriptPath,
		Agent:          agentName,
		Instance:       os.Getenv("AGENTCTL_INSTANCE_NAME"),
		Window:         windowID,
		Cwd:            input.Cwd,
		PermissionMode: input.PermissionMode,
		OK:             false,
		Error:          input.Error,
		Summary:        errSummary,
	})
}

// clearPendingToolIfMatch clears ws's pending-tool fields when they belong
// to toolName -- the tool call that is completing (PostTool/PostToolFailure).
// Claude Code issues parallel tool calls, so several PreToolUse events can
// land before the first PostToolUse; an unconditional clear here would erase
// a newer pending tool that is still in flight, so this only fires on a name
// match.
func clearPendingToolIfMatch(ws *windowstate.WindowState, toolName string) {
	if ws.PendingTool != toolName {
		return
	}
	ws.PendingTool = ""
	ws.PendingToolDetail = ""
	ws.PendingToolAt = time.Time{}
}

// appendAuditRecord fills in the User and project-attribution fields and
// appends rec to the audit trail. It is best-effort: an audit failure must
// never block or fail a tool call, so errors are logged and swallowed.
func appendAuditRecord(rec audit.Record) {
	rec.User = os.Getenv("USER")
	rec.Project, rec.RepoRoot = auditProjectAttribution()
	if err := audit.Append(rec); err != nil {
		logging.Log("hook: audit.Append error: %v", err)
	}
}

// auditProjectAttribution resolves the audit trail's Project/RepoRoot fields
// from paths.DirName()/paths.ProjectDir(), NOT from any per-record cwd. For a
// worktree dispatch, paths.RuntimeDir() already resolves to the PARENT
// project's .agentctl (see paths.ownsDir), so ProjectDir() yields the true
// project root while a cwd-derived value would report the worktree
// (<repo>/.worktrees/<slug>) -- exactly the case attribution matters most
// for. Both return "" when paths was never initialized; that is a real,
// distinct condition (see audit.Record.Project's doc comment), not something
// to paper over with a placeholder.
//
// Split out from appendAuditRecord so the uninitialized-paths case is
// unit-testable independent of whether audit.Append happens to succeed.
func auditProjectAttribution() (project, repoRoot string) {
	return paths.DirName(), paths.ProjectDir()
}

// summarizeToolResult builds a short, human-scannable one-line description
// of a tool result for the audit table view. It never returns an empty
// string.
func summarizeToolResult(toolName string, resp json.RawMessage, errMsg string) string {
	if errMsg != "" {
		return truncateSummary(errMsg)
	}
	if len(resp) > 0 {
		var probe struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
		}
		if json.Unmarshal(resp, &probe) == nil {
			text := probe.Stdout
			if text == "" {
				text = probe.Stderr
			}
			if line := firstLine(text); line != "" {
				return truncateSummary(line)
			}
		}
	}
	return toolName + " completed"
}

// firstLine returns the first non-empty trimmed line of s.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// truncateSummary caps a summary string to a table-friendly length.
func truncateSummary(s string) string {
	const limit = 160
	if len(s) > limit {
		return s[:limit]
	}
	return s
}
