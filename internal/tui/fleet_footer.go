package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ── Transient info messages ──────────────────────────────────────────────────

// fleetInfoTTL is how long a success notice stays in the footer. The hub's
// poll tick (fleetPollInterval) clears it, so it lasts about this long.
const fleetInfoTTL = 4 * time.Second

// fleetInfoGlyph leads a success notice.
const fleetInfoGlyph = "✓ "

// setProgress shows an in-flight notice ("launching agent in x…"). It does
// not expire: the outcome replaces it.
func (m *FleetModel) setProgress(msg string) {
	m.infoMsg, m.infoOK, m.infoUntil = msg, false, time.Time{}
}

// setSuccess shows a finished notice that expires after fleetInfoTTL.
func (m *FleetModel) setSuccess(msg string) {
	m.infoMsg, m.infoOK, m.infoUntil = msg, true, fleetNow().Add(fleetInfoTTL)
}

// clearInfo drops any info notice.
func (m *FleetModel) clearInfo() {
	m.infoMsg, m.infoOK, m.infoUntil = "", false, time.Time{}
}

// expireInfo clears a success notice whose time is up; run on every poll tick.
func (m *FleetModel) expireInfo(now time.Time) {
	if m.infoOK && !now.Before(m.infoUntil) {
		m.clearInfo()
	}
}

// projectLabel names session's group by its project for notices, falling
// back to the tmux session name when the group or its project is unknown.
func (m *FleetModel) projectLabel(session string) string {
	if g := m.groupForSession(session); g != nil && g.Project != "" {
		return g.Project
	}
	return session
}

// ── Footer ───────────────────────────────────────────────────────────────────

// fleetHint is one footer hint: the key as shown and its label.
type fleetHint struct{ key, label string }

// fleetFooterHints are the footer's hints, most important first; the last one
// ("? more") is never dropped. fleetHelpItems lists everything else.
var fleetFooterHints = []fleetHint{
	{"j/k", "move"},
	{"⏎", "focus"},
	{"b", "blocked"},
	{"n", "new"},
	{fleetSweepKey, "sweep"},
	{"x", "close"},
	{"q", "leave"},
	{"?", "more"},
}

// fleetNewHints replace the footer while the shell | agent selector is armed.
var fleetNewHints = []fleetHint{
	{"s", "shell"},
	{"a", "agent"},
	{"t", "schedule"},
	{"esc", "cancel"},
}

const fleetHintSep = " · "

func renderHint(h fleetHint) string {
	return menuHintKeyStyle.Render(h.key) + " " + menuHintLabelStyle.Render(h.label)
}

// fitHints renders hints on one line no wider than cw, dropping hints from
// the right (but never the last one) until it fits, and centres it.
func fitHints(hints []fleetHint, cw int) string {
	keep := append([]fleetHint(nil), hints...)
	join := func(hs []fleetHint) string {
		parts := make([]string, len(hs))
		for i, h := range hs {
			parts[i] = renderHint(h)
		}
		return strings.Join(parts, menuHintDotStyle.Render(fleetHintSep))
	}
	line := join(keep)
	for lipgloss.Width(line) > cw && len(keep) > 1 {
		keep = append(keep[:len(keep)-2], keep[len(keep)-1])
		line = join(keep)
	}
	line = truncateToWidth(line, cw)
	return tableStyles.HintBar.MarginTop(0).Width(cw).Render(line)
}

// renderFooter renders the message line(s) and the one-line hint bar. The
// message line is the context menu, the "?" key reference (which may take
// several lines; liveHintRows accounts for them), an error, or a notice.
func (m FleetModel) renderFooter(cw int) string {
	return m.renderOverflowBelow(cw) + m.footerBody(cw)
}

// footerBody is the footer without the overflow indicator: a line break, the
// message line(s) and the hint bar.
func (m FleetModel) footerBody(cw int) string {
	var b strings.Builder
	b.WriteString("\n")
	switch {
	case m.newSessionOpen():
		b.WriteString(m.newSessionView(cw))
	case m.menuOpen && m.menuHelp:
		lines, _ := fleetHelpLayout(fleetHelpItems(), cw)
		b.WriteString(strings.Join(lines, "\n"))
	case m.menuOpen:
		text, _ := menuItemSpans(m.fleetMenuItems(m.menuLine))
		b.WriteString(fleetBlockedStyle.Render(truncateToWidth(text+"   esc", cw)))
	case m.sweepArmed():
		b.WriteString(fleetBlockedStyle.Render("  " + m.sweepPrompt(cw-2)))
	case m.schedConfirm != nil:
		b.WriteString(fleetBlockedStyle.Render("  " + truncateToWidth(m.schedConfirmPrompt(), cw-2)))
	case m.jumpErr != "":
		b.WriteString(fleetErrStyle.Render("  " + truncateToWidth(m.jumpErr, cw-2)))
	case m.infoMsg != "" && m.infoOK:
		b.WriteString(fleetSuccessStyle.Render("  " + truncateToWidth(fleetInfoGlyph+m.infoMsg, cw-2)))
	case m.infoMsg != "":
		b.WriteString(fleetDimStyle.Render("  " + truncateToWidth(m.infoMsg, cw-2)))
	}
	b.WriteString("\n")
	if m.newSessionOpen() && m.jumpErr != "" {
		// The message line holds the prompt: its error takes the hint bar.
		b.WriteString(fleetErrStyle.Render("  " + truncateToWidth(m.jumpErr, cw-2)))
		return b.String()
	}
	hints := fleetFooterHints
	switch {
	case m.newSessionOpen():
		hints = m.newSessionHints()
	case m.renameOpen():
		hints = fleetRenameHints
	case m.newArmed():
		hints = fleetNewHints
	case m.sweepArmed():
		hints = fleetSweepHints
	case m.schedFormOpen() && (m.schedForm.review || m.schedForm.discard):
		hints = fleetSchedYesNoHints
		if m.schedForm.review {
			hints = fleetSchedReviewHints
		}
	case m.schedFormOpen():
		hints = fleetSchedFormHints
	case m.schedConfirm != nil:
		hints = fleetSchedYesNoHints
	case m.selectedSchedLineValue() != nil:
		hints = m.schedHints()
	}
	b.WriteString(fitHints(hints, cw))
	return b.String()
}

