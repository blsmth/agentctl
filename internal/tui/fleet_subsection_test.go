package tui

import (
	"testing"

	"github.com/blsmth/agentctl/internal/fleet"
)

func subRow(win string, dispatched bool) fleet.Row {
	r := testRow("agent"+win, "s1", "idle", "")
	r.WindowID = win
	r.Dispatched = dispatched
	return r
}

func subModel(t *testing.T, rows ...fleet.Row) FleetModel {
	t.Helper()
	InitStyles()
	m := NewFleetModel()
	m.termWidth, m.termHeight = 120, 40
	m.configureSplitPane()
	m.loaded = true
	m.groups = []fleet.Group{{Session: "s1", Project: "p1", Rows: rows}}
	m.rebuildLines()
	m.resolveSelection()
	return m
}

// describe returns a compact kind/label sequence: H, B:<label>, R:<win>.
func describe(m FleetModel) []string {
	var out []string
	for _, l := range m.lines {
		switch l.kind {
		case fleetLineHeader:
			out = append(out, "H")
		case fleetLineSubheader:
			out = append(out, "S:"+l.label)
		case fleetLineRow:
			out = append(out, "R:"+l.row.WindowID)
		case fleetLineDivider:
			out = append(out, "D")
		default:
			out = append(out, "P")
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRebuildLinesSubsections(t *testing.T) {
	m := subModel(t, subRow("@1", false), subRow("@2", true), subRow("@3", false), subRow("@4", true))
	want := []string{"H", "R:@1", "R:@3", "S:dispatched (2)", "R:@2", "R:@4"}
	if got := describe(m); !equalStrings(got, want) {
		t.Errorf("lines = %v, want %v", got, want)
	}
	if !m.lines[1].groupHead || m.lines[2].groupHead || m.lines[4].groupHead {
		t.Error("only the first row of the group should be groupHead")
	}
}

func TestRebuildLinesOnlyDispatched(t *testing.T) {
	m := subModel(t, subRow("@1", true), subRow("@2", true))
	want := []string{"H", "S:dispatched (2)", "R:@1", "R:@2"}
	if got := describe(m); !equalStrings(got, want) {
		t.Errorf("lines = %v, want %v", got, want)
	}
}

func TestCursorSkipsSubheader(t *testing.T) {
	m := subModel(t, subRow("@1", false), subRow("@2", true))
	m.selectLine(1) // bare row @1
	m.moveCursor(1)
	if l := m.lines[m.splitPane.Cursor()]; l.kind != fleetLineRow || l.row.WindowID != "@2" {
		t.Errorf("down from last bare row landed on %+v, want row @2", l)
	}
	m.moveCursor(-1)
	if l := m.lines[m.splitPane.Cursor()]; l.kind != fleetLineRow || l.row.WindowID != "@1" {
		t.Errorf("up landed on %+v, want row @1", l)
	}
}

func TestClickSubheaderIgnored(t *testing.T) {
	m := subModel(t, subRow("@1", false), subRow("@2", true))
	// Lines: 0 header, 1 row, 2 dispatched label, 3 row.
	for _, idx := range []int{2} {
		if m.lines[idx].kind != fleetLineSubheader {
			t.Fatalf("line %d is not a subheader", idx)
		}
		// Content starts a few rows below the chrome; probe each y for the line.
		for y := 0; y < 20; y++ {
			got, ok := m.lineAt(5, y)
			if !ok || got != idx {
				continue
			}
			if _, act := m.buttonDecision(5, y, 1); act != fleetClickIgnore { // tea.MouseLeft
				t.Errorf("click on subheader %d action = %v, want ignore", idx, act)
			}
		}
	}
}

func TestCloseLastBareSelectsNeighbourNotSubheader(t *testing.T) {
	m := subModel(t, subRow("@1", false), subRow("@2", true))
	m.selectLine(1)
	m.recordClose(m.lines[1].row)
	if m.closePending.nextID != "@2" {
		t.Errorf("nextID = %q, want @2 (first dispatched row)", m.closePending.nextID)
	}
	m.closePending.killed = true
	m.groups[0].Rows = []fleet.Row{subRow("@2", true)}
	m.rebuildLines()
	m.applyClosePending()
	m.resolveSelection()
	l := m.lines[m.splitPane.Cursor()]
	if l.kind != fleetLineRow || l.row.WindowID != "@2" {
		t.Errorf("selection after close = %+v, want row @2", l)
	}
}
