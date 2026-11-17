package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/launchopts"
)

// Column widths of an agent row (display cells).
const (
	rowNameW  = 18
	rowBadgeW = 14
	// wideMin is the narrowest client that gets the side-by-side layout.
	wideMin = 100
	// optLabelW is the width of the options panel's label column.
	optLabelW = 10
)

// listRow is one rendered line of the agent list.
type listRow struct {
	text   string
	header bool
	sel    bool
}

// onSel applies the selected-row background to s when sel is set, so the
// highlight survives the style resets between row segments.
func onSel(s lipgloss.Style, sel bool) lipgloss.Style {
	if sel {
		return s.Background(mnSelectedBG)
	}
	return s
}

// badge is the compact backend/model label of an agent row.
func (m MenuModel) badge(a MenuAgent) string {
	if _, ok := m.cfg.Backends[a.Backend]; ok && a.Backend != "" {
		model := m.cfg.Backends[a.Backend].Model
		return strings.TrimSpace(a.Backend + " " + model)
	}
	return a.Model
}

// renderAgentRow renders one agent row, exactly width cells wide. Every
// measurement is in display cells, so multibyte descriptions cannot ragged the
// right edge.
func renderAgentRow(a MenuAgent, badge string, selected bool, width int) string {
	cursor := "  "
	if selected {
		cursor = "▸ "
	}
	dot, dotStyle := "·", mnStoppedStyle
	count := ""
	if n := len(a.Instances); n > 0 {
		dot, dotStyle = "●", mnRunningStyle
		if n > 1 {
			count = fmt.Sprint(n)
		}
	}
	nameStyle := mnNameStyle
	if selected {
		nameStyle = mnNameSelStyle
	}
	const sp = " "
	fixed := 2 + 3 + 1 + rowNameW + 1 + rowBadgeW + 1
	descW := width - fixed
	desc := ""
	if descW >= 4 {
		desc = truncW(strings.Join(strings.Fields(a.Description), " "), descW)
	}
	var b strings.Builder
	b.WriteString(onSel(cursorActiveStyle, selected).Render(cursor))
	b.WriteString(onSel(dotStyle, selected).Render(dot + padW(count, 2)))
	b.WriteString(onSel(lipgloss.NewStyle(), selected).Render(sp))
	b.WriteString(onSel(nameStyle, selected).Render(padW(a.Name, rowNameW)))
	b.WriteString(onSel(lipgloss.NewStyle(), selected).Render(sp))
	b.WriteString(onSel(mnBadgeStyle, selected).Render(padW(badge, rowBadgeW)))
	b.WriteString(onSel(lipgloss.NewStyle(), selected).Render(sp))
	b.WriteString(onSel(mnDescStyle, selected).Render(desc))
	out := b.String()
	if gap := width - ansi.StringWidth(out); gap > 0 {
		out += onSel(lipgloss.NewStyle(), selected).Render(strings.Repeat(" ", gap))
	}
	return out
}

// listRows builds the agent list: section headers, agent rows, create row.
// It returns the rows and the index of the highlighted one.
func (m MenuModel) listRows(width int) ([]listRow, int) {
	var rows []listRow
	cursorRow := 0
	last := MenuSection(-1)
	for i, idx := range m.visible {
		a := m.agents[idx]
		if a.Section != last {
			last = a.Section
			rows = append(rows, listRow{
				text:   mnSectionStyle.Render(a.Section.Label()) + " " + mnRuleStyle.Render(strings.Repeat("─", max(0, width-len(a.Section.Label())-1))),
				header: true,
			})
		}
		sel := i == m.cursor && m.focus == menuFocusList
		if i == m.cursor {
			cursorRow = len(rows)
		}
		rows = append(rows, listRow{text: renderAgentRow(a, m.badge(a), sel, width), sel: i == m.cursor})
	}
	if len(m.visible) == 0 {
		rows = append(rows, listRow{text: mnNoteStyle.Render("no agents match the filter")})
	}
	rows = append(rows, listRow{text: mnRuleStyle.Render(strings.Repeat("─", width)), header: true})
	createSel := m.cursor == len(m.visible)
	if createSel {
		cursorRow = len(rows)
	}
	label := "+ Create New Agent"
	cursor := "  "
	style := mnCreateStyle
	if createSel {
		cursor, style = "▸ ", mnNameSelStyle
	}
	line := onSel(cursorActiveStyle, createSel).Render(cursor) + onSel(style, createSel).Render(padW(label, width-2))
	rows = append(rows, listRow{text: line, sel: createSel})
	return rows, cursorRow
}

