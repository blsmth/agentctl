package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/theme"
)

// LogEntry is a single formatted entry shown in the HUD log stream.
type LogEntry struct {
	Timestamp time.Time
	Kind      string         // "tool_call", "text", "user", "system"
	Summary   string         // One-line formatted summary
	FullInput map[string]any // Raw tool input (nil for non-tool entries)
	FullText  string         // Full text content (for text/user entries, untruncated)
	ToolName  string         // e.g. "Bash", "Edit", "Read" (empty for non-tool entries)
	Style     lipgloss.Style
}

// hudLogStyles holds the lipgloss styles used to render log entries.
var (
	hudToolStyle lipgloss.Style
	hudTextStyle lipgloss.Style
	hudUserStyle lipgloss.Style
	hudTimeStyle lipgloss.Style
)

// TailResult holds all data returned by a single TailLog call.
type TailResult struct {
	Entries           []LogEntry
	NewOffset         int64
	InputTokens       int
	OutputTokens      int
	CacheReadTokens   int
	CacheCreateTokens int
	Turns             int
	Model             string
}

// TailLog reads new JSONL entries from the given file starting at fromOffset.
// Returns a TailResult with parsed entries, new file offset, token counts,
// cache token counts, turn count (one per assistant JSONL entry), model name,
// and any scan error.
func TailLog(logPath string, fromOffset int64) (TailResult, error) {
	var result TailResult
	result.NewOffset = fromOffset

	f, err := os.Open(logPath)
	if err != nil {
		return result, err
	}
	defer f.Close()

	// File-shrink detection: if the file is smaller than the last-known
	// offset (rotation or /clear-style truncation), reset to the start of
	// the new file instead of seeking past EOF (which is legal in Go and
	// would silently yield zero lines forever).
	if info, statErr := f.Stat(); statErr == nil && fromOffset > info.Size() {
		fromOffset = 0
	}
	result.NewOffset = fromOffset

	// Seek to last known position.
	if fromOffset > 0 {
		if _, seekErr := f.Seek(fromOffset, 0); seekErr != nil {
			return result, seekErr
		}
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	currentOffset := fromOffset
	for scanner.Scan() {
		line := scanner.Bytes()
		currentOffset += int64(len(line)) + 1 // +1 for newline

		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}

		entryType, _ := raw["type"].(string)
		ts := parseHudTimestamp(raw)

		switch entryType {
		case "assistant":
			msg, _ := raw["message"].(map[string]any)
			if msg == nil {
				continue
			}

			// Each assistant JSONL entry is one turn.
			result.Turns++

			// Extract model name (keep latest).
			if m, ok := msg["model"].(string); ok && m != "" {
				result.Model = m
			}

			// Extract token usage.
			if usage, ok := msg["usage"].(map[string]any); ok {
				inp := logstream.JSONFloat(usage, "input_tokens")
				cacheCreate := logstream.JSONFloat(usage, "cache_creation_input_tokens")
				cacheRead := logstream.JSONFloat(usage, "cache_read_input_tokens")
				out := logstream.JSONFloat(usage, "output_tokens")
				result.InputTokens += int(inp + cacheCreate + cacheRead)
				result.OutputTokens += int(out)
				result.CacheReadTokens += int(cacheRead)
				result.CacheCreateTokens += int(cacheCreate)
			}

			content, _ := msg["content"].([]any)
			for _, block := range content {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				switch b["type"] {
				case "tool_use":
					name, _ := b["name"].(string)
					input, _ := b["input"].(map[string]any)
					detail := extractHudToolDetail(name, input)
					summary := "▶ " + name
					if detail != "" {
						summary += "  " + detail
					}
					result.Entries = append(result.Entries, LogEntry{
						Timestamp: ts,
						Kind:      "tool_call",
						Summary:   summary,
						FullInput: input,
						ToolName:  name,
						Style:     hudToolStyle,
					})
				case "text":
					text, _ := b["text"].(string)
					if text == "" {
						continue
					}
					preview := strings.ReplaceAll(text, "\n", " ")
					if len(preview) > 80 {
						preview = preview[:80] + "…"
					}
					result.Entries = append(result.Entries, LogEntry{
						Timestamp: ts,
						Kind:      "text",
						Summary:   "💬 \"" + preview + "\"",
						FullText:  text,
						Style:     hudTextStyle,
					})
				}
			}

		case "user":
			msg, _ := raw["message"].(map[string]any)
			if msg == nil {
				continue
			}
			content, _ := msg["content"].([]any)
			for _, block := range content {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				if b["type"] == "tool_result" {
					// Skip tool results — they are noise.
					continue
				}
				if b["type"] == "text" {
					text, _ := b["text"].(string)
					if text == "" {
						continue
					}
					preview := strings.ReplaceAll(text, "\n", " ")
					if len(preview) > 80 {
						preview = preview[:80] + "…"
					}
					result.Entries = append(result.Entries, LogEntry{
						Timestamp: ts,
						Kind:      "user",
						Summary:   "👤 \"" + preview + "\"",
						FullText:  text,
						Style:     hudUserStyle,
					})
				}
			}
		}
	}

	result.NewOffset = currentOffset
	return result, scanner.Err()
}

