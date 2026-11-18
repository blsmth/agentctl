package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
)

// Group reordering: `shift+up` / `shift+down` and dragging a group header.
// Both end in moveGroup, which reorders m.groups and writes the saved order
// (fleet.GroupOrderPath) that fleet.Load applies for every consumer.

// groupOrderPath is the saved order's path; a seam for tests.
var groupOrderPath = fleet.GroupOrderPath

// fleetDrag is a left press on a group header, and the drag it may become.
type fleetDrag struct {
	session string
	line    int  // the pressed header's line index
	active  bool // the pointer left the header row: a real drag
	slot    int  // group index the drop would land on; -1 outside the list
	// wasFolded is the group's fold state before the press, which already
	// ran the click action: starting a drag restores it.
	wasFolded bool
}

// groupIndex is the index of the group with the given key in m.groups, or -1.
func (m FleetModel) groupIndex(key string) int {
	return slices.IndexFunc(m.groups, func(g fleet.Group) bool { return fleet.GroupKey(g) == key })
}

// moveGroup moves the group with the given key to index toIndex of the group
// list and saves the order. Every group has a header line whatever the filter
// hides, so the visible list is m.groups. It reports whether anything moved
// (one group, an unknown key or the same slot do nothing and write nothing).
// The selection is kept: it is identified by session and window, not by line.
func (m *FleetModel) moveGroup(key string, toIndex int) bool {
	from := m.groupIndex(key)
	if from < 0 || len(m.groups) < 2 {
		return false
	}
	toIndex = max(0, min(toIndex, len(m.groups)-1))
	if toIndex == from {
		return false
	}
	groups := slices.Clone(m.groups)
	g := groups[from]
	groups = slices.Delete(groups, from, from+1)
	groups = slices.Insert(groups, toIndex, g)
	m.groups = groups
	m.relayout()
	m.wheelHold = false
	m.ensureCursorVisible()
	m.saveGroupOrder()
	return true
}

// saveGroupOrder writes the current order, keeping the slots of saved groups
// that are not live right now. A model without a path persists nothing.
func (m *FleetModel) saveGroupOrder() {
	if m.orderPath == "" {
		return
	}
	live := make([]string, len(m.groups))
	for i, g := range m.groups {
		live[i] = fleet.GroupKey(g)
	}
	keys := fleet.MergeGroupOrder(fleet.ReadGroupOrder(m.orderPath), live)
	if err := fleet.WriteGroupOrder(m.orderPath, keys); err != nil {
		logging.Log("hub: saving group order: %v", err)
	}
}

// moveSelectedGroup is shift+up / shift+down: move the selection's group one
// slot (delta -1 up, +1 down).
func (m *FleetModel) moveSelectedGroup(delta int) {
	g := m.groupForSession(m.selSession)
	if g == nil {
		return
	}
	key := fleet.GroupKey(*g)
	m.moveGroup(key, m.groupIndex(key)+delta)
}

// ── Mouse drag ───────────────────────────────────────────────────────────────

// pressHeader handles a left press: the usual click, plus remembering a press
// on a header (not its buttons) as a possible drag.
func (m FleetModel) pressHeader(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	var start *fleetDrag
	if msg.Button == tea.MouseLeft {
		idx, action := m.buttonDecision(msg.X, msg.Y, msg.Button)
		if (action == fleetClickSelect || action == fleetClickFold) && m.lines[idx].kind == fleetLineHeader {
			s := m.lines[idx].group.Session
			start = &fleetDrag{session: s, line: idx, slot: -1, wasFolded: m.isFolded(s)}
		}
	}
	next, cmd := m.handleClick(msg)
	fm := next.(FleetModel)
	fm.drag = start
	return fm, cmd
}

