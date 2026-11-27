package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// clock is the pane's time source (time.Now unless a test pins it).
func (m HubStatusModel) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// liveRefresh starts the background reads behind the live signals for the
// selected row: the transcript tail for the sparkline and the PR's CI state.
// Each is nil while its cache is fresh or a read is in flight.
func (m HubStatusModel) liveRefresh() tea.Cmd {
	st := m.sel.Status
	if st == nil || !m.sel.valid() {
		return nil
	}
	now := m.clock()
	return tea.Batch(m.activity.refresh(st, now), m.ci.refresh(st, now))
}

// withLive is the selection with the cached live signals attached to a copy
// of its status block, for rendering.
func (m HubStatusModel) withLive() HubSelection {
	sel := m.sel
	if sel.Status == nil {
		return sel
	}
	st := *sel.Status
	st.activity = m.activity.calls(&st)
	st.ci = m.ci.result(&st)
	sel.Status = &st
	return sel
}
