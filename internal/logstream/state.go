package logstream

import "strings"

// StateTracker is a Subscriber that tracks the last assistant message's
// content for state detection: tool name, tool detail, and last text block.
// CtxPct is intentionally omitted — context percentage is the responsibility
// of UsageAccumulator (which computes it with model-aware window sizing).
type StateTracker struct {
	ToolName   string // last tool_use block's name (empty if last content was text)
	ToolDetail string // file_path, command, pattern, etc. (truncated to ~80 chars)
	LastText   string // last text block content (truncated to ~100 chars)
	LastType   string // "assistant" or "user" — type of the last entry seen
}

// OnEntry processes a single log entry, updating tool/text state on assistant
// entries. Non-assistant entries only update LastType.
func (s *StateTracker) OnEntry(entry Entry) {
	if entry.Type == "assistant" || entry.Type == "user" {
		s.LastType = entry.Type
	}
	if entry.Type != "assistant" {
		return
	}

	// Scan from the last content block backwards to find the most recent
	// tool_use or text block (mirrors checkLastMessage's reverse-scan logic).
	s.ToolName = ""
	s.ToolDetail = ""
	s.LastText = ""
	for i := len(entry.Content) - 1; i >= 0; i-- {
		block := entry.Content[i]
		switch block.Type {
		case "tool_use":
			s.ToolName = block.ToolName
			s.ToolDetail = extractStateToolDetail(block.ToolName, block.ToolInput)
			return
		case "text":
			s.LastText = truncateState(flattenWhitespace(block.Text), 100)
			return
		}
	}
}

// OnReset zeroes all tracked state. Called by the parent Stream when a new
// log file is opened (e.g. after /clear or a new session file is discovered).
func (s *StateTracker) OnReset() {
	*s = StateTracker{}
}

// extractStateToolDetail returns the most useful display detail for a tool
// call, mirroring the logic previously inline in checkLastMessage.
func extractStateToolDetail(toolName string, input map[string]any) string {
	if input == nil {
		return toolName
	}
	var detail string
	switch toolName {
	case "Bash":
		detail, _ = input["command"].(string)
	case "Edit", "Read", "Write":
		detail, _ = input["file_path"].(string)
	case "Grep":
		detail, _ = input["pattern"].(string)
	default:
		detail = toolName
	}
	return truncateState(flattenWhitespace(detail), 80)
}

// truncateState returns s truncated to at most n bytes (by byte length, not
// rune count — tool details are almost always ASCII).
func truncateState(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// flattenWhitespace collapses any run of whitespace (newlines, tabs, carriage
// returns, repeated spaces) into a single space and trims the ends, so
// multi-line tool input or assistant text can't break the one-row-per-agent
// layout consumers render it into. A string that is only whitespace flattens
// to "", not a lone space. Must run before truncateState — truncating first
// would chop a long multi-line string down to mostly-whitespace before this
// ever collapses it.
func flattenWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
