package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/tokens"
)

// Layout of the sessions browser.
//
// The frame is exactly termHeight lines and no line is wider than termWidth,
// whatever size the browser is given. From the top:
//
//	title bar · chip row · column header · rule · list body · totals · hints
//
// At sessPreviewMinWidth columns and up the list body shares its rows with a
// preview panel on the right; below that the panel collapses (enter opens the
// full detail view) and the list takes the whole width, dropping low-priority
// columns until the rest fit.
const (
	sessPreviewMinWidth = 100 // below this the preview panel collapses
	sessPreviewWidth    = 42
	sessMargin          = 2 // left margin of every line
)

// sessCol is one list column. Elastic columns (agent, project) share the
// width the fixed ones leave over.
type sessCol struct {
	id    string
	title string
	w     int
}

// sessColumnSpecs lists the list columns in display order, with the order in
// which they are dropped when the list is too narrow (lowest number first;
// 0 = never dropped).
var sessColumnSpecs = []struct {
	id, title string
	w         int
	drop      int
}{
	{"age", "AGE", 9, 0},
	{"agent", "AGENT", 0, 0},
	{"project", "PROJECT", 0, 0},
	{"model", "MODEL", 8, 8},
	{"ticket", "#", 6, 5},
	{"dur", "DUR", 7, 6},
	{"cost", "COST", 8, 0},
	{"in", "IN", 7, 3},
	{"out", "OUT", 7, 2},
	{"tpm", "T/M", 7, 1},
	{"ctx", "CTX", 11, 4},
	{"turns", "TURNS", 6, 7},
}

const (
	sessCursorW    = 3
	sessElasticMin = 8
)

// sessColumns picks the columns that fit in lw columns and sizes the elastic
// ones. Fixed columns drop in priority order until agent and project keep at
// least sessElasticMin each.
func sessColumns(lw int) []sessCol {
	dropped := map[string]bool{}
	fixed := func() int {
		n := sessCursorW
		for _, c := range sessColumnSpecs {
			if !dropped[c.id] {
				n += c.w
			}
		}
		return n
	}
	for lw-fixed() < 2*sessElasticMin {
		best, bestDrop := "", 0
		for _, c := range sessColumnSpecs {
			if c.drop > 0 && !dropped[c.id] && (best == "" || c.drop < bestDrop) {
				best, bestDrop = c.id, c.drop
			}
		}
		if best == "" {
			break
		}
		dropped[best] = true
	}
	surplus := lw - fixed()
	agentW, projW := sessElasticMin, sessElasticMin
	if surplus > 2*sessElasticMin {
		extra := surplus - 2*sessElasticMin
		agentW = sessElasticMin + extra/2
		projW = sessElasticMin + extra - extra/2
	}
	var cols []sessCol
	for _, c := range sessColumnSpecs {
		if dropped[c.id] {
			continue
		}
		w := c.w
		switch c.id {
		case "agent":
			w = agentW
		case "project":
			w = projW
		}
		cols = append(cols, sessCol{id: c.id, title: c.title, w: w})
	}
	return cols
}

// showPreview reports whether the preview panel has room.
func (m SessionsModel) showPreview() bool {
	return m.termWidth >= sessPreviewMinWidth
}

// listWidth is the width of the list pane inside the content width.
func (m SessionsModel) listWidth() int {
	cw := m.contentWidth()
	if m.showPreview() {
		return cw - sessPreviewWidth - 3
	}
	return cw
}

// listHeight is the number of body rows in the list view.
func (m SessionsModel) listHeight() int {
	h := m.termHeight - sessChromRows
	if h < 1 {
		h = 1
	}
	return h
}

// ── Line model ───────────────────────────────────────────────────────────────

// sessLine is one line of the list body: a date-group header (entry -1) or a
// session row.
type sessLine struct {
	entry int
	group string
}

// listLines lays the entries out with a header at every date-group change.
func (m SessionsModel) listLines() []sessLine {
	var lines []sessLine
	last := ""
	for i, e := range m.entries {
		if g := dateGroup(e.Session.StartTime); g != last {
			last = g
			lines = append(lines, sessLine{entry: -1, group: g})
		}
		lines = append(lines, sessLine{entry: i})
	}
	return lines
}

