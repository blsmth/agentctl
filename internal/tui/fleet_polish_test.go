package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/theme"
)

func TestRowRight_DoneBadgeAndAge(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	since := sidebarNow.Add(-4 * time.Hour)
	ex := fleetExceptions{common: "opus"}
	full := fitRightCols(200, ex, false, false, 0)
	done := fleet.Row{StateSince: since, PRNumber: 105, AgentState: state.AgentState{Done: true}}
	got := rowRight(done, full, ex, fleetDisplay{})
	if plain := ansi.Strip(got); plain != "#105  4h" {
		t.Errorf("done right = %q, want %q", plain, "#105  4h")
	}
	if !strings.Contains(got, fleetDoneStyle.Render("#105")) {
		t.Errorf("done badge should use the done style: %q", got)
	}
	working := fleet.Row{StateSince: sidebarNow.Add(-time.Second), PRNumber: 108, AgentState: state.AgentState{State: "working"}}
	if plain := ansi.Strip(rowRight(working, full, ex, fleetDisplay{})); plain != "#108  1s" {
		t.Errorf("working right = %q", plain)
	}
	if plain := ansi.Strip(rowRight(working, fleetRightCols{pr: true}, ex, fleetDisplay{})); plain != "#108" {
		t.Errorf("PR-only right = %q", plain)
	}
	// The usual model and a calm context are not shown at all: the slot is
	// blank and its width goes back to the recap.
	working.ModelName, working.ContextPct = "claude-opus-4-7", 42
	if plain := ansi.Strip(rowRight(working, full, ex, fleetDisplay{})); plain != "#108  1s" {
		t.Errorf("usual model right = %q", plain)
	}
	if got := rowRight(working, fleetRightCols{}, ex, fleetDisplay{}); got != "" {
		t.Errorf("no slots = %q", got)
	}
}

func TestRowAgeFallback(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	tests := []struct {
		name string
		row  fleet.Row
		want string
	}{
		{"state since", fleet.Row{StateSince: sidebarNow.Add(-2 * time.Minute)}, "2m"},
		{"last activity", fleet.Row{AgentState: state.AgentState{LastActivity: sidebarNow.Add(-3 * time.Hour).Unix(), StartTime: sidebarNow.Add(-50 * time.Hour).Unix()}}, "3h"},
		{"start time", fleet.Row{AgentState: state.AgentState{StartTime: sidebarNow.Add(-50 * time.Hour).Unix()}}, "2d"},
		{"nothing known", fleet.Row{}, fleetNoAge},
		{"shell", fleet.Row{Shell: true}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rowAge(tt.row)
			if plain := ansi.Strip(got); plain != tt.want {
				t.Errorf("rowAge = %q, want %q", plain, tt.want)
			}
			if tt.want == fleetNoAge && got != fleetDimStyle.Render(fleetNoAge) {
				t.Errorf("unknown age should be a dim dash: %q", got)
			}
		})
	}
}

func TestGroupHeaderCountsAndSession(t *testing.T) {
	InitStyles()
	m := FleetModel{}
	mk := func(st string, done bool) fleet.Row {
		r := testRow("a", "s", st, "")
		r.Done = done
		return r
	}
	g := fleet.Group{
		Session: "agentctl-proj", Project: "proj", Rollup: fleet.RollupWorking,
		Rows: []fleet.Row{mk("working", false), mk("working", false), mk("blocked", false), mk("", true), mk("", false)},
	}
	m.groups = []fleet.Group{g}
	out := ansi.Strip(m.renderGroupHeader(g, false, 80))
	if !strings.Contains(out, "⏸ 1  ● 2  ✓ 1  ○ 1") {
		t.Errorf("counts missing: %q", out)
	}
	if !strings.HasPrefix(out, "  ⏸ "+fleetUnfoldedGlyph+" proj") {
		t.Errorf("blocked group should carry the ⏸ flag: %q", out)
	}
	if strings.Contains(out, "agentctl-proj") || strings.Contains(out, "agent") {
		t.Errorf("default session should be hidden: %q", out)
	}

	g.Rows = []fleet.Row{mk("working", false)}
	g.Session = "custom"
	m.groups = []fleet.Group{g}
	out = ansi.Strip(m.renderGroupHeader(g, false, 80))
	if !strings.Contains(out, "custom") || strings.Contains(out, "⏸") || strings.Contains(out, "✓") || strings.Contains(out, "○") {
		t.Errorf("custom session shown and zero buckets blank: %q", out)
	}
	if !strings.HasPrefix(out, "    "+fleetUnfoldedGlyph+" proj") {
		t.Errorf("no rollup glyph without a blocked agent: %q", out)
	}
}

