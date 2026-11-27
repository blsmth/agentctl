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
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/tokens"
)

// Layout constants (display cells).
const (
	maxNameW = 24
	statusW  = 10
	// optLabelW is the width of the options sheet's label column.
	optLabelW = 10
	// Below these content widths the status and model columns are dropped.
	noStatusW = 60
	noModelW  = 50
	// Below these popup sizes the popup renders a single message.
	minPopupW = 40
	minPopupH = 10
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

// modelLabel is the effective model of an agent as shown in the list: the
// model alias ("inherit" when none), backend-qualified for non-Anthropic
// backends ("local · qwen3-coder").
func (m MenuModel) modelLabel(a MenuAgent) string {
	if _, ok := m.cfg.Backends[a.Backend]; ok && a.Backend != "" {
		if model := m.cfg.Backends[a.Backend].Model; model != "" {
			return a.Backend + " · " + model
		}
		return a.Backend
	}
	if a.Model != "" {
		if strings.HasPrefix(a.Model, "claude-") {
			if l := tokens.Lookup(a.Model); l.Known && l.Source != "family" && l.Label != "" {
				return l.Label
			}
		}
		return a.Model
	}
	return launchopts.Inherit
}

// isFree reports whether the agent runs on a backend that costs nothing.
func (m MenuModel) isFree(a MenuAgent) bool {
	if _, ok := m.cfg.Backends[a.Backend]; !ok || a.Backend == "" {
		return false
	}
	return sessions.ClassifyBilling(m.cfg.Backends[a.Backend].Model, a.Backend, m.cfg.Backends) == sessions.BillingFree
}

// renderAgentRow renders one agent row, exactly width cells wide. Every
// measurement is in display cells, so multibyte names cannot ragged the right
// edge. Narrow widths drop the status column (the running count moves next to
// the name) and then the model column.
func renderAgentRow(a MenuAgent, label string, free bool, nameW int, selected bool, width int) string {
	bar := "  "
	if selected {
		bar = "▌ "
	}
	nameStyle := mnNameStyle
	if selected {
		nameStyle = mnNameSelStyle
	}
	n := len(a.Instances)
	showStatus := width >= noStatusW
	showModel := width >= noModelW

	// Without a status column the running count rides after the name, in
	// three cells reserved on every row so the model column stays aligned.
	const suffixW = 3
	nameCol := nameW
	if !showStatus {
		nameCol += suffixW
	}
	if !showModel {
		nameCol = width - 2
	}
	name := padW(a.Name, nameCol)
	suffix := ""
	if n > 0 && !showStatus {
		suffix = fmt.Sprintf(" ×%d", n)
		name = padW(a.Name, nameCol-ansi.StringWidth(suffix))
		if !showModel {
			name = truncW(a.Name, nameCol-ansi.StringWidth(suffix))
		}
	}
	sp := onSel(lipgloss.NewStyle(), selected).Render(" ")
	var b strings.Builder
	b.WriteString(onSel(cursorActiveStyle, selected).Render(bar))
	b.WriteString(onSel(nameStyle, selected).Render(name))
	if suffix != "" {
		b.WriteString(onSel(mnRunningStyle, selected).Render(suffix))
	}
	if showModel {
		right := 0
		if showStatus {
			right = statusW + 1
		}
		modelW := max(0, width-2-nameCol-1-right)
		b.WriteString(sp)
		b.WriteString(onSel(mnBadgeStyle, selected).Render(padW(label, modelW)))
		if showStatus {
			b.WriteString(sp)
			text, style := "", mnDescStyle
			switch {
			case n > 0:
				text, style = fmt.Sprintf("%d running", n), mnRunningStyle
			case free:
				text = "free"
			}
			b.WriteString(onSel(style, selected).Render(padLeftW(text, statusW)))
		}
	}
	out := b.String()
	if w := ansi.StringWidth(out); w > width {
		out = ansi.Truncate(out, width, "")
	} else if gap := width - w; gap > 0 {
		out += onSel(lipgloss.NewStyle(), selected).Render(strings.Repeat(" ", gap))
	}
	return out
}

// sectionRule renders a section header line of exactly width cells.
func sectionRule(label string, width int) string {
	label = truncW(strings.ToUpper(label), width)
	head := mnSectionStyle.Render(label)
	rest := width - lipgloss.Width(head) - 1
	if rest <= 0 {
		return head
	}
	return head + " " + mnRuleStyle.Render(strings.Repeat("─", rest))
}

// listRows builds the agent list: (section headers,) agent rows, create row.
// It returns the rows and the index of the highlighted one. Section headers
// are hidden while a filter is active, since results are ranked, not grouped.
func (m MenuModel) listRows(width int) ([]listRow, int) {
	var rows []listRow
	cursorRow := 0
	last := MenuSection(-1)
	filtering := strings.TrimSpace(m.filter.Value()) != ""
	for i, idx := range m.visible {
		a := m.agents[idx]
		if !filtering && a.Section != last {
			last = a.Section
			rows = append(rows, listRow{text: sectionRule(m.sectionLabel(a.Section), width), header: true})
		}
		sel := i == m.cursor
		if sel {
			cursorRow = len(rows)
		}
		label := m.modelLabel(a)
		if label == launchopts.Inherit {
			label = "" // only a pinned model is worth a column cell
		}
		rows = append(rows, listRow{text: renderAgentRow(a, label, m.isFree(a), m.nameW, sel, width), sel: sel})
	}
	createSel := m.cursor == len(m.visible)
	if createSel {
		cursorRow = len(rows)
	}
	bar, style := "  ", mnCreateStyle
	if createSel {
		bar, style = "▌ ", mnNameSelStyle
	}
	line := onSel(cursorActiveStyle, createSel).Render(bar) + onSel(style, createSel).Render(padW("+ new agent…", width-2))
	rows = append(rows, listRow{text: line, sel: createSel})
	return rows, cursorRow
}

// listLines renders the list as exactly height lines of width cells.
func (m MenuModel) listLines(width, height int) []string {
	rows, cur := m.listRows(width)
	start := 0
	if len(rows) > height {
		scrollTo := cur
		if scrollTo > 0 && rows[scrollTo-1].header {
			scrollTo-- // keep the section header with its first row
		}
		start = max(0, min(scrollTo-height/2, len(rows)-height))
	}
	var out []string
	for i := start; i < len(rows) && i < start+height; i++ {
		out = append(out, rows[i].text)
	}
	return fitLines(out, height)
}

// joinLR lays left and right out on one line of width cells; left is
// truncated to make room for right.
func joinLR(left, right string, width int) string {
	rw := ansi.StringWidth(right)
	if rw >= width {
		return truncW(right, width)
	}
	lw := width - rw - 1
	left = truncW(left, lw)
	return left + strings.Repeat(" ", width-ansi.StringWidth(left)-rw) + right
}

// header renders the title line.
func (m MenuModel) header(title, right string, width int) string {
	return joinLR(mnHeadingStyle.Render(title), mnDescStyle.Render(right), width)
}

// countSummary is the "14 agents · 8 running" header text.
func (m MenuModel) countSummary() string {
	running := 0
	for _, a := range m.agents {
		running += len(a.Instances)
	}
	return fmt.Sprintf("%d agents · %d running", len(m.agents), running)
}

// filterLine renders the filter input row.
func (m MenuModel) filterLine(width int) string {
	left := menuHintKeyStyle.Render("> ") + m.filter.View()
	right := ""
	if m.filter.Value() == "" {
		right = mnNoteStyle.Render("type to filter")
	}
	return joinLR(left, right, width)
}

// wrapDesc wraps a description to at most two lines of width cells, ending the
// second in an ellipsis when it was cut.
func wrapDesc(desc string, width int) []string {
	desc = strings.Join(strings.Fields(desc), " ")
	if desc == "" || width < 4 {
		return nil
	}
	lines := strings.Split(ansi.Wordwrap(desc, width, ""), "\n")
	if len(lines) > 2 {
		lines = []string{lines[0], truncW(lines[1]+"…", width)}
	}
	return lines
}

// runsSummary is the "runs" line: what a plain launch of a would use.
func (m MenuModel) runsSummary(a MenuAgent) string {
	o := m.newAgentOpts(a)
	class, _ := m.billingFor(o.backend, o.model)
	model := o.model
	if model == "" {
		model = launchopts.Inherit
	}
	be := o.backend
	if class.String() != "" {
		be += " (" + class.String() + ")"
	}
	wt := "worktree"
	if !o.worktree {
		wt = "no worktree"
	}
	return strings.Join([]string{model, be, "effort " + o.effort, wt}, " · ")
}

// lastSession is the "last" line of the detail strip.
func (m MenuModel) lastSession(a MenuAgent, width int) string {
	label := mnLabelStyle.Render(padW("last", 6))
	st := m.sessions[a.Name]
	switch {
	case st == nil || !st.done:
		if m.cfg.LoadSessions == nil {
			return label + mnNoteStyle.Render("no recent sessions")
		}
		return label + mnNoteStyle.Render("loading…")
	case len(st.list) == 0:
		return label + mnNoteStyle.Render("no recent sessions")
	}
	s := st.list[0]
	preview := strings.Join(strings.Fields(s.Preview), " ")
	text := mnDescStyle.Render(relTime(m.now().Sub(s.When)))
	if preview != "" {
		text += "  " + mnSessionTextSty.Render("\""+preview+"\"")
	}
	right := ""
	if n := len(st.list) - 1; n > 0 {
		right = menuHintKeyStyle.Render("ctrl+r") + "  " + menuHintLabelStyle.Render(fmt.Sprintf("%d more", n))
	} else {
		right = menuHintKeyStyle.Render("ctrl+r")
	}
	return joinLR(label+text, right, width)
}

// detailLines renders the detail strip as exactly n lines (n is 5, 2 or 0).
func (m MenuModel) detailLines(width, n int) []string {
	if n <= 0 {
		return nil
	}
	a, ok := m.current()
	if !ok {
		lines := []string{
			mnHeadingStyle.Render("+ new agent…"),
			mnDescStyle.Render(truncW("Opens the agent creation wizard.", width)),
		}
		return fitLines(lines, n)
	}
	indent := min(ansi.StringWidth(a.Name)+2, width/3)
	desc := wrapDesc(a.Description, width-indent)
	pad := strings.Repeat(" ", indent)
	line := func(i int) string {
		if i < len(desc) {
			return pad + mnDescStyle.Render(desc[i])
		}
		return ""
	}
	var first string
	if len(desc) > 0 {
		first = mnHeadingStyle.Render(padW(a.Name, indent-2)) + "  " + mnDescStyle.Render(desc[0])
	} else {
		first = mnHeadingStyle.Render(truncW(a.Name, width))
	}
	runs := mnLabelStyle.Render(padW("runs", 6)) + mnValueStyle.Render(truncW(m.runsSummary(a), width-6))
	if n < 5 {
		return fitLines([]string{first, truncW(runs, width)}, n)
	}
	dir := shortenDir(m.newAgentOpts(a).dir)
	if w := ansi.StringWidth(dir); w > width-6 {
		dir = "…" + ansi.TruncateLeft(dir, w-(width-6)+1, "")
	}
	return []string{
		first,
		line(1),
		runs,
		strings.Repeat(" ", 6) + mnDescStyle.Render(dir),
		m.lastSession(a, width),
	}
}

// sessionLines renders the recent-sessions block (resume view) as exactly n lines.
func (m MenuModel) sessionLines(a MenuAgent, width, n int) []string {
	out := []string{mnHeadingStyle.Render("Resume "+a.Name) + "  " + mnNoteStyle.Render("on the agent's configured backend/model")}
	st := m.sessions[a.Name]
	switch {
	case st == nil || !st.done:
		out = append(out, "  "+mnNoteStyle.Render("loading…"))
	case len(st.list) == 0:
		out = append(out, "  "+mnNoteStyle.Render("none in the last 7 days"))
	default:
		now := m.now()
		for i, s := range st.list {
			sel := i == m.sessCursor
			bar := "  "
			if sel {
				bar = "▌ "
			}
			preview := s.Preview
			style := mnSessionTextStyle(sel)
			if preview == "" {
				preview, style = "(no preview)", mnSessionDimSty
			}
			line := onSel(cursorActiveStyle, sel).Render(bar) +
				onSel(mnDescStyle, sel).Render(padW(relTime(now.Sub(s.When)), 9)) +
				onSel(style, sel).Render(truncW(strings.Join(strings.Fields(preview), " "), width-11))
			if gap := width - ansi.StringWidth(line); gap > 0 && sel {
				line += onSel(lipgloss.NewStyle(), sel).Render(strings.Repeat(" ", gap))
			}
			out = append(out, line)
		}
	}
	return fitLines(out, n)
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

// ---- options sheet ----

// sheetRow renders one options row: cursor bar, label, changed marker, value
// and a right-aligned hint.
func (m MenuModel) sheetRow(f optField, label, value, hint string, changed bool, width int) string {
	focused := m.view == viewSheet && m.field == f
	ls := mnLabelStyle
	bar := "  "
	if focused {
		ls, bar = mnLabelFocusSty, "▌ "
	}
	mark := "  "
	if changed {
		mark = mnWarnStyle.Render("• ")
	}
	left := menuHintKeyStyle.Render(bar) + ls.Render(padW(label, optLabelW)) + mark + value
	if focused {
		hint = menuHintLabelStyle.Render(hint)
	} else {
		hint = mnNoteStyle.Render(hint)
	}
	return joinLR(left, hint, width)
}

// choiceList renders inline choices with the selected one marked ▸x◂.
func choiceList(choices []string, sel string) string {
	parts := make([]string, len(choices))
	for i, c := range choices {
		if c == sel {
			parts[i] = menuHintKeyStyle.Render("▸") + mnValueFocusSty.Render(c) + menuHintKeyStyle.Render("◂")
		} else {
			parts[i] = mnDescStyle.Render(c)
		}
	}
	return strings.Join(parts, "  ")
}

// sheetLines renders the options sheet as exactly height lines.
func (m MenuModel) sheetLines(width, height int) []string {
	a, ok := m.current()
	if !ok {
		return fitLines(nil, height)
	}
	o, def := m.opts, m.newAgentOpts(a)
	indent := strings.Repeat(" ", 2+optLabelW+2)
	valW := max(10, width-2-optLabelW-2-16)

	var out []string
	out = append(out, m.sheetRow(fBackend, "Backend", choiceList(o.backends, o.backend), "←→", o.backend != def.backend, width))

	mv := o.model
	for _, c := range m.modelChoices() {
		if c.Value == o.model && c.Default {
			mv += " (default)"
		}
	}
	out = append(out, m.sheetRow(fModel, "Model", mnValueStyle.Render(truncW(mv, valW)), "space to pick", o.model != def.model, width))
	out = append(out, indent+mnNoteStyle.Render(truncW(m.priceHint(), width-len(indent))))

	out = append(out, m.sheetRow(fEffort, "Effort", choiceList(m.effortOptions(), o.effort), "←→", o.effort != def.effort, width))

	var dv string
	if m.editing {
		dv = m.edit.View()
	} else {
		dv = shortenDir(o.dir)
		if w := ansi.StringWidth(dv); w > valW {
			dv = "…" + ansi.TruncateLeft(dv, w-valW+1, "")
		}
		dv = mnValueStyle.Render(dv)
	}
	out = append(out, m.sheetRow(fDir, "Dir", dv, "space to edit", o.dir != def.dir, width))

	wt := choiceList([]string{"on", "off"}, map[bool]string{true: "on", false: "off"}[o.worktree])
	out = append(out, m.sheetRow(fWorktree, "Worktree", wt, "←→", o.worktree != def.worktree, width))
	out = append(out, "")
	for _, n := range m.modelNotes() {
		out = append(out, truncW(n, width))
	}
	return fitLines(out, height)
}

// modelNotes returns the inline status lines for the model: live-list state,
// the "not on the server" warning and the last validation error.
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
			notes = append(notes, mnWarnStyle.Render("⚠ "+msg))
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

// ---- footer and frame ----

type hint struct{ key, label string }

// hintSegments returns the footer hints of the current view, most important
// first (the footer drops from the end).
func (m MenuModel) hintSegments() []hint {
	switch m.view {
	case viewPicker:
		return []hint{{"⏎", "select"}, {"↑↓", "move"}, {"type", "filter"}, {"esc", "back"}}
	case viewResume:
		return []hint{{"⏎", "resume"}, {"↑↓", "select"}, {"esc", "back"}}
	case viewSheet:
		if m.editing {
			return []hint{{"⏎", "confirm"}, {"esc", "cancel"}}
		}
		return []hint{{"⏎", "launch"}, {"↑↓", "field"}, {"←→", "change"}, {"space", "pick/edit"}, {"ctrl+u", "reset"}, {"esc", "back"}}
	}
	esc := "close"
	if m.filter.Value() != "" {
		esc = "clear"
	}
	enter := "launch"
	if _, ok := m.current(); !ok {
		enter = "create"
	}
	return []hint{{"⏎", enter}, {"tab", "options"}, {"ctrl+r", "resume"}, {"ctrl+g", "go to running"}, {"esc", esc}}
}

// footer renders the hint segments that fit in width, whole, dropping from the
// end. A pending notice replaces the hints for one keypress.
func (m MenuModel) footer(width int) string {
	if m.notice != "" {
		return mnWarnStyle.Render(truncW(m.notice, width))
	}
	segs := m.hintSegments()
	for n := len(segs); n > 0; n-- {
		parts := make([]string, n)
		for i, h := range segs[:n] {
			parts[i] = menuHintKeyStyle.Render(h.key) + " " + menuHintLabelStyle.Render(h.label)
		}
		line := strings.Join(parts, "  ")
		if ansi.StringWidth(line) <= width {
			return line
		}
	}
	return ""
}

// layout returns the list and detail heights for a popup of height h.
func (m MenuModel) layout(h int) (listH, detailH int) {
	detailH = 5
	switch {
	case h < 16:
		detailH = 0
	case h < 24:
		detailH = 2
	}
	if m.view == viewResume {
		n := 1
		if ss := m.currentSessions(); len(ss) > 0 {
			n += len(ss)
		} else {
			n++
		}
		detailH = max(detailH, n)
	}
	for {
		fixed := 3 // header, filter, footer
		if detailH > 0 {
			fixed += detailH + 2 // two rules
		}
		listH = h - fixed
		if listH >= 3 || detailH == 0 {
			return max(listH, 1), detailH
		}
		detailH--
		if detailH < 2 {
			detailH = 0
		}
	}
}

func (m MenuModel) View() tea.View {
	w, h := m.width, m.height
	if w < minPopupW || h < minPopupH {
		return tea.NewView(truncW("popup too small", w))
	}
	cw := w - 2 // one-cell margin on each side
	var body []string
	switch m.view {
	case viewSheet:
		a, _ := m.current()
		body = append(body, m.header("Launch "+a.Name, "• = changed from agent", cw))
		body = append(body, m.sheetLines(cw, h-2)...)
	case viewPicker:
		a, _ := m.current()
		body = append(body, m.header("Model for "+a.Name+" · "+m.opts.backend, "", cw))
		body = append(body, m.pickerLines(cw, h-2)...)
	default:
		listH, detailH := m.layout(h)
		body = append(body, m.header("Launch agent", m.countSummary(), cw), m.filterLine(cw))
		body = append(body, m.listLines(cw, listH)...)
		if detailH > 0 {
			rule := mnRuleStyle.Render(strings.Repeat("─", cw))
			body = append(body, rule)
			if a, ok := m.current(); m.view == viewResume && ok {
				body = append(body, m.sessionLines(a, cw, detailH)...)
			} else {
				body = append(body, m.detailLines(cw, detailH)...)
			}
			body = append(body, rule)
		}
	}
	body = fitLines(body, h-1)
	lines := make([]string, 0, h)
	for _, l := range body {
		lines = append(lines, " "+truncW(l, cw))
	}
	lines = append(lines, " "+m.footer(cw))
	return tea.NewView(strings.Join(lines, "\n"))
}
