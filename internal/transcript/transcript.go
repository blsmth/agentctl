// Package transcript normalizes transcript records written by non-Claude
// harnesses into Claude Code's JSONL record shape, so every transcript
// reader (logstream, sessions, cost, the chrome bar) works unchanged on any
// harness's session file. It is a leaf package.
//
// Only pi is handled today. pi session files (docs/session-format.md in the
// pi package, version 3) are JSONL trees of {"type", "id", "parentId",
// "timestamp", ...} entries; a "message" entry wraps an AgentMessage whose
// role is user, assistant, toolResult or system. Claude Code records are
// never of type "message" or "session", so detection is unambiguous.
package transcript

import (
	"strings"
)

// HarnessPi is the harness whose records this package rewrites.
const HarnessPi = "pi"

// AgentEntryType is the pi custom-entry type agentctl's bridge extension
// appends at session start, carrying {"agent": <agent name>}.
const AgentEntryType = "agentctl"

// piPrefix marks pi records that have no Claude Code equivalent: they keep
// their content but no Claude reader matches their type.
const piPrefix = "pi:"

// IsPi reports whether rec is a pi session record.
func IsPi(rec map[string]any) bool {
	t, _ := rec["type"].(string)
	switch t {
	case "session":
		_, ok := rec["version"]
		return ok
	case "message", "model_change", "thinking_level_change", "usage", "compaction",
		"context_edit", "branch_summary", "custom", "custom_message", "label", "session_info":
		_, hasParent := rec["parentId"]
		return hasParent
	}
	return false
}

// Normalize returns rec in Claude Code's shape. Claude records (anything
// that is not a pi record) are returned unchanged.
func Normalize(rec map[string]any) map[string]any {
	if rec == nil || !IsPi(rec) {
		return rec
	}
	t, _ := rec["type"].(string)
	switch t {
	case "message":
		return normalizeMessage(rec)
	case "custom":
		if ct, _ := rec["customType"].(string); ct == AgentEntryType {
			data, _ := rec["data"].(map[string]any)
			if agent, _ := data["agent"].(string); agent != "" {
				return map[string]any{
					"type":         "agent-setting",
					"agentSetting": agent,
					"timestamp":    rec["timestamp"],
				}
			}
		}
	case "usage":
		return normalizeUsageEntry(rec)
	}
	out := copyMap(rec)
	out["type"] = piPrefix + t
	return out
}

func normalizeMessage(rec map[string]any) map[string]any {
	msg, _ := rec["message"].(map[string]any)
	role, _ := msg["role"].(string)
	id, _ := rec["id"].(string)
	base := map[string]any{
		"timestamp": rec["timestamp"],
		"uuid":      id,
	}
	if p, ok := rec["parentId"]; ok {
		base["parentUuid"] = p
	}
	switch role {
	case "assistant":
		base["type"] = "assistant"
		m := map[string]any{
			"id":      id,
			"role":    "assistant",
			"content": assistantContent(msg["content"]),
		}
		if model, _ := msg["model"].(string); model != "" {
			m["model"] = model
		}
		// A request that failed before the model answered (pi records it with
		// all-zero usage) carries no usage, as in Claude Code transcripts, so
		// it does not reset the context fill to zero.
		if u, ok := msg["usage"].(map[string]any); ok && !zeroUsage(u) {
			m["usage"] = claudeUsage(u)
		}
		if sr, _ := msg["stopReason"].(string); sr != "" {
			m["stop_reason"] = sr
		}
		base["message"] = m
		return base
	case "user":
		base["type"] = "user"
		base["message"] = map[string]any{
			"role":    "user",
			"content": userContent(msg["content"]),
		}
		return base
	case "toolResult":
		base["type"] = "user"
		block := map[string]any{
			"type":        "tool_result",
			"tool_use_id": msg["toolCallId"],
			"content":     textOf(msg["content"]),
		}
		if e, ok := msg["isError"].(bool); ok && e {
			block["is_error"] = true
		}
		base["message"] = map[string]any{
			"role":    "user",
			"content": []any{block},
		}
		return base
	}
	out := copyMap(rec)
	out["type"] = piPrefix + "message:" + role
	return out
}

// normalizeUsageEntry turns a pi usage entry (cache warming and other
// model-attributed usage outside the conversation) into an assistant record
// with usage and no content, so it counts toward totals and cost.
func normalizeUsageEntry(rec map[string]any) map[string]any {
	u, _ := rec["usage"].(map[string]any)
	id, _ := rec["id"].(string)
	m := map[string]any{"id": id, "role": "assistant", "content": []any{}}
	if model, _ := rec["model"].(string); model != "" {
		m["model"] = model
	}
	if u != nil {
		m["usage"] = claudeUsage(u)
	}
	return map[string]any{
		"type":      "assistant",
		"timestamp": rec["timestamp"],
		"uuid":      id,
		"message":   m,
	}
}

