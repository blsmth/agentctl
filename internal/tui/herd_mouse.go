package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// herdClickAction is what a mouse click on the hub should do. Clicks only
// select (the live pane follows the selection). The only side effects are the
// explicit "+ new" / "✕" buttons and the right-click menu.
type herdClickAction int

const (
	herdClickIgnore herdClickAction = iota // nothing happens
	herdClickSelect                        // move the cursor only, like j/k
	herdClickNew                           // header's "+ new" button: launch in that group
	herdClickClose                         // row's "✕" button: arm / confirm close
	herdClickMenu                          // right click: open the context menu
)

// herdListTop returns the screen row where the list (or sidebar) begins: the
// number of rows the header renders. Derived from the rendered header rather
// than hardcoded so style changes cannot skew click targets.
func (m HerdModel) herdListTop() int {
	return strings.Count(m.renderHeader(m.contentWidth()), "\n")
}

// herdListRows is the number of screen rows the list area spans.
func (m HerdModel) herdListRows() int {
	return m.visibleLines()
}

// clickable reports whether the hub is showing real list data (not a
// loading/error/empty state).
func (m HerdModel) clickable() bool {
	_, isStatus := m.renderStatusOrEmpty()
	return !isStatus && len(m.lines) > 0
}

// lineAt maps a screen coordinate to a line index in m.lines. ok is false when
// the point is outside the list area, or the hub is in a loading/error/empty
// state. A returned idx may be a divider line.
func (m HerdModel) lineAt(x, y int) (idx int, ok bool) {
	if !m.clickable() || x < 0 {
		return -1, false
	}
	row := y - m.herdListTop()
	if row < 0 || row >= m.herdListRows() {
		return -1, false
	}
	idx = m.scrollOffset + row
	if idx >= len(m.lines) {
		return -1, false
	}
	return idx, true
}

// listColWidth is the width of the list column lines are rendered at.
func (m HerdModel) listColWidth() int {
	return m.contentWidth()
}

// onNewButton reports whether column x hits a header's right-aligned "+ new"
// button (with one column of slack either side).
func (m HerdModel) onNewButton(x int) bool {
	end := m.listColWidth() - herdSidebarRightGutter
	return x >= end-lipgloss.Width(herdNewLabel)-1 && x < end+1
}

// onCloseButton reports whether column x hits a row's right-aligned "✕"
// (with slack that includes the gutter).
func (m HerdModel) onCloseButton(x int) bool {
	w := m.listColWidth()
	return x >= w-herdSidebarRightGutter-lipgloss.Width(herdCloseLabel)-2 && x < w
}

// clickDecision resolves a left click at (x, y) to a target line and action.
// idx is -1 when the click is ignored.
func (m HerdModel) clickDecision(x, y int) (idx int, action herdClickAction) {
	return m.buttonDecision(x, y, tea.MouseLeft)
}

// buttonDecision resolves a click with the given button. Right click on a
// row or header opens the context menu; left click on a header's "+ new" or a
// row's "✕" triggers that button, anything else on a line just selects it.
func (m HerdModel) buttonDecision(x, y int, button tea.MouseButton) (idx int, action herdClickAction) {
	idx, ok := m.lineAt(x, y)
	if !ok {
		return -1, herdClickIgnore
	}
	l := m.lines[idx]
	switch button {
	case tea.MouseRight:
		if l.kind == herdLineRow || l.kind == herdLineHeader {
			return idx, herdClickMenu
		}
		return idx, herdClickIgnore
	case tea.MouseLeft:
	default:
		return -1, herdClickIgnore
	}
	switch l.kind {
	case herdLineHeader:
		if groupCanLaunch(l.group) && m.onNewButton(x) {
			return idx, herdClickNew
		}
		return idx, herdClickSelect
	case herdLineRow:
		if m.onCloseButton(x) {
			return idx, herdClickClose
		}
		return idx, herdClickSelect
	case herdLinePlaceholder:
		return idx, herdClickSelect
	}
	return idx, herdClickIgnore
}

// selectLine moves the cursor to idx with the same bookkeeping as moveCursor.
func (m *HerdModel) selectLine(idx int) {
	m.splitPane.SetListContent(m.splitPane.listContent, len(m.lines))
	m.splitPane.SetCursor(idx)
	m.syncSelectionFromCursor()
	m.ensureCursorVisible()
}

func (m HerdModel) handleClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button == tea.MouseLeft && m.menuOpen && msg.Y == m.footerMessageY() {
		return m.clickMenu(msg.X)
	}
	idx, action := m.buttonDecision(msg.X, msg.Y, msg.Button)
	switch action {
	case herdClickSelect:
		m.menuOpen = false
		m.selectLine(idx)
		show := m.maybeShowCmd()
		return m, show
	case herdClickNew:
		m.menuOpen = false
		m.selectLine(idx)
		return m, m.launchForLine(idx)
	case herdClickClose:
		m.menuOpen = false
		if m.confirmingRow(m.lines[idx].row) {
			return m, m.confirmClose() // second click on the armed ✕
		}
		m.selectLine(idx)
		m.startClose(idx)
		show := m.maybeShowCmd()
		return m, show
	case herdClickMenu:
		m.openMenu(idx)
		show := m.maybeShowCmd()
		return m, show
	}
	m.menuOpen = false
	return m, nil
}

// clickMenu handles a left click on the footer's context-menu line.
func (m HerdModel) clickMenu(x int) (tea.Model, tea.Cmd) {
	items := m.herdMenuItems(m.menuLine)
	_, spans := menuItemSpans(items)
	for i, sp := range spans {
		if x >= sp[0] && x < sp[1] {
			return m, m.runMenuKey(items[i].key)
		}
	}
	m.menuOpen = false
	return m, nil
}
