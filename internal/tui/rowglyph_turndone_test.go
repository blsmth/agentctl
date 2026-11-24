package tui

import (
	"testing"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
)

func TestRowGlyph_TurnDone(t *testing.T) {
	mk := func(st string, td bool) fleet.Row {
		return fleet.Row{AgentState: state.AgentState{State: st, TurnDone: td}}
	}
	if g, _ := rowGlyph(mk("idle", true)); g != "✓" {
		t.Errorf("idle+TurnDone glyph = %q, want ✓", g)
	}
	if g, _ := rowGlyph(mk("working", true)); g != "●" {
		t.Errorf("working+TurnDone glyph = %q, want ●", g)
	}
	if g, _ := rowGlyph(mk("idle", false)); g != "○" {
		t.Errorf("idle glyph = %q, want ○", g)
	}
	// Counts and filters key on the weight, which TurnDone must not change.
	if w := rowWeight(mk("idle", true)); w != fleetWeightIdle {
		t.Errorf("rowWeight changed for TurnDone: %v", w)
	}
}

func TestTurnDoneCountsAgreeWithGlyph(t *testing.T) {
	mk := func(_, st string, td bool) fleet.Row {
		return fleet.Row{AgentState: state.AgentState{State: st, TurnDone: td}}
	}
	rows := []fleet.Row{
		mk("a", "idle", true),    // ✓ via TurnDone
		mk("b", "working", true), // ●
		mk("c", "idle", false),   // ○
	}
	m := FleetModel{groups: []fleet.Group{{Session: "s", Rows: rows}}}
	b, w, d, i := m.fleetCounts()
	if b != 0 || w != 1 || d != 1 || i != 1 {
		t.Errorf("fleetCounts = %d %d %d %d, want 0 1 1 1", b, w, d, i)
	}
	gc := groupStateCounts(m.groups[0], 2, 1)
	if gc.blocked != 0 || gc.working != 1 || gc.done != 1 || gc.idle != 1 {
		t.Errorf("groupStateCounts = %+v", gc)
	}
	var tr []treeRow
	for _, r := range rows {
		tr = append(tr, treeRow{Row: r})
	}
	sc := tallySubtree(tr)
	if sc.done != 1 || sc.working != 1 || sc.idle != 1 {
		t.Errorf("tallySubtree = %+v", sc)
	}
	if groupSweepable(m.groups[0]) != 0 {
		t.Error("sweep count must stay Done-only")
	}
	if !fleetFilterNeeds.matches(rows[0]) || fleetFilterNeeds.matches(rows[2]) {
		t.Error("needs filter must follow the glyph")
	}
}
