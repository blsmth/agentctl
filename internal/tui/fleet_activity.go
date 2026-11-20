package tui

import (
	"fmt"
	"math"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/tmux"
)

// The hub's activity pane: a third pane at the right edge of the hub window
// running `agentctl hud --follow-hub`, a terminal-style feed of the selected
// agent's tool activity. `v` or the title row's [activity] button toggles
// it. It opens narrow and slides to its width over a few resize steps; it
// closes by being killed. While it is open the hub publishes its selection
// (hub-selection.json) for the follower. Its pane ID and any user-dragged
// width are kept in the view state (hub-view.json), so a hot reload
// reattaches to it instead of opening a second one.

// fleetActivityKey toggles the activity pane.
const fleetActivityKey = "v"

// fleetActivityButton is the title row's toggle.
const fleetActivityButton = "[activity]"

// fleetActivityGap separates the button from what precedes it.
const fleetActivityGap = "  "

// Activity pane geometry.
const (
	activityRatio      = 0.35 // default share of the hub window
	activityMinCols    = 40   // narrowest default or dragged width
	activityViewerMin  = 60   // the live viewer keeps this much beside it
	activityMinUsable  = 20   // below this the pane is refused
	activitySlideStart = 4    // width the pane is created at
	activitySlideSteps = 3    // resize steps to the full width
	activitySlideEvery = 40 * time.Millisecond
)

// hubLayout is the column split of the hub window with the activity pane
// open: list | viewer | activity, or list | activity when the viewer does
// not fit (hideViewer).
type hubLayout struct {
	list, viewer, activity int
	hideViewer             bool
	err                    error
}

// decideHubLayout splits a hub window win columns wide whose list column is
// list wide. userAct is the activity width the user dragged to (0 = none);
// the default is activityRatio of the window, at least activityMinCols. The
// live viewer keeps at least activityViewerMin columns: the activity pane
// gives up width for it down to activityMinCols, and when even that does not
// fit the activity pane replaces the viewer (taking everything right of the
// list). Each divider takes one column.
func decideHubLayout(win, list, userAct int) hubLayout {
	act := userAct
	if act <= 0 {
		act = int(math.Round(float64(win) * activityRatio))
	}
	act = max(act, activityMinCols)
	avail := win - list - 2
	if limit := avail - activityViewerMin; act > limit {
		act = limit
	}
	if act >= activityMinCols {
		return hubLayout{list: list, viewer: avail - act, activity: act}
	}
	act = win - list - 1
	if act < activityMinUsable {
		return hubLayout{list: list, err: fmt.Errorf("terminal too narrow for the activity pane")}
	}
	return hubLayout{list: list, activity: act, hideViewer: true}
}

// slideWidths are the widths the slide-in passes through on the way from
// activitySlideStart to target (ending at target).
func slideWidths(target int) []int {
	ws := make([]int, 0, activitySlideSteps)
	for i := 1; i <= activitySlideSteps; i++ {
		w := activitySlideStart + (target-activitySlideStart)*i/activitySlideSteps
		ws = append(ws, max(w, activitySlideStart))
	}
	return ws
}

// fleetActivity is the hub's activity pane state.
type fleetActivity struct {
	// pane is the open pane's ID ("" = closed). After a hot reload it comes
	// from the view state and is confirmed (or dropped) by the first poll.
	pane string
	// busy is set while an open is in flight.
	busy bool
	// slide is the slide-in in progress: its target widths, and the step
	// reached; nil when not sliding.
	slide *fleetSlide
	// width is the activity width last applied; listWidth the list width
	// that went with it. winWidth is the window width they were computed
	// for: a different one on a poll means the window was resized.
	width, listWidth, winWidth int
	// userWidth is the width the user dragged the pane to (0 = none); kept
	// across window resizes and restarts.
	userWidth int
	// hidesViewer is set while the activity pane replaces the live viewer
	// (the window cannot fit list + viewer + activity).
	hidesViewer bool
	// checked is set after the first pane poll (which reattaches a pane a
	// previous hub image left behind).
	checked bool
	// sawViewer is whether the last poll saw a live viewer pane; a change
	// means tmux reflowed the panes and the layout is re-applied rather
	// than read as a drag.
	sawViewer bool
}

