package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/fleet"
)

// fleetClickAction is what a mouse click on the hub should do. Clicks only
// select (the live pane follows the selection). The only side effects are the
// explicit "+ new" / "sweep" / "✕" buttons and the right-click menu.
type fleetClickAction int

const (
	fleetClickIgnore   fleetClickAction = iota // nothing happens
	fleetClickSelect                           // move the cursor only, like j/k
	fleetClickNew                              // header's "+ new" button: arm the shell | agent selector
	fleetClickNewShell                         // armed selector's "shell": open a shell window
	fleetClickNewAgent                         // armed selector's "agent": launch an agent
	fleetClickClose                            // row's "✕" button: arm / confirm close
	fleetClickMenu                             // right click: open the context menu
	fleetClickRename                           // double click on a row's title: open the rename editor
	fleetClickFold                             // header's project name: fold / unfold the group
	fleetClickSweep                            // header's "sweep" button: preview a sweep of the group
	fleetClickCollapse                         // a parent row's ▾/▸ marker: collapse / expand its sub-agents
)

// fleetListTop returns the screen row where the list (or sidebar) begins: the
// number of rows the header renders. Derived from the rendered header rather
// than hardcoded so style changes cannot skew click targets.
func (m FleetModel) fleetListTop() int {
	return strings.Count(m.renderHeader(m.contentWidth()), "\n")
}

// fleetListRows is the number of screen rows the list area spans.
func (m FleetModel) fleetListRows() int {
	return m.visibleLines()
}

// clickable reports whether the hub is showing real list data (not a
// loading/error/empty state).
func (m FleetModel) clickable() bool {
	_, isStatus := m.renderStatusOrEmpty()
	return !isStatus && len(m.lines) > 0
}

// lineAt maps a screen coordinate to a line index in m.lines. ok is false when
// the point is outside the list area, or the hub is in a loading/error/empty
// state. A returned idx may be a divider line.
func (m FleetModel) lineAt(x, y int) (idx int, ok bool) {
	idx, _, ok = m.lineAtSub(x, y)
	return idx, ok
}