func TestFleetContinuation(t *testing.T) {
	tests := map[string]string{
		"":                   "",
		treeBranch:           treeBar,
		treeLast:             treeGap,
		treeBar:              treeBar,
		treeGap:              treeGap,
		treeBar + treeBranch: treeBar + treeBar,
		treeGap + treeLast:   treeGap + treeGap,
		"├─":                 "  ",
	}
	for in, want := range tests {
		if got := fleetContinuation(in); got != want {
			t.Errorf("fleetContinuation(%q) = %q, want %q", in, got, want)
		}
	}
}

func nestedSelected(t *testing.T, prefix string) (FleetModel, int) {
	t.Helper()
	m := sampleSidebar(t, 56)
	i := m.splitPane.Cursor()
	m.lines[i].treePrefix = prefix
	r := m.lines[i].row
	r.Recap = fleet.RecapLine{Text: "Short recap text", Source: fleet.RecapSummary}
	r.LastToolDetail = "cd internal && sed -i foo"
	m.lines[i].row = r
	return m, i
}

func TestDetailIndentAndContinuation(t *testing.T) {
	InitStyles()
	for _, tt := range []struct {
		name, prefix, wantLead string
	}{
		{"with following siblings", treeBranch, "▎    │  Short recap text"},
		{"last sibling", treeLast, "▎       Short recap text"},
		{"root", "", "▎    Short recap text"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, i := nestedSelected(t, tt.prefix)
			cw := m.contentWidth()
			l := m.lines[i]
			lines := strings.Split(m.renderTreeRow(l.row, l.treePrefix, l.nameCol, true, cw), "\n")
			if len(lines) != m.lineHeight(i, cw) {
				t.Fatalf("rendered %d lines, lineHeight %d", len(lines), m.lineHeight(i, cw))
			}
			for _, ln := range lines {
				if w := lipgloss.Width(ln); w != cw {
					t.Errorf("line width %d, want %d", w, cw)
				}
			}
			if got := ansi.Strip(lines[1]); !strings.HasPrefix(got, tt.wantLead) {
				t.Errorf("detail = %q, want prefix %q", got, tt.wantLead)
			}
			if got := ansi.Strip(lines[2]); !strings.Contains(got, "› cd internal") {
				t.Errorf("activity line lacks marker: %q", got)
			}
		})
	}
}

func TestCardAlignmentAndContextBar(t *testing.T) {
	InitStyles()
	cs := newCardStyles()
	now := time.Unix(1_000_000, 0)
	r := testRow("worker", "s1", "working", "")
	r.ModelName = "claude-sonnet-4-20250514"
	r.ContextPct = 13
	r.InputTokens = 100
	r.OutputTokens = 25000
	r.CacheReadTokens = 4_516_000
	r.Branch = "agent/x"
	lines := cardContent(r, nil, "boss", now, 50, cs)
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = ansi.Strip(l)
	}
	joined := strings.Join(plain, "\n")
	for _, want := range []string{"██░░░░░░░░ 13%", "100 in · 25k out · 4.5M cached", "model   sonnet", "branch  agent", "from    boss"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if !strings.HasPrefix(plain[1], "─") {
		t.Errorf("divider expected under title: %q", plain[1])
	}
	blank := -1
	for i, l := range plain {
		if strings.TrimSpace(l) == "" {
			blank = i
		}
	}
	if blank < 0 || !strings.HasPrefix(plain[blank+1], "branch") {
		t.Errorf("blank line should separate usage and provenance:\n%s", joined)
	}

	r.Branch = ""
	for _, l := range cardContent(r, nil, "", now, 50, cs) {
		if strings.TrimSpace(ansi.Strip(l)) == "" {
			t.Errorf("no blank line without provenance section")
		}
	}
}

func TestContextBarThresholds(t *testing.T) {
	InitStyles()
	cs := newCardStyles()
	th := theme.Current()
	for _, tt := range []struct {
		pct   int
		color string
	}{{10, th.Green}, {49, th.Green}, {50, th.Yellow}, {79, th.Yellow}, {80, th.Red}, {100, th.Red}} {
		filled := (tt.pct*cardBarCells + 99) / 100
		want := cs.bg.Foreground(lipgloss.Color(tt.color)).Render(strings.Repeat("█", filled))
		got := contextBar(tt.pct, cs)
		if !strings.HasPrefix(got, want) {
			t.Errorf("pct %d: want colour prefix %q in %q", tt.pct, want, got)
		}
		if plain := ansi.Strip(got); lipgloss.Width(plain) != cardBarCells+len(fmt.Sprintf(" %d%%", tt.pct)) {
			t.Errorf("pct %d: bad width %q", tt.pct, plain)
		}
	}
}