// parseHudTimestamp extracts the timestamp field from a JSONL entry.
func parseHudTimestamp(entry map[string]any) time.Time {
	if ts, ok := entry["timestamp"].(string); ok {
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err == nil {
			return t
		}
	}
	return time.Now()
}

// extractHudToolDetail pulls the most useful display detail from a tool call.
func extractHudToolDetail(toolName string, input map[string]any) string {
	if input == nil {
		return ""
	}
	switch toolName {
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			// Strip newlines, trim.
			cmd = strings.ReplaceAll(cmd, "\n", " ")
			cmd = strings.TrimSpace(cmd)
			if len(cmd) > 60 {
				cmd = cmd[:60] + "…"
			}
			return cmd
		}
		if desc, ok := input["description"].(string); ok {
			return desc
		}
	case "Read", "Write", "Edit":
		if fp, ok := input["file_path"].(string); ok {
			return abbreviateHudPath(fp)
		}
	case "Glob":
		if p, ok := input["pattern"].(string); ok {
			return p
		}
	case "Grep":
		if p, ok := input["pattern"].(string); ok {
			return p
		}
	case "Agent":
		if desc, ok := input["description"].(string); ok {
			if len(desc) > 60 {
				desc = desc[:60] + "…"
			}
			return desc
		}
	}
	return ""
}

// abbreviateHudPath replaces the home directory prefix with ~.
func abbreviateHudPath(p string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(p, home) {
		p = "~" + p[len(home):]
	}
	return p
}

// ── Agentctl Runtime Log (Logs tab) ─────────────────────────────────────────

var (
	hudRuntimeErrorStyle   lipgloss.Style
	hudRuntimeHeaderStyle  lipgloss.Style
	hudRuntimeHookStyle    lipgloss.Style
	hudRuntimeStateStyle   lipgloss.Style
	hudRuntimeDefaultStyle lipgloss.Style
)

// initHudlogStyles initialises HUD log styles from the active theme.
func initHudlogStyles() {
	t := theme.Current()
	hudToolStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Blue))
	hudTextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green))
	hudUserStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Yellow))
	hudTimeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))

	hudRuntimeErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Red))
	hudRuntimeHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Sky))
	hudRuntimeHookStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Overlay))
	hudRuntimeStateStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Yellow))
	hudRuntimeDefaultStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
}

// agentctlLogPath returns the path to the agentctl runtime log file.
// Uses the paths package to resolve .agentctl/logs/agentctl.log.
func agentctlLogPath() (string, error) {
	logDir := paths.LogDir()
	if logDir == "" {
		return "", fmt.Errorf("paths not initialized")
	}
	return filepath.Join(logDir, "agentctl.log"), nil
}

// TailAgentctlLog reads new plain-text lines from the agentctl runtime log
// starting at fromOffset. The caller provides the logPath (either a
// session-specific path or the global agentctl.log fallback).
// Returns styled lines, new offset, and any error.
func TailAgentctlLog(logPath string, fromOffset int64) ([]string, int64, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return nil, fromOffset, err
	}
	defer f.Close()

	// File-shrink detection: rotation (e.g. lumberjack) or a /clear-style
	// truncation replaces the file with a smaller one. Seeking past EOF is
	// legal in Go and returns no error, so without this check the scanner
	// would silently read zero lines forever and the offset would never
	// self-heal. Reset to the start of the new file instead.
	if info, statErr := f.Stat(); statErr == nil && fromOffset > info.Size() {
		fromOffset = 0
	}

	if fromOffset > 0 {
		if _, seekErr := f.Seek(fromOffset, 0); seekErr != nil {
			return nil, fromOffset, seekErr
		}
	}

	var lines []string
	currentOffset := fromOffset
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 512*1024), 4*1024*1024)

	for scanner.Scan() {
		text := scanner.Text()
		currentOffset += int64(len(scanner.Bytes())) + 1 // +1 for newline

		lines = append(lines, colorizeRuntimeLine(text))
	}

	return lines, currentOffset, scanner.Err()
}

// colorizeRuntimeLine applies basic colorization to a single agentctl log line.
func colorizeRuntimeLine(line string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "error"):
		return hudRuntimeErrorStyle.Render(line)
	case strings.HasPrefix(line, "==="):
		return hudRuntimeHeaderStyle.Render(line)
	case strings.Contains(line, "hook-"):
		return hudRuntimeHookStyle.Render(line)
	case strings.Contains(line, "state=working") ||
		strings.Contains(line, "state=idle") ||
		strings.Contains(line, "state=blocked"):
		return hudRuntimeStateStyle.Render(line)
	default:
		return hudRuntimeDefaultStyle.Render(line)
	}
}