// liveHintRows is how many rows the footer occupies beyond the rows the
// chrome budget (fleetChromeRows) already reserves: the open key reference's
// extra lines. The hint bar itself is always one line.
func (m FleetModel) liveHintRows() int {
	// footer = overflow/blank line + "\n" + message line(s) + "\n" + hint bar
	extra := strings.Count(m.footerBody(m.contentWidth()), "\n") - 2
	return max(extra, 0)
}

// ── "?" key reference ────────────────────────────────────────────────────────

// fleetHelpItem is one binding in the key reference. action is the key runKey
// runs when the entry is clicked ("" for bindings the hub does not own).
type fleetHelpItem struct {
	keys, label, action string
}

// fleetHelpItems lists every hub binding, including the ones the footer drops.
func fleetHelpItems() []fleetHelpItem {
	return []fleetHelpItem{
		{"j/k", "move", "j"},
		{"⏎/tab", "focus live", "enter"},
		{"^b o", "back to list", ""},
		{"b", "next blocked", "b"},
		{"n", "new", "n"},
		{fleetNewSessionKey, "new session", fleetNewSessionKey},
		{"x", "close", "x"},
		{fleetRenameKey, "rename", fleetRenameKey},
		{fleetSweepKey, "sweep project", fleetSweepKey},
		{"J", "home", "J"},
		{"p", "open PR", "p"},
		{"r", "refresh", "r"},
		{"c", "chrome", "c"},
		{fleetActivityKey, "activity pane", fleetActivityKey},
		{"⇧↑/⇧↓", "move project", "shift+up"},
		{"z", "fold / collapse", "z"},
		{"Z", "focus mode", "Z"},
		{"f", "filter", "f"},
		{"o", "sort recent", "o"},
		{fleetShellsKey, "shells", fleetShellsKey},
		{fleetClosedKey, "closed agents", fleetClosedKey},
		{fleetSchedulesKey, "schedules", fleetSchedulesKey},
		{"e/space/g/d/x", "schedule row: edit/pause/run/dup/delete", ""},
		{fleetServeKey, "serve on/off", fleetServeKey},
		{"q", "leave", "q"},
		{"esc", "close help", "?"},
	}
}

// fleetHelpSpan is the clickable extent of one key-reference entry: its line
// (0 = the footer's message line) and [start, end) columns.
type fleetHelpSpan struct {
	line, start, end int
}

// fleetHelpLayout wraps the key reference into lines no wider than cw (two
// columns of indent, " · " between entries) and returns each entry's span.
func fleetHelpLayout(items []fleetHelpItem, cw int) (lines []string, spans []fleetHelpSpan) {
	const indent = 2
	sepW := lipgloss.Width(fleetHintSep)
	var cur strings.Builder
	col := indent
	line := 0
	cur.WriteString(strings.Repeat(" ", indent))
	for i, it := range items {
		w := lipgloss.Width(it.keys) + 1 + lipgloss.Width(it.label)
		if i > 0 {
			if col+sepW+w > cw-fleetSidebarRightGutter {
				lines = append(lines, cur.String())
				cur.Reset()
				cur.WriteString(strings.Repeat(" ", indent))
				col = indent
				line++
			} else {
				cur.WriteString(menuHintDotStyle.Render(fleetHintSep))
				col += sepW
			}
		}
		cur.WriteString(renderHint(fleetHint{it.keys, it.label}))
		spans = append(spans, fleetHelpSpan{line: line, start: col, end: col + w})
		col += w
	}
	lines = append(lines, cur.String())
	return lines, spans
}

// openHelp opens the "?" key reference in the footer.
func (m *FleetModel) openHelp() {
	m.cancelNewSession()
	m.cancelRename()
	m.cancelClose()
	m.cancelNew()
	m.cancelSweep()
	m.menuOpen, m.menuHelp, m.menuLine = true, true, m.splitPane.Cursor()
	m.ensureCursorVisible() // the footer grew; keep the cursor on screen
}

// closeMenu closes the context menu or key reference.
func (m *FleetModel) closeMenu() {
	m.menuOpen, m.menuHelp = false, false
}

// helpLineAt reports which key-reference line screen row y is, when the
// reference is open.
func (m FleetModel) helpLineAt(y int) (int, bool) {
	if !m.menuOpen || !m.menuHelp {
		return 0, false
	}
	lines, _ := fleetHelpLayout(fleetHelpItems(), m.contentWidth())
	line := y - m.footerMessageY()
	return line, line >= 0 && line < len(lines)
}

// clickHelp runs the key-reference entry under (x, line), closing the
// reference either way.
func (m FleetModel) clickHelp(x, line int) (tea.Model, tea.Cmd) {
	items := fleetHelpItems()
	_, spans := fleetHelpLayout(items, m.contentWidth())
	m.closeMenu()
	m.ensureCursorVisible()
	for i, sp := range spans {
		if sp.line == line && x >= sp.start && x < sp.end && items[i].action != "" && items[i].action != "?" {
			return m.runKey(items[i].action)
		}
	}
	return m, nil
}