// fleetSlide is a slide-in in progress.
type fleetSlide struct {
	widths []int
	step   int
}

// activityQuiet is how long list-pane resizes are not read as divider
// drags after the activity pane changed the split.
const activityQuiet = 1500 * time.Millisecond

// quietPanes starts (or extends) the window in which list-pane resizes are
// tmux reflowing the split rather than the user dragging.
func (m *FleetModel) quietPanes() { m.paneQuietUntil = fleetNow().Add(activityQuiet) }

// open reports whether the activity pane is open.
func (a fleetActivity) open() bool { return a.pane != "" }

// tmux seams for the activity pane, stubbed in tests.
var (
	activityOpenPane   = tmux.OpenActivityPane
	activityKillPanes  = tmux.KillHubActivity
	activityResize     = tmux.ResizeHubPane
	activityPanes      = tmux.HubPanes
	activityListPane   = tmux.HubListPane
	activityHideViewer = tmux.HideLiveViewer
)

// fleetActivityOpenedMsg reports an open attempt.
type fleetActivityOpenedMsg struct {
	pane string
	lay  hubLayout
	win  int
	err  error
}

// fleetActivitySlideMsg advances the slide-in of pane.
type fleetActivitySlideMsg struct{ pane string }

// fleetActivityPanesMsg is a poll of the hub window's panes.
type fleetActivityPanesMsg struct {
	panes []tmux.HubPane
	win   int
	err   error
}

// fleetActivityErrMsg reports a failed resize or kill.
type fleetActivityErrMsg struct{ err error }

// toggleActivity opens or closes the activity pane.
func (m *FleetModel) toggleActivity() tea.Cmd {
	if m.activity.busy {
		return nil
	}
	if m.activity.open() {
		return m.closeActivity()
	}
	m.activity.busy = true
	m.quietPanes()
	fallback, userLeft, userAct := m.termWidth, m.userLeft, m.activity.userWidth
	return func() tea.Msg {
		win := liveWindowWidth(fallback)
		left, _, _ := fleetLiveSplitFor(win, userLeft)
		lay := decideHubLayout(win, left, userAct)
		if lay.err != nil {
			return fleetActivityOpenedMsg{err: lay.err}
		}
		if lay.hideViewer {
			_ = activityHideViewer()
		}
		pane, err := activityOpenPane(activitySlideStart)
		return fleetActivityOpenedMsg{pane: pane, lay: lay, win: win, err: err}
	}
}

// applyActivityOpened records an opened pane and starts the slide-in.
func (m *FleetModel) applyActivityOpened(msg fleetActivityOpenedMsg) tea.Cmd {
	m.activity.busy = false
	if msg.err != nil {
		m.jumpErr = "activity: " + msg.err.Error()
		if msg.lay.hideViewer { // the viewer was dropped for nothing
			m.liveShown = ""
			return m.maybeShowCmd()
		}
		return nil
	}
	m.jumpErr = ""
	m.quietPanes()
	m.activity.pane = msg.pane
	m.activity.width, m.activity.listWidth, m.activity.winWidth = msg.lay.activity, msg.lay.list, msg.win
	m.activity.hidesViewer = msg.lay.hideViewer
	m.activity.sawViewer = !msg.lay.hideViewer
	m.activity.checked = true
	if msg.lay.hideViewer {
		m.liveShown = "" // the viewer is gone; re-shown when the pane closes
	}
	m.selPublished = false // publish the current selection for the follower now
	m.activity.slide = &fleetSlide{widths: slideWidths(msg.lay.activity)}
	return tea.Batch(activitySlideTick(msg.pane), m.saveViewCmd())
}

