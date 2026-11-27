package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func attnRow(name, st string, done, turnDone, attn bool) fleet.Row {
	r := fleet.Row{AgentState: state.AgentState{Name: name, State: st, Done: done, TurnDone: turnDone}}
	if attn {
		r.Attention = &windowstate.Attention{Reason: windowstate.AttentionQuestion, Detail: "Merge it?"}
	}
	return r
}

func TestRowGlyph_AttentionPrecedence(t *testing.T) {
	InitStyles()
	tests := []struct {
		name string
		row  fleet.Row
		want string
		w    fleetWeight
	}{
		{"blocked wins", attnRow("a", "blocked", false, false, true), "⏸", fleetWeightBlocked},
		{"working wins", attnRow("a", "working", false, false, true), "●", fleetWeightWorking},
		{"attention over done", attnRow("a", "idle", true, false, true), "!", fleetWeightAttention},
		{"attention over turn done", attnRow("a", "idle", false, true, true), "!", fleetWeightAttention},
		{"attention over idle", attnRow("a", "idle", false, false, true), "!", fleetWeightAttention},
		{"done", attnRow("a", "idle", true, false, false), "✓", fleetWeightDone},
		{"idle", attnRow("a", "idle", false, false, false), "○", fleetWeightIdle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if g, _ := rowGlyph(tt.row); g != tt.want {
				t.Errorf("rowGlyph = %q, want %q", g, tt.want)
			}
			if w := glyphWeight(tt.row); w != tt.w {
				t.Errorf("glyphWeight = %v, want %v", w, tt.w)
			}
		})
	}
	// Attention is display-only: the weight sweep and name styling use is
	// unchanged.
	if w := rowWeight(attnRow("a", "idle", false, false, true)); w != fleetWeightIdle {
		t.Errorf("rowWeight changed for attention: %v", w)
	}
	if w := rowWeight(attnRow("a", "idle", true, false, true)); w != fleetWeightDone {
		t.Errorf("rowWeight of a done row with attention: %v", w)
	}
}

func TestAttentionCountsAndFilters(t *testing.T) {
	InitStyles()
	rows := []fleet.Row{
		attnRow("a", "idle", false, false, true),    // !
		attnRow("b", "working", false, false, true), // ●
		attnRow("c", "idle", true, false, false),    // ✓
		attnRow("d", "idle", false, false, false),   // ○
	}
	m := FleetModel{groups: []fleet.Group{{Session: "s", Rows: rows}}}
	b, w, a, d, i := m.fleetCounts()
	if b != 0 || w != 1 || a != 1 || d != 1 || i != 1 {
		t.Errorf("fleetCounts = %d %d %d %d %d, want 0 1 1 1 1", b, w, a, d, i)
	}
	if !m.anyAttention() {
		t.Error("anyAttention = false")
	}
	gc := groupStateCountsWith(m.groups[0], 1, 1, true)
	if gc.attention != 1 || gc.idle != 1 || gc.done != 1 {
		t.Errorf("groupStateCounts = %+v", gc)
	}
	if !strings.Contains(ansi.Strip(gc.text), "! 1") {
		t.Errorf("header slots %q lack the ! slot", ansi.Strip(gc.text))
	}
	if txt := ansi.Strip(groupStateCounts(m.groups[0], 1, 1).text); strings.Contains(txt, "!") {
		t.Errorf("header slots without attn show !: %q", txt)
	}
	match := func(f fleetFilter) []string {
		var out []string
		for _, r := range rows {
			if f.matches(r) {
				out = append(out, r.Name)
			}
		}
		return out
	}
	if got := strings.Join(match(fleetFilterNeeds), ","); got != "a,c" {
		t.Errorf("needs you = %s, want a,c", got)
	}
	if got := strings.Join(match(fleetFilterAttention), ","); got != "a" {
		t.Errorf("attention filter = %s, want a", got)
	}
	if got := strings.Join(match(fleetFilterIdle), ","); got != "d" {
		t.Errorf("idle filter = %s, want d", got)
	}
	if parseFleetFilter(fleetFilterAttention.key()) != fleetFilterAttention {
		t.Error("attention filter does not round-trip its key")
	}
	var tr []treeRow
	for _, r := range rows {
		tr = append(tr, treeRow{Row: r})
	}
	if c := tallySubtree(tr); c.attention != 1 {
		t.Errorf("tallySubtree attention = %d", c.attention)
	}
	if s := ansi.Strip(subtreeSummary(tallySubtree(tr), 40)); !strings.Contains(s, "!1") {
		t.Errorf("subtree summary %q lacks !1", s)
	}
}

func TestAttentionRowDetailAndStatus(t *testing.T) {
	InitStyles()
	r := attnRow("a", "idle", false, false, true)
	r.Recap = fleet.RecapLine{Text: "Reworking the hub counts", Source: fleet.RecapTask}
	p := detailPartsFor(r, true)
	if p.Activity != "asks you: Merge it?" || p.Marker != fleetAttentionMarker {
		t.Errorf("detail parts = %+v", p)
	}
	st := hubStatusFor(r, fleetDisplay{}, nil)
	if st.State != "needs you" || st.Attention != "asks you: Merge it?" || st.Glyph != "!" {
		t.Errorf("status = state %q attention %q glyph %q", st.State, st.Attention, st.Glyph)
	}
	st.Version = hubStatusVersion
	lines := hubStatusLines(st, 60, 6, fleetNow())
	if !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "needs you ▸ asks you: Merge it?") {
		t.Errorf("status pane lacks the attention line:\n%s", ansi.Strip(strings.Join(lines, "\n")))
	}
}

func TestChromeBarTabAttention(t *testing.T) {
	InitStyles()
	out := ansi.Strip(renderTabLabel(TabInfo{Name: "brandon", State: "attention"}, "*", newTabStyles()))
	if out != "! brandon" {
		t.Errorf("tab label = %q, want \"! brandon\"", out)
	}
}

func TestHudStateLabelAttention(t *testing.T) {
	InitStyles()
	m := HudModel{state: "idle", attention: "asks you: Merge it?"}
	if got := ansi.Strip(m.stateLabel()); !strings.Contains(got, "! Needs you") {
		t.Errorf("stateLabel = %q", got)
	}
	m.state = "working"
	if got := ansi.Strip(m.stateLabel()); !strings.Contains(got, "Working") {
		t.Errorf("working stateLabel = %q", got)
	}
}