// leftLines renders the list pane as exactly height lines of width cells.
func (m MenuModel) leftLines(width, height int) []string {
	var out []string
	if m.filtering || m.filter.Value() != "" {
		prompt := menuHintKeyStyle.Render("/") + " "
		val := m.filter.Value()
		if m.filtering {
			val = m.filter.View()
		}
		out = append(out, truncW(prompt+val, width))
		height--
	}
	rows, cur := m.listRows(width)
	start := 0
	if len(rows) > height {
		scrollTo := cur
		if scrollTo > 0 && rows[scrollTo-1].header {
			scrollTo-- // keep the section header with its first row
		}
		start = max(0, min(scrollTo-height/2, len(rows)-height))
	}
	for i := start; i < len(rows) && i < start+height; i++ {
		out = append(out, rows[i].text)
	}
	return out
}

// field renders one options row: label, value cell, focus styling.
func (m MenuModel) optRow(f optField, label, value string) string {
	focused := m.focus == menuFocusOpts && m.field == f
	ls, vs := mnLabelStyle, mnValueStyle
	arrows := "  "
	if focused {
		ls, vs = mnLabelFocusSty, mnValueFocusSty
		arrows = menuHintKeyStyle.Render("◂ ")
	}
	if m.editing != editNone && m.field == f {
		return ls.Render(padW(label, optLabelW)) + m.edit.View()
	}
	cell := arrows + vs.Render(value)
	if focused && f != fDir {
		cell += menuHintKeyStyle.Render(" ▸")
	}
	return ls.Render(padW(label, optLabelW)) + cell
}

// rightLines renders the options panel and recent sessions as at most height
// lines of at most width cells.
func (m MenuModel) rightLines(width, height int) []string {
	a, ok := m.current()
	if !ok {
		return []string{
			mnHeadingStyle.Render("Create New Agent"),
			"",
			mnNoteStyle.Render("Opens the agent creation wizard."),
			mnNoteStyle.Render("Press enter (or n from anywhere)."),
		}
	}
	var out []string
	head := mnHeadingStyle.Render(a.Name)
	if n := len(a.Instances); n > 0 {
		head += "  " + mnRunningStyle.Render(fmt.Sprintf("● %d running", n))
	}
	out = append(out, head)
	if a.Description != "" {
		wrapped := strings.Split(ansi.Wordwrap(strings.Join(strings.Fields(a.Description), " "), width, ""), "\n")
		if len(wrapped) > 2 {
			wrapped = append(wrapped[:1], truncW(wrapped[1]+"…", width))
		}
		for _, l := range wrapped {
			out = append(out, mnDescStyle.Render(l))
		}
	}
	out = append(out, "")

	indent := strings.Repeat(" ", optLabelW+2)
	valW := width - optLabelW - 4
	o := m.opts

	be := o.backend
	if be == o.defaultBE {
		be += " (default)"
	}
	out = append(out, m.optRow(fBackend, "Backend", truncW(be, valW)))

	choices := m.modelChoices()
	mv := o.model
	for _, c := range choices {
		if c.Value == o.model {
			if c.Default {
				mv += " (default)"
			}
			if c.Note != "" {
				mv += " · " + c.Note
			}
		}
	}
	if len(choices) > 6 {
		mv += fmt.Sprintf("  [%d/%d]", modelIndex(choices, o.model)+1, len(choices))
	}
	out = append(out, m.optRow(fModel, "Model", truncW(mv, valW)))
	out = append(out, indent+mnNoteStyle.Render(truncW(m.priceHint(), width-len(indent))))
	for _, n := range m.modelNotes() {
		out = append(out, indent+truncW(n, width-len(indent)))
	}

	ev := o.effort
	if o.effort == a.Effort || (o.effort == launchopts.Inherit && a.Effort == "") {
		ev += " (default)"
	}
	out = append(out, m.optRow(fEffort, "Effort", ev))
	dv := shortenDir(o.dir)
	if o.dir != a.Dir {
		dv += " (custom)"
	}
	if w := ansi.StringWidth(dv); w > valW {
		dv = "…" + ansi.TruncateLeft(dv, w-valW+1, "")
	}
	out = append(out, m.optRow(fDir, "Dir", dv))
	wt := "on"
	if !o.worktree {
		wt = "off (--no-worktree)"
	}
	out = append(out, m.optRow(fWorktree, "Worktree", wt))
	out = append(out, "")

	out = append(out, m.sessionLines(a, width, height-len(out))...)
	if len(out) > height {
		out = out[:height]
	}
	for i := range out {
		out[i] = truncW(out[i], width)
	}
	return out
}

