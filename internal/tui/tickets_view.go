package tui

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/charmbracelet/x/ansi"
)

// initTicketStyles initialises tickets-browser styles from the active theme.
// Shared styles (ColHeader, Rule, Dim, etc.) are sourced from tableStyles,
// which must be initialised first by initTableStyles().
func initTicketStyles() {
	t := theme.Current()

	tkStyles = NewStyles(90)

	// ── Shared table styles ───────────────────────────────────────────────────
	tkColHeaderStyle = tableStyles.ColHeader
	tkRuleStyle = tableStyles.Rule
	tkDimStyle = tableStyles.Dim
	tkDetailLabelStyle = tableStyles.DetailLabel.Width(16)
	tkDetailValueStyle = tableStyles.DetailValue
	tkDetailSectionStyle = tableStyles.DetailSection
	tkSubtitleStyle = tableStyles.Subtitle

	// ── Ticket-specific styles ────────────────────────────────────────────────
	tkNumStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Width(tkColNum)

	tkNumSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true).
		Width(tkColNum)

	tkTitleColStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))

	tkTitleSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)

	tkAgeStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Width(tkColAge)

	tkCodeBlockStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(lipgloss.Color(t.Surface)).
		PaddingLeft(1)
}

// labelColor maps known label names to theme colors for visual differentiation.
func labelColor(name string) string {
	t := theme.Current()
	switch name {
	case "bug":
		return t.Red
	case "enhancement":
		return t.Blue
	case "quick-fix":
		return t.Green
	case "refactor":
		return t.Muted
	case "triage":
		return t.Yellow
	default:
		return t.Subtle
	}
}

func fgStyle(color string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color))
}

// ── Interactive pieces ───────────────────────────────────────────────────────

// button renders "[ label ]" as a click zone; it renders reversed while the
// pointer is over it.
func (m TicketsModel) button(id, label string, st lipgloss.Style) string {
	if m.hover == id {
		st = st.Reverse(true)
	}
	return zoneMark(id, st.Render("[ "+label+" ]"))
}

// compactButton is button without the inner padding, for the narrow strip.
func (m TicketsModel) compactButton(id, label string, st lipgloss.Style) string {
	if m.hover == id {
		st = st.Reverse(true)
	}
	return zoneMark(id, st.Render("["+label+"]"))
}

// link renders a text click target; it underlines while hovered.
func (m TicketsModel) link(id, text string, st lipgloss.Style) string {
	if m.hover == id {
		st = st.Underline(true)
	}
	return zoneMark(id, st.Render(text))
}

// tkAction is one sidebar action: its button label, compact label, the key
// it runs and whether it is destructive.
type tkAction struct {
	label, short, key string
	danger            bool
}

// tkSidebarActions are the issue actions shown as buttons in the preview
// and detail sidebars, in display order.
func tkSidebarActions() []tkAction {
	return []tkAction{
		{label: "Edit", short: "Edit", key: "e"},
		{label: "Triage", short: "Triage", key: "t"},
		{label: "Dispatch…", short: "Dispatch…", key: "T"},
		{label: "Assign", short: "Assign", key: "a"},
		{label: "Open ↗", short: "Open↗", key: "o"},
		{label: "Close", short: "Close", key: "d", danger: true},
	}
}