// lineAtSub is lineAt that also reports which screen row of the line was hit:
// 0 for a line's own row, 1.. for a selected row's detail lines.
func (m FleetModel) lineAtSub(x, y int) (idx, sub int, ok bool) {
	if !m.clickable() || x < 0 {
		return -1, 0, false
	}
	row := y - m.fleetListTop()
	if row < 0 || row >= m.fleetListRows() {
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
func (m FleetModel) listColWidth() int {
	return m.contentWidth()
}

// onNewButton reports whether column x hits group g's "+ new" button as
// drawn under the pointer (with one column of slack either side).
func (m FleetModel) onNewButton(g fleet.Group, x int) bool {
	span := m.headerNewSpan(g, m.listColWidth())
	return span[1] > 0 && x >= span[0]-1 && x < span[1]+1
}

// selectorSpans returns the [start,end) columns of the armed selector's
// "shell" and "agent" labels. The "|" separator belongs to neither.
func (m FleetModel) selectorSpans() (shell, agent [2]int) {
	end := m.listColWidth() - fleetSidebarRightGutter
	aw := lipgloss.Width(fleetAgentLabel)
	sw := lipgloss.Width(fleetShellLabel)
	agent = [2]int{end - aw, end}
	shell = [2]int{agent[0] - lipgloss.Width(fleetSelSep) - sw, agent[0] - lipgloss.Width(fleetSelSep)}
	return shell, agent
}

// onSelector reports which side of the armed selector column x hits: the
// outer edges get one column of slack, the inner separator's spaces go to the
// adjacent label and the "|" itself hits nothing.
func (m FleetModel) onSelector(x int) fleetHoverRegion {
	shell, agent := m.selectorSpans()
	switch {
	case x >= shell[0]-1 && x < shell[1]+1:
		return fleetHoverNewShell
	case x >= agent[0]-1 && x < agent[1]+1:
		return fleetHoverNewAgent
	}
	return fleetHoverNone
}

// onCloseButton reports whether column x hits a row's right-aligned "✕"
// (with slack that includes the gutter).
func (m FleetModel) onCloseButton(x int) bool {
	w := m.listColWidth()
	return x >= w-fleetSidebarRightGutter-lipgloss.Width(fleetCloseLabel)-2 && x < w
}

// clickDecision resolves a left click at (x, y) to a target line and action.
// idx is -1 when the click is ignored.
func (m FleetModel) clickDecision(x, y int) (idx int, action fleetClickAction) {
	return m.buttonDecision(x, y, tea.MouseLeft)
}

// buttonDecision resolves a click with the given button. Right click on a row
// that has sub-agents (anywhere but its ✕) toggles its collapse, anywhere else
// on a row or header it opens the context menu; left click on a header's "+ new" or a
// row's "✕" triggers that button, anything else on a line just selects it.
func (m FleetModel) buttonDecision(x, y int, button tea.MouseButton) (idx int, action fleetClickAction) {
	idx, sub, ok := m.lineAtSub(x, y)
	if !ok {
		return -1, fleetClickIgnore
	}
	l := m.lines[idx]
	switch button {
	case tea.MouseRight:
		if l.kind == fleetLineRow && m.lineCollapsible(idx) && !(sub == 0 && m.onCloseButton(x)) {
			return idx, fleetClickCollapse
		}
		if l.kind == fleetLineRow || l.kind == fleetLineHeader || l.kind == fleetLineClosed || l.kind == fleetLineSchedule {
			return idx, fleetClickMenu
		}
		return idx, fleetClickIgnore
	case tea.MouseLeft:
	default:
		return -1, fleetClickIgnore
	}
	switch l.kind {
	case fleetLineHeader:
		if groupCanLaunch(l.group) {
			if m.newArmedFor(l.group) {
				switch m.onSelector(x) {
				case fleetHoverNewShell:
					return idx, fleetClickNewShell
				case fleetHoverNewAgent:
					return idx, fleetClickNewAgent
				}
			} else if m.onNewButton(l.group, x) {
				return idx, fleetClickNew
			}
		}
		if m.onSweepButton(l.group, x) {
			return idx, fleetClickSweep
		}
		if m.onHeaderName(l.group, x) {
			return idx, fleetClickFold
		}
		return idx, fleetClickSelect
	case fleetLineRow:
		if sub == 0 && m.onCloseButton(x) { // detail lines have no ✕
			return idx, fleetClickClose
		}
		if sub == 0 && m.onCollapseMarker(idx, x) {
			return idx, fleetClickCollapse
		}
		return idx, fleetClickSelect
	case fleetLinePlaceholder, fleetLineClosed, fleetLineSchedule:
		return idx, fleetClickSelect
	}
	return idx, fleetClickIgnore
}

// selectLine moves the cursor to idx with the same bookkeeping as moveCursor.
func (m *FleetModel) selectLine(idx int) {
	m.pendingFocus = nil // a manual selection beats a pending launch focus
	m.focusOnShow = ""
	m.wheelHold = false
	m.splitPane.SetListContent(m.splitPane.listContent, len(m.lines))
	m.splitPane.SetCursor(idx)
	m.syncSelectionFromCursor()
	m.ensureCursorVisible()
}

func (m FleetModel) handleClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if line, ok := m.helpLineAt(msg.Y); ok && msg.Button == tea.MouseLeft {
		return m.clickHelp(msg.X, line)
	}
	if msg.Button == tea.MouseLeft && m.menuOpen && !m.menuHelp && msg.Y == m.footerMessageY() {
		return m.clickMenu(msg.X)
	}
	if m.newSessionOpen() {
		if msg.Y == m.footerMessageY() {
			return m, nil // a click on the prompt itself keeps it open
		}
		m.cancelNewSession() // any other click dismisses the prompt
	}
	if m.renameOpen() {
		if idx, sub, ok := m.lineAtSub(msg.X, msg.Y); ok && sub == 0 && m.renamingRow(m.lines[idx].row) && m.onRowTitle(idx, msg.X) {
			return m, nil // a click on the editor itself keeps it open
		}
		m.cancelRename() // any other click cancels the rename
	}
	if msg.Button == tea.MouseLeft && m.onServeBadge(msg.X, msg.Y) {
		m.closeMenu()
		m.cancelNew()
		return m, m.toggleServe()
	}
	if msg.Button == tea.MouseLeft && m.onActivityButton(msg.X, msg.Y) {
		m.closeMenu()
		m.cancelNew()
		return m, m.toggleActivity()
	}
	idx, action := m.buttonDecision(msg.X, msg.Y, msg.Button)
	if msg.Button == tea.MouseLeft {
		if m.doubleClickTitle(msg.X, msg.Y, idx, action) {
			action = fleetClickRename
		}
	}
	if action != fleetClickNewShell && action != fleetClickNewAgent {
		m.cancelNew() // any other click dismisses the selector
	}
	if action != fleetClickSweep {
		m.cancelSweep() // any other click cancels an armed sweep
	}
	switch action {
	case fleetClickSelect:
		m.closeMenu()
		m.selectLine(idx)
		show := m.maybeShowCmd()
		return m, show
	case fleetClickNew:
		m.closeMenu()
		m.selectLine(idx)
		m.armNew(idx)
		return m, nil
	case fleetClickNewShell:
		m.closeMenu()
		m.cancelNew()
		return m, m.shellForGroup(m.groupForSession(m.lines[idx].group.Session))
	case fleetClickNewAgent:
		m.closeMenu()
		m.cancelNew()
		return m, m.launchForGroup(m.groupForSession(m.lines[idx].group.Session))
	case fleetClickClose:
		m.closeMenu()
		if m.confirmingRow(m.lines[idx].row) {
			return m, m.confirmClose() // second click on the armed ✕
		}
		m.selectLine(idx)
		m.startClose(idx)
		show := m.maybeShowCmd()
		return m, show
	case fleetClickSweep:
		m.closeMenu()
		if m.sweepArmedFor(m.lines[idx].group) || m.sweepBusy != "" {
			return m, nil // a repeat click neither confirms nor starts a second sweep
		}
		m.selectLine(idx)
		return m, m.startSweep(idx)
	case fleetClickFold:
		m.closeMenu()
		m.selectLine(idx)
		return m, m.toggleFoldAt(idx)
	case fleetClickCollapse:
		m.closeMenu()
		m.selectLine(idx)
		return m, m.toggleCollapseAt(idx)
	case fleetClickRename:
		blink := m.openRename(idx)
		return m, tea.Batch(blink, m.maybeShowCmd())
	case fleetClickMenu:
		m.openMenu(idx)
		show := m.maybeShowCmd()
		return m, show
	}
	m.closeMenu()
	return m, nil
}

// fleetDoubleClickWindow is the longest gap between two left clicks on a row's
// title that counts as a double click.
const fleetDoubleClickWindow = 400 * time.Millisecond

// fleetLastClick is the previous left click on a renamable row's title, keyed
// by window rather than line index so a reorder between clicks does not matter.
type fleetLastClick struct {
	session, windowID string
	at                time.Time
}

// doubleClickTitle records a left click and reports whether it completes a
// double click on a renamable row's title. Only a plain select on the title
// text counts, so the ✕, the collapse marker and headers never start or finish
// one. A completed double click consumes the record.
func (m *FleetModel) doubleClickTitle(x, y, idx int, action fleetClickAction) bool {
	prev := m.lastClick
	m.lastClick = nil
	if action != fleetClickSelect {
		return false
	}
	_, sub, ok := m.lineAtSub(x, y)
	if !ok || !m.rowCanRenameAt(idx) {
		return false
	}
	r := m.lines[idx].row
	now := fleetNow()
	// The first click selects the row, which can move it up the list by the
	// previous selection's detail lines, so the second click may land on one of
	// the row's own detail lines instead of the title.
	if prev != nil && prev.session == r.Session && prev.windowID == r.WindowID && now.Sub(prev.at) <= fleetDoubleClickWindow {
		return true
	}
	if sub != 0 || !m.onRowTitle(idx, x) {
		return false
	}
	m.lastClick = &fleetLastClick{session: r.Session, windowID: r.WindowID, at: now}
	return false
}

// clickMenu handles a left click on the footer's context-menu line.
func (m FleetModel) clickMenu(x int) (tea.Model, tea.Cmd) {
	items := m.fleetMenuItems(m.menuLine)
	_, spans := menuItemSpans(items)
	for i, sp := range spans {
		if x >= sp[0] && x < sp[1] {
			return m, m.runMenuKey(items[i].key)
		}
	}
	m.closeMenu()
	return m, nil
}

// fleetHoverRegion is which part of a line the pointer is over.
type fleetHoverRegion int

const (
	fleetHoverNone     fleetHoverRegion = iota
	fleetHoverBody                      // a row's body (not its ✕)
	fleetHoverClose                     // a row's ✕ button
	fleetHoverNew                       // a group header's "+ new" button
	fleetHoverNewShell                  // armed selector's "shell"
	fleetHoverNewAgent                  // armed selector's "agent"
	fleetHoverFold                      // a group header's fold marker and project name
	fleetHoverSweep                     // a group header's "sweep" button
	fleetHoverHeader                    // a group header outside its buttons
	fleetHoverCollapse                  // a parent row's ▾/▸ collapse marker
)

// fleetHover is the hover target: a line index plus the region on it. The zero
// value (region none) hovers nothing.
type fleetHover struct {
	line int
	reg  fleetHoverRegion
}

// region returns the hovered region of line i (none when another line is hovered).
func (h fleetHover) region(i int) fleetHoverRegion {
	if h.reg != fleetHoverNone && h.line == i {
		return h.reg
	}
	return fleetHoverNone
}

// hoverAt resolves the hover target under (x, y) with the same hit-testing as
// clicks, so hover and click can never disagree.
func (m FleetModel) hoverAt(x, y int) fleetHover {
	idx, sub, ok := m.lineAtSub(x, y)
	if !ok {
		return fleetHover{}
	}
	l := m.lines[idx]
	switch l.kind {
	case fleetLineHeader:
		if groupCanLaunch(l.group) {
			if m.newArmedFor(l.group) {
				if r := m.onSelector(x); r != fleetHoverNone {
					return fleetHover{line: idx, reg: r}
				}
			} else if m.onNewButton(l.group, x) {
				return fleetHover{line: idx, reg: fleetHoverNew}
			}
		}
		if m.onSweepButton(l.group, x) {
			return fleetHover{line: idx, reg: fleetHoverSweep}
		}
		if m.onHeaderName(l.group, x) {
			return fleetHover{line: idx, reg: fleetHoverFold}
		}
		// Anywhere else on the header still counts as hovering it: the
		// header then shows its "+ new" button.
		return fleetHover{line: idx, reg: fleetHoverHeader}
	case fleetLineRow:
		if sub != 0 { // detail lines: no hover effect
			break
		}
		if m.onCloseButton(x) {
			return fleetHover{line: idx, reg: fleetHoverClose}
		}
		if m.onCollapseMarker(idx, x) {
			return fleetHover{line: idx, reg: fleetHoverCollapse}
		}
		return fleetHover{line: idx, reg: fleetHoverBody}
	}
	return fleetHover{}
}

// handleMotion tracks the pointer. All-motion reporting floods events, so a
// move that lands on the same target changes nothing and returns a nil cmd.
func (m FleetModel) handleMotion(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	m.hoverX, m.hoverY, m.hasPointer = msg.X, msg.Y, true
	prev := m.hover
	m.hover = m.hoverAt(msg.X, msg.Y)
	if m.hover == prev {
		return m, nil
	}
	m.hideCard()
	if m.hover.reg == fleetHoverBody {
		return m, fleetCardTickCmd(m.hoverGen, m.hover)
	}
	return m, nil
}

// refreshHover recomputes the hover from the last pointer position, after
// the content under it moved (reload, scroll). Clears it without a pointer.
func (m *FleetModel) refreshHover() {
	if !m.hasPointer {
		m.hover = fleetHover{}
		m.hideCard()
		return
	}
	m.hover = m.hoverAt(m.hoverX, m.hoverY)
	// The card survives a reload only while its row is still under the pointer.
	if r, idx, ok := m.rowForCard(m.card); m.card != nil && (!ok || m.hover.reg != fleetHoverBody || m.hover.line != idx || r.WindowID == "") {
		m.hideCard()
	}
}

// onHeaderName reports whether column x hits group g's fold marker or project
// name on its header (the fold toggle), never overlapping the "+ new" button.
func (m FleetModel) onHeaderName(g fleet.Group, x int) bool {
	span := m.headerNameSpan(g, m.listColWidth())
	return x >= span[0] && x < span[1]
}
