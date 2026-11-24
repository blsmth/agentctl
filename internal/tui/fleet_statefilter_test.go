package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func clickAt(t *testing.T, m FleetModel, x, y int) FleetModel {
	t.Helper()
	next, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	return next.(FleetModel)
}

func TestStateFilterKeysRoundTrip(t *testing.T) {
	for _, f := range []fleetFilter{fleetFilterAll, fleetFilterActive, fleetFilterNeeds, fleetFilterBlocked, fleetFilterWorking, fleetFilterDone, fleetFilterIdle} {
		if got := parseFleetFilter(f.key()); got != f {
			t.Errorf("parse(key(%v)) = %v", f, got)
		}
	}
	for f, want := range map[fleetFilter]string{fleetFilterBlocked: "blocked", fleetFilterWorking: "working", fleetFilterDone: "done", fleetFilterIdle: "idle"} {
		if f.label() != want || f.key() != want {
			t.Errorf("%v: label %q key %q, want %q", f, f.label(), f.key(), want)
		}
	}
}

func TestStateFilterMatches(t *testing.T) {
	m := foldModel(t)
	want := map[fleetFilter][]string{
		fleetFilterBlocked: {"g"},
		fleetFilterWorking: {"k"},
		fleetFilterDone:    {"d"},
		fleetFilterIdle:    {"p", "x", "e"},
	}
	for f, names := range want {
		var got []string
		for _, g := range m.groups {
			for _, r := range g.Rows {
				if f.matches(r) {
					got = append(got, r.Name)
				}
			}
		}
		slices.Sort(got)
		slices.Sort(names)
		if !slices.Equal(got, names) {
			t.Errorf("%v matches %v, want %v", f, got, names)
		}
	}
	shell := fleet.Row{Shell: true}
	for f := range want {
		if f.matches(shell) {
			t.Errorf("%v matched a shell row", f)
		}
	}
}

func TestStateFilterKeepsAncestorsNotChildren(t *testing.T) {
	m := foldModel(t)
	kept, ctx := filterRows(m.groups[0].Rows, fleetFilterWorking)
	if len(kept) != 2 || kept[0].Name != "p" || kept[1].Name != "k" || !ctx["@1"] {
		t.Errorf("kept %v ctx %v, want p (context) and k", kept, ctx)
	}
	kept, _ = filterRows(m.groups[0].Rows, fleetFilterIdle)
	for _, r := range kept {
		if r.Name == "k" {
			t.Errorf("idle parent pulled in its working child")
		}
	}
}

func TestFKeyLeavesStateFilter(t *testing.T) {
	m := foldModel(t)
	m.filter = fleetFilterWorking
	m.cycleFilter()
	if m.filter != fleetFilterAll {
		t.Errorf("f from state filter = %v, want all", m.filter)
	}
	m.cycleFilter()
	m.cycleFilter()
	m.cycleFilter()
	if m.filter != fleetFilterAll {
		t.Errorf("full cycle = %v, want all", m.filter)
	}
}

// withAttention marks s2's idle row "e" (@5) as needing attention.
func withAttention(m FleetModel) FleetModel {
	m.groups[1].Rows[1].Attention = &windowstate.Attention{Reason: windowstate.AttentionQuestion, Detail: "Merge it?"}
	m.rebuildLines()
	return m
}

func TestTitleCountClicks(t *testing.T) {
	m := withAttention(foldModel(t))
	m.liveLeft = 0
	for i, want := range fleetStateFilters {
		l := m.titleLayout(m.contentWidth())
		sp := l.counts[i]
		if sp[1] == 0 {
			t.Fatalf("%v: no span", want)
		}
		m2 := clickAt(t, m, sp[0], 0)
		if m2.filter != want {
			t.Fatalf("click %v: filter = %v", want, m2.filter)
		}
		if m3 := clickAt(t, m2, m2.titleLayout(m2.contentWidth()).counts[i][1]-1, 0); m3.filter != fleetFilterAll {
			t.Errorf("second click %v: filter = %v, want all", want, m3.filter)
		}
		if got := m2.titleLayout(m2.contentWidth()); ansi.Strip(got.line) == ansi.Strip(l.line) && got.line == l.line {
			t.Errorf("%v: active cell not styled differently", want)
		}
	}
	// Persisted.
	m2 := clickAt(t, m, m.titleLayout(m.contentWidth()).counts[1][0], 0)
	if m2.viewState().Filter != "working" {
		t.Errorf("view state filter = %q", m2.viewState().Filter)
	}
}