// modelNotes returns the inline status lines under the model row.
func (m MenuModel) modelNotes() []string {
	var notes []string
	be := m.opts.backend
	if _, ok := m.cfg.Backends[be]; ok && !m.isNous(be) {
		switch st := m.ollama[be]; {
		case st == nil || !st.done:
			notes = append(notes, mnNoteStyle.Render("loading…"))
		case st.err != nil:
			msg := "server unreachable: " + st.err.Error()
			if errors.Is(st.err, launchopts.ErrNoModelList) {
				msg = "server did not return a model list"
			}
			notes = append(notes, mnWarnStyle.Render(msg))
		}
	}
	if w := m.modelWarning(); w != "" {
		notes = append(notes, mnWarnStyle.Render("⚠ "+w))
	}
	if m.errMsg != "" {
		notes = append(notes, mnErrStyle.Render("✗ "+m.errMsg))
	}
	return notes
}

// sessionLines renders the recent-sessions block in at most room lines.
func (m MenuModel) sessionLines(a MenuAgent, width, room int) []string {
	if room < 2 {
		return nil
	}
	title := mnHeadingStyle.Render("Recent sessions")
	if m.focus == menuFocusSessions {
		title += "  " + mnNoteStyle.Render("resumes on the agent's configured backend/model")
	} else {
		title += "  " + menuHintKeyStyle.Render("r") + " " + menuHintLabelStyle.Render("to resume")
	}
	out := []string{title}
	st := m.sessions[a.Name]
	switch {
	case st == nil || !st.done:
		out = append(out, "  "+mnNoteStyle.Render("loading…"))
	case len(st.list) == 0:
		out = append(out, "  "+mnNoteStyle.Render("none in the last 7 days"))
	default:
		now := m.now()
		for i, s := range st.list {
			sel := m.focus == menuFocusSessions && i == m.sessCursor
			cursor := "  "
			if sel {
				cursor = "▸ "
			}
			preview := s.Preview
			style := mnSessionTextStyle(sel)
			if preview == "" {
				preview, style = "(no preview)", mnSessionDimSty
			}
			line := onSel(cursorActiveStyle, sel).Render(cursor) +
				onSel(mnDescStyle, sel).Render(padW(relTime(now.Sub(s.When)), 9)) +
				onSel(style, sel).Render(truncW(strings.Join(strings.Fields(preview), " "), width-11))
			if gap := width - ansi.StringWidth(line); gap > 0 && sel {
				line += onSel(lipgloss.NewStyle(), sel).Render(strings.Repeat(" ", gap))
			}
			out = append(out, line)
		}
	}
	if len(out) > room {
		out = out[:room]
	}
	return out
}

func mnSessionTextStyle(sel bool) lipgloss.Style {
	if sel {
		return mnNameSelStyle
	}
	return mnSessionTextSty
}

// relTime formats an age compactly ("just now", "5m ago", "2h ago", "3d ago").
func relTime(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// hints returns the footer for the current mode.
func (m MenuModel) hints() string {
	var kv []string
	switch {
	case m.filtering:
		kv = []string{"type", "filter", "enter", "keep", "esc", "clear"}
	case m.editing != editNone:
		kv = []string{"enter", "confirm", "esc", "cancel"}
	case m.focus == menuFocusSessions:
		kv = []string{"↑↓", "select", "enter", "resume", "esc", "back"}
	case m.focus == menuFocusOpts:
		kv = []string{"tab", "next field", "←→", "change", "e", "type value", "enter", "launch", "esc", "back"}
	default:
		kv = []string{"↑↓", "navigate", "tab", "options", "/", "filter", "r", "resume", "n", "new", "enter", "launch", "esc", "cancel"}
	}
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, menuHintKeyStyle.Render(kv[i])+" "+menuHintLabelStyle.Render(kv[i+1]))
	}
	return strings.Join(parts, "  "+menuHintDotStyle.Render("·")+"  ")
}

// body lays the panes out: side by side on a wide client, stacked otherwise.
func (m MenuModel) body(width, height int) []string {
	if width >= wideMin {
		lw := width * 60 / 100
		rw := width - lw - 3
		left, right := m.leftLines(lw, height), m.rightLines(rw, height)
		sep := mnRuleStyle.Render(" │ ")
		out := make([]string, height)
		for i := range out {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			out[i] = l + strings.Repeat(" ", max(0, lw-ansi.StringWidth(l))) + sep + r
		}
		return out
	}
	listH := max(5, height*2/5)
	out := m.leftLines(width, listH)
	for len(out) < listH {
		out = append(out, "")
	}
	out = append(out, mnRuleStyle.Render(strings.Repeat("━", width)))
	return append(out, m.rightLines(width, height-listH-1)...)
}

func (m MenuModel) View() tea.View {
	w := max(40, m.width-2)
	h := max(12, m.height)
	title := menuTitleStyle.Width(w).Render("Launch Agent")
	content := " " + title + "\n\n"
	for _, l := range m.body(w, h-4) {
		content += " " + l + "\n"
	}
	content += " " + truncW(m.hints(), w)
	return tea.NewView(content)
}
