package tui

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
)

func TestViewerSyncDecide(t *testing.T) {
	row := func(s, w string) fleetLine {
		return fleetLine{kind: fleetLineRow, row: fleet.Row{Session: s, AgentState: state.AgentState{WindowID: w}}}
	}
	lines := []fleetLine{row("agentctl-a", "@1"), row("agentctl-a", "@2"), {kind: fleetLineHeader}}
	const (
		a1 = "agentctl-a|@1"
		a2 = "agentctl-a|@2"
		a3 = "agentctl-a|@3"
		a9 = "agentctl-a|@9"
	)
	tests := []struct {
		name        string
		st          viewerSyncState
		home, win   string
		intent, tok string
		want        viewerSyncDecision
	}{
		{"tab click is followed", viewerSyncState{liveShown: a1, seen: a1}, "agentctl-a", "@2", "@2", "t1",
			viewerSyncDecision{seen: a2, action: viewerSyncFollow, target: a2}},
		{"index shift repoints to the watched window", viewerSyncState{liveShown: a1, seen: a1}, "agentctl-a", "@2", "", "",
			viewerSyncDecision{seen: a1, action: viewerSyncRepoint, target: a1, repoint: true}},
		{"shift onto a window not in the list repoints", viewerSyncState{liveShown: a1, seen: a1}, "agentctl-a", "@9", "", "",
			viewerSyncDecision{seen: a1, action: viewerSyncRepoint, target: a1, repoint: true}},
		{"spent click while the hub's show is in flight", viewerSyncState{liveShown: a1, seen: a2, intent: "t1"}, "agentctl-a", "@2", "@2", "t1",
			viewerSyncDecision{seen: a2}},
		{"spent click does not pull the hub back", viewerSyncState{liveShown: a1, seen: a1, intent: "t1"}, "agentctl-a", "@2", "@2", "t1",
			viewerSyncDecision{seen: a1, action: viewerSyncRepoint, target: a1, repoint: true}},
		{"click and shift in one poll", viewerSyncState{liveShown: a1, seen: a1}, "agentctl-a", "@3", "@2", "t2",
			viewerSyncDecision{seen: a3, action: viewerSyncFollow, target: a2, repoint: true}},
		{"click on a non-row window stays put", viewerSyncState{liveShown: a1, seen: a1}, "agentctl-a", "@9", "@9", "t3",
			viewerSyncDecision{seen: a9}},
		{"click on a non-row window, then shifted", viewerSyncState{liveShown: a1, seen: a1}, "agentctl-a", "@3", "@9", "t3",
			viewerSyncDecision{seen: a3, action: viewerSyncRepoint, target: a9, repoint: true}},
		{"click on the shown window is a no-op", viewerSyncState{liveShown: a1, seen: a1}, "agentctl-a", "@1", "@1", "t4",
			viewerSyncDecision{seen: a1}},
		{"hub caused change", viewerSyncState{liveShown: a2, seen: a1}, "agentctl-a", "@2", "", "",
			viewerSyncDecision{seen: a2}},
		{"unchanged", viewerSyncState{liveShown: a1, seen: a1}, "agentctl-a", "@1", "", "",
			viewerSyncDecision{seen: a1}},
		{"stale poll while hub switching", viewerSyncState{liveShown: a2, seen: a1}, "agentctl-a", "@1", "", "",
			viewerSyncDecision{seen: a1}},
		{"watched window closed: follow as before", viewerSyncState{liveShown: a3, seen: a3}, "agentctl-a", "@2", "", "",
			viewerSyncDecision{seen: a2, action: viewerSyncFollow, target: a2}},
		{"watched window closed onto a non-row", viewerSyncState{liveShown: a3, seen: a3}, "agentctl-a", "@9", "", "",
			viewerSyncDecision{seen: a9}},
		{"nothing shown: follow as before", viewerSyncState{seen: a1}, "agentctl-a", "@2", "", "",
			viewerSyncDecision{seen: a2, action: viewerSyncFollow, target: a2}},
		{"failed hand-off is not forced", viewerSyncState{liveShown: a1, liveFailed: a1, seen: a1}, "agentctl-a", "@2", "", "",
			viewerSyncDecision{seen: a2, action: viewerSyncFollow, target: a2}},
		{"viewer on another home", viewerSyncState{liveShown: "agentctl-b|@5", seen: a1}, "agentctl-a", "@2", "", "",
			viewerSyncDecision{seen: a2, action: viewerSyncFollow, target: a2}},
		{"no viewer", viewerSyncState{liveShown: a1, seen: a1}, "", "", "", "",
			viewerSyncDecision{seen: a1}},
	}
	isRow := func(s, w string) bool { return fleetLinesHaveRow(lines, s, w) }
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := fleetViewerMsg{viewer: "agentctl-hubview", home: tt.home, window: tt.win, intent: tt.intent, token: tt.tok}
			if got := viewerSyncDecide(lines, tt.st, msg, isRow); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// viewerTestModel is a hub over foldGroups watching s1's @1, past its first
// viewer poll, with the repoint and zoom seams recording instead of tmux.
func viewerTestModel(t *testing.T) (FleetModel, *[]string) {
	t.Helper()
	m := foldModel(t)
	m.selSession, m.selWindowID = "s1", "@1"
	m.resolveSelection()
	m.liveShown, m.viewerSeen, m.viewerPolled = "s1|@1", "s1|@1", true
	var calls []string
	oldRepoint, oldZoom, oldShow, oldWidth, oldApplied := repointViewer, hubZoomSync, showLiveViewer, hubWindowWidth, fleetViewerApplied
	repointViewer = func(viewer, window string) error {
		calls = append(calls, viewer+" "+window)
		return nil
	}
	showLiveViewer = func(sess, win string, _ int) error {
		calls = append(calls, "show "+sess+"|"+win)
		return nil
	}
	hubWindowWidth = func() (int, error) { return 200, nil }
	hubZoomSync = func(bool) {}
	fleetViewerApplied = 0
	t.Cleanup(func() {
		repointViewer, hubZoomSync, showLiveViewer, hubWindowWidth, fleetViewerApplied = oldRepoint, oldZoom, oldShow, oldWidth, oldApplied
	})
	return m, &calls
}

func runViewerCmd(cmd tea.Cmd) {
	if cmd != nil {
		cmd()
	}
}

func TestSyncFromViewerIndexShiftKeepsSelection(t *testing.T) {
	m, calls := viewerTestModel(t)
	before := lineKeys(m)
	m, cmd := m.syncFromViewer(fleetViewerMsg{viewer: "agentctl-hubview", home: "s1", window: "@2"})
	runViewerCmd(cmd)
	if r := m.selectedRow(); r == nil || r.WindowID != "@1" {
		t.Errorf("selected = %+v, want @1 kept", r)
	}
	if m.liveShown != "s1|@1" {
		t.Errorf("liveShown = %q, want s1|@1", m.liveShown)
	}
	if !slices.Equal(lineKeys(m), before) {
		t.Errorf("an index shift changed the list: %v -> %v", before, lineKeys(m))
	}
	if !slices.Equal(*calls, []string{"agentctl-hubview @1"}) {
		t.Errorf("repoint calls = %v, want the viewer back on @1", *calls)
	}
}

func TestSyncFromViewerTabClickFollows(t *testing.T) {
	m, calls := viewerTestModel(t)
	m, cmd := m.syncFromViewer(fleetViewerMsg{viewer: "agentctl-hubview", home: "s1", window: "@3", intent: "@3", token: "@3/1"})
	runViewerCmd(cmd)
	if r := m.selectedRow(); r == nil || r.WindowID != "@3" {
		t.Errorf("selected = %+v, want @3", r)
	}
	if len(*calls) != 0 {
		t.Errorf("repoint calls = %v, want none", *calls)
	}
	// Later the hub moves on; the same (spent) click must not pull it back,
	// and an index shift now repoints to the hub's window.
	m.selSession, m.selWindowID, m.liveShown = "s1", "@1", "s1|@1"
	m.resolveSelection()
	m, cmd = m.syncFromViewer(fleetViewerMsg{viewer: "agentctl-hubview", home: "s1", window: "@1", intent: "@3", token: "@3/1"})
	runViewerCmd(cmd)
	m, cmd = m.syncFromViewer(fleetViewerMsg{viewer: "agentctl-hubview", home: "s1", window: "@3", intent: "@3", token: "@3/1"})
	runViewerCmd(cmd)
	if r := m.selectedRow(); r == nil || r.WindowID != "@1" {
		t.Errorf("selected = %+v, want @1 after a spent click", r)
	}
	if !slices.Equal(*calls, []string{"agentctl-hubview @1"}) {
		t.Errorf("repoint calls = %v", *calls)
	}
	// A second click on the same window is a new token and is followed.
	m, _ = m.syncFromViewer(fleetViewerMsg{viewer: "agentctl-hubview", home: "s1", window: "@3", intent: "@3", token: "@3/2"})
	if r := m.selectedRow(); r == nil || r.WindowID != "@3" {
		t.Errorf("selected = %+v, want @3 after a new click", r)
	}
}

func TestSyncFromViewerClickAndShiftInOnePoll(t *testing.T) {
	m, calls := viewerTestModel(t)
	m, cmd := m.syncFromViewer(fleetViewerMsg{viewer: "agentctl-hubview", home: "s1", window: "@1", intent: "@3", token: "@3/1"})
	runViewerCmd(cmd)
	if r := m.selectedRow(); r == nil || r.WindowID != "@3" {
		t.Errorf("selected = %+v, want the clicked @3", r)
	}
	if !slices.Equal(*calls, []string{"agentctl-hubview @3"}) {
		t.Errorf("repoint calls = %v, want the viewer on the clicked @3", *calls)
	}
}

func TestSyncFromViewerStartupIntentIgnored(t *testing.T) {
	m, calls := viewerTestModel(t)
	m.viewerPolled = false
	m, cmd := m.syncFromViewer(fleetViewerMsg{viewer: "agentctl-hubview", home: "s1", window: "@3", intent: "@3", token: "@3/1"})
	runViewerCmd(cmd)
	if r := m.selectedRow(); r == nil || r.WindowID != "@1" {
		t.Errorf("selected = %+v, want @1: a click from before the hub started is not followed", r)
	}
	if !slices.Equal(*calls, []string{"agentctl-hubview @1"}) {
		t.Errorf("repoint calls = %v", *calls)
	}
}

func TestSyncFromViewerWatchedWindowClosed(t *testing.T) {
	m, calls := viewerTestModel(t)
	m.liveShown, m.viewerSeen = "s1|@9", "s1|@9" // a window no longer listed
	m, cmd := m.syncFromViewer(fleetViewerMsg{viewer: "agentctl-hubview", home: "s1", window: "@3"})
	runViewerCmd(cmd)
	if r := m.selectedRow(); r == nil || r.WindowID != "@3" {
		t.Errorf("selected = %+v, want @3 (follow as before)", r)
	}
	if len(*calls) != 0 {
		t.Errorf("repoint calls = %v, want none", *calls)
	}
}

// A repoint decided on a poll must not land after a show the hub decided
// later (fast j/k during a dispatch): the viewer would sit on the old window
// with the hub on the new one, and nothing would correct it.
func TestSyncFromViewerStaleRepointAfterShow(t *testing.T) {
	m, calls := viewerTestModel(t)
	m, repoint := m.syncFromViewer(fleetViewerMsg{viewer: "agentctl-hubview", home: "s1", window: "@2"})
	if repoint == nil {
		t.Fatal("drift should repoint")
	}
	show := m.liveShowFor("s1", "@3") // the user moves on before the repoint runs
	if show == nil {
		t.Fatal("selection change should show")
	}
	show()
	repoint() // reaches the lock last
	if !slices.Equal(*calls, []string{"show s1|@3"}) {
		t.Errorf("viewer calls = %v, want only the newer show", *calls)
	}
	// The viewer is on @3 now: the next poll leaves everything alone.
	m, cmd := m.syncFromViewer(fleetViewerMsg{viewer: "agentctl-hubview", home: "s1", window: "@3"})
	if cmd != nil || m.liveShown != "s1|@3" {
		t.Errorf("after the show: cmd=%v liveShown=%q, want no repoint on s1|@3", cmd != nil, m.liveShown)
	}

	// In decision order the repoint still runs.
	*calls = nil
	m, repoint = m.syncFromViewer(fleetViewerMsg{viewer: "agentctl-hubview", home: "s1", window: "@1"})
	runViewerCmd(repoint)
	if !slices.Equal(*calls, []string{"agentctl-hubview @3"}) {
		t.Errorf("viewer calls = %v, want the repoint to @3", *calls)
	}
}
