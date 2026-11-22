package demo

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// PaneSpec is everything a fake pane needs, written by WriteWorld.
type PaneSpec struct {
	Window     Window     `json:"window"`
	Transcript Transcript `json:"transcript"`
	Agentctl   string     `json:"agentctl"`
	WorkDir    string     `json:"work_dir"`
	Branch     string     `json:"branch"`
	LogPath    string     `json:"log_path"`
	SessionID  string     `json:"session_id"`
}

// ReadPaneSpec loads a pane spec file.
func ReadPaneSpec(path string) (PaneSpec, error) {
	var spec PaneSpec
	data, err := os.ReadFile(path)
	if err != nil {
		return spec, fmt.Errorf("reading pane spec: %w", err)
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return spec, fmt.Errorf("parsing pane spec: %w", err)
	}
	return spec, nil
}

// Claude Code's dark-theme palette, near enough for screenshots.
const (
	cReset   = "\x1b[0m"
	cBold    = "\x1b[1m"
	cDim     = "\x1b[38;2;153;153;153m"
	cText    = "\x1b[38;2;255;255;255m"
	cClaude  = "\x1b[38;2;215;119;87m"  // orange: welcome box, spinner
	cGreen   = "\x1b[38;2;78;186;101m"  // tool succeeded
	cRed     = "\x1b[38;2;255;107;128m" // tool failed
	cPerm    = "\x1b[38;2;177;185;249m" // permission dialog
	cBorder  = "\x1b[38;2;136;136;136m"
	cAuto    = "\x1b[38;2;175;135;255m"
	bgAdd    = "\x1b[48;2;34;92;43m"
	bgDel    = "\x1b[48;2;122;41;54m"
	bgPrompt = "\x1b[48;2;55;55;55m"
	cInverse = "\x1b[7m"
)

// ccVersion and ccPlan are what the banner claims. They track the Claude
// Code release the screenshots were last compared against (just
// demo-pane-check).
const (
	ccVersion = "2.1.328"
	ccPlan    = "Claude Max"
)

// Render draws the pane as Claude Code v2.1 would at width columns. statusline
// is what `agentctl statusline` printed for this window ("" when it was not
// run); extra holds lines appended by the approval round-trip.
func Render(spec PaneSpec, width int, statusline string, extra []string) string {
	if width < 40 {
		width = 40
	}
	boxW := min(width-2, 110)
	w := spec.Window
	var out []string
	out = append(out, banner(spec)...)
	out = append(out, "")
	out = append(out, renderEntries(spec.Transcript.Entries, width)...)
	out = append(out, extra...)
	if len(extra) > 0 {
		out = append(out, "")
	}
	switch w.State {
	case "blocked":
		out = append(out, permissionBox(w, boxW)...)
		return strings.Join(out, "\r\n")
	case "working":
		spin := w.Spinner
		if spin == "" {
			spin = "Working… (esc to interrupt)"
		}
		out = append(out, cClaude+"✻ "+spin+cReset, "")
	default:
		if len(spec.Transcript.Entries) > 0 {
			out = append(out, turnFooter(w), "")
		}
	}
	out = append(out, inputArea(w, width)...)
	if statusline != "" {
		for _, l := range strings.Split(strings.TrimRight(statusline, "\n"), "\n") {
			out = append(out, "  "+l+cReset)
		}
	}
	return strings.Join(out, "\r\n")
}

// turnFooter is the line Claude Code leaves after a finished turn.
func turnFooter(w Window) string {
	done := time.Now().Add(-w.SinceDuration()).Format("3:04 PM")
	return cClaude + "✻ " + cDim + "Brewed for 49s · done " + done + cReset
}

// inputArea is the prompt between two full-width rules, then the mode line.
func inputArea(w Window, width int) []string {
	rule := cBorder + strings.Repeat("─", width) + cReset
	out := []string{rule, cText + "❯ " + cInverse + " " + cReset, rule}
	if w.Mode == "bare" {
		return append(out, cDim+"  ? for shortcuts"+cReset)
	}
	return append(out, cAuto+"  ⏵⏵ auto mode on"+cDim+" (shift+tab to cycle) · esc to interrupt"+cReset)
}

