package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/fleet"
)

// A parent row's sub-agents can be collapsed (`z` on the row, or a click on
// its ▾/▸ marker): the whole subtree is hidden and the parent shows a compact
// summary of what it hides instead, so a blocked child still reads as
// blocked. The collapsed set is keyed by collapseKey and persisted with the
// rest of the view state (hubViewState.Collapsed).

// collapseKey is the collapsed-set key of a row: "session|windowID".
func collapseKey(session, windowID string) string {
	return session + "|" + windowID
}

// isCollapsed reports whether the row (session, windowID) is collapsed.
func (m FleetModel) isCollapsed(session, windowID string) bool {
	return windowID != "" && m.collapsed[collapseKey(session, windowID)]
}

// setCollapsed collapses or expands the row (session, windowID).
func (m *FleetModel) setCollapsed(session, windowID string, collapse bool) {
	key := collapseKey(session, windowID)
	if collapse {
		if m.collapsed == nil {
			m.collapsed = make(map[string]bool)
		}
		m.collapsed[key] = true
		return
	}
	delete(m.collapsed, key)
}

// lineCollapsible reports whether line idx is a row with children in the
// tree, the rows `z` collapses rather than folding the group.
func (m FleetModel) lineCollapsible(idx int) bool {
	return idx >= 0 && idx < len(m.lines) && m.lines[idx].kind == fleetLineRow && m.lines[idx].hasKids
}

// toggleFoldKey is `z`: on a row with children it collapses or expands that
// row's subtree; anywhere else (a header, a childless row) it folds the group.
func (m *FleetModel) toggleFoldKey(idx int) tea.Cmd {
	if m.lineCollapsible(idx) {
		return m.toggleCollapseAt(idx)
	}
	return m.toggleFoldAt(idx)
}

// toggleCollapseAt collapses or expands the subtree of the row at line idx.
// A selection inside the subtree that collapsing hides moves to the parent
// (resolveSelection, via hiddenUnder).
func (m *FleetModel) toggleCollapseAt(idx int) tea.Cmd {
	if !m.lineCollapsible(idx) {
		return nil
	}
	r := m.lines[idx].row
	m.setCollapsed(r.Session, r.WindowID, !m.isCollapsed(r.Session, r.WindowID))
	m.relayout()
	return m.saveViewCmd()
}

// expandAncestors expands every collapsed ancestor (by Parent, within rows)
// of the row windowID in session, and reports whether any was collapsed.
func (m *FleetModel) expandAncestors(session, windowID string, rows []fleet.Row) bool {
	idx := make(map[string]int, len(rows))
	for i, r := range rows {
		if r.WindowID != "" {
			if _, dup := idx[r.WindowID]; !dup {
				idx[r.WindowID] = i
			}
		}
	}
	cur, ok := idx[windowID]
	if !ok {
		return false
	}
	changed := false
	// The step bound breaks parent cycles.
	for step := 0; step < len(rows); step++ {
		p, ok := idx[rows[cur].Parent]
		if rows[cur].Parent == "" || !ok || p == cur {
			break
		}
		if m.isCollapsed(session, rows[p].WindowID) {
			m.setCollapsed(session, rows[p].WindowID, false)
			changed = true
		}
		cur = p
	}
	return changed
}

// Markers drawn before a parent row's name: expanded or collapsed.
func collapseMarker(collapsed, hovered bool) string {
	style, glyph := fleetDimStyle, fleetUnfoldedGlyph
	if collapsed {
		style, glyph = fleetCountStyle, fleetFoldedGlyph
	}
	if hovered {
		style = fleetNewHoverStyle
	}
	return style.Render(glyph)
}

// fleetCollapseMarkerW is the columns a parent row's marker takes before its
// name: the glyph and a space. Childless rows take none.
const fleetCollapseMarkerW = 2

// subtreeSummary renders a collapsed parent's summary in at most w columns:
// a dim "+N" (rows hidden) and the ⏸ ● ✓ counts in the header chips' styles.
// As w shrinks it drops the done count, then the "+N", keeping the blocked
// and working counts longest: a blocked child must stay visible.
func subtreeSummary(c fleetSubtreeCounts, w int) string {
	if c.total == 0 || w <= 0 {
		return ""
	}
	type part struct {
		text string
		st   lipgloss.Style
	}
	total := part{fmt.Sprintf("+%d", c.total), fleetDimStyle}
	var blocked, working, done []part
	if c.blocked > 0 {
		blocked = []part{{fmt.Sprintf("⏸%d", c.blocked), fleetBlockedStyle}}
	}
	if c.working > 0 {
		working = []part{{fmt.Sprintf("●%d", c.working), fleetWorkingStyle}}
	}
	if c.done > 0 {
		done = []part{{fmt.Sprintf("✓%d", c.done), fleetDoneStyle}}
	}
	join := func(groups ...[]part) []part {
		var out []part
		for _, g := range groups {
			out = append(out, g...)
		}
		return out
	}
	urgent := join(blocked, working)
	variants := [][]part{
		join([]part{total}, urgent, done),
		join([]part{total}, urgent),
		urgent,
		blocked,
	}
	render := func(ps []part) (string, int) {
		texts := make([]string, len(ps))
		plain := make([]string, len(ps))
		for i, p := range ps {
			texts[i] = p.st.Render(p.text)
			plain[i] = p.text
		}
		return strings.Join(texts, " "), lipgloss.Width(strings.Join(plain, " "))
	}
	var last []part
	for _, v := range variants {
		if len(v) == 0 {
			continue
		}
		if s, sw := render(v); sw <= w {
			return s
		}
		last = v
	}
	s, _ := render(last)
	return truncateToWidth(s, w)
}

// onCollapseMarker reports whether column x hits the ▾/▸ marker of the row
// at line idx (with one column of slack after it, its space).
func (m FleetModel) onCollapseMarker(idx, x int) bool {
	if !m.lineCollapsible(idx) {
		return false
	}
	lay := m.rowLayout(m.lines[idx], idx == m.splitPane.Cursor(), fleetHoverBody, m.listColWidth())
	return lay.marker[1] > 0 && x >= lay.marker[0] && x < lay.marker[1]+1
}