// dragMotion handles pointer motion while a press is pending or a drag is
// active. handled is false when motion should take the normal hover path.
func (m FleetModel) dragMotion(msg tea.MouseMotionMsg) (next tea.Model, cmd tea.Cmd, handled bool) {
	if msg.Button != tea.MouseLeft {
		m.drag = nil // the release was missed
		return m, nil, false
	}
	m.hoverX, m.hoverY, m.hasPointer = msg.X, msg.Y, true
	d := *m.drag
	if !d.active {
		if idx, _, ok := m.lineAtSub(msg.X, msg.Y); ok && idx == d.line {
			return m, nil, true // still on the pressed header: not a drag yet
		}
		d.active = true
		m.hover = fleetHover{}
		m.hideCard()
		if m.isFolded(d.session) != d.wasFolded {
			m.setFolded(d.session, d.wasFolded) // undo the press's fold toggle
			m.relayout()
			cmd = m.saveViewCmd()
		}
	}
	d.slot = -1
	if slot, ok := m.dropSlotAt(msg.Y); ok {
		d.slot = slot
	}
	m.drag = &d
	return m, cmd, true
}

// releaseDrag handles the button release: drop the dragged group, or cancel
// when released outside the list. A release with no drag does nothing.
func (m FleetModel) releaseDrag(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	d := m.drag
	m.drag = nil
	if d == nil || !d.active {
		return m, nil
	}
	if slot, ok := m.dropSlotAt(msg.Y); ok {
		if g := m.groupForSession(d.session); g != nil {
			m.moveGroup(fleet.GroupKey(*g), slot)
		}
	}
	return m, nil
}

// dragging reports whether a header drag is in progress.
func (m FleetModel) dragging() bool { return m.drag != nil && m.drag.active }

// dropSlotAt maps a screen row to the group index a drop there lands on: the
// group whose header or body is under the pointer (the dragged group takes
// that slot), the last group below the list's last line. ok is false outside
// the list area. The scroll offset and each line's height are applied.
func (m FleetModel) dropSlotAt(y int) (slot int, ok bool) {
	if !m.clickable() {
		return -1, false
	}
	row := y - m.fleetListTop()
	if row < 0 || row >= m.fleetListRows() {
		return -1, false
	}
	if idx, _, ok := m.lineAtSub(0, y); ok {
		return m.groupIndexOfLine(idx), true
	}
	return len(m.groups) - 1, true
}

// groupIndexOfLine is the index in m.groups of the group line i belongs to (a
// divider belongs to the group above it).
func (m FleetModel) groupIndexOfLine(i int) int {
	n := -1
	for _, l := range m.lines[:i+1] {
		if l.kind == fleetLineHeader {
			n++
		}
	}
	return max(n, 0)
}

// dragIndicatorLine is the line index where the drop's insertion rule is
// drawn: the divider between the two groups the drop lands between, or the
// header below that boundary when there is no divider (adjacent folded groups).
// Overlaying an existing line keeps the geometry stable while dragging.
func (m FleetModel) dragIndicatorLine() (int, bool) {
	if !m.dragging() || m.drag.slot < 0 {
		return 0, false
	}
	from := slices.IndexFunc(m.groups, func(g fleet.Group) bool { return g.Session == m.drag.session })
	to := m.drag.slot
	if from < 0 || to == from {
		return 0, false
	}
	boundary := to // insert above the target group...
	if to > from {
		boundary = to + 1 // ...or below it when moving down
	}
	if boundary >= len(m.groups) {
		return len(m.lines) - 1, len(m.lines) > 0
	}
	n := -1
	for i, l := range m.lines {
		if l.kind != fleetLineHeader {
			continue
		}
		if n++; n == boundary {
			if i > 0 && m.lines[i-1].kind == fleetLineDivider {
				return i - 1, true
			}
			return i, true
		}
	}
	return 0, false
}

// renderDragIndicator is the insertion rule: an accent line across the list.
func renderDragIndicator(cw int) string {
	return fleetTitleStyle.Render(strings.Repeat("─", max(cw-fleetSidebarRightGutter, 1)))
}

// dimDraggedHeader mutes the dragged group's header, keeping its project bar.
func dimDraggedHeader(project, line string) string {
	_, rest, ok := strings.Cut(ansi.Strip(line), "▎")
	if !ok {
		return fleetDimStyle.Render(ansi.Strip(line))
	}
	return fleetProjectBar(project) + fleetDimStyle.Render(rest)
}