// cursorLine is the index in listLines of the selected row (its group header
// when the row is the first of the group, so the header scrolls into view).
func (m SessionsModel) cursorLine() int {
	lines := m.listLines()
	for i, l := range lines {
		if l.entry == m.cursor {
			if i > 0 && lines[i-1].entry < 0 {
				return i - 1
			}
			return i
		}
	}
	return 0
}

// syncScroll keeps the selected row inside the visible window of the list.
func (m *SessionsModel) syncScroll() {
	h := m.listHeight()
	total := len(m.listLines())
	cl := m.cursorLine()
	if cl < m.listTop {
		m.listTop = cl
	}
	if cl >= m.listTop+h {
		m.listTop = cl - h + 1
	}
	if maxTop := total - h; m.listTop > maxTop {
		m.listTop = maxTop
	}
	if m.listTop < 0 {
		m.listTop = 0
	}
}

// ── Cells ────────────────────────────────────────────────────────────────────

// sessCell renders content in style within exactly width columns: cut by
// visible width first, so wide runes and long names never wrap.
func sessCell(style lipgloss.Style, width int, content, bg string) string {
	if width < 1 {
		return ""
	}
	content = ansi.Truncate(content, width-1, "…")
	s := style.UnsetWidth().Width(width).MaxWidth(width)
	if bg != "" {
		s = s.Background(lipgloss.Color(bg))
	}
	return s.Render(content)
}

// rowCells renders entry e as a list row of width lw. bg tints the row
// (selected or hovered); marker draws the selection arrow.
func (m SessionsModel) rowCells(e SessionEntry, cols []sessCol, lw int, bg string, selected bool) string {
	t := theme.Current()
	cursor := "   "
	if selected {
		cursor = " " + cursorActiveStyle.Render("▸") + " "
	}
	if bg != "" {
		cursor = lipgloss.NewStyle().Background(lipgloss.Color(bg)).Render(cursor)
	}
	var b strings.Builder
	b.WriteString(cursor)
	for _, c := range cols {
		switch c.id {
		case "age":
			b.WriteString(sessCell(sessAgeStyle, c.w, e.Age, bg))
		case "agent":
			st := sessAgentStyle
			if selected {
				st = sessAgentSelectedStyle
			}
			b.WriteString(sessCell(st, c.w, e.Agent, bg))
		case "project":
			b.WriteString(sessCell(sessProjectStyle, c.w, e.Project, bg))
		case "model":
			b.WriteString(sessCell(sessModelStyle, c.w, e.ModelShort, bg))
		case "ticket":
			s := "—"
			if e.TicketNumber > 0 {
				s = fmt.Sprintf("#%d", e.TicketNumber)
			}
			b.WriteString(sessCell(sessTicketStyle, c.w, s, bg))
		case "dur":
			b.WriteString(sessCell(sessDimStyle, c.w, fmtDuration(e.Duration), bg))
		case "cost":
			b.WriteString(sessCell(sessCostStyle, c.w, e.costStr(), bg))
		case "in":
			b.WriteString(sessCell(sessTokenInStyle, c.w, fmtTokensShort(e.InputTokens), bg))
		case "out":
			b.WriteString(sessCell(sessTokenOutStyle, c.w, fmtTokensShort(e.OutputTokens), bg))
		case "tpm":
			b.WriteString(sessCell(sessTpmStyle, c.w, fmtTokensShort(e.TokPerMin), bg))
		case "ctx":
			b.WriteString(renderCtxBarCell(e.CtxPct, &t, c.w, bg != "", bg))
		case "turns":
			st := lipgloss.NewStyle().Foreground(lipgloss.Color(turnsColor(e.Turns, &t)))
			b.WriteString(sessCell(st, c.w, fmt.Sprintf("%d", e.Turns), bg))
		}
	}
	return ansi.Truncate(b.String(), lw, "")
}

