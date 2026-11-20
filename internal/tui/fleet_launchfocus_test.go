package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/fleet"
)

// execCmds executes cmd (flattening batches and sequences one level deep is
// enough here) and discards messages; side effects are what the tests count.
func execCmds(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			execCmds(c)
		}
	}
}

func stubFocusViewer(t *testing.T) *int {
	t.Helper()
	old := focusViewerPane
	n := 0
	focusViewerPane = func() error { n++; return nil }
	t.Cleanup(func() { focusViewerPane = old })
	return &n
}

func loadedWithNew(m FleetModel, win string) FleetModel {
	groups := append([]fleet.Group(nil), m.groups...)
	r := testRow("new", "s1", "idle", "")
	r.WindowID = win
	groups[0].Rows = append(append([]fleet.Row(nil), groups[0].Rows...), r)
	mm, _ := m.Update(fleetLoadedMsg{groups: groups})
	return mm.(FleetModel)
}

func TestLaunchFocusesLivePane(t *testing.T) {
	launch := func(m FleetModel, shell bool) FleetModel {
		var msg tea.Msg = fleetLaunchedMsg{session: "s1", windowID: "@9"}
		if shell {
			msg = fleetShellOpenedMsg{session: "s1", windowID: "@9"}
		}
		mm, _ := m.Update(msg)
		return mm.(FleetModel)
	}
	for _, shell := range []bool{false, true} {
		name := "focus after show agent"
		if shell {
			name = "focus after show shell"
		}
		t.Run(name, func(t *testing.T) {
			n := stubFocusViewer(t)
			m := launch(mouseTestModel(t, 80, 24), shell)
			m = loadedWithNew(m, "@9")
			if m.focusOnShow != "s1|@9" {
				t.Fatalf("focusOnShow=%q", m.focusOnShow)
			}
			if *n != 0 {
				t.Fatal("focused before the live pane showed the row")
			}
			mm, cmd := m.Update(fleetLiveMsg{key: "s1|@9"})
			execCmds(cmd)
			if *n != 1 || mm.(FleetModel).focusOnShow != "" {
				t.Fatalf("calls=%d", *n)
			}
		})
	}
	t.Run("manual selection in between", func(t *testing.T) {
		n := stubFocusViewer(t)
		m := loadedWithNew(launch(mouseTestModel(t, 80, 24), false), "@9")
		m.selectLine(0)
		mm, cmd := m.Update(fleetLiveMsg{key: "s1|@9"})
		execCmds(cmd)
		if *n != 0 || mm.(FleetModel).focusOnShow != "" {
			t.Fatalf("calls=%d", *n)
		}
	})
	t.Run("keyboard move in between", func(t *testing.T) {
		n := stubFocusViewer(t)
		m := loadedWithNew(launch(mouseTestModel(t, 80, 24), false), "@9")
		m.moveCursor(-1)
		_, cmd := m.Update(fleetLiveMsg{key: "s1|@9"})
		execCmds(cmd)
		if *n != 0 {
			t.Fatalf("calls=%d", *n)
		}
	})
	t.Run("stale selection without clearing", func(t *testing.T) {
		n := stubFocusViewer(t)
		m := loadedWithNew(launch(mouseTestModel(t, 80, 24), false), "@9")
		m.focusOnShow = "s1|@9"
		m.splitPane.SetCursor(0)
		m.syncSelectionFromCursor()
		_, cmd := m.Update(fleetLiveMsg{key: "s1|@9"})
		execCmds(cmd)
		if *n != 0 {
			t.Fatalf("calls=%d", *n)
		}
	})
	t.Run("second load while show in flight", func(t *testing.T) {
		n := stubFocusViewer(t)
		m := loadedWithNew(launch(mouseTestModel(t, 80, 24), false), "@9")
		m.liveShown = "s1|@9" // show issued, result pending
		mm, cmd := m.Update(fleetLoadedMsg{groups: m.groups})
		execCmds(cmd)
		if *n != 0 || mm.(FleetModel).focusOnShow != "s1|@9" {
			t.Fatalf("calls=%d focusOnShow=%q", *n, mm.(FleetModel).focusOnShow)
		}
	})
	t.Run("retry give-up", func(t *testing.T) {
		n := stubFocusViewer(t)
		m := launch(mouseTestModel(t, 80, 24), false)
		msg := fleetFocusRetryMsg{session: "s1", windowID: "@9"}
		for i := 0; i <= fleetFocusMaxRetries; i++ {
			m.focusRetry(msg)
		}
		m = loadedWithNew(m, "@9")
		mm, cmd := m.Update(fleetLiveMsg{key: "s1|@9"})
		execCmds(cmd)
		if *n != 0 || mm.(FleetModel).focusOnShow != "" {
			t.Fatalf("calls=%d", *n)
		}
	})
	t.Run("restore-style pendingFocus", func(t *testing.T) {
		n := stubFocusViewer(t)
		m := mouseTestModel(t, 80, 24)
		m.pendingFocus = &fleetPendingFocus{session: "s1", windowID: "@9"}
		m = loadedWithNew(m, "@9")
		mm, cmd := m.Update(fleetLiveMsg{key: "s1|@9"})
		execCmds(cmd)
		if *n != 0 || m.focusOnShow != "" || mm.(FleetModel).focusOnShow != "" {
			t.Fatalf("calls=%d", *n)
		}
	})
	t.Run("launch error", func(t *testing.T) {
		n := stubFocusViewer(t)
		m := mouseTestModel(t, 80, 24)
		mm, _ := m.Update(fleetLaunchedMsg{session: "s1", err: errTest})
		m = loadedWithNew(mm.(FleetModel), "@9")
		_, cmd := m.Update(fleetLiveMsg{key: "s1|@9"})
		execCmds(cmd)
		if *n != 0 {
			t.Fatalf("calls=%d", *n)
		}
	})
}