// claudeUsage maps pi usage (input excludes cache reads and writes, as in
// Anthropic's API) onto Claude Code's usage keys.
func claudeUsage(u map[string]any) map[string]any {
	return map[string]any{
		"input_tokens":                num(u["input"]),
		"output_tokens":               num(u["output"]),
		"cache_read_input_tokens":     num(u["cacheRead"]),
		"cache_creation_input_tokens": num(u["cacheWrite"]),
	}
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

// assistantContent maps pi content blocks (text, thinking, toolCall) onto
// Claude's (text, thinking, tool_use).
func assistantContent(v any) []any {
	blocks, _ := v.([]any)
	out := make([]any, 0, len(blocks))
	for _, raw := range blocks {
		b, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch b["type"] {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": b["text"]})
		case "thinking":
			out = append(out, map[string]any{"type": "thinking", "thinking": b["thinking"]})
		case "toolCall":
			name, _ := b["name"].(string)
			args, _ := b["arguments"].(map[string]any)
			out = append(out, map[string]any{
				"type":  "tool_use",
				"id":    b["id"],
				"name":  ToolName(name),
				"input": ToolInput(name, args),
			})
		}
	}
	return out
}

// userContent turns a pi user message's content (a string or text/image
// blocks) into Claude text blocks.
func userContent(v any) []any {
	switch c := v.(type) {
	case string:
		return []any{map[string]any{"type": "text", "text": c}}
	case []any:
		out := make([]any, 0, len(c))
		for _, raw := range c {
			if b, ok := raw.(map[string]any); ok && b["type"] == "text" {
				out = append(out, map[string]any{"type": "text", "text": b["text"]})
			}
		}
		return out
	}
	return []any{}
}

// textOf joins the text blocks of a pi content array.
func textOf(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, raw := range c {
			if b, ok := raw.(map[string]any); ok && b["type"] == "text" {
				if s, _ := b["text"].(string); s != "" {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// toolNames maps pi's built-in tool names to Claude Code's, which the hook
// handlers, toolinput.Summarize and the session summaries key on.
var toolNames = map[string]string{
	"bash":       "Bash",
	"powershell": "PowerShell",
	"read":       "Read",
	"edit":       "Edit",
	"write":      "Write",
	"grep":       "Grep",
	"find":       "Glob",
	"ls":         "LS",
}

// ToolName returns the Claude Code name for a pi tool; unknown (extension)
// tools keep their own name.
func ToolName(pi string) string {
	if n, ok := toolNames[pi]; ok {
		return n
	}
	return pi
}

// ToolInput returns a pi tool's arguments with Claude Code's field names
// (pi's read/edit/write "path" is Claude's "file_path"). The input map is
// not modified.
func ToolInput(pi string, args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	switch pi {
	case "read", "edit", "write":
		if p, ok := args["path"]; ok {
			if _, has := args["file_path"]; !has {
				out := copyMap(args)
				out["file_path"] = p
				return out
			}
		}
	}
	return args
}

// PiSessionID returns the session ID in a pi session file name
// (<timestamp>_<session id>.jsonl), or "" when name is not one.
func PiSessionID(name string) string {
	name = strings.TrimSuffix(name, ".jsonl")
	i := strings.LastIndex(name, "_")
	if i < 0 || i == len(name)-1 {
		return ""
	}
	return name[i+1:]
}

// LogMatchesSession reports whether a transcript file name belongs to
// sessionID: Claude names it <id>.jsonl, pi <timestamp>_<id>.jsonl.
func LogMatchesSession(base, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	return base == sessionID+".jsonl" || strings.HasSuffix(base, "_"+sessionID+".jsonl")
}

// SessionIDFromLog returns the session ID a transcript file name carries:
// <id>.jsonl for Claude, <timestamp>_<id>.jsonl for pi. Claude session IDs
// are UUIDs and never contain "_".
func SessionIDFromLog(base string) string {
	if id := PiSessionID(base); id != "" && strings.HasSuffix(base, ".jsonl") {
		return id
	}
	return strings.TrimSuffix(base, ".jsonl")
}

// zeroUsage reports whether a pi usage block counts no tokens at all.
func zeroUsage(u map[string]any) bool {
	return num(u["input"])+num(u["output"])+num(u["cacheRead"])+num(u["cacheWrite"]) == 0
}