// listBody renders the visible rows of the list: exactly h lines of lw
// columns each, rows wrapped in their click zones.
func (m SessionsModel) listBody(lw, h int) []string {
	cols := sessColumns(lw)
	lines := m.listLines()
	out := make([]string, 0, h)

	if len(m.entries) == 0 {
		msg := "No sessions found"
		if m.loading {
			msg = "Loading sessions…"
		}
		out = append(out, sessDimStyle.Render("   "+ansi.Truncate(msg, lw-3, "…")))
	}

	top := m.listTop
	if top > len(lines) {
		top = 0
	}
	for i := top; i < len(lines) && len(out) < h; i++ {
		l := lines[i]
		if l.entry < 0 {
			out = append(out, sessSectionStyle.Render(ansi.Truncate("  "+l.group, lw, "")))
			continue
		}
		id := fmt.Sprintf("row:%d", l.entry)
		selected := l.entry == m.cursor
		bg := ""
		if selected || id == m.hoverRow {
			bg = sessHighlightBg
		}
		row := m.rowCells(m.entries[l.entry], cols, lw, bg, selected)
		if bg != "" {
			row = lipgloss.NewStyle().Background(lipgloss.Color(bg)).Width(lw).MaxWidth(lw).Render(row)
		}
		out = append(out, zoneMark(id, row))
	}
	for len(out) < h {
		out = append(out, "")
	}
	return out
}

// ── Chrome ───────────────────────────────────────────────────────────────────

// button renders a click-zone button; it reverses while hovered.
func (m SessionsModel) button(id, label string, st lipgloss.Style) string {
	return zoneMark(id, tkButton(label, st, false, m.hover == id))
}

// link renders a text click target; it underlines while hovered.
func (m SessionsModel) link(id, text string, st lipgloss.Style) string {
	if m.hover == id {
		st = st.Underline(true)
	}
	return zoneMark(id, st.Render(text))
}

// chip renders a "[ text ]" chip that sends key on click; clear, when set, is
// the zone of a trailing × that clears it.
func (m SessionsModel) chip(id, text string, active bool, clear string) string {
	t := theme.Current()
	st := fgStyle(t.Muted)
	if active {
		st = fgStyle(t.Accent).Bold(true)
	}
	if m.hover == id {
		st = st.Underline(true)
	}
	s := zoneMark(id, st.Render("[ "+text+" ]"))
	if clear != "" {
		x := fgStyle(t.Muted)
		if m.hover == clear {
			x = fgStyle(t.Red).Bold(true)
		}
		s += zoneMark(clear, x.Render("×"))
	}
	return s
}

// titleBar renders the accent title with an optional right-aligned note and a
// leading "← back" link.
func (m SessionsModel) titleBar(title, right string, back bool) string {
	cw := m.contentWidth()
	prefix := ""
	if back {
		prefix = m.link("key:esc@back", "← back", fgStyle(theme.Current().Accent).Bold(true)) + "  "
	}
	barW := cw - lipgloss.Width(prefix)
	if barW < 8 {
		barW = 8
	}
	inner := barW - 4 // the style's horizontal padding
	text := title
	if right != "" && lipgloss.Width(title)+lipgloss.Width(right)+2 <= inner {
		text = title + strings.Repeat(" ", inner-lipgloss.Width(title)-lipgloss.Width(right)) + right
	}
	text = ansi.Truncate(text, inner, "…")
	return prefix + sessTitleStyle.Align(lipgloss.Left).Width(barW).MaxWidth(barW).Render(text)
}