// ModelLabel is the model name the banner shows: "claude-opus-5-5" →
// "Opus 5.5".
func ModelLabel(id string) string {
	parts := strings.Split(strings.TrimPrefix(id, "claude-"), "-")
	if len(parts) == 0 || parts[0] == "" {
		return "Opus 5.5"
	}
	name := strings.ToUpper(parts[0][:1]) + parts[0][1:]
	var ver []string
	for _, p := range parts[1:] {
		if len(p) > 2 || strings.Trim(p, "0123456789") != "" {
			break
		}
		ver = append(ver, p)
	}
	if len(ver) == 0 {
		return name
	}
	return name + " " + strings.Join(ver, ".")
}

func banner(spec PaneSpec) []string {
	cwd := spec.WorkDir
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(cwd, home) {
		cwd = "~" + strings.TrimPrefix(cwd, home)
	}
	return []string{
		cClaude + " ▐▛███▜▌" + cReset + "   " + cBold + "Claude Code" + cReset + cDim + " v" + ccVersion + cReset,
		cClaude + "▝▜█████▛▘" + cReset + "  " + cDim + ModelLabel(spec.Window.Model) + " · " + ccPlan + cReset,
		cClaude + "  ▘▘ ▝▝" + cReset + "    " + cDim + cwd + cReset,
	}
}

// renderEntries draws the transcript, one blank line between items, with runs
// of read-only tool calls collapsed to one summary line the way Claude Code
// does.
func renderEntries(entries []Entry, width int) []string {
	var out []string
	for i := 0; i < len(entries); {
		if n := collapsibleRun(entries[i:]); n >= 2 {
			out = append(out, collapsedSummary(entries[i:i+n]), "")
			i += n
			continue
		}
		out = append(out, renderEntry(entries[i], width)...)
		out = append(out, "")
		i++
	}
	return out
}

// readOnlyBash are command prefixes that count as inspection, so a run of
// them collapses with reads.
var readOnlyBash = []string{"ls", "cat ", "git log", "git diff", "git status", "gh pr diff", "gh pr view", "grep ", "rg "}

func collapsible(e Entry) bool {
	if e.Tool == "" || e.Error || e.Running || e.Diff != "" {
		return false
	}
	switch e.Tool {
	case "Read", "Grep", "Glob", "LS":
		return true
	case "Bash":
		cmd := toolDetail(e)
		for _, p := range readOnlyBash {
			if strings.HasPrefix(cmd, p) {
				return true
			}
		}
	}
	return false
}

// collapsibleRun is how many entries from the start of es collapse together.
func collapsibleRun(es []Entry) int {
	n := 0
	for n < len(es) && collapsible(es[n]) {
		n++
	}
	return n
}

func collapsedSummary(es []Entry) string {
	var read, search, list, ran int
	for _, e := range es {
		switch e.Tool {
		case "Read":
			read++
		case "Grep", "Glob":
			search++
		case "LS":
			list++
		default:
			ran++
		}
	}
	var parts []string
	add := func(n int, verb, one, many string) {
		if n == 0 {
			return
		}
		noun := many
		if n == 1 {
			noun = one
		}
		parts = append(parts, fmt.Sprintf("%s %d %s", verb, n, noun))
	}
	add(read, "read", "file", "files")
	add(search, "searched for", "pattern", "patterns")
	add(list, "listed", "directory", "directories")
	add(ran, "ran", "shell command", "shell commands")
	text := strings.Join(parts, ", ")
	return "  " + cDim + strings.ToUpper(text[:1]) + text[1:] + cReset
}

func permissionBox(w Window, width int) []string {
	p := w.Permission
	if p == nil {
		p = &Permission{Title: "Bash command", Command: w.PendingDetail}
		if w.PendingTool != "Bash" {
			p.Title = w.PendingTool
		}
	}
	opt2 := p.Option2
	if opt2 == "" {
		opt2 = "Yes, and don't ask again for this command"
	}
	lines := []string{cBold + cPerm + p.Title + cReset, ""}
	for _, l := range wrap(p.Command, width-8) {
		lines = append(lines, "  "+cText+l+cReset)
	}
	if p.Description != "" {
		lines = append(lines, "  "+cDim+p.Description+cReset)
	}
	lines = append(lines, "",
		"Do you want to proceed?",
		cPerm+"❯ 1. Yes"+cReset,
		"  2. "+opt2,
		"  3. No, and tell Claude what to do differently "+cDim+"(esc)"+cReset)
	return box(cPerm, width, lines)
}

