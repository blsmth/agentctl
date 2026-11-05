// Package toolinput builds a short, human-scannable summary of a Claude Code
// tool call's INPUT -- as opposed to its output/result. The most useful fact
// about a Bash call, for instance, is the command that ran, not whatever
// stdout it produced; that fact only lives in tool_input.
//
// This is a leaf package (no dependency on internal/hooks, internal/audit,
// internal/windowstate, or internal/cmd) so both the PreToolUse notification
// banner (internal/hooks) and the audit trail's DETAIL column
// (internal/cmd) can call it and describe the same tool call identically
// instead of drifting.
package toolinput

import (
	"encoding/json"
	"strings"

	"github.com/blsmth/agentctl/internal/textutil"
)

// detailField maps a tool name to the tool_input field that best describes
// what it's about to do. Table-driven so adding a new tool is a one-line
// change. Task/Agent is handled separately (combines two fields); any tool
// not listed here (and not "Task"/"Agent") summarizes to "".
var detailField = map[string]string{
	"Bash":         "command",
	"Read":         "file_path",
	"Write":        "file_path",
	"Edit":         "file_path",
	"NotebookEdit": "file_path",
	"Glob":         "pattern",
	"Grep":         "pattern",
	"WebFetch":     "url",
	"WebSearch":    "query",
}

// Summarize builds a short, human-scannable detail line describing the tool
// call in input -- e.g. the Bash command about to run, or the file path an
// Edit/Write/Read targets -- capped at maxLen runes. Returns "" when nothing
// useful is available: unknown tool, malformed JSON, or an empty/absent
// relevant field.
//
// toolName is matched against both "Task" and "Agent": older Claude Code
// releases report the subagent-launching tool as "Task" in tool_name, newer
// ones as "Agent". Both name the same tool_input shape (subagent_type plus
// description), so both are summarized identically.
func Summarize(toolName string, input json.RawMessage, maxLen int) string {
	if len(input) == 0 {
		return ""
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(input, &probe); err != nil {
		return ""
	}

	var detail string
	switch {
	case toolName == "Task" || toolName == "Agent":
		detail = taskInputSummary(probe)
	default:
		field, ok := detailField[toolName]
		if !ok {
			return ""
		}
		detail = stringField(probe, field)
		if toolName == "Bash" {
			detail = firstLine(detail)
		}
	}

	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	return textutil.Truncate(detail, maxLen, "")
}

// stringField extracts a string field from a decoded tool_input object,
// returning "" if the field is absent or not a string.
func stringField(probe map[string]json.RawMessage, field string) string {
	raw, ok := probe[field]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// taskInputSummary builds the Task/Agent tool's detail line: "subagent_type:
// description", degrading to whichever of the two is present.
func taskInputSummary(probe map[string]json.RawMessage) string {
	subagentType := stringField(probe, "subagent_type")
	description := stringField(probe, "description")
	switch {
	case subagentType != "" && description != "":
		return subagentType + ": " + description
	case subagentType != "":
		return subagentType
	default:
		return description
	}
}

// firstLine returns the first non-empty trimmed line of s.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
