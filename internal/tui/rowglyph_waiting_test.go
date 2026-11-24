package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/fleet"
)

func waitRow(st string, done, turnDone, attn bool) fleet.Row {
	r := attnRow("a", st, done, turnDone, attn)
	r.WaitingOn = []fleet.WaitChild{{Label: "go-engineer:3"}, {Label: "code-reviewer:2", Blocked: true}}
	return r
}

func TestRowGlyph_WaitingPrecedence(t *testing.T) {
	InitStyles()
	tests := []struct {
		name string
		row  fleet.Row
		want string
	}{
		{"blocked wins", waitRow("blocked", false, false, false), "⏸"},
		{"working wins", waitRow("working", false, false, false), "●"},
		{"attention wins", waitRow("idle", false, false, true), "!"},
		{"over done", waitRow("idle", true, false, false), fleet.WaitingGlyph},
		{"over turn done", waitRow("idle", false, true, false), fleet.WaitingGlyph},
		{"over idle", waitRow("idle", false, false, false), fleet.WaitingGlyph},
		{"no children", attnRow("a", "idle", false, false, false), "○"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if g, _ := rowGlyph(tt.row); g != tt.want {
				t.Errorf("rowGlyph = %q, want %q", g, tt.want)
			}
		})
	}
	if w := lipgloss.Width(fleet.WaitingGlyph); w != 1 {
		t.Errorf("glyph width = %d, want 1", w)
	}
}

func TestWaitingDisplayOnly(t *testing.T) {
	r := waitRow("idle", false, false, false)
	if w := rowWeight(r); w != fleetWeightIdle {
		t.Errorf("rowWeight = %v, want idle", w)
	}
	if r.Done {
		t.Error("Done set")
	}
}

func TestDetailParts_Waiting(t *testing.T) {
	InitStyles()
	r := waitRow("idle", false, false, false)
	r.Recap = fleet.RecapLine{Source: fleet.RecapSummary, Text: "dispatched two workers"}
	p := detailPartsFor(r, false)
	if !strings.HasPrefix(p.Marker, fleet.WaitingGlyph) || p.Activity != "waiting on go-engineer:3 ●, code-reviewer:2 ⏸" {
		t.Errorf("parts = %+v", p)
	}
	// Attention still wins.
	r = waitRow("idle", false, false, true)
	r.Recap = fleet.RecapLine{Source: fleet.RecapSummary, Text: "asked"}
	if p := detailPartsFor(r, false); strings.Contains(p.Activity, "waiting") || p.Marker != fleetAttentionMarker {
		t.Errorf("attention lost: %+v", p)
	}
	// No recap: the line stands alone.
	r = waitRow("idle", false, false, false)
	if p := detailPartsFor(r, false); !strings.HasPrefix(p.Recap, "waiting on") {
		t.Errorf("no-recap parts = %+v", p)
	}
}