// chipRow renders the filter chips (or the loading / error note).
func (m SessionsModel) chipRow() string {
	cw := m.contentWidth()
	switch {
	case m.loading:
		return sessSubtitleStyle.Render("Loading sessions…")
	case m.err != nil:
		return sessSubtitleStyle.Render(ansi.Truncate(fmt.Sprintf("Error: %v", m.err), cw, "…"))
	}
	build := func(compact bool) string {
		agent := "all agents"
		if m.agentIdx >= 0 && m.filterAgent != "" {
			agent = m.filterAgent
		}
		proj := "all projects"
		if m.filterProject != "" {
			proj = m.filterProject
		}
		if compact {
			agent = ansi.Truncate(agent, 14, "…")
			proj = ansi.Truncate(proj, 14, "…")
		}
		var parts []string
		agentClear, projClear := "", ""
		if m.agentIdx >= 0 && m.filterAgent != "" {
			agentClear = "chip:agent-clear"
		}
		if m.filterProject != "" {
			projClear = "chip:project-clear"
		}
		parts = append(parts, m.chip("key:a@chip", agent+" ▾", agentClear != "", agentClear))
		parts = append(parts, m.chip("key:p@chip", proj+" ▾", projClear != "", projClear))
		if m.searchQuery != "" {
			q := m.searchQuery
			if compact {
				q = ansi.Truncate(q, 12, "…")
			}
			parts = append(parts, m.chip("key:/@chip", "/ "+q, true, "chip:search-clear"))
		} else {
			parts = append(parts, m.chip("key:/@chip", "/ search", false, ""))
		}
		return strings.Join(parts, " ")
	}
	row := build(false)
	if lipgloss.Width(row) > cw {
		row = build(true)
	}
	return ansi.Truncate(row, cw, "…")
}

// totalsLine summarises the entries shown.
func (m SessionsModel) totalsLine() string {
	if len(m.entries) == 0 {
		return sessDimStyle.Render("0 sessions")
	}
	var totalDuration time.Duration
	var totalCost float64
	var unpriced int
	var totalIn, totalOut, totalCacheRead, totalCacheCreate int
	for _, e := range m.entries {
		totalDuration += e.Duration
		if e.CostUnknown {
			unpriced++
		} else {
			totalCost += e.CostUSD
		}
		totalIn += e.InputTokens
		totalOut += e.OutputTokens
		totalCacheRead += e.CacheReadTokens
		totalCacheCreate += e.CacheCreateTokens
	}
	cached := ""
	if c := totalCacheRead + totalCacheCreate; c > 0 {
		cached = fmt.Sprintf(" (+%s cached)", fmtTokensShort(c))
	}
	cost := sessions.FormatCost(totalCost)
	if unpriced > 0 {
		cost += fmt.Sprintf(" (%d unpriced)", unpriced)
	}
	s := fmt.Sprintf("%d sessions · %s · %s · %s in%s / %s out",
		len(m.entries), fmtDuration(totalDuration), cost,
		fmtTokensShort(totalIn), cached, fmtTokensShort(totalOut))
	return sessDimStyle.Render(ansi.Truncate(s, m.contentWidth(), "…"))
}

// hintBar renders the bottom line. In search mode the input replaces it.
func (m SessionsModel) hintBar(hints []tkHint) string {
	cw := m.contentWidth()
	if m.searchMode {
		dot := "  " + menuHintDotStyle.Render("·") + "  "
		line := menuHintKeyStyle.Render("/") + " " + m.searchInput.View() + dot +
			zoneMark("key:enter@hint", menuHintKeyStyle.Render("enter")+" "+menuHintLabelStyle.Render("confirm")) + dot +
			zoneMark("key:esc@hint", menuHintKeyStyle.Render("esc")+" "+menuHintLabelStyle.Render("clear"))
		return ansi.Truncate(line, cw, "…")
	}
	return clickableHintLine(hints, m.hover, cw)
}

// ── Views ────────────────────────────────────────────────────────────────────