// activitySlideTick schedules the next slide-in step.
func activitySlideTick(pane string) tea.Cmd {
	return tea.Tick(activitySlideEvery, func(time.Time) tea.Msg { return fleetActivitySlideMsg{pane: pane} })
}

// applyActivitySlide performs one slide-in step: widen the pane, and on the
// last step pin the list column too (the split took its columns from the
// panes already there).
func (m *FleetModel) applyActivitySlide(msg fleetActivitySlideMsg) tea.Cmd {
	s := m.activity.slide
	if s == nil || msg.pane != m.activity.pane || s.step >= len(s.widths) {
		m.activity.slide = nil
		return nil
	}
	w := s.widths[s.step]
	s.step++
	last := s.step >= len(s.widths)
	pane := msg.pane
	if !last {
		return tea.Batch(activityResizeCmd(pane, w), activitySlideTick(pane))
	}
	m.activity.slide = nil
	m.quietPanes()
	return m.activityApplyCmd(pane, w, m.activity.listWidth, m.activity.hidesViewer)
}

// activityResizeCmd resizes the activity pane, reporting a failure.
func activityResizeCmd(pane string, width int) tea.Cmd {
	return func() tea.Msg {
		if err := activityResize(pane, width); err != nil {
			return fleetActivityErrMsg{err: err}
		}
		return nil
	}
}

// activityApplyCmd pins the activity pane to width and, with the viewer
// shown, the list pane to list (the viewer between them takes the rest).
func (m *FleetModel) activityApplyCmd(pane string, width, list int, hidesViewer bool) tea.Cmd {
	return func() tea.Msg {
		if !hidesViewer && list > 0 {
			if lp := activityListPane(); lp != "" {
				if err := activityResize(lp, list); err != nil {
					return fleetActivityErrMsg{err: err}
				}
			}
		}
		if err := activityResize(pane, width); err != nil {
			return fleetActivityErrMsg{err: err}
		}
		return nil
	}
}

// closeActivity kills the activity pane and, when it had replaced the live
// viewer, brings the viewer back (after the kill, so the viewer is split
// from the list into the freed columns).
func (m *FleetModel) closeActivity() tea.Cmd {
	restore := m.resetActivity()
	kill := func() tea.Msg {
		if err := activityKillPanes(); err != nil {
			return fleetActivityErrMsg{err: err}
		}
		return nil
	}
	if restore {
		if show := m.maybeShowCmd(); show != nil {
			return tea.Batch(tea.Sequence(kill, show), m.saveViewCmd())
		}
	}
	return tea.Batch(kill, m.saveViewCmd())
}

// resetActivity clears the open-pane state (keeping the user's width) and
// reports whether the live viewer must be shown again.
func (m *FleetModel) resetActivity() (restoreViewer bool) {
	restoreViewer = m.activity.hidesViewer
	m.quietPanes()
	m.activity = fleetActivity{userWidth: m.activity.userWidth, checked: true}
	m.selPublished = false
	if restoreViewer {
		m.liveShown = ""
	}
	return restoreViewer
}

// activityPollCmd polls the hub window's panes while the activity pane is
// open, and once at startup to reattach a pane a previous image left.
func (m *FleetModel) activityPollCmd() tea.Cmd {
	if !m.activity.open() && m.activity.checked {
		return nil
	}
	fallback := m.termWidth
	return func() tea.Msg {
		panes, err := activityPanes()
		if err != nil {
			return fleetActivityPanesMsg{err: err}
		}
		return fleetActivityPanesMsg{panes: panes, win: liveWindowWidth(fallback)}
	}
}

