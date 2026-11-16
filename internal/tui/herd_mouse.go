package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// herdClickAction is what a mouse click on the hub should do. Clicks never
// open an agent; only Enter (or J) does.
type herdClickAction int

const (
	herdClickIgnore herdClickAction = iota // nothing happens
	herdClickSelect                        // move the cursor only, like j/k
)

// herdListTop returns the screen row where the list (or sidebar) begins: the
// number of rows the header renders. Derived from the rendered header rather
// than hardcoded so style changes cannot skew click targets.
func (m HerdModel) herdListTop() int {
	return strings.Count(m.renderHeader(m.contentWidth()), "\n")
}

// herdListRows is the number of screen rows the list area spans.
func (m HerdModel) herdListRows() int {
	if useSplitLayout(m.termWidth) {
		return m.splitPane.paneHeight()
	}
	return m.visibleLines()
}

// clickable reports whether the hub is showing real list data (not a
// loading/error/empty state).
func (m HerdModel) clickable() bool {
	_, isStatus := m.renderStatusOrEmpty()
	return !isStatus && len(m.lines) > 0
}

// lineAt maps a screen coordinate to a line index in m.lines. inDetail is true
// (with idx -1) when the point is inside the split layout's main panel. ok is
// false when the point is outside the list and detail areas, or the layout is
// in a loading/error/empty state. A returned idx may be a divider line.
func (m HerdModel) lineAt(x, y int) (idx int, inDetail bool, ok bool) {
	if !m.clickable() || x < 0 {
		return -1, false, false
	}
	row := y - m.herdListTop()
	if row < 0 || row >= m.herdListRows() {
		return -1, false, false
	}
	if !useSplitLayout(m.termWidth) {
		idx = m.scrollOffset + row
		if idx >= len(m.lines) {
			return -1, false, false
		}
		return idx, false, true
	}
	if x >= m.splitPane.ListWidth() {
		if x >= m.splitPane.termWidth {
			return -1, false, false
		}
		return -1, true, true
	}
	idx = m.splitPane.ListScroll() + row
	if idx >= len(m.lines) {
		return -1, false, false
	}
	return idx, false, true
}

// clickDecision resolves a left click at (x, y) to a target line and action.
// idx is -1 when the click is ignored, including clicks in the detail panel.
func (m HerdModel) clickDecision(x, y int) (idx int, action herdClickAction) {
	idx, inDetail, ok := m.lineAt(x, y)
	if !ok || inDetail {
		return -1, herdClickIgnore
	}
	switch m.lines[idx].kind {
	case herdLineRow, herdLineHeader, herdLinePlaceholder:
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
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	idx, action := m.clickDecision(msg.X, msg.Y)
	if action != herdClickSelect {
		return m, nil
	}
	m.selectLine(idx)
	return m, m.maybeCaptureCmd()
}

func (m HerdModel) handleWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if !m.clickable() {
		return m, nil
	}
	if _, _, ok := m.lineAt(msg.X, msg.Y); !ok {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseWheelUp:
		m.moveCursor(-1)
	case tea.MouseWheelDown:
		m.moveCursor(1)
	default:
		return m, nil
	}
	return m, m.maybeCaptureCmd()
}