// box draws lines inside a rounded border width columns wide.
func box(color string, width int, lines []string) []string {
	inner := width - 4
	out := []string{color + "╭" + strings.Repeat("─", width-2) + "╮" + cReset}
	for _, l := range lines {
		l = ansi.Truncate(l, inner, "…")
		pad := inner - ansi.StringWidth(l)
		out = append(out, color+"│"+cReset+" "+l+strings.Repeat(" ", max(pad, 0))+" "+color+"│"+cReset)
	}
	return append(out, color+"╰"+strings.Repeat("─", width-2)+"╯"+cReset)
}

func renderEntry(e Entry, width int) []string {
	switch {
	case e.User != "":
		var out []string
		for i, l := range wrap(e.User, width-4) {
			lead := "  "
			if i == 0 {
				lead = "❯ "
			}
			text := lead + l
			pad := strings.Repeat(" ", max(width-ansi.StringWidth(text), 0))
			out = append(out, bgPrompt+cText+text+pad+cReset)
		}
		return out
	case e.Say != "":
		var out []string
		for i, l := range wrap(e.Say, width-4) {
			lead := "  "
			if i == 0 {
				lead = cText + "⏺" + cReset + " "
			}
			out = append(out, lead+l)
		}
		return out
	}
	dot := cGreen
	if e.Error {
		dot = cRed
	}
	if e.Running {
		dot = cDim
	}
	head := dot + "⏺" + cReset + " " + cBold + displayTool(e.Tool) + cReset
	if d := toolDetail(e); d != "" {
		head += "(" + ansi.Truncate(d, width-len(e.Tool)-6, "…") + ")"
	}
	out := []string{head}
	result := strings.ReplaceAll(strings.TrimRight(e.Result, "\n"), "\t", "    ")
	if e.Running {
		result = "Running…"
	}
	if e.Diff != "" && result == "" {
		result = diffSummary(e)
	}
	for i, l := range firstLines(result, 4) {
		lead := "     "
		if i == 0 {
			lead = "  " + cDim + "⎿" + cReset + "  "
		}
		color := cDim
		if e.Error {
			color = cRed
		}
		out = append(out, lead+color+ansi.Truncate(l, width-8, "…")+cReset)
	}
	if e.Diff != "" {
		out = append(out, renderDiff(e.Diff, width)...)
	}
	return out
}

// displayTool is the name Claude Code shows: edits appear as Update.
func displayTool(name string) string {
	switch name {
	case "Edit", "MultiEdit":
		return "Update"
	}
	return name
}

func diffSummary(e Entry) string {
	add, del := 0, 0
	for _, l := range strings.Split(e.Diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+"):
			add++
		case strings.HasPrefix(l, "-"):
			del++
		}
	}
	return fmt.Sprintf("Updated %s with %d additions and %d removals", toolDetail(e), add, del)
}

func renderDiff(diff string, width int) []string {
	var out []string
	n := 40
	for _, l := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		l = strings.ReplaceAll(l, "\t", "    ")
		mark, body := " ", l
		if l != "" {
			mark, body = l[:1], l[1:]
		}
		bg := ""
		switch mark {
		case "+":
			bg = bgAdd
		case "-":
			bg = bgDel
		}
		num := fmt.Sprintf("%4d", n)
		if mark != "-" {
			n++
		}
		text := ansi.Truncate(fmt.Sprintf("%s %s %s", num, mark, body), width-8, "…")
		pad := width - 8 - ansi.StringWidth(text)
		if bg == "" {
			out = append(out, "       "+cDim+text+cReset)
			continue
		}
		out = append(out, "       "+bg+cText+text+strings.Repeat(" ", max(pad, 0))+cReset)
	}
	return out
}

func firstLines(s string, n int) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		more := len(lines) - n
		lines = append(lines[:n], fmt.Sprintf("… +%d lines (ctrl+r to expand)", more))
	}
	return lines
}

// wrap breaks s into lines of at most width cells at spaces.
func wrap(s string, width int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case ansi.StringWidth(line)+1+ansi.StringWidth(word) > width:
				out = append(out, line)
				line = word
			default:
				line += " " + word
			}
		}
		out = append(out, line)
	}
	return out
}
