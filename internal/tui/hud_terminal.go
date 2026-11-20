package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/theme"
)

// terminalTailLines is how many output lines a collapsed Bash entry shows.
const terminalTailLines = 8

// maxExpandedOutputLines caps the rows shown for an expanded entry.
const maxExpandedOutputLines = 500

var (
	hudPromptStyle  lipgloss.Style
	hudCmdStyle     lipgloss.Style
	hudOutputStyle  lipgloss.Style
	hudDimStyle     lipgloss.Style
	hudErrOutStyle  lipgloss.Style
	hudRunningStyle lipgloss.Style
)

func initHudTerminalStyles() {
	t := theme.Current()
	hudPromptStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Green))
	hudCmdStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	hudOutputStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))
	hudDimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	hudErrOutStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Red))
	hudRunningStyle = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color(t.Yellow))
}

// wrapCells hard-wraps s to rows of at most width terminal cells, measuring
// with lipgloss (never len). Tabs become two spaces. An empty s yields one
// empty row.
func wrapCells(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	s = strings.ReplaceAll(s, "\t", "  ")
	var rows []string
	var cur strings.Builder
	curW := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if curW+rw > width && curW > 0 {
			rows = append(rows, cur.String())
			cur.Reset()
			curW = 0
		}
		cur.WriteRune(r)
		curW += rw
	}
	return append(rows, cur.String())
}

// truncateCells cuts s to width cells, ending in "…" when shortened.
func truncateCells(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// toolIcon returns the one-line glyph for a non-Bash tool.
func toolIcon(name string) string {
	switch name {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return "✎"
	case "Read":
		return "📖"
	case "Grep", "Glob":
		return "🔍"
	case "Agent", "Task":
		return "🤖"
	}
	return "▶"
}

// renderTerminalEntry renders one log entry as terminal-feed lines (without
// the leading pane margin). expanded shows full output / full detail.
func renderTerminalEntry(e LogEntry, width int, selected, expanded bool) []string {
	if width < 10 {
		width = 10
	}
	mark := func(s string) string {
		if selected {
			return hudSelectedStyle.Render(s)
		}
		return s
	}

	switch {
	case e.Kind == "tool_call" && e.ToolName == "Bash":
		return renderTerminalBash(e, width, selected, expanded)

	case e.Kind == "tool_call":
		name := e.ToolName
		line := toolIcon(name) + " " + name
		if d := extractHudToolDetail(name, e.FullInput); d != "" {
			line = toolIcon(name) + " " + d
			if name != "Edit" && name != "Write" && name != "Read" {
				line = toolIcon(name) + " " + name + " " + d
			}
		}
		line = truncateCells(line, width)
		var out []string
		if selected {
			out = append(out, hudSelectedStyle.Render(line))
		} else {
			out = append(out, hudDimStyle.Render(line))
		}
		if expanded {
			out = append(out, renderExpandedDetail(e, width)...)
		}
		return out

	case e.Kind == "user":
		line := truncateCells("› "+oneLine(e.FullText), width)
		out := []string{mark(e.Style.Render(line))}
		if expanded {
			out = append(out, renderExpandedDetail(e, width)...)
		}
		return out

	default: // assistant text and anything else: one muted line
		text := e.FullText
		if text == "" {
			text = e.Summary
		}
		line := truncateCells(oneLine(text), width)
		out := []string{mark(hudDimStyle.Render(line))}
		if expanded {
			out = append(out, renderExpandedDetail(e, width)...)
		}
		return out
	}
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

func renderTerminalBash(e LogEntry, width int, selected, expanded bool) []string {
	cmd, _ := e.FullInput["command"].(string)
	if cmd == "" {
		cmd, _ = e.FullInput["description"].(string)
	}
	cmd = strings.TrimRight(sanitizeToolOutput(cmd), "\n ")

	prompt := "$ "
	if selected {
		prompt = "▷ "
	}
	var out []string
	first := true
	for _, cl := range strings.Split(cmd, "\n") {
		for _, row := range wrapCells(cl, width-2) {
			if first {
				out = append(out, hudPromptStyle.Render(prompt)+hudCmdStyle.Render(row))
				first = false
			} else {
				out = append(out, "  "+hudCmdStyle.Render(row))
			}
		}
	}
	if first { // empty command
		out = append(out, hudPromptStyle.Render(prompt))
	}

	if !e.HasResult {
		return append(out, "  "+hudRunningStyle.Render("⠋ running…"))
	}

	outStyle := hudOutputStyle
	if e.IsError {
		outStyle = hudErrOutStyle
	}
	body := strings.TrimRight(e.Output, "\n")
	if body == "" {
		if e.IsError {
			out = append(out, "  "+hudErrOutStyle.Render("(error, no output)"))
		}
		return out
	}
	srcLines := strings.Split(body, "\n")
	var rows []string
	hidden := 0
	if expanded {
		for _, l := range srcLines {
			rows = append(rows, wrapCells(l, width-2)...)
			if len(rows) > maxExpandedOutputLines {
				rows = append(rows[:maxExpandedOutputLines], "… (truncated)")
				break
			}
		}
	} else {
		// Wrap only from the end until the tail is full, so the cost is
		// bounded by the tail, not by the total output size.
		for i := len(srcLines) - 1; i >= 0 && len(rows) < terminalTailLines; i-- {
			l := srcLines[i]
			if rs := []rune(l); len(rs) > terminalTailLines*width {
				l = string(rs[len(rs)-terminalTailLines*width:])
			}
			rows = append(wrapCells(l, width-2), rows...)
			hidden = i
		}
		if len(rows) > terminalTailLines {
			rows = rows[len(rows)-terminalTailLines:]
		}
		if hidden > 0 {
			noun := "lines"
			if hidden == 1 {
				noun = "line"
			}
			out = append(out, "  "+hudDimStyle.Render(fmt.Sprintf("… %d more %s", hidden, noun)))
		}
	}
	for _, r := range rows {
		out = append(out, "  "+outStyle.Render(r))
	}
	return out
}