// View renders the frame, records its click zones and strips the markers.
func (m SessionsModel) View() tea.View {
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
// View has been recorded yet).
func (m SessionsModel) renderZones() zoneList {
	_, z := scanZones(m.render())
	return z
}

// render composes the frame, zone markers still in it. The help overlay owns
// the mouse, so the view behind it loses its zones.
func (m SessionsModel) render() string {
	var bg string
	if m.viewMode == 1 {
		bg = m.viewDetail()
	} else {
		bg = m.viewList()
	}
	if m.helpOpen {
		box := zoneMarkLines("modal:box", tkModalView(m.helpModal()))
		bg = compositeOverlay(faint(stripZones(bg)), box, m.termWidth, m.termHeight)
	}
	return m.clampFrame(bg)
}

// clampFrame guarantees the frame is exactly termHeight lines with none wider
// than termWidth, whatever the views above produced.
func (m SessionsModel) clampFrame(s string) string {
	lines := strings.Split(s, "\n")
	h, w := m.termHeight, m.termWidth
	if h > 0 {
		if len(lines) > h {
			lines = lines[:h]
		}
		for len(lines) < h {
			lines = append(lines, "")
		}
	}
	if w > 0 {
		for i, l := range lines {
			if lipgloss.Width(l) > w {
				lines[i] = ansi.Truncate(l, w, "")
			}
		}
	}
	return strings.Join(lines, "\n")
}

func margin(lines ...string) string {
	for i, l := range lines {
		lines[i] = strings.Repeat(" ", sessMargin) + l
	}
	return strings.Join(lines, "\n")
}

func (m SessionsModel) listHints() []tkHint {
	return []tkHint{
		{key: "j/k", label: "navigate", send: "j", pin: true},
		{key: "enter", label: "detail", pin: true},
		{key: "r", label: "resume", pin: true},
		{key: "l", label: "log"},
		{key: "t", label: "turns"},
		{key: "a", label: "agent"},
		{key: "p", label: "project"},
		{key: "/", label: "search"},
		{key: "?", label: "keys", pin: true},
		{key: "q", label: "quit", pin: true},
	}
}

// viewList renders the list view.
func (m SessionsModel) viewList() string {
	lw := m.listWidth()
	h := m.listHeight()

	pageInfo := ""
	if !m.loading && m.err == nil && len(m.entries) > 0 {
		pageInfo = fmt.Sprintf("%d of %d", m.cursor+1, len(m.entries))
	}
	var out []string
	out = append(out, margin(m.titleBar("Sessions", pageInfo, false)))
	out = append(out, margin(m.chipRow()))

	if m.loading || m.err != nil {
		// Keep the frame height stable while there is nothing to list.
		out = append(out, margin(""), margin(""))
		for i := 0; i < h; i++ {
			out = append(out, "")
		}
		out = append(out, margin(""), margin(m.hintBar(m.listHints())))
		return strings.Join(out, "\n")
	}

	cols := sessColumns(lw)
	var hdr strings.Builder
	hdr.WriteString(strings.Repeat(" ", sessCursorW))
	for _, c := range cols {
		hdr.WriteString(sessColHeaderStyle.Render(fmt.Sprintf("%-*s", c.w, ansi.Truncate(c.title, c.w, ""))))
	}
	header := ansi.Truncate(hdr.String(), lw, "")
	rule := sessRuleStyle.Render(strings.Repeat("╌", lw))

	body := m.listBody(lw, h)
	var preview []string
	if m.showPreview() {
		preview = m.previewPanel(sessPreviewWidth, h+2)
		divider := sessRuleStyle.Render("│")
		hdrPad := sessPadRight(header, lw)
		out = append(out, margin(zoneMark("pane:list", hdrPad)+" "+divider+" "+zoneMark("pane:preview", preview[0])))
		out = append(out, margin(zoneMark("pane:list", rule)+" "+divider+" "+zoneMark("pane:preview", preview[1])))
		for i, l := range body {
			out = append(out, margin(zoneMark("pane:list", sessPadRight(l, lw))+" "+divider+" "+zoneMark("pane:preview", preview[i+2])))
		}
	} else {
		out = append(out, margin(header), margin(rule))
		for _, l := range body {
			out = append(out, margin(zoneMark("pane:list", sessPadRight(l, lw))))
		}
	}
	out = append(out, margin(m.totalsLine()), margin(m.hintBar(m.listHints())))
	return strings.Join(out, "\n")
}

// padRight pads s with spaces to width w (visible), or cuts it to w.
func sessPadRight(s string, w int) string {
	n := lipgloss.Width(s)
	if n > w {
		return ansi.Truncate(s, w, "")
	}
	return s + strings.Repeat(" ", w-n)
}

// ── Preview and detail ───────────────────────────────────────────────────────

// sessAction is one preview/detail button and the key it runs.
type sessAction struct{ label, key string }

func (m SessionsModel) actions() []sessAction {
	turns := "Turns"
	if m.viewMode == 1 && m.detailTab == 1 {
		turns = "Summary"
	}
	return []sessAction{
		{"Resume", "r"},
		{"Open log", "l"},
		{turns, "t"},
		{"Detail", "enter"},
	}
}

// actionButtons flows the action buttons into lines of at most width columns.
// The detail view omits the Detail button (it is already there).
func (m SessionsModel) actionButtons(where string, width int) []string {
	var lines []string
	cur, curW := "", 0
	for _, a := range m.actions() {
		if where == "detail" && a.key == "enter" {
			continue
		}
		b := m.button("key:"+a.key+"@"+where, a.label, tkButtonText())
		bw := lipgloss.Width(b)
		if curW > 0 && curW+1+bw > width {
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

// infoLines renders the selected session as single-column lines of at most
// width columns: details, token usage, user messages and tool calls.
func (m SessionsModel) infoLines(width int) []string {
	if len(m.entries) == 0 || m.cursor >= len(m.entries) || m.entries[m.cursor].Summary == nil {
		return []string{sessDimStyle.Render("No session selected")}
	}
	entry := m.entries[m.cursor]
	sum := entry.Summary
	s := entry.Session
	loc := time.Now().Location()
	t := theme.Current()

	labelW := 12
	label := sessDetailLabelStyle.UnsetWidth().Width(labelW)
	kv := func(k string, v string) string {
		return ansi.Truncate(label.Render(k)+v, width, "…")
	}
	section := func(name string) string { return sessDetailSectionStyle.Render("── " + name + " ──") }
	val := func(s string) string { return sessDetailValueStyle.Render(s) }
	col := func(c, s string) string { return fgStyle(c).Render(s) }

	var l []string
	l = append(l, section("Session Details"))
	l = append(l, kv("Agent:", val(s.Agent)))
	l = append(l, kv("Project:", val(shortenDir(s.Project))))
	if sum.GitBranch != "" {
		l = append(l, kv("Branch:", sessDimStyle.Render(sum.GitBranch)))
	}
	if entry.TicketNumber > 0 {
		l = append(l, kv("Ticket:", col(t.Yellow, fmt.Sprintf("#%d", entry.TicketNumber))))
	}
	l = append(l, kv("Started:", val(s.StartTime.In(loc).Format("Mon Jan 2 3:04 PM"))))
	l = append(l, kv("Duration:", val(fmtDuration(entry.Duration))))
	if sum.ModelName != "" {
		l = append(l, kv("Model:", sessDimStyle.Render(sum.ModelName)))
	}
	l = append(l, "", section("Token Usage"))
	l = append(l, kv("Input:", col(t.TokenInput, fmtTokens(sum.TotalInputTokens)+" tokens")))
	l = append(l, kv("Output:", col(t.TokenOutput, fmtTokens(sum.TotalOutputTokens)+" tokens")))
	l = append(l, kv("Cache write:", sessDimStyle.Render(fmtTokens(sum.CacheCreateTokens)+" tokens")))
	l = append(l, kv("Cache read:", sessDimStyle.Render(fmtTokens(sum.CacheReadTokens)+" tokens")))
	l = append(l, kv("Turns:", val(fmt.Sprintf("%d", sum.TurnCount))))
	l = append(l, kv("Cost:", col(t.Green, entry.costStr())))
	if entry.TokPerMin > 0 {
		l = append(l, kv("Tok/min:", col(t.Orange, fmtTokensShort(entry.TokPerMin))))
	}
	if entry.CtxPct > 0 {
		l = append(l, kv("Context:", renderCtxBar(entry.CtxPct, &t)))
	}
	if len(sum.UserMsgs) > 0 {
		l = append(l, "", section("User Messages"))
		for _, msg := range sum.UserMsgs {
			msg = strings.Join(strings.Fields(msg), " ")
			l = append(l, ansi.Truncate(sessBulletStyle.Render("›")+" "+val(msg), width, "…"))
		}
	}
	if len(sum.ToolCalls) > 0 {
		l = append(l, "", section("Tool Calls"))
		type toolCount struct {
			name  string
			count int
		}
		var sorted []toolCount
		for name, count := range sum.ToolBreakdown() {
			sorted = append(sorted, toolCount{name, count})
		}
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].count != sorted[j].count {
				return sorted[i].count > sorted[j].count
			}
			return sorted[i].name < sorted[j].name
		})
		for _, tc := range sorted {
			l = append(l, ansi.Truncate(col(t.Blue, fmt.Sprintf("%-14s", ansi.Truncate(tc.name, 14, "…")))+
				sessDimStyle.Render(fmt.Sprintf("%4d", tc.count)), width, "…"))
		}
	}
	return l
}

// scrolled windows lines to h rows starting at *scroll, clamping *scroll.
func scrolled(lines []string, scroll *int, h int) []string {
	if maxS := len(lines) - h; *scroll > maxS {
		*scroll = maxS
	}
	if *scroll < 0 {
		*scroll = 0
	}
	end := *scroll + h
	if end > len(lines) {
		end = len(lines)
	}
	out := append([]string(nil), lines[*scroll:end]...)
	for len(out) < h {
		out = append(out, "")
	}
	return out
}

// previewPanel renders the side panel: h lines of width columns. The first
// line names the session, then the action buttons, then the scrollable info.
func (m SessionsModel) previewPanel(width, h int) []string {
	var head []string
	if len(m.entries) > 0 && m.cursor < len(m.entries) {
		e := m.entries[m.cursor]
		head = append(head, sessSectionStyle.Render(ansi.Truncate(e.Project+" › "+e.Agent, width, "…")))
		head = append(head, m.actionButtons("preview", width)...)
		head = append(head, "")
	}
	room := h - len(head)
	if room < 1 {
		room = 1
	}
	scroll := m.previewScroll
	body := scrolled(m.infoLines(width), &scroll, room)
	lines := append(head, body...)
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		lines[i] = sessPadRight(l, width)
	}
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return lines
}

