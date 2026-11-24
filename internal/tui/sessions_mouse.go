package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Mouse handling for the sessions browser. It follows the rules of the
// tickets browser (tickets_mouse.go) and shares its machinery: zones recorded
// at render (zones.go), tkPress, tkKey, zoneParts, tkNow and tkDoubleClick.
//
//   - A click runs its key's code path: zones named "key:<k>@<where>" are
//     synthesized key presses through handleKey.
//   - A click fires on release, in the zone where it was pressed.
//   - A double-click (same zone within tkDoubleClick) on the selected row is
//     enter, which opens the detail view.
//   - The ? overlay owns the mouse: its zones are the only live ones, a click
//     outside closes it and the wheel does nothing.
//   - Mouse is additive: every zone has a key.

// currentZones returns the zones of the last rendered frame, rendering one
// when nothing has been drawn yet.
func (m SessionsModel) currentZones() zoneList {
	if m.zones != nil && len(*m.zones) > 0 {
		return *m.zones
	}
	return m.renderZones()
}

func (m SessionsModel) clickZoneAt(x, y int) string {
	return m.currentZones().at(x, y)
}

// hoverZoneAt is the innermost target under (x, y), ignoring area regions.
func (m SessionsModel) hoverZoneAt(z zoneList, x, y int) string {
	best, bestW := "", -1
	for _, r := range z {
		if r.y != y || x < r.x0 || x >= r.x1 || isRegionZone(r.id) {
			continue
		}
		if w := r.x1 - r.x0; bestW < 0 || w < bestW {
			best, bestW = r.id, w
		}
	}
	return best
}

func (m SessionsModel) handleMousePress(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	ev := msg.Mouse()
	if ev.Button != tea.MouseLeft {
		return m, nil
	}
	m.press = tkPress{zone: m.clickZoneAt(ev.X, ev.Y), at: tkNow(), active: true}
	return m, nil
}

func (m SessionsModel) handleMouseRelease(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	if !m.press.active {
		return m, nil
	}
	ev := msg.Mouse()
	pressed := m.press
	m.press = tkPress{}
	if zone := m.clickZoneAt(ev.X, ev.Y); zone == pressed.zone {
		return m.click(zone)
	}
	return m, nil
}

// click activates zone (pressed and released in it).
func (m SessionsModel) click(zone string) (tea.Model, tea.Cmd) {
	now := tkNow()
	double := zone != "" && m.lastClick.zone == zone && now.Sub(m.lastClick.at) <= tkDoubleClick
	if double {
		m.lastClick = tkPress{}
	} else {
		m.lastClick = tkPress{zone: zone, at: now}
	}
	if m.helpOpen {
		return m.clickHelp(zone)
	}
	if m.searchMode && !strings.HasSuffix(zone, "@hint") {
		// Clicking elsewhere keeps the typed query, as enter does.
		m.searchMode = false
		m.searchQuery = m.searchInput.Value()
		m.searchInput.Blur()
		m.applyFilters()
	}
	return m.activate(zone, double)
}

// activate runs a click on a zone of the list or detail view.
func (m SessionsModel) activate(zone string, double bool) (tea.Model, tea.Cmd) {
	kind, arg := zoneParts(zone)
	switch kind {
	case "key":
		return m.handleKey(tkKey(arg))

	case "row":
		i, err := strconv.Atoi(arg)
		if err != nil || i < 0 || i >= len(m.entries) {
			return m, nil
		}
		if double && m.cursor == i {
			return m.handleKey(tkKey("enter"))
		}
		m.moveCursor(i)
		return m, nil

	case "chip":
		switch arg {
		case "agent-clear":
			m.clearAgentFilter()
		case "project-clear":
			m.clearProjectFilter()
		case "search-clear":
			m.clearSearch()
		}
	}
	return m, nil
}

// clickHelp handles a click while the overlay is open; zone is "" outside it.
func (m SessionsModel) clickHelp(zone string) (tea.Model, tea.Cmd) {
	if zone == "" {
		m.helpOpen = false
		return m, nil
	}
	if kind, arg := zoneParts(zone); kind == "mkey" {
		return m.handleKey(tkKey(arg))
	}
	return m, nil // inside the box, not on a control
}

func (m SessionsModel) handleMouseMotion(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	ev := msg.Mouse()
	z := m.currentZones()
	hover := m.hoverZoneAt(z, ev.X, ev.Y)
	row := ""
	if !m.helpOpen {
		row = z.atPrefix(ev.X, ev.Y, "row:")
	}
	m.hover, m.hoverRow = hover, row
	return m, nil
}

func (m SessionsModel) handleMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.helpOpen {
		return m, nil // the wheel never scrolls the view behind the overlay
	}
	ev := msg.Mouse()
	var dir int
	switch ev.Button {
	case tea.MouseWheelUp:
		dir = -1
	case tea.MouseWheelDown:
		dir = 1
	default:
		return m, nil
	}
	switch m.currentZones().atPrefix(ev.X, ev.Y, "pane:") {
	case "pane:list":
		key := "j"
		if dir < 0 {
			key = "k"
		}
		return m.handleKey(tkKey(key))
	case "pane:preview", "pane:body":
		m.previewScroll += dir * tkWheelStep
		if m.previewScroll < 0 {
			m.previewScroll = 0
		}
	}
	return m, nil
}
