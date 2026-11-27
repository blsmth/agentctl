package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/tmux"
)

// The hub's status pane: a short pane stacked above the live viewer pane
// running `agentctl hub-status`, which shows the selected row's detail (the
// state and title, the needs-you line, the full recap wrapped and the facts) that the
// list otherwise expands beneath the row. It opens and closes with the
// viewer: every show of the viewer ensures it (found by its pane option, so
// a hot-reloaded hub reuses the one it finds), and every hide of the viewer
// kills it (tmux.HideLiveViewer). While it is open the list draws no inline
// detail lines; with no viewer (narrow hub, Schedules section, activity pane
// in the viewer's place, a failed show) the inline expansion is back. The
// content comes from the hub selection file (HubSelection.Status).

// Status pane geometry. fleetStatusRows fits a title, a needs-you line, up to
// three recap lines and the facts (the height stays fixed so the viewer never
// resizes when the selection changes; content packs upward and short rows
// leave blanks). It matches the inline detail (three text lines plus chips)
// plus its title, so nothing that fits under a row is lost, while the viewer
// gives up only seven rows (with the divider).
// Windows shorter than fleetStatusMinWindowRows keep every row for the
// viewer and the list keeps the inline detail.
const (
	fleetStatusRows          = 6
	fleetStatusMinWindowRows = 24
)

// tmux seams for the status pane, stubbed in tests.
var (
	hubStatusSync = tmux.EnsureHubStatusPane
	hubStatusPin  = tmux.PinHubStatusPane
)

// fleetStatusMsg reports the status pane's state after a sync.
type fleetStatusMsg struct{ open bool }

// statusWanted reports whether a hub window height rows tall gets a status
// pane.
func statusWanted(height int) bool { return height >= fleetStatusMinWindowRows }

// statusPaneOpen reports whether the status pane is showing the selection:
// it was last reported open and the viewer it sits above is up.
func (m FleetModel) statusPaneOpen() bool {
	return m.statusPane && m.liveShown != "" && m.liveShown != m.liveFailed &&
		!m.activity.hidesViewer
}

// inlineDetail reports whether the selected row expands its detail lines
// beneath itself: only when there is no status pane to show them.
func (m FleetModel) inlineDetail() bool { return !m.statusPaneOpen() }

// syncStatusPane ensures (want) or kills the status pane and reports whether
// it is open. Called with fleetViewerMu held or from a command that takes it,
// so it never races a viewer show or hide.
func syncStatusPane(want bool) bool {
	open, err := hubStatusSync(want, fleetStatusRows)
	if err != nil {
		logging.Log("hub: status pane: %v", err)
	}
	return open
}

// statusResizeCmd re-syncs the status pane after the hub window's height
// changed: it opens or closes it across fleetStatusMinWindowRows, and pins
// it back to fleetStatusRows (tmux spreads a height change over the viewer
// column's panes).
func (m *FleetModel) statusResizeCmd() tea.Cmd {
	if m.liveShown == "" || m.activity.hidesViewer {
		return nil
	}
	want := statusWanted(m.termHeight)
	return func() tea.Msg {
		fleetViewerMu.Lock()
		defer fleetViewerMu.Unlock()
		open := syncStatusPane(want)
		if open {
			if err := hubStatusPin(fleetStatusRows); err != nil {
				logging.Log("hub: pinning status pane: %v", err)
			}
		}
		return fleetStatusMsg{open: open}
	}
}

// applyStatus records whether the status pane is open. A change moves the
// selected row's detail between the list and the pane, so the row heights
// the scroll and hit testing count change with it.
func (m *FleetModel) applyStatus(open bool) {
	if open == m.statusPane {
		return
	}
	m.statusPane = open
	m.selPublished = false // a new follower: publish the selection now
	m.clampScroll()
	m.ensureCursorVisible()
}