func (m SessionsModel) detailHints() []tkHint {
	turns := "turns"
	if m.detailTab == 1 {
		turns = "summary"
	}
	return []tkHint{
		{key: "esc", label: "back", pin: true},
		{key: "t", label: turns},
		{key: "r", label: "resume", pin: true},
		{key: "l", label: "log"},
		{key: "j/k", label: "scroll", send: "j"},
		{key: "?", label: "keys", pin: true},
		{key: "q", label: "quit", pin: true},
	}
}

// viewDetail renders the full-size detail (or turn metrics) view.
func (m SessionsModel) viewDetail() string {
	cw := m.contentWidth()
	if len(m.entries) == 0 || m.cursor >= len(m.entries) {
		return margin(sessDimStyle.Render("No session selected"))
	}
	entry := m.entries[m.cursor]
	s := entry.Session
	loc := time.Now().Location()

	name := "Session Detail"
	sub := fmt.Sprintf("%s  ·  %s", s.StartTime.In(loc).Format("Mon Jan 2, 2006 3:04 PM"), fmtDuration(entry.Duration))
	var lines []string
	if m.detailTab == 1 {
		name = "Turn Metrics"
		sub = fmt.Sprintf("%d turns  ·  %s total", entry.Turns, entry.costStr())
		lines = m.turnMetricLines(cw)
	} else {
		lines = m.infoLines(cw)
	}

	var out []string
	out = append(out, margin(m.titleBar(fmt.Sprintf("%s  ·  %s › %s", name, entry.Project, entry.Agent), "", true)))
	out = append(out, margin(sessSubtitleStyle.Render(ansi.Truncate(sub, cw, "…"))))
	out = append(out, margin(sessRuleStyle.Render(strings.Repeat("╌", cw))))

	btns := strings.Join(m.actionButtons("detail", 1<<20), "")
	h := m.termHeight - len(out) - 2
	if h < 1 {
		h = 1
	}
	scroll := m.previewScroll
	for _, l := range scrolled(lines, &scroll, h) {
		out = append(out, margin(zoneMark("pane:body", sessPadRight(l, cw))))
	}
	out = append(out, margin(ansi.Truncate(btns, cw, "")))
	out = append(out, margin(clickableHintLine(m.detailHints(), m.hover, cw)))
	return strings.Join(out, "\n")
}

