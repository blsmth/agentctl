package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/fleet"
)

// wheelTestModel is a loaded hub with one group of n rows (lines: header,
// subheader label, then rows) in a terminal of the given height.
func wheelTestModel(t *testing.T, n, height int) FleetModel {
	t.Helper()
	InitStyles()
	m := NewFleetModel()
	m.termWidth, m.termHeight = 120, height
	m.configureSplitPane()
	m.loaded = true
	var rows []fleet.Row
	for i := 0; i < n; i++ {
		r := testRow(fmt.Sprintf("a%d", i), "s1", "idle", "")
		r.WindowID = fmt.Sprintf("@%d", i+1)
		r.Dispatched = true
		rows = append(rows, r)
	}
	m.groups = []fleet.Group{{Session: "s1", Rows: rows}}
	m.rebuildLines()
	m.resolveSelection()
	return m
}

func wheel(m FleetModel, b tea.MouseButton, x int) FleetModel {
	out, _ := m.Update(tea.MouseWheelMsg{X: x, Y: 5, Button: b})
	return out.(FleetModel)
}

func TestWheelScroll(t *testing.T) {
	tests := []struct {
		name    string
		rows    int
		wheels  []tea.MouseButton
		wantOff func(max int) int
	}{
		{"down one notch", 40, []tea.MouseButton{tea.MouseWheelDown}, func(int) int { return 3 }},
		{"down two, up one", 40, []tea.MouseButton{tea.MouseWheelDown, tea.MouseWheelDown, tea.MouseWheelUp}, func(int) int { return 3 }},
		{"up at top clamps", 40, []tea.MouseButton{tea.MouseWheelUp}, func(int) int { return 0 }},
		{"down at bottom clamps", 40, repeatBtn(tea.MouseWheelDown, 100), func(max int) int { return max }},
		{"short list no-op", 3, []tea.MouseButton{tea.MouseWheelDown}, func(int) int { return 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := wheelTestModel(t, tt.rows, 20)
			cursor := m.splitPane.Cursor()
			for _, b := range tt.wheels {
				m = wheel(m, b, 5)
			}
			if want := tt.wantOff(m.maxScrollOffset()); m.scrollOffset != want {
				t.Errorf("scrollOffset = %d, want %d", m.scrollOffset, want)
			}
			if m.splitPane.Cursor() != cursor {
				t.Errorf("cursor moved %d -> %d", cursor, m.splitPane.Cursor())
			}
		})
	}
}

func repeatBtn(b tea.MouseButton, n int) []tea.MouseButton {
	out := make([]tea.MouseButton, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestWheelMaxFillsList(t *testing.T) {
	m := wheelTestModel(t, 40, 20)
	for i := 0; i < 100; i++ {
		m = wheel(m, tea.MouseWheelDown, 5)
	}
	cw := m.contentWidth()
	if got := m.shownEnd(cw); got != len(m.lines) {
		t.Errorf("shownEnd = %d, want %d (last line visible)", got, len(m.lines))
	}
	if rows := m.rowsBetween(m.scrollOffset, len(m.lines)-1, cw); rows > m.visibleLines() {
		t.Errorf("rows from offset = %d > visible %d", rows, m.visibleLines())
	}
}

func TestWheelOverflowCounts(t *testing.T) {
	m := wheelTestModel(t, 40, 20)
	m = wheel(m, tea.MouseWheelDown, 5)
	m = wheel(m, tea.MouseWheelDown, 5)
	cw := m.contentWidth()
	above, _ := m.hiddenRows(0, m.scrollOffset)
	below, _ := m.hiddenRows(m.shownEnd(cw), len(m.lines))
	if !strings.Contains(m.renderOverflowAbove(cw), fmt.Sprintf("↑ %d more", above)) || above == 0 {
		t.Errorf("above = %q, want ↑ %d more", m.renderOverflowAbove(cw), above)
	}
	if !strings.Contains(m.renderOverflowBelow(cw), fmt.Sprintf("↓ %d more", below)) || below == 0 {
		t.Errorf("below = %q, want ↓ %d more", m.renderOverflowBelow(cw), below)
	}
}

func TestWheelClickHitsRightRow(t *testing.T) {
	m := wheelTestModel(t, 40, 20)
	m = wheel(m, tea.MouseWheelDown, 5)
	m = wheel(m, tea.MouseWheelDown, 5) // offset 6
	y := m.fleetListTop() + 2
	idx, ok := m.lineAt(5, y)
	if !ok || idx != m.scrollOffset+2 {
		t.Fatalf("lineAt = (%d,%v), want %d", idx, ok, m.scrollOffset+2)
	}
	out, _ := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
	m = out.(FleetModel)
	if m.splitPane.Cursor() != idx {
		t.Errorf("cursor = %d, want %d", m.splitPane.Cursor(), idx)
	}
}

func TestWheelHoldSurvivesReloadButNotKeys(t *testing.T) {
	m := wheelTestModel(t, 40, 20)
	m = wheel(m, tea.MouseWheelDown, 5)
	m = wheel(m, tea.MouseWheelDown, 5)
	off := m.scrollOffset
	groups := m.groups
	out, _ := m.Update(fleetLoadedMsg{groups: groups})
	m = out.(FleetModel)
	if m.scrollOffset != off {
		t.Errorf("reload moved scrollOffset %d -> %d", off, m.scrollOffset)
	}
	out, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = out.(FleetModel)
	if m.scrollOffset >= off {
		t.Errorf("key press did not snap back: offset %d (was %d)", m.scrollOffset, off)
	}
}

func TestWheelIgnoredCases(t *testing.T) {
	t.Run("menu open", func(t *testing.T) {
		m := wheelTestModel(t, 40, 20)
		m.menuOpen = true
		m = wheel(m, tea.MouseWheelDown, 5)
		if m.scrollOffset != 0 {
			t.Errorf("scrollOffset = %d, want 0", m.scrollOffset)
		}
	})
	t.Run("help open", func(t *testing.T) {
		m := wheelTestModel(t, 40, 20)
		m.menuOpen, m.menuHelp = true, true
		m = wheel(m, tea.MouseWheelDown, 5)
		if m.scrollOffset != 0 {
			t.Errorf("scrollOffset = %d, want 0", m.scrollOffset)
		}
	})
	t.Run("over live pane", func(t *testing.T) {
		m := wheelTestModel(t, 40, 20)
		m.liveLeft = 60
		m = wheel(m, tea.MouseWheelDown, 70)
		if m.scrollOffset != 0 {
			t.Errorf("scrollOffset = %d, want 0", m.scrollOffset)
		}
	})
}

func TestWheelViewerSelectionFollows(t *testing.T) {
	m := wheelTestModel(t, 40, 20)
	m = wheel(m, tea.MouseWheelDown, 5)
	m = wheel(m, tea.MouseWheelDown, 5)
	m, _ = m.syncFromViewer(fleetViewerMsg{home: "s1", window: "@40"})
	cw := m.contentWidth()
	c := m.splitPane.Cursor()
	if c < m.scrollOffset || c >= m.shownEnd(cw) {
		t.Errorf("cursor %d off screen (offset %d, shownEnd %d)", c, m.scrollOffset, m.shownEnd(cw))
	}
}

func TestWheelResizeClamps(t *testing.T) {
	m := wheelTestModel(t, 40, 20)
	for i := 0; i < 100; i++ {
		m = wheel(m, tea.MouseWheelDown, 5)
	}
	out, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = out.(FleetModel)
	if mx := m.maxScrollOffset(); m.scrollOffset > mx {
		t.Errorf("scrollOffset %d > max %d after grow", m.scrollOffset, mx)
	}
}