// applyActivityPanes reconciles the model with the hub window's panes: it
// reattaches a marked pane, notices one that died on its own, re-applies the
// layout after a window resize, and remembers a width the user dragged the
// pane to.
func (m *FleetModel) applyActivityPanes(msg fleetActivityPanesMsg) tea.Cmd {
	if msg.err != nil || m.activity.busy {
		return nil
	}
	m.activity.checked = true
	var cur *tmux.HubPane
	viewer := false
	for i, p := range msg.panes {
		viewer = viewer || p.Viewer
		if p.Activity && (cur == nil || p.ID == m.activity.pane) {
			cur = &msg.panes[i]
		}
	}
	if cur == nil {
		if !m.activity.open() {
			return nil
		}
		// The pane died on its own (its HUD quit, or it was killed).
		var show tea.Cmd
		if m.resetActivity() {
			show = m.maybeShowCmd()
		}
		return tea.Batch(show, m.saveViewCmd())
	}
	adopted := cur.ID != m.activity.pane
	if adopted {
		m.activity.pane = cur.ID
		m.selPublished = false
	}
	if m.activity.slide != nil {
		return nil
	}
	reflowed := viewer != m.activity.sawViewer
	m.activity.sawViewer = viewer
	if adopted || reflowed || (msg.win > 0 && msg.win != m.activity.winWidth) {
		return tea.Batch(m.applyActivityLayout(msg.win), m.saveViewCmd())
	}
	if m.activity.hidesViewer || !viewer {
		// With no viewer between them the list divider moves the activity
		// pane too: nothing to remember.
		m.activity.width = cur.Width
		return nil
	}
	if cur.Width == m.activity.width {
		return nil
	}
	// A drag of the viewer | activity divider.
	w := max(cur.Width, activityMinCols)
	m.activity.userWidth, m.activity.width = w, w
	var resize tea.Cmd
	if w != cur.Width {
		resize = activityResizeCmd(cur.ID, w)
	}
	return tea.Batch(resize, m.saveViewCmd())
}

// applyActivityLayout re-splits the hub window (win columns, 0 = unknown)
// for the open activity pane: it hides the live viewer when it no longer
// fits, brings it back when it fits again, and otherwise pins the list and
// activity widths.
func (m *FleetModel) applyActivityLayout(win int) tea.Cmd {
	if win <= 0 || !m.activity.open() {
		return nil
	}
	left, _, _ := fleetLiveSplitFor(win, m.userLeft)
	lay := decideHubLayout(win, left, m.activity.userWidth)
	m.activity.winWidth = win
	if lay.err != nil {
		m.jumpErr = "activity: " + lay.err.Error()
		return nil
	}
	m.quietPanes()
	pane, wasHidden := m.activity.pane, m.activity.hidesViewer
	m.activity.width, m.activity.listWidth = lay.activity, lay.list
	m.activity.hidesViewer = lay.hideViewer
	switch {
	case lay.hideViewer && !wasHidden:
		m.liveShown = ""
		m.activity.sawViewer = false
		hide := func() tea.Msg { _ = activityHideViewer(); return nil }
		return tea.Sequence(hide, activityResizeCmd(pane, lay.activity))
	case !lay.hideViewer && wasHidden:
		// Narrow the activity pane first, then split the viewer from the
		// list into the freed columns.
		m.liveShown = ""
		m.activity.sawViewer = true
		resize := activityResizeCmd(pane, lay.activity)
		if show := m.maybeShowCmd(); show != nil {
			return tea.Sequence(resize, show)
		}
		return resize
	}
	return m.activityApplyCmd(pane, lay.activity, lay.list, lay.hideViewer)
}

// activityButton renders the title row's toggle: dim when closed, accent
// when open.
func (m FleetModel) activityButton() string {
	if m.activity.open() {
		return fleetSuccessStyle.Render(fleetActivityButton)
	}
	return fleetCountStyle.Render(fleetActivityButton)
}

// onActivityButton reports whether (x, y) hits the title row's toggle.
func (m FleetModel) onActivityButton(x, y int) bool {
	if y != 0 {
		return false
	}
	span := m.titleLayout(m.contentWidth()).activity
	return span[1] > 0 && x >= span[0] && x < span[1]
}