func TestTitleCountSpansCoverCells(t *testing.T) {
	m := foldModel(t)
	l := m.titleLayout(m.contentWidth())
	plain := ansi.Strip(l.line)
	for i, want := range []string{" ⏸ 1 ", "● 1", "", "✓ 1", "○ 3"} {
		if got := ansi.Cut(plain, l.counts[i][0], l.counts[i][1]); got != want {
			t.Errorf("cell %d covers %q, want %q", i, got, want)
		}
	}
	if l.counts[fleetStateAttentionIdx] != [2]int{} {
		t.Errorf("attention span %v with nothing needing attention", l.counts[fleetStateAttentionIdx])
	}
	m = withAttention(m)
	l = m.titleLayout(m.contentWidth())
	plain = ansi.Strip(l.line)
	for i, want := range []string{" ⏸ 1 ", "● 1", "! 1", "✓ 1", "○ 2"} {
		if got := ansi.Cut(plain, l.counts[i][0], l.counts[i][1]); got != want {
			t.Errorf("with attention: cell %d covers %q, want %q", i, got, want)
		}
	}
}

func TestTitleBlockedCellAbsentAtZero(t *testing.T) {
	m := foldModel(t)
	m.groups[2].Rows = nil
	m.rebuildLines()
	l := m.titleLayout(m.contentWidth())
	if l.counts[0] != [2]int{} {
		t.Errorf("blocked span %v with 0 blocked", l.counts[0])
	}
	if l.counts[1][1] == 0 {
		t.Errorf("working span missing")
	}
}

func TestTitleCountsNarrowNoTargets(t *testing.T) {
	m := foldModel(t)
	for w := 4; w <= 120; w++ {
		l := m.titleLayout(w)
		plain := ansi.Strip(l.line)
		for i, sp := range l.counts {
			if sp[1] == 0 {
				if sp[0] != 0 {
					t.Errorf("w=%d cell %d: span %v", w, i, sp)
				}
				continue
			}
			if sp[1] > w-fleetSidebarRightGutter || ansi.Cut(plain, sp[0], sp[1]) == "" {
				t.Errorf("w=%d cell %d: span %v outside %q", w, i, sp, plain)
			}
		}
	}
	// Narrow enough that the counts are dropped entirely.
	if l := m.titleLayout(20); l.counts != [fleetStateCount][2]int{} {
		t.Errorf("counts at 20 cols: %v", l.counts)
	}
}

func TestGroupHeaderCountClicks(t *testing.T) {
	m := foldModel(t)
	m.liveLeft = 0
	w := m.listColWidth()
	// s1 header is line 0; its working (k) and idle (p, x) slots are filled,
	// blocked and done are blank.
	g := m.groups[0]
	spans := m.pointerHeaderSpans(g, w).counts
	if spans[0] != [2]int{} || spans[2] != [2]int{} || spans[3] != [2]int{} {
		t.Fatalf("blank slots have spans: %v", spans)
	}
	y := m.fleetListTop()
	plain := ansi.Strip(m.renderGroupHeaderHover(g, false, fleetHoverHeader, w))
	if got := ansi.Cut(plain, spans[1][0], spans[1][1]); !strings.HasPrefix(got, "●") {
		t.Errorf("working slot covers %q", got)
	}
	if got := ansi.Cut(plain, spans[4][0], spans[4][1]); !strings.HasPrefix(got, "○") {
		t.Errorf("idle slot covers %q", got)
	}
	m2 := clickAt(t, m, spans[1][0], y)
	if m2.filter != fleetFilterWorking {
		t.Fatalf("slot click: filter = %v", m2.filter)
	}
	y = m2.fleetListTop()
	if m3 := clickAt(t, m2, m2.pointerHeaderSpans(m2.groups[0], w).counts[1][0], y); m3.filter != fleetFilterAll {
		t.Errorf("second slot click: filter = %v, want all", m3.filter)
	}
	// A click on a blank slot (blocked) changes nothing.
	blank := spans[1][1] + fleetCountGap(w) + 1 // inside the blank done slot
	if f, ok := m.headerCountAt(g, blank); ok {
		t.Errorf("blank slot at %d is a target (%v)", blank, f)
	}
	if m4 := clickAt(t, m, blank, y); m4.filter != fleetFilterAll {
		t.Errorf("blank click set filter %v", m4.filter)
	}
}

func TestGroupHeaderCountDigitsPadding(t *testing.T) {
	m := foldModel(t)
	// A group with 10 idle rows widens every slot to 2 digits.
	var rows []fleet.Row
	for i := 0; i < 10; i++ {
		rows = append(rows, foldRow("s4", "delta", "@"+string(rune('a'+i)), "z", "idle", false, ""))
	}
	m.groups[3].Rows = rows
	m.rebuildLines()
	w := m.listColWidth()
	spans := m.pointerHeaderSpans(m.groups[0], w).counts
	if spans[1][1]-spans[1][0] != 4 {
		t.Fatalf("slot width = %d, want 4", spans[1][1]-spans[1][0])
	}
	plain := ansi.Strip(m.renderGroupHeaderHover(m.groups[0], false, fleetHoverHeader, w))
	if got := ansi.Cut(plain, spans[1][0], spans[1][1]); got != "●  1" {
		t.Errorf("padded slot = %q", got)
	}
	if _, ok := m.headerCountAt(m.groups[0], spans[0][0]); ok {
		t.Errorf("blank slot position is a target")
	}
}
