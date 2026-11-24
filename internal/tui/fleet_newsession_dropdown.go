package tui

import (
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/theme"
)

// The new-session dropdown: a panel drawn over the list directly below the
// title row, anchored under the "+ open" button (N opens the same one). It
// holds the directory input, an error line when the directory is invalid and
// the recent projects. It slides in (height 0 to full) like the schedule
// drawer; closing is instant.

const (
	// fleetNsPanelW is the panel's preferred width including its border.
	fleetNsPanelW = 56
	// fleetNsNarrow is the content width below which the panel takes it all.
	fleetNsNarrow = 30
	// fleetNsPanelTop is the screen row of the panel's top border: the row
	// under the title.
	fleetNsPanelTop = 1
	// fleetNsSlideDur is the slide-in duration; fleetNsSlideEvery the frame
	// interval.
	fleetNsSlideDur   = 130 * time.Millisecond
	fleetNsSlideEvery = 16 * time.Millisecond
	// fleetNsChrome is the columns the border and padding take.
	fleetNsChrome = 4
)

// fleetNewSessionHints are the footer hints while the dropdown is open.
var fleetNewSessionHints = []fleetHint{
	{"⏎", "open"},
	{"esc", "cancel"},
}

// nsDropTickMsg is one frame of the slide-in. gen guards against ticks of an
// earlier open.
type nsDropTickMsg struct{ gen int }

// nsDropTick schedules a frame; a seam so tests drive ticks by message.
var nsDropTick = func(gen int) tea.Cmd {
	return tea.Tick(fleetNsSlideEvery, func(time.Time) tea.Msg { return nsDropTickMsg{gen: gen} })
}

// startNewSessionSlide begins the slide-in of the just-opened dropdown.
func (m *FleetModel) startNewSessionSlide() tea.Cmd {
	m.nsAnimGen++
	s := m.dirPrompt
	s.animOn, s.animP, s.animStart = true, 0, fleetNow()
	return nsDropTick(m.nsAnimGen)
}

// applyNewSessionTick advances the slide-in (ease-out) and schedules the next
// frame, or stops ticking once settled. Stale ticks are ignored.
func (m *FleetModel) applyNewSessionTick(msg nsDropTickMsg) tea.Cmd {
	s := m.dirPrompt
	if s == nil || msg.gen != m.nsAnimGen || !s.animOn {
		return nil
	}
	t := float64(fleetNow().Sub(s.animStart)) / float64(fleetNsSlideDur)
	if t >= 1 {
		s.animOn, s.animP = false, 1
		return nil
	}
	t = math.Max(t, 0)
	s.animP = 1 - math.Pow(1-t, 3)
	return nsDropTick(msg.gen)
}

// newSessionHints are the footer hints while the dropdown is open.
func (m FleetModel) newSessionHints() []fleetHint {
	hs := append([]fleetHint(nil), fleetNewSessionHints...)
	if len(m.recentEntries()) > 0 {
		hs = append(hs, fleetHint{"↑↓", "pick"})
	}
	return hs
}

// fleetNewSessionInputWidth is the input's width in a panel of width w (one
// column stays free for the cursor).
func fleetNewSessionInputWidth(cw int) int {
	return max(nsPanelWidth(cw)-fleetNsChrome-1, 4)
}

// nsPanelWidth is the panel's width on a cw-wide content area.
func nsPanelWidth(cw int) int {
	if cw < fleetNsNarrow {
		return cw
	}
	return min(fleetNsPanelW, cw)
}

// nsPanelX is the panel's left column: under the "+ open" button when it
// fits, clamped to the content width, and at the left edge when the button is
// hidden.
func (m FleetModel) nsPanelX() int {
	cw := m.contentWidth()
	w := nsPanelWidth(cw)
	x := 0
	if span := m.titleLayout(cw).open; span[1] > 0 {
		x = span[0]
	}
	return max(min(x, cw-w), 0)
}

// nsPanelLines renders the full panel (border included), one string per row.
func (m FleetModel) nsPanelLines() []string {
	s := m.dirPrompt
	cw := m.contentWidth()
	iw := max(nsPanelWidth(cw)-fleetNsChrome, 1)
	in := s.input
	in.SetWidth(max(iw-1, 1))
	in.SetCursor(in.Position()) // re-run the scroll for this width
	rows := []string{in.View()}
	if s.err != "" {
		rows = append(rows, fleetErrStyle.Render(truncateToWidth(s.err, iw)))
	}
	rows = append(rows, m.recentLines(iw)...)
	for i, r := range rows {
		rows[i] = padToWidth(r, iw)
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(theme.Current().Accent)).
		Padding(0, 1).
		Render(strings.Join(rows, "\n"))
	return strings.Split(box, "\n")
}

// nsVisibleRows is how many of the panel's rows are showing: all once
// settled, a share of them while sliding, never more than the list area.
func (m FleetModel) nsVisibleRows(full int) int {
	h := full
	if s := m.dirPrompt; s.animOn {
		h = int(math.Round(float64(full) * s.animP))
	}
	room := m.fleetListTop() + m.fleetListRows() - fleetNsPanelTop
	return max(min(h, room), 0)
}

// withNewSessionDropdown draws the dropdown over the rendered list.
func (m FleetModel) withNewSessionDropdown(list string) string {
	if !m.newSessionOpen() {
		return list
	}
	lines := m.nsPanelLines()
	h := m.nsVisibleRows(len(lines))
	if h <= 0 {
		return list
	}
	cw := m.contentWidth()
	total := len(strings.Split(list, "\n"))
	return compositeOverlayAt(list, strings.Join(lines[:h], "\n"), m.nsPanelX(), fleetNsPanelTop, cw, total)
}

// nsPanelRect is the dropdown's visible screen rectangle.
func (m FleetModel) nsPanelRect() (x, y, w, h int) {
	w = nsPanelWidth(m.contentWidth())
	return m.nsPanelX(), fleetNsPanelTop, w, m.nsVisibleRows(len(m.nsPanelLines()))
}

// nsRecentAt maps a screen cell to the recent entry drawn there, or -1.
func (m FleetModel) nsRecentAt(x, y int) int {
	px, py, pw, ph := m.nsPanelRect()
	if x < px || x >= px+pw || y < py || y >= py+ph {
		return -1
	}
	first := fleetNsPanelTop + 2 // top border, input
	if m.dirPrompt.err != "" {
		first++
	}
	i := y - first
	if i < 0 || i >= len(m.recentEntries()) {
		return -1
	}
	return i
}

// clickNewSession handles a left click at (x, y) while the dropdown is open.
// handled is false when the click landed outside the panel (the caller closes
// it); a click inside keeps it open, opening a recent entry when it hit one.
func (m *FleetModel) clickNewSession(x, y int, left bool) (cmd tea.Cmd, handled bool) {
	px, py, pw, ph := m.nsPanelRect()
	if x < px || x >= px+pw || y < py || y >= py+ph {
		return nil, false
	}
	if left {
		if i := m.nsRecentAt(x, y); i >= 0 {
			return m.openRecent(m.recentEntries()[i]), true
		}
	}
	return nil, true
}