// actionButtons renders the sidebar actions flowed into lines of at most
// width columns (one line when compact).
func (m TicketsModel) actionButtons(where string, width int, compact bool) []string {
	t := theme.Current()
	var lines []string
	cur, curW := "", 0
	for _, a := range tkSidebarActions() {
		st := fgStyle(t.Subtle)
		if a.danger {
			st = fgStyle(t.Red)
		}
		id := "key:" + a.key + "@" + where
		var b string
		if compact {
			b = m.compactButton(id, a.short, st)
		} else {
			b = m.button(id, a.label, st)
		}
		bw := lipgloss.Width(b)
		if curW > 0 && curW+1+bw > width && !compact {
			lines = append(lines, cur)
			cur, curW = "", 0
		}
		if curW > 0 {
			cur += " "
			curW++
		}
		cur += b
		curW += bw
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// labelChips renders an issue's labels as clickable filter chips.
func (m TicketsModel) labelChips(labels []ghLabel, where string, brackets bool) string {
	var parts []string
	for _, l := range labels {
		text := l.Name
		if brackets {
			text = "[" + l.Name + "]"
		}
		parts = append(parts, m.link("label:"+l.Name+"@"+where, text, fgStyle(labelColor(l.Name))))
	}
	return strings.Join(parts, " ")
}

// prLink renders issue n's linked PR as "#172↗", or "" when it has none.
func (m TicketsModel) prLink(n int, where string) string {
	pr, ok := m.linkedPRs[n]
	if !ok {
		return ""
	}
	st := fgStyle(theme.Current().Green)
	return m.link("pr:"+strconv.Itoa(n)+"@"+where, fmt.Sprintf("#%d↗", pr.Number), st)
}

// assigneeLink renders the assignees (or "unassigned") as the assign target.
func (m TicketsModel) assigneeLink(n int, assignees []ghUser, where string) string {
	id := "assign:" + strconv.Itoa(n) + "@" + where
	if len(assignees) == 0 {
		return m.link(id, "unassigned", tkDimStyle.Italic(true))
	}
	var logins []string
	for _, u := range assignees {
		logins = append(logins, "@"+u.Login)
	}
	return m.link(id, strings.Join(logins, ", "), tkDetailValueStyle)
}

// ── Frame helpers ────────────────────────────────────────────────────────────

// fitFrame pads or clips body to h-1 lines and puts hint on the last line,
// so every frame is exactly h lines tall. A frame that changed height would
// leave stale lines from the previous one on screen.
func fitFrame(body, hint string, h int) string {
	if h <= 0 {
		return body + "\n" + hint
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	want := h - 1
	if len(lines) > want {
		lines = lines[:want]
	}
	for len(lines) < want {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, hint), "\n")
}

// tkHint is one hint-bar entry: the key shown, its label, the key it sends
// when clicked and whether it survives when the bar is too wide.
type tkHint struct {
	key, label, send string
	pin              bool
}

// renderHintBar renders the hints centered in the content width, dropping
// unpinned hints from the end until the bar fits. Every hint is a click
// zone that sends its key.
func (m TicketsModel) renderHintBar(hints []tkHint) string {
	sep := "  " + menuHintDotStyle.Render("·") + "  "
	render := func(h tkHint) string {
		send := h.send
		if send == "" {
			send = h.key
		}
		id := "key:" + send + "@hint"
		label := menuHintLabelStyle
		if m.hover == id {
			label = label.Underline(true)
		}
		return zoneMark(id, menuHintKeyStyle.Render(h.key)+" "+label.Render(h.label))
	}
	cw := m.contentWidth()
	keep := append([]tkHint(nil), hints...)
	for {
		var parts []string
		for _, h := range keep {
			parts = append(parts, render(h))
		}
		bar := strings.Join(parts, sep)
		w := lipgloss.Width(bar)
		if w <= cw || len(keep) == 0 {
			pad := (cw - w) / 2
			if pad < 0 {
				pad = 0
			}
			return "  " + strings.Repeat(" ", pad) + bar
		}
		dropped := false
		for i := len(keep) - 1; i >= 0; i-- {
			if !keep[i].pin {
				keep = append(keep[:i], keep[i+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			return "  " + ansi.Truncate(bar, cw, "…")
		}
	}
}

// renderTitleBar renders the accent title bar, optionally preceded by the
// detail view's "← back" link, filling width columns.
func (m TicketsModel) renderTitleBar(title string, back bool, width int) string {
	prefix := ""
	if back {
		prefix = m.link("key:esc@back", "← back", fgStyle(theme.Current().Accent).Bold(true)) + "  "
	}
	barW := width - lipgloss.Width(prefix)
	if barW < 10 {
		barW = 10
	}
	text := ansi.Truncate(title, barW-4, "…")
	return prefix + tkStyles.Title.Width(barW).Render(text)
}

// View renders the frame, records its click zones and strips the markers.
func (m TicketsModel) View() tea.View {
	s, zones := scanZones(m.render())
	if m.zones != nil {
		*m.zones = zones
	}
	v := tea.NewView(s)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	return v
}

// renderZones renders the frame and returns its click zones (used when no
// View has been recorded yet, e.g. a model built in a test).
func (m TicketsModel) renderZones() zoneList {
	_, z := scanZones(m.render())
	return z
}

// render composes the frame with zone markers still in it. An open modal
// owns all mouse input, so the view behind it loses its zones.
func (m TicketsModel) render() string {
	var bg string
	if m.viewMode == tkViewDetail && m.detail != nil {
		bg = m.viewDetail()
	} else {
		bg = m.viewList()
	}
	modal, ok := m.activeModal()
	if !ok {
		return bg
	}
	box := zoneMarkLines("modal:box", modal.View())
	return compositeOverlay(stripZones(bg), box, m.termWidth, m.termHeight)
}

// activeModal returns the modal on top, with the pointer's hover applied to
// its content, if any is open.
func (m TicketsModel) activeModal() (Modal, bool) {
	for _, md := range []Modal{m.choiceModal, m.confirmModal, m.promptModal, m.agentModal, m.createModal, m.helpModal} {
		if md.Visible {
			if h, ok := md.Content.(tkHoverable); ok {
				md.Content = h.withHover(m.hover)
			}
			return md, true
		}
	}
	return Modal{}, false
}

// modalOpen reports whether any modal or overlay is showing.
func (m TicketsModel) modalOpen() bool {
	_, ok := m.activeModal()
	return ok
}

// tkHoverable is modal content that renders hover feedback on its buttons.
type tkHoverable interface {
	withHover(id string) tea.Model
}

// ── List view ────────────────────────────────────────────────────────────────

func (m TicketsModel) viewList() string {
	var b strings.Builder
	cw := m.contentWidth()

	titleText := "Tickets"
	if m.repoSlug != "" {
		titleText = fmt.Sprintf("Tickets  %s", m.repoSlug)
	}
	b.WriteString("  " + m.renderHeaderLine(titleText, false, cw))
	b.WriteString("\n")

	// Status line — always exactly one line so the chrome height is stable.
	b.WriteString("  " + m.renderStatusLine(cw))
	b.WriteString("\n")

	b.WriteString("  " + m.renderChips())
	b.WriteString("\n\n")

	if !m.loading && m.err == nil {
		b.WriteString("  " + m.renderColumnHeaders())
		b.WriteString("\n")
		b.WriteString("  " + tkRuleStyle.Render(strings.Repeat("╌", cw)))
		b.WriteString("\n")

		// SplitPane: list rows + separator + detail preview.
		sp := m.splitPane
		sp.SetListContent(m.buildListContent(), len(m.issues))
		// SetListContent establishes totalRows; now sync the cursor so that
		// scroll clamping uses the actual cursor position.
		sp.SetCursor(m.cursor)
		sp.SetDetailContent(m.buildPreviewContent())
		b.WriteString(sp.View())
	}

	return fitFrame(b.String(), m.listHintBar(), m.termHeight)
}

// renderStatusLine is the line under the title: the flash message, else the
// autopilot status.
func (m TicketsModel) renderStatusLine(cw int) string {
	t := theme.Current()
	if m.flash != "" {
		if m.flashErr {
			return ansi.Truncate(fgStyle(t.Red).Render(m.flash), cw, "…")
		}
		check := fgStyle(t.Green).Bold(true).Render("✓")
		return ansi.Truncate(check+" "+tkDimStyle.Render(m.flash), cw, "…")
	}
	return ""
}

// tkMinTitleBar is the narrowest the title bar may get before the
// autopilot pill drops its detail.
const tkMinTitleBar = 28

// renderHeaderLine is the title bar line of the list and detail views: the
// title bar, then the always-visible autopilot pill at the right.
func (m TicketsModel) renderHeaderLine(title string, back bool, cw int) string {
	pill := m.renderPill(false)
	if cw-lipgloss.Width(pill)-2 < tkMinTitleBar {
		pill = m.renderPill(true)
	}
	bar := m.renderTitleBar(title, back, cw-lipgloss.Width(pill)-2)
	return bar + "  " + pill
}

// renderChips renders the clickable filter/sort/search/new chips under the
// title. Each chip runs the same code path as its key.
func (m TicketsModel) renderChips() string {
	t := theme.Current()
	if m.err != nil && !m.loading {
		msg := m.err.Error()
		if m.err == errNoRepo {
			msg = "No GitHub remote found in this directory"
		}
		return fgStyle(t.Red).Render("✗ " + msg)
	}
	chip := fgStyle(t.Subtle)
	active := fgStyle(t.Accent).Bold(true)

	state := m.issueState
	if state == "" {
		state = tkStateOpen
	}
	count := "…"
	if !m.loading {
		count = strconv.Itoa(len(m.allIssues))
		if len(m.issues) != len(m.allIssues) {
			count = fmt.Sprintf("%d/%d", len(m.issues), len(m.allIssues))
		}
	}
	stateSt := chip
	if state != tkStateOpen {
		stateSt = active
	}
	parts := []string{
		m.button("key:S@chip", state+" "+count+" ▾", stateSt),
		m.button("key:s@chip", "sort: "+m.sortBy.label()+" ▾", chip),
	}

	if m.filterLabel == "" {
		parts = append(parts, m.button("key:l@chip", "label: any ▾", chip))
	} else {
		clear := m.link("chip:label-clear", "×", fgStyle(t.Red).Bold(true))
		parts = append(parts, m.chipWithClear("key:l@chip", "label: "+m.filterLabel, clear, active))
	}

	if m.searchQuery == "" {
		parts = append(parts, m.button("key:/@chip", "/ search", chip))
	} else {
		clear := m.link("chip:search-clear", "×", fgStyle(t.Red).Bold(true))
		parts = append(parts, m.chipWithClear("key:/@chip", fmt.Sprintf("/ %q", m.searchQuery), clear, active))
	}
	parts = append(parts, m.button("key:c@chip", "+ New", fgStyle(t.Green)))
	return strings.Join(parts, "  ")
}

// chipWithClear renders "[ text × ]": the chip runs id, the × clears it.
func (m TicketsModel) chipWithClear(id, text, clear string, st lipgloss.Style) string {
	if m.hover == id {
		st = st.Reverse(true)
	}
	return zoneMark(id, st.Render("[ "+text+" ")+clear+st.Render(" ]"))
}

// tkListCols is which optional list columns fit, and the title width.
type tkListCols struct {
	pr, labels, assignee bool
	title                int
}

// listColumns drops ASSIGNEE, then LABELS, then PR as the terminal narrows.
func (m TicketsModel) listColumns() tkListCols {
	c := tkListCols{
		pr:       m.termWidth >= tkDropPRCols,
		labels:   m.termWidth >= tkDropLabelsCols,
		assignee: m.termWidth >= tkDropAssigneeCols,
	}
	w := m.contentWidth() - tkRowPrefix - tkColNum - tkColAge
	if c.pr {
		w -= tkColPR
	}
	if c.labels {
		w -= tkColLabels
	}
	if c.assignee {
		w -= tkColAssignee
	}
	if w < 10 {
		w = 10
	}
	c.title = w
	return c
}

// padTo pads s with spaces (rendered in st) to width w.
func padTo(s string, w int, st lipgloss.Style) string {
	if gap := w - lipgloss.Width(s); gap > 0 {
		return s + st.Render(strings.Repeat(" ", gap))
	}
	return s
}

// renderColumnHeaders renders the list's column headers. "#" and "AGE" are
// sort toggles and show an arrow when they are the active sort.
func (m TicketsModel) renderColumnHeaders() string {
	c := m.listColumns()
	hdr := tkColHeaderStyle
	act := hdr.Foreground(lipgloss.Color(theme.Current().Accent))
	plain := lipgloss.NewStyle()

	num, numSt := "#", hdr
	switch m.sortBy {
	case sortNumber:
		num, numSt = "# ↑", act
	case sortNumberDesc:
		num, numSt = "# ↓", act
	}
	age, ageSt := "AGE", hdr
	switch m.sortBy {
	case sortNewest:
		age, ageSt = "AGE ↑", act
	case sortOldest:
		age, ageSt = "AGE ↓", act
	}

	var b strings.Builder
	b.WriteString(strings.Repeat(" ", tkRowPrefix))
	b.WriteString(padTo(m.link("sort:num@hdr", num, numSt), tkColNum, plain))
	if c.pr {
		b.WriteString(hdr.Width(tkColPR).Render("PR"))
	}
	b.WriteString(hdr.Width(c.title).Render("TITLE"))
	if c.labels {
		b.WriteString(hdr.Width(tkColLabels).Render("LABELS"))
	}
	if c.assignee {
		b.WriteString(hdr.Width(tkColAssignee).Render("ASSIGNEE"))
	}
	b.WriteString(m.link("sort:age@hdr", age, ageSt))
	return b.String()
}

// listHintBar is the list view's bottom hint bar (or the search input).
func (m TicketsModel) listHintBar() string {
	if m.searchMode {
		dot := "  " + menuHintDotStyle.Render("·") + "  "
		searchLine := menuHintKeyStyle.Render("/") + " " + m.searchInput.View()
		return "  " + searchLine + dot +
			zoneMark("key:enter@search", menuHintKeyStyle.Render("enter")+" "+menuHintLabelStyle.Render("confirm")) + dot +
			zoneMark("key:esc@search", menuHintKeyStyle.Render("esc")+" "+menuHintLabelStyle.Render("clear"))
	}
	hints := []tkHint{
		{key: "enter", label: "detail"},
		{key: "e", label: "edit"},
		{key: "d", label: "close"},
		{key: "c", label: "new"},
		{key: "l", label: "label"},
		{key: "s", label: "sort"},
		{key: "/", label: "search"},
		{key: "o", label: "open"},
		{key: "t", label: "triage"},
		{key: "T", label: "dispatch"},
		{key: "y", label: "copy url"},
		{key: "P", label: "autopilot"},
	}
	if m.searchQuery != "" {
		hints = append(hints, tkHint{key: "esc", label: "clear search", pin: true})
	}
	hints = append(hints,
		tkHint{key: "?", label: "help", pin: true},
		tkHint{key: "q", label: "quit", pin: true},
	)
	return m.renderHintBar(hints)
}

// buildListContent returns all issue rows rendered as a single string (one row
// per line).  SplitPane handles viewport scrolling based on the cursor.
func (m TicketsModel) buildListContent() string {
	if len(m.issues) == 0 {
		msg := "No open issues found"
		switch {
		case len(m.allIssues) > 0:
			msg = "No issues match the filter"
		case m.issueState == tkStateClosed:
			msg = "No closed issues found"
		case m.issueState == tkStateAll:
			msg = "No issues found"
		}
		return "     " + tkDimStyle.Render(msg)
	}
	c := m.listColumns()
	rows := make([]string, 0, len(m.issues))
	for i, issue := range m.issues {
		rows = append(rows, m.renderRow(i, issue, c))
	}
	return strings.Join(rows, "\n")
}

// renderRow renders one list row. The row is a click zone (select; double
// click opens), with nested zones for its PR link, label chips and assignee.
func (m TicketsModel) renderRow(i int, issue ghIssue, c tkListCols) string {
	t := theme.Current()
	selected := i == m.cursor
	n := strconv.Itoa(issue.Number)
	rowID := "row:" + n
	hovered := !selected && m.hoverRow == rowID
	closed := strings.EqualFold(issue.State, "closed")

	// tint gives a hovered row a faint background on every segment, so the
	// tint is continuous across the separately styled cells.
	tint := func(s lipgloss.Style) lipgloss.Style {
		if hovered {
			return s.Background(lipgloss.Color(t.Surface))
		}
		return s
	}
	plain := tint(lipgloss.NewStyle())

	var b strings.Builder
	if selected {
		b.WriteString(fgStyle(t.Accent).Render("▌") + cursorActiveStyle.Render("▸") + " ")
	} else {
		b.WriteString(plain.Render(strings.Repeat(" ", tkRowPrefix)))
	}

	numSt := tkNumStyle
	if selected {
		numSt = tkNumSelectedStyle
	}
	b.WriteString(tint(numSt).Render("#" + n))

	if c.pr {
		b.WriteString(padTo(m.prLinkTinted(issue.Number, tint), tkColPR, plain))
	}

	title := issue.Title
	if closed {
		title = "✓ " + title
	}
	title = ansi.Truncate(title, c.title-2, "…")
	titleSt := tkTitleColStyle
	switch {
	case selected:
		titleSt = tkTitleSelectedStyle
	case closed:
		titleSt = tkDimStyle
	}
	b.WriteString(tint(titleSt).Width(c.title).Render(title))

	if c.labels {
		b.WriteString(padTo(m.rowLabels(issue, tint), tkColLabels, plain))
	}
	if c.assignee {
		cell := ""
		if len(issue.Assignees) > 0 {
			login := ansi.Truncate("@"+issue.Assignees[0].Login, tkColAssignee-1, "…")
			cell = m.link("assign:"+n+"@row", login, tint(tkDimStyle))
		}
		b.WriteString(padTo(cell, tkColAssignee, plain))
	}
	b.WriteString(tint(tkAgeStyle).Render(fmtAge(time.Since(issue.CreatedAt))))

	return zoneMark("pane:list", "  "+zoneMark(rowID, b.String()))
}

// prLinkTinted is prLink with the row tint applied.
func (m TicketsModel) prLinkTinted(n int, tint func(lipgloss.Style) lipgloss.Style) string {
	pr, ok := m.linkedPRs[n]
	if !ok {
		return ""
	}
	return m.link("pr:"+strconv.Itoa(n)+"@row", fmt.Sprintf("#%d↗", pr.Number), tint(fgStyle(theme.Current().Green)))
}

// rowLabels renders a row's labels as chips that fit the LABELS column,
// with "+N" for the ones that do not.
func (m TicketsModel) rowLabels(issue ghIssue, tint func(lipgloss.Style) lipgloss.Style) string {
	limit := tkColLabels - 1
	var parts []string
	used := 0
	for i, l := range issue.Labels {
		w := lipgloss.Width(l.Name)
		more := ""
		if rest := len(issue.Labels) - i - 1; rest > 0 {
			more = fmt.Sprintf(" +%d", rest)
		}
		sep := 0
		if used > 0 {
			sep = 1
		}
		if used+sep+w+lipgloss.Width(more) > limit && i < len(issue.Labels)-1 || used+sep+w > limit {
			rest := len(issue.Labels) - i
			if used == 0 {
				// Not even one fits: truncate the first.
				name := ansi.Truncate(l.Name, limit-len(fmt.Sprintf(" +%d", rest-1))-1, "…")
				parts = append(parts, m.link("label:"+l.Name+"@"+strconv.Itoa(issue.Number), name, tint(fgStyle(labelColor(l.Name)))))
				if rest > 1 {
					parts = append(parts, tint(tkDimStyle).Render(fmt.Sprintf("+%d", rest-1)))
				}
			} else {
				parts = append(parts, tint(tkDimStyle).Render(fmt.Sprintf("+%d", rest)))
			}
			break
		}
		parts = append(parts, m.link("label:"+l.Name+"@"+strconv.Itoa(issue.Number), l.Name, tint(fgStyle(labelColor(l.Name)))))
		used += sep + w
	}
	return strings.Join(parts, tint(lipgloss.NewStyle()).Render(" "))
}

// previewInnerWidth is the width inside the preview box's border and padding.
func (m TicketsModel) previewInnerWidth() int {
	w := m.termWidth - detailBoxHorizontalOverhead
	if w < 20 {
		w = 20
	}
	return w
}

// buildPreviewContent returns the rendered preview of the highlighted issue:
// title, labels and body on the left with an actions sidebar on the right,
// or a single column with an actions strip on narrow terminals. SplitPane
// handles clipping.
func (m TicketsModel) buildPreviewContent() string {
	maxH := m.splitPane.DetailHeight()
	if maxH < 1 {
		maxH = 1
	}
	width := m.previewInnerWidth()

	if m.previewDetail == nil {
		lines := []string{tkDimStyle.Render("Loading preview...")}
		for len(lines) < maxH {
			lines = append(lines, "")
		}
		return zoneMarkLines("pane:preview", strings.Join(lines, "\n"))
	}
	d := m.previewDetail
	head, body, bodyW := m.previewParts(d, width)
	rows := maxH - len(head)
	if rows < 1 {
		rows = 1
	}
	off := m.previewScroll
	if mx := len(body) - rows; off > mx {
		off = mx
	}
	if off < 0 {
		off = 0
	}
	end := off + rows
	if end > len(body) {
		end = len(body)
	}
	left := append(append([]string(nil), head...), body[off:end]...)

	if m.narrow() {
		// Pad to the full width so the preview box spans the terminal.
		block := lipgloss.NewStyle().Width(width).Render(clipLines(left, maxH, bodyW))
		return zoneMarkLines("pane:preview", block)
	}
	rightW := width - bodyW - 2
	right := []string{tkDetailSectionStyle.Render("Actions"), ""}
	right = append(right, m.actionButtons("prev", rightW, false)...)
	right = append(right, "")
	right = append(right, m.sidebarMeta(d, "prev", rightW)...)

	leftCol := lipgloss.NewStyle().Width(bodyW).Render(clipLines(left, maxH, bodyW))
	rightCol := lipgloss.NewStyle().Width(rightW).Render(clipLines(right, maxH, rightW))
	return zoneMarkLines("pane:preview", lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "  ", rightCol))
}

// previewParts splits the preview's left column into its fixed head (title,
// labels or the narrow action strip) and its scrollable body lines, and
// returns the column width.
func (m TicketsModel) previewParts(d *tkDetailFetchedMsg, width int) (head, body []string, colW int) {
	titleSt := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.Current().Text))
	title := fmt.Sprintf("#%d  %s", d.number, d.title)
	if m.narrow() {
		head = []string{ansi.Truncate(titleSt.Render(title), width, "…")}
		head = append(head, m.actionButtons("prev", width, true)...)
		head = append(head, ansi.Truncate(m.metaLine(d, "prev"), width, "…"), "")
		return head, m.renderBody(d.body, width), width
	}
	colW = width * 60 / 100
	head = []string{ansi.Truncate(titleSt.Render(title), colW, "…")}
	if len(d.labels) > 0 {
		head = append(head, ansi.Truncate(m.labelChips(d.labels, "prev", true), colW, "…"))
	}
	head = append(head, "")
	return head, m.renderBody(d.body, colW), colW
}

// previewMaxScroll is the largest useful preview body scroll offset.
func (m TicketsModel) previewMaxScroll() int {
	if m.previewDetail == nil {
		return 0
	}
	head, body, _ := m.previewParts(m.previewDetail, m.previewInnerWidth())
	n := len(body) - (m.splitPane.DetailHeight() - len(head))
	if n < 0 {
		return 0
	}
	return n
}

// scrollPreview moves the preview body by delta lines, clamped.
func (m *TicketsModel) scrollPreview(delta int) {
	m.previewScroll += delta
	if mx := m.previewMaxScroll(); m.previewScroll > mx {
		m.previewScroll = mx
	}
	if m.previewScroll < 0 {
		m.previewScroll = 0
	}
}

// metaLine is the narrow layout's one-line summary: labels · assignee · PR.
func (m TicketsModel) metaLine(d *tkDetailFetchedMsg, where string) string {
	sep := tkDimStyle.Render("  ·  ")
	parts := []string{}
	if len(d.labels) > 0 {
		parts = append(parts, m.labelChips(d.labels, where, false))
	}
	parts = append(parts, m.assigneeLink(d.number, d.assignees, where))
	if pr := m.prLink(d.number, where); pr != "" {
		parts = append(parts, "PR "+pr)
	}
	return strings.Join(parts, sep)
}

// sidebarMeta is the sidebar's PR, assignee and URL block.
func (m TicketsModel) sidebarMeta(d *tkDetailFetchedMsg, where string, width int) []string {
	lbl := tkDetailLabelStyle.Width(10)
	var lines []string
	if pr := m.prLink(d.number, where); pr != "" {
		lines = append(lines, lbl.Render("PR")+pr)
		if p, ok := m.linkedPRs[d.number]; ok {
			lines = append(lines, ansi.Truncate(tkDimStyle.Render(p.HeadRefName), width, "…"))
		}
		lines = append(lines, "")
	}
	lines = append(lines, lbl.Render("Assignee")+m.assigneeLink(d.number, d.assignees, where))
	if strings.EqualFold(d.state, "closed") {
		lines = append(lines, lbl.Render("State")+fgStyle(theme.Current().Red).Render("closed"))
	}
	if d.url != "" {
		lines = append(lines, "", m.link("url@"+where, ansi.Truncate(d.url, width, "…"), tkDimStyle))
	}
	return lines
}

// clipLines truncates every line to width and keeps at most h lines.
func clipLines(lines []string, h, width int) string {
	if len(lines) > h {
		lines = lines[:h]
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Truncate(l, width, "…")
	}
	return strings.Join(out, "\n")
}

// renderBody renders a markdown body to lines at most width wide.
func (m TicketsModel) renderBody(body string, width int) []string {
	if strings.TrimSpace(body) == "" {
		return []string{tkDimStyle.Render("(no body)")}
	}
	wrap := width - 2
	if wrap < 20 {
		wrap = 20
	}
	rendered := tkGlamour(body, wrap)
	return strings.Split(strings.TrimRight(rendered, "\n"), "\n")
}

// tkGlamourCache memoizes rendered bodies: motion events re-render the frame
// often and building a glamour renderer is far too slow to repeat per frame.
var tkGlamourCache = struct {
	sync.Mutex
	entries map[string]string
	order   []string
}{entries: map[string]string{}}

func tkGlamour(body string, width int) string {
	key := strconv.Itoa(width) + "\x00" + body
	tkGlamourCache.Lock()
	if s, ok := tkGlamourCache.entries[key]; ok {
		tkGlamourCache.Unlock()
		return s
	}
	tkGlamourCache.Unlock()
	s := glamourRender(body, width)
	tkGlamourCache.Lock()
	defer tkGlamourCache.Unlock()
	if _, ok := tkGlamourCache.entries[key]; !ok {
		const maxEntries = 48
		if len(tkGlamourCache.order) >= maxEntries {
			delete(tkGlamourCache.entries, tkGlamourCache.order[0])
			tkGlamourCache.order = tkGlamourCache.order[1:]
		}
		tkGlamourCache.order = append(tkGlamourCache.order, key)
	}
	tkGlamourCache.entries[key] = s
	return s
}

// renderBodyLines renders a markdown body string into display lines, applying
// per-line styling and visually differentiating fenced code blocks (``` ... ```)
// from prose. Lines inside a code block are styled with tkCodeBlockStyle; prose
// lines use tkDetailValueStyle. Long lines are hard-wrapped at maxWidth.
func renderBodyLines(body string, maxWidth int) []string {
	var out []string
	inCodeBlock := false

	appendLine := func(text string, inBlock bool) {
		if inBlock {
			out = append(out, tkCodeBlockStyle.Render(text))
		} else {
			out = append(out, "  "+tkDetailValueStyle.Render(text))
		}
	}

	wrapAndAppend := func(rawLine string, inBlock bool) {
		if len(rawLine) <= maxWidth {
			appendLine(rawLine, inBlock)
			return
		}
		for len(rawLine) > maxWidth {
			appendLine(rawLine[:maxWidth], inBlock)
			rawLine = rawLine[maxWidth:]
		}
		if rawLine != "" {
			appendLine(rawLine, inBlock)
		}
	}

	for _, rawLine := range strings.Split(body, "\n") {
		if strings.HasPrefix(rawLine, "```") {
			inCodeBlock = !inCodeBlock
			// Render the fence marker itself in code-block style so there's
			// no gap in the left border gutter.
			appendLine(rawLine, true)
			continue
		}
		wrapAndAppend(rawLine, inCodeBlock)
	}
	return out
}

// previewFetchCmd returns a command to fetch a preview for the given issue, using the cache
// to avoid redundant API calls.
func (m TicketsModel) previewFetchCmd(number int) tea.Cmd {
	if cached, ok := m.detailCache[number]; ok {
		// Serve from cache immediately via a synthetic message.
		d := *cached
		return func() tea.Msg { return tkPreviewFetchedMsg(d) }
	}
	return tkFetchPreviewCmd(m.repoSlug, number)
}

// ── Detail view ──────────────────────────────────────────────────────────────

// detailLayout is the detail view's geometry for the current width.
type detailLayout struct {
	narrow        bool
	leftW, rightW int
}

func (m TicketsModel) detailLayout() detailLayout {
	cw := m.contentWidth()
	if m.narrow() {
		return detailLayout{narrow: true, leftW: cw}
	}
	left := cw * 60 / 100
	return detailLayout{leftW: left, rightW: cw - left - 2}
}

// detailBodyLines renders the detail body for the current layout.
func (m TicketsModel) detailBodyLines() []string {
	if m.detail == nil {
		return nil
	}
	return m.renderBody(m.detail.body, m.detailLayout().leftW)
}

// detailBodyRows is how many body rows the detail view shows (the narrow
// layout spends three rows on the action strip and meta line).
func (m TicketsModel) detailBodyRows() int {
	rows := m.detailVisibleRows()
	if m.detailLayout().narrow {
		rows -= 3
	}
	if rows < 1 {
		rows = 1
	}
	return rows
}

// detailMaxScroll is the largest useful detail scroll offset.
func (m TicketsModel) detailMaxScroll() int {
	n := len(m.detailBodyLines()) - m.detailBodyRows()
	if n < 0 {
		return 0
	}
	return n
}

func (m TicketsModel) viewDetail() string {
	if m.detail == nil {
		return m.viewList()
	}
	d := m.detail
	cw := m.contentWidth()
	lay := m.detailLayout()

	var b strings.Builder
	b.WriteString("  " + m.renderHeaderLine(fmt.Sprintf("Issue #%d  %s", d.number, d.title), true, cw))
	b.WriteString("\n")
	b.WriteString("  " + m.renderStatusLine(cw))
	b.WriteString("\n")

	bodyLines := m.detailBodyLines()
	rows := m.detailBodyRows()
	maxScroll := m.detailMaxScroll()
	off := m.scrollOffset
	if off > maxScroll {
		off = maxScroll
	}
	end := off + rows
	if end > len(bodyLines) {
		end = len(bodyLines)
	}
	visible := append([]string(nil), bodyLines[off:end]...)
	for len(visible) < rows {
		visible = append(visible, "")
	}
	bodyBlock := zoneMarkLines("pane:body", clipLines(visible, rows, lay.leftW))

	if lay.narrow {
		var top []string
		top = append(top, m.actionButtons("detail", cw, true)...)
		top = append(top, ansi.Truncate(m.metaLine(d, "detail"), cw, "…"), "")
		for _, l := range top {
			b.WriteString("  " + l + "\n")
		}
		for _, l := range strings.Split(bodyBlock, "\n") {
			b.WriteString("  " + l + "\n")
		}
	} else {
		right := m.detailSidebar(d, lay.rightW)
		rows := m.detailVisibleRows()
		leftCol := lipgloss.NewStyle().Width(lay.leftW).Render(bodyBlock)
		rightCol := lipgloss.NewStyle().Width(lay.rightW).Render(clipLines(right, rows, lay.rightW))
		for _, l := range strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "  ", rightCol), "\n") {
			b.WriteString("  " + l + "\n")
		}
	}

	// Scroll indicator (always one line, blank when everything fits).
	if maxScroll > 0 {
		b.WriteString(tkDimStyle.Render(fmt.Sprintf("  ── %d%% ──", off*100/maxScroll)))
	}
	b.WriteString("\n")

	return fitFrame(b.String(), m.detailHintBar(), m.termHeight)
}