// turnMetricLines renders the per-turn table of the selected session.
func (m SessionsModel) turnMetricLines(width int) []string {
	entry := m.entries[m.cursor]
	sum := entry.Summary
	t := theme.Current()
	if sum == nil || len(sum.TurnMetrics) == 0 {
		return []string{sessDimStyle.Render("No turn metrics available")}
	}
	sampled := sessions.SampleTurnMetrics(sum.TurnMetrics, 15)

	avgCostPerTurn := 0.0
	if sum.TurnCount > 0 {
		avgCostPerTurn = entry.CostUSD / float64(sum.TurnCount)
	}
	// Sweet spot: last turn before cost/turn exceeded 2x average.
	sweetSpotTurn := -1
	for _, tm := range sum.TurnMetrics {
		c, _ := sessions.TurnCost(tm, turnModel(tm, sum))
		if c > 2*avgCostPerTurn {
			break
		}
		sweetSpotTurn = tm.Turn
	}

	rule := sessRuleStyle.Render(strings.Repeat("╌", width))
	l := []string{
		sessColHeaderStyle.Render(fmt.Sprintf("%-6s", "TURN")) + sessColHeaderStyle.Render(fmt.Sprintf("%-9s", "INPUT")) +
			sessColHeaderStyle.Render(fmt.Sprintf("%-9s", "OUTPUT")) + sessColHeaderStyle.Render(fmt.Sprintf("%-9s", "COST")) +
			sessColHeaderStyle.Render(fmt.Sprintf("%-8s", "CTX%")),
		rule,
	}
	for _, tm := range sampled {
		cost, costOK := sessions.TurnCost(tm, turnModel(tm, sum))
		ctxPct := tokens.CtxPct(sum.ModelName, tm.InputTokens+tm.CacheRead+tm.CacheCreate)
		annotation := ""
		if avgCostPerTurn > 0 && cost > 2*avgCostPerTurn {
			annotation = fgStyle(t.Red).Render("  ← cost/turn 2x avg")
		}
		ctxColor := t.Green
		if ctxPct >= 80 {
			ctxColor = t.Red
		} else if ctxPct >= 60 {
			ctxColor = t.Yellow
		}
		line := fgStyle(t.Muted).Render(fmt.Sprintf("%-6d", tm.Turn)) +
			fgStyle(t.TokenInput).Render(fmt.Sprintf("%-9s", fmtTokensShort(tm.InputTokens))) +
			fgStyle(t.TokenOutput).Render(fmt.Sprintf("%-9s", fmtTokensShort(tm.OutputTokens))) +
			fgStyle(t.Green).Render(fmt.Sprintf("%-9s", sessions.FormatCostOK(cost, costOK))) +
			fgStyle(ctxColor).Render(fmt.Sprintf("%-8s", fmt.Sprintf("%d%%", ctxPct))) + annotation
		l = append(l, ansi.Truncate(line, width, "…"))
	}
	l = append(l, rule)
	if sweetSpotTurn > 0 {
		l = append(l, sessDimStyle.Render(ansi.Truncate(fmt.Sprintf("Sweet spot: ~turn %d (cost/turn doubled after)", sweetSpotTurn), width, "…")))
	}
	return l
}

// turnModel is the model a turn ran on: its own when recorded, else the
// session's last-seen model.
func turnModel(tm sessions.TurnMetric, sum *sessions.Summary) string {
	if tm.Model != "" {
		return tm.Model
	}
	return sum.ModelName
}

// costStr renders the entry's cost: "?" when its model has no known price.
func (e SessionEntry) costStr() string {
	return sessions.FormatCostOK(e.CostUSD, !e.CostUnknown)
}

// dateGroup returns the period group label for a given time.
func dateGroup(t time.Time) string {
	now := time.Now()
	y1, mo1, d1 := now.Date()
	y2, mo2, d2 := t.In(time.Local).Date()
	if y1 == y2 && mo1 == mo2 && d1 == d2 {
		return "Today"
	}
	yesterday := now.AddDate(0, 0, -1)
	y3, mo3, d3 := yesterday.Date()
	if y2 == y3 && mo2 == mo3 && d2 == d3 {
		return "Yesterday"
	}
	weekAgo := now.AddDate(0, 0, -7)
	if t.After(weekAgo) {
		return "This Week"
	}
	return "Older"
}
