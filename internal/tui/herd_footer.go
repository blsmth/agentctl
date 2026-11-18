package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ── Transient info messages ──────────────────────────────────────────────────

// herdInfoTTL is how long a success notice stays in the footer. The hub's
// poll tick (herdPollInterval) clears it, so it lasts about this long.
const herdInfoTTL = 4 * time.Second

// herdInfoGlyph leads a success notice.
const herdInfoGlyph = "✓ "

// setProgress shows an in-flight notice ("launching agent in x…"). It does
// not expire: the outcome replaces it.
func (m *HerdModel) setProgress(msg string) {
	m.infoMsg, m.infoOK, m.infoUntil = msg, false, time.Time{}
}

// setSuccess shows a finished notice that expires after herdInfoTTL.
func (m *HerdModel) setSuccess(msg string) {
	m.infoMsg, m.infoOK, m.infoUntil = msg, true, herdNow().Add(herdInfoTTL)
}

// clearInfo drops any info notice.
func (m *HerdModel) clearInfo() {
	m.infoMsg, m.infoOK, m.infoUntil = "", false, time.Time{}
}

// expireInfo clears a success notice whose time is up; run on every poll tick.
func (m *HerdModel) expireInfo(now time.Time) {
	if m.infoOK && !now.Before(m.infoUntil) {
		m.clearInfo()
	}
}

// projectLabel names session's group by its project for notices, falling
// back to the tmux session name when the group or its project is unknown.
func (m *HerdModel) projectLabel(session string) string {
	if g := m.groupForSession(session); g != nil && g.Project != "" {
		return g.Project
	}
	return session
}

// ── Footer ───────────────────────────────────────────────────────────────────

// herdHint is one footer hint: the key as shown and its label.
type herdHint struct{ key, label string }

// herdFooterHints are the footer's hints, most important first; the last one
// ("? more") is never dropped. herdHelpItems lists everything else.
var herdFooterHints = []herdHint{
	{"j/k", "move"},
	{"⏎", "focus"},
	{"b", "blocked"},
	{"n", "new"},
	{"x", "close"},
	{"q", "leave"},
	{"?", "more"},
}

// herdNewHints replace the footer while the shell | agent selector is armed.
var herdNewHints = []herdHint{
	{"s", "shell"},
	{"a", "agent"},
	{"esc", "cancel"},
}

const herdHintSep = " · "

func renderHint(h herdHint) string {
	return menuHintKeyStyle.Render(h.key) + " " + menuHintLabelStyle.Render(h.label)
}

