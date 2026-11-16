package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/herd"
)

// mouseTestModel builds a loaded hub with lines:
// 0 header(s1), 1 row a, 2 row b, 3 divider, 4 header(s2), 5 row c.
func mouseTestModel(t *testing.T, width, height int) HerdModel {
	t.Helper()
	InitStyles()
	row := func(name, win, sess string) herd.Row {
		r := testRow(name, sess, "idle", "")
		r.WindowID = win
		return r
	}
	m := NewHerdModel()
	m.termWidth, m.termHeight = width, height
	m.configureSplitPane()
	m.loaded = true
	m.groups = []herd.Group{
		{Session: "s1", Rows: []herd.Row{row("a", "@1", "s1"), row("b", "@2", "s1")}},
		{Session: "s2", Rows: []herd.Row{row("c", "@3", "s2")}},
	}
	m.rebuildLines()
	m.resolveSelection()
	return m
}

func TestLineAt(t *testing.T) {
	tests := []struct {
		name       string
		width      int
		height     int
		scrollTo   int // select this line first (-1 = none)
		x, y       int
		wantIdx    int
		wantDetail bool
		wantOK     bool
	}{
		{"split first row", 120, 40, -1, 5, 3, 0, false, true},
		{"split row b", 120, 40, -1, 5, 5, 2, false, true},
		{"split divider", 120, 40, -1, 5, 6, 3, false, true},
		{"split header", 120, 40, -1, 5, 7, 4, false, true},
		{"split above list", 120, 40, -1, 5, 1, -1, false, false},
		{"split below last line", 120, 40, -1, 5, 10, -1, false, false},
		{"split detail", 120, 40, -1, 60, 6, -1, true, true},
		{"split scrolled", 120, 10, 5, 5, 3, 3, false, true},
		{"flat first row", 60, 40, -1, 5, 3, 0, false, true},
		{"flat row c", 60, 40, -1, 5, 8, 5, false, true},
		{"flat above list", 60, 40, -1, 5, 2, -1, false, false},
		{"flat below last line", 60, 40, -1, 5, 9, -1, false, false},
		{"flat scrolled", 60, 10, 5, 5, 4, 4, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := mouseTestModel(t, tt.width, tt.height)
			if tt.scrollTo >= 0 {
				m.selectLine(tt.scrollTo)
			}
			idx, detail, ok := m.lineAt(tt.x, tt.y)
			if idx != tt.wantIdx || detail != tt.wantDetail || ok != tt.wantOK {
				t.Errorf("lineAt(%d,%d) = (%d,%v,%v), want (%d,%v,%v)",
					tt.x, tt.y, idx, detail, ok, tt.wantIdx, tt.wantDetail, tt.wantOK)
			}
		})
	}
}

func TestLineAt_NotLoaded(t *testing.T) {
	m := mouseTestModel(t, 120, 40)
	m.loaded = false
	if _, _, ok := m.lineAt(5, 3); ok {
		t.Error("click while loading should be ignored")
	}
}

func TestClickDecision(t *testing.T) {
	tests := []struct {
		name    string
		width   int
		x, y    int
		sel     int
		want    herdClickAction
		wantIdx int
	}{
		{"row selects", 120, 5, 4, 0, herdClickSelect, 1},
		{"header selects", 120, 5, 7, 1, herdClickSelect, 4},
		{"divider ignored", 120, 5, 6, 1, herdClickIgnore, 3},
		{"chrome ignored", 120, 5, 0, 1, herdClickIgnore, -1},
		{"detail with row selected ignored", 120, 60, 5, 1, herdClickIgnore, -1},
		{"detail with header selected ignored", 120, 60, 5, 0, herdClickIgnore, -1},
		{"flat row selects", 60, 5, 4, 0, herdClickSelect, 1},
		{"flat header selects", 60, 5, 7, 0, herdClickSelect, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := mouseTestModel(t, tt.width, 40)
			m.selectLine(tt.sel)
			idx, act := m.clickDecision(tt.x, tt.y)
			if act != tt.want || idx != tt.wantIdx {
				t.Errorf("clickDecision = (%d,%v), want (%d,%v)", idx, act, tt.wantIdx, tt.want)
			}
		})
	}
}

func TestClickHeaderSelectsWithoutJump(t *testing.T) {
	m := mouseTestModel(t, 120, 40)
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 7})
	got := next.(HerdModel)
	if !got.selIsHeader || got.selSession != "s2" || got.selWindowID != "" {
		t.Errorf("selection = %q/%q/%v, want header of s2", got.selSession, got.selWindowID, got.selIsHeader)
	}
	if got.jumpErr != "" {
		t.Errorf("unexpected jump attempt: %q", got.jumpErr)
	}
}

func TestClickRowSelectsWithoutOpening(t *testing.T) {
	for _, reclick := range []bool{false, true} {
		m := mouseTestModel(t, 120, 40)
		m.selectLine(0)
		msg := tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 5}
		next, cmd := m.Update(msg)
		if reclick {
			next, cmd = next.(HerdModel).Update(msg)
		}
		got := next.(HerdModel)
		if cursorOf(got) != 2 || got.selWindowID != "@2" || got.selSession != "s1" || got.selIsHeader {
			t.Errorf("selection = cursor %d %q/%q header=%v, want row b (@2)",
				cursorOf(got), got.selSession, got.selWindowID, got.selIsHeader)
		}
		if got.jumpErr != "" {
			t.Errorf("unexpected jump attempt: %q", got.jumpErr)
		}
		if cmd == nil {
			t.Errorf("reclick=%v: expected preview capture cmd", reclick)
		}
	}
}

func TestClickRightButtonIgnored(t *testing.T) {
	m := mouseTestModel(t, 120, 40)
	before := cursorOf(m)
	next, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: 5, Y: 7})
	if cmd != nil || cursorOf(next) != before {
		t.Error("right click should be ignored")
	}
}

func TestWheelMovesCursor(t *testing.T) {
	m := mouseTestModel(t, 120, 40)
	m.selectLine(0)
	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 5, Y: 4})
	got := next.(HerdModel)
	if cursorOf(got) != 1 || got.selWindowID != "@1" {
		t.Errorf("after wheel down cursor=%d sel=%q, want 1/@1", cursorOf(got), got.selWindowID)
	}
	next, _ = got.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 5, Y: 4})
	if c := cursorOf(next); c != 0 {
		t.Errorf("after wheel up cursor=%d, want 0", c)
	}
}

func cursorOf(m tea.Model) int {
	h := m.(HerdModel)
	return h.splitPane.Cursor()
}
