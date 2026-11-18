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
	herdClickIgnore   herdClickAction = iota // nothing happens
	herdClickSelect                          // move the cursor only, like j/k
	herdClickNew                             // header's "+ new" button: arm the shell | agent selector
	herdClickNewShell                        // armed selector's "shell": open a shell window
	herdClickNewAgent                        // armed selector's "agent": launch an agent
	herdClickClose                           // row's "✕" button: arm / confirm close
	herdClickMenu                            // right click: open the context menu
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
	idx, _, ok = m.lineAtSub(x, y)
	return idx, ok
}

// lineAtSub is lineAt that also reports which screen row of the line was hit:
// 0 for a line's own row, 1.. for a selected row's detail lines.
func (m HerdModel) lineAtSub(x, y int) (idx, sub int, ok bool) {
	if !m.clickable() || x < 0 {
		return -1, 0, false
	}
	row := y - m.herdListTop()
	if row < 0 || row >= m.herdListRows() {
		return -1, 0, false
	}
	cw := m.listColWidth()
	for i := m.scrollOffset; i < len(m.lines); i++ {
		h := m.lineHeight(i, cw)
		if row < h {
			return i, row, true
		}
		row -= h
	}
	return -1, 0, false
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

// selectorSpans returns the [start,end) columns of the armed selector's
// "shell" and "agent" labels. The "|" separator belongs to neither.
func (m HerdModel) selectorSpans() (shell, agent [2]int) {
	end := m.listColWidth() - herdSidebarRightGutter
	aw := lipgloss.Width(herdAgentLabel)
	sw := lipgloss.Width(herdShellLabel)
	agent = [2]int{end - aw, end}
	shell = [2]int{agent[0] - lipgloss.Width(herdSelSep) - sw, agent[0] - lipgloss.Width(herdSelSep)}
	return shell, agent
}

// onSelector reports which side of the armed selector column x hits: the
// outer edges get one column of slack, the inner separator's spaces go to the
// adjacent label and the "|" itself hits nothing.
func (m HerdModel) onSelector(x int) herdHoverRegion {
	shell, agent := m.selectorSpans()
	switch {
	case x >= shell[0]-1 && x < shell[1]+1:
		return herdHoverNewShell
	case x >= agent[0]-1 && x < agent[1]+1:
		return herdHoverNewAgent
	}
	return herdHoverNone
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
	idx, sub, ok := m.lineAtSub(x, y)
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
		if groupCanLaunch(l.group) {
			if m.newArmedFor(l.group) {
				switch m.onSelector(x) {
				case herdHoverNewShell:
					return idx, herdClickNewShell
				case herdHoverNewAgent:
					return idx, herdClickNewAgent
				}
			} else if m.onNewButton(x) {
				return idx, herdClickNew
			}
		}
		return idx, herdClickSelect
	case herdLineRow:
		if sub == 0 && m.onCloseButton(x) { // detail lines have no ✕
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
	m.pendingFocus = nil // a manual selection beats a pending launch focus
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
	if action != herdClickNewShell && action != herdClickNewAgent {
		m.cancelNew() // any other click dismisses the selector
	}
	switch action {
	case herdClickSelect:
		m.menuOpen = false
		m.selectLine(idx)
		show := m.maybeShowCmd()
		return m, show
	case herdClickNew:
		m.menuOpen = false
		m.selectLine(idx)
		m.armNew(idx)
		return m, nil
	case herdClickNewShell:
		m.menuOpen = false
		m.cancelNew()
		return m, m.shellForLine(idx)
	case herdClickNewAgent:
		m.menuOpen = false
		m.cancelNew()
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

// herdHoverRegion is which part of a line the pointer is over.
type herdHoverRegion int

const (
	herdHoverNone     herdHoverRegion = iota
	herdHoverBody                     // a row's body (not its ✕)
	herdHoverClose                    // a row's ✕ button
	herdHoverNew                      // a group header's "+ new" button
	herdHoverNewShell                 // armed selector's "shell"
	herdHoverNewAgent                 // armed selector's "agent"
)

// herdHover is the hover target: a line index plus the region on it. The zero
// value (region none) hovers nothing.
type herdHover struct {
	line int
	reg  herdHoverRegion
}

// region returns the hovered region of line i (none when another line is hovered).
func (h herdHover) region(i int) herdHoverRegion {
	if h.reg != herdHoverNone && h.line == i {
		return h.reg
	}
	return herdHoverNone
}

// hoverAt resolves the hover target under (x, y) with the same hit-testing as
// clicks, so hover and click can never disagree.
func (m HerdModel) hoverAt(x, y int) herdHover {
	idx, sub, ok := m.lineAtSub(x, y)
	if !ok {
		return herdHover{}
	}
	l := m.lines[idx]
	switch l.kind {
	case herdLineHeader:
		if groupCanLaunch(l.group) {
			if m.newArmedFor(l.group) {
				if r := m.onSelector(x); r != herdHoverNone {
					return herdHover{line: idx, reg: r}
				}
			} else if m.onNewButton(x) {
				return herdHover{line: idx, reg: herdHoverNew}
			}
		}
	case herdLineRow:
		if sub != 0 { // detail lines: no hover effect
			break
		}
		if m.onCloseButton(x) {
			return herdHover{line: idx, reg: herdHoverClose}
		}
		return herdHover{line: idx, reg: herdHoverBody}
	}
	return herdHover{}
}

// handleMotion tracks the pointer. All-motion reporting floods events, so a
// move that lands on the same target changes nothing and returns a nil cmd.
func (m HerdModel) handleMotion(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	m.hoverX, m.hoverY, m.hasPointer = msg.X, msg.Y, true
	prev := m.hover
	m.hover = m.hoverAt(msg.X, msg.Y)
	if m.hover == prev {
		return m, nil
	}
	m.hideCard()
	if m.hover.reg == herdHoverBody {
		return m, herdCardTickCmd(m.hoverGen, m.hover)
	}
	return m, nil
}

// refreshHover recomputes the hover from the last pointer position, after
// the content under it moved (reload, scroll). Clears it without a pointer.
func (m *HerdModel) refreshHover() {
	if !m.hasPointer {
		m.hover = herdHover{}
		m.hideCard()
		return
	}
	m.hover = m.hoverAt(m.hoverX, m.hoverY)
	// The card survives a reload only while its row is still under the pointer.
	if r, idx, ok := m.rowForCard(m.card); m.card != nil && (!ok || m.hover.reg != herdHoverBody || m.hover.line != idx || r.WindowID == "") {
		m.hideCard()
	}
}
