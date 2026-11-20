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