// fitHints renders hints on one line no wider than cw, dropping hints from
// the right (but never the last one) until it fits, and centres it.
func fitHints(hints []herdHint, cw int) string {
	keep := append([]herdHint(nil), hints...)
	join := func(hs []herdHint) string {
		parts := make([]string, len(hs))
		for i, h := range hs {
			parts[i] = renderHint(h)
		}
		return strings.Join(parts, menuHintDotStyle.Render(herdHintSep))
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
func (m HerdModel) renderFooter(cw int) string {
	return m.renderOverflowBelow(cw) + m.footerBody(cw)
}

// footerBody is the footer without the overflow indicator: a line break, the
// message line(s) and the hint bar.
func (m HerdModel) footerBody(cw int) string {
	var b strings.Builder
	b.WriteString("\n")
	switch {
	case m.menuOpen && m.menuHelp:
		lines, _ := herdHelpLayout(herdHelpItems(), cw)
		b.WriteString(strings.Join(lines, "\n"))
	case m.menuOpen:
		text, _ := menuItemSpans(m.herdMenuItems(m.menuLine))
		b.WriteString(herdBlockedStyle.Render(truncateToWidth(text+"   esc", cw)))
	case m.jumpErr != "":
		b.WriteString(herdErrStyle.Render("  " + truncateToWidth(m.jumpErr, cw-2)))
	case m.infoMsg != "" && m.infoOK:
		b.WriteString(herdSuccessStyle.Render("  " + truncateToWidth(herdInfoGlyph+m.infoMsg, cw-2)))
	case m.infoMsg != "":
		b.WriteString(herdDimStyle.Render("  " + truncateToWidth(m.infoMsg, cw-2)))
	}
	b.WriteString("\n")
	hints := herdFooterHints
	if m.newArmed() {
		hints = herdNewHints
	}
	b.WriteString(fitHints(hints, cw))
	return b.String()
}

// liveHintRows is how many rows the footer occupies beyond the rows the
// chrome budget (herdChromeRows) already reserves: the open key reference's
// extra lines. The hint bar itself is always one line.
func (m HerdModel) liveHintRows() int {
	// footer = overflow/blank line + "\n" + message line(s) + "\n" + hint bar
	extra := strings.Count(m.footerBody(m.contentWidth()), "\n") - 2
	return max(extra, 0)
}

// ── "?" key reference ────────────────────────────────────────────────────────

// herdHelpItem is one binding in the key reference. action is the key runKey
// runs when the entry is clicked ("" for bindings the hub does not own).
type herdHelpItem struct {
	keys, label, action string
}

// herdHelpItems lists every hub binding, including the ones the footer drops.
func herdHelpItems() []herdHelpItem {
	return []herdHelpItem{
		{"j/k", "move", "j"},
		{"⏎/tab", "focus live", "enter"},
		{"^b o", "back to list", ""},
		{"b", "next blocked", "b"},
		{"n", "new", "n"},
		{"x", "close", "x"},
		{"J", "home", "J"},
		{"p", "open PR", "p"},
		{"r", "refresh", "r"},
		{"c", "chrome", "c"},
		{"z", "fold", "z"},
		{"Z", "focus mode", "Z"},
		{"f", "filter", "f"},
		{"q", "leave", "q"},
		{"esc", "close help", "?"},
	}
}

// herdHelpSpan is the clickable extent of one key-reference entry: its line
// (0 = the footer's message line) and [start, end) columns.
type herdHelpSpan struct {
	line, start, end int
}

// herdHelpLayout wraps the key reference into lines no wider than cw (two
// columns of indent, " · " between entries) and returns each entry's span.
func herdHelpLayout(items []herdHelpItem, cw int) (lines []string, spans []herdHelpSpan) {
	const indent = 2
	sepW := lipgloss.Width(herdHintSep)
	var cur strings.Builder
	col := indent
	line := 0
	cur.WriteString(strings.Repeat(" ", indent))
	for i, it := range items {
		w := lipgloss.Width(it.keys) + 1 + lipgloss.Width(it.label)
		if i > 0 {
			if col+sepW+w > cw-herdSidebarRightGutter {
				lines = append(lines, cur.String())
				cur.Reset()
				cur.WriteString(strings.Repeat(" ", indent))
				col = indent
				line++
			} else {
				cur.WriteString(menuHintDotStyle.Render(herdHintSep))
				col += sepW
			}
		}
		cur.WriteString(renderHint(herdHint{it.keys, it.label}))
		spans = append(spans, herdHelpSpan{line: line, start: col, end: col + w})
		col += w
	}
	lines = append(lines, cur.String())
	return lines, spans
}

// openHelp opens the "?" key reference in the footer.
func (m *HerdModel) openHelp() {
	m.cancelClose()
	m.cancelNew()
	m.menuOpen, m.menuHelp, m.menuLine = true, true, m.splitPane.Cursor()
	m.ensureCursorVisible() // the footer grew; keep the cursor on screen
}

// closeMenu closes the context menu or key reference.
func (m *HerdModel) closeMenu() {
	m.menuOpen, m.menuHelp = false, false
}

// helpLineAt reports which key-reference line screen row y is, when the
// reference is open.
func (m HerdModel) helpLineAt(y int) (int, bool) {
	if !m.menuOpen || !m.menuHelp {
		return 0, false
	}
	lines, _ := herdHelpLayout(herdHelpItems(), m.contentWidth())
	line := y - m.footerMessageY()
	return line, line >= 0 && line < len(lines)
}

// clickHelp runs the key-reference entry under (x, line), closing the
// reference either way.
func (m HerdModel) clickHelp(x, line int) (tea.Model, tea.Cmd) {
	items := herdHelpItems()
	_, spans := herdHelpLayout(items, m.contentWidth())
	m.closeMenu()
	m.ensureCursorVisible()
	for i, sp := range spans {
		if sp.line == line && x >= sp.start && x < sp.end && items[i].action != "" && items[i].action != "?" {
			return m.runKey(items[i].action)
		}
	}
	return m, nil
}