// detailSidebar is the wide detail view's right column.
func (m TicketsModel) detailSidebar(d *tkDetailFetchedMsg, width int) []string {
	lines := []string{tkDetailSectionStyle.Render("── Details ──"), ""}
	lines = append(lines, tkDetailLabelStyle.Render("Title:"))
	lines = append(lines, tkDetailValueStyle.Width(width).Render(d.title))
	lines = append(lines, "")
	lines = append(lines, tkDetailLabelStyle.Render("Number:")+tkDetailValueStyle.Render(fmt.Sprintf("#%d", d.number)))
	if len(d.labels) > 0 {
		lines = append(lines, tkDetailLabelStyle.Render("Labels:")+m.labelChips(d.labels, "detail", false))
	}
	lines = append(lines, "")
	lines = append(lines, m.sidebarMeta(d, "detail", width)...)
	lines = append(lines, "", tkDetailSectionStyle.Render("── Actions ──"), "")
	lines = append(lines, m.actionButtons("detail", width, false)...)
	// The title can wrap to several lines inside its Width style.
	return strings.Split(strings.Join(lines, "\n"), "\n")
}

// detailHintBar is the detail view's bottom hint bar.
func (m TicketsModel) detailHintBar() string {
	hints := []tkHint{
		{key: "esc", label: "back", pin: true},
		{key: "j/k", label: "scroll", send: "j"},
		{key: "e", label: "edit"},
		{key: "d", label: "close"},
		{key: "a", label: "assign"},
		{key: "o", label: "open"},
		{key: "t", label: "triage"},
		{key: "T", label: "dispatch"},
		{key: "y", label: "copy url"},
	}
	if m.detail != nil {
		if _, ok := m.linkedPRs[m.detail.number]; ok {
			hints = append(hints, tkHint{key: "p", label: "open PR"})
		}
	}
	hints = append(hints, tkHint{key: "?", label: "help", pin: true})
	return m.renderHintBar(hints)
}
