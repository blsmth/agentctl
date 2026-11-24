package tui

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func countPads(m FleetModel) int {
	n := 0
	for _, l := range m.lines {
		if l.kind == fleetLineGroupPad {
			n++
		}
	}
	return n
}

func TestGroupPadShortBodiesReachMinimum(t *testing.T) {
	m := foldModel(t)
	// s3 has one row: padded to fleetGroupMinBody lines; s2 has two rows
	// (header + 2 + 1 pad); s1 (tree of 3 rows) already meets the minimum;
	// s4 is last and never padded.
	want := []string{"H:s1", "R:p", "R:k", "R:x", "-", "H:s2", "R:d", "R:e", "_", "-", "H:s3", "R:g", "_", "_", "-", "H:s4", "P:s4"}
	if got := lineKeys(m); !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

func TestGroupPadNotSelectableOrCounted(t *testing.T) {
	if (fleetLine{kind: fleetLineGroupPad}).selectable() {
		t.Error("pad line must not be selectable")
	}
}

func TestGroupPadFoldedAndLastGetNone(t *testing.T) {
	m := foldModel(t)
	for _, s := range []string{"s1", "s2", "s3"} {
		m.setFolded(s, true)
	}
	m.relayout()
	if n := countPads(m); n != 0 {
		t.Errorf("folded groups padded: %v", lineKeys(m))
	}
	// A single group is the last one: no padding.
	m = foldModel(t)
	m.groups = m.groups[2:3]
	m.rebuildLines()
	if n := countPads(m); n != 0 {
		t.Errorf("last group padded: %v", lineKeys(m))
	}
}

func TestGroupPadSearchGetsNone(t *testing.T) {
	m := searchFor(t, searchModel(t), "e")
	if !m.searching() {
		t.Fatal("not searching")
	}
	if n := countPads(m); n != 0 {
		t.Errorf("search padded groups: %v", lineKeys(m))
	}
}

func TestGroupPadCursorSkipsToNextHeader(t *testing.T) {
	m := foldModel(t)
	m.selectLine(lineIndex(t, m, "R:g"))
	m, _ = pressHub(m, "j")
	if got := lineKeys(m)[cursorOf(m)]; got != "H:s4" {
		t.Errorf("j from last padded row landed on %s, want H:s4", got)
	}
	m, _ = pressHub(m, "k")
	if got := lineKeys(m)[cursorOf(m)]; got != "R:g" {
		t.Errorf("k back landed on %s, want R:g", got)
	}
}

func TestGroupPadClickIsNoop(t *testing.T) {
	m := foldModel(t)
	pad := slices.Index(lineKeys(m), "_")
	if pad < 0 {
		t.Fatal("no pad line")
	}
	y := -1
	for yy := 0; yy < m.termHeight; yy++ {
		if got, _, ok := m.lineAtSub(10, yy); ok && got == pad {
			y = yy
			break
		}
	}
	if y < 0 {
		t.Fatal("pad line not on screen")
	}
	before := cursorOf(m)
	next, _ := m.Update(tea.MouseClickMsg{X: 10, Y: y, Button: tea.MouseLeft})
	m = next.(FleetModel)
	if cursorOf(m) != before {
		t.Errorf("click on pad moved the cursor %d -> %d", before, cursorOf(m))
	}
	if m.lines[cursorOf(m)].kind == fleetLineGroupPad {
		t.Error("cursor rests on a pad line")
	}
}
