package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
)

// TestScheduledRowRender: a cron row shows "⏲ <job>" in the recap column,
// keeps its state glyph, and neither row shows its agent name.
func TestScheduledRowRender(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	for _, width := range []int{44, 56, 120} {
		t.Run(fmt.Sprintf("cw%d", width), func(t *testing.T) {
			plain := sampleRow("s", "proj", "@1", "lead:1", "lead", "idle", false, fleet.LogInfo{Recap: "PLAINRECAP x"}, nil)
			cron := sampleRow("s", "proj", "@2", "triage:1", "triage", "working", false, fleet.LogInfo{Recap: "CRONRECAP y"}, func(r *fleet.Row) {
				r.Dispatched = true
				r.ScheduledBy = "nightly-triage"
				r.ScheduledAt = sidebarNow.Add(-time.Hour)
			})
			m := newFleetModel()
			m.termWidth, m.termHeight, m.liveLeft = width, 30, width
			m.liveShown = "x"
			m.configureSplitPane()
			m.loaded = true
			m.groups = []fleet.Group{{Session: "s", Project: "proj", Rows: []fleet.Row{plain, cron}}}
			m.rebuildLines()
			cw := m.contentWidth()

			var labels []string
			cols := map[string]int{}
			for _, l := range m.lines {
				if l.kind == fleetLineSubheader {
					labels = append(labels, l.label)
				}
				if l.kind != fleetLineRow {
					continue
				}
				out := m.renderTreeRow(l.row, l.treePrefix, false, cw)
				first := ansi.Strip(strings.Split(out, "\n")[0])
				if w := lipgloss.Width(first); w > cw {
					t.Errorf("%s width %d > cw %d", l.row.WindowID, w, cw)
				}
				// Rows no longer carry the agent name: the recap (after the cron
				// marker, on a cron row) follows the glyph directly.
				if strings.Contains(strings.ReplaceAll(first, "nightly-triage", ""), l.row.AgentName) {
					t.Errorf("%s shows the agent name: %q", l.row.WindowID, first)
				}
				cols[l.row.WindowID] = strings.Index(first, "RECAP") - strings.Index(first, "○")
				if l.row.ScheduledBy != "" {
					if !strings.Contains(first, fleetScheduledGlyph+" nightly") {
						t.Errorf("cron row missing marker: %q", first)
					}
					if !strings.Contains(first, "●") {
						t.Errorf("cron row lost its state glyph: %q", first)
					}
				} else if strings.Contains(first, fleetScheduledGlyph) {
					t.Errorf("plain row has marker: %q", first)
				}
			}
			if cols["@1"] <= 0 || cols["@2"] <= cols["@1"] {
				t.Errorf("recap positions: plain %d, cron %d (cron has its marker first)", cols["@1"], cols["@2"])
			}
			if len(labels) != 1 || labels[0] != "scheduled (1)" {
				t.Errorf("subheaders = %q, want [scheduled (1)]", labels)
			}
		})
	}
}

func TestScheduledTag(t *testing.T) {
	if got := scheduledTag(fleet.Row{}); got != "" {
		t.Errorf("plain row tag = %q", got)
	}
	r := fleet.Row{}
	r.ScheduledBy = "nightly-triage"
	if got := scheduledTag(r); got != "⏲ nightly-triage" {
		t.Errorf("tag = %q", got)
	}
}

func TestCardShowsSchedule(t *testing.T) {
	InitStyles()
	now := sidebarNow
	r := sampleRow("s", "proj", "@2", "triage:1", "triage", "working", false, fleet.LogInfo{}, func(r *fleet.Row) {
		r.ScheduledBy = "nightly-triage"
		r.ScheduledAt = now.Add(-3 * time.Hour)
	})
	full := ansi.Strip(strings.Join(cardContent(r, nil, "", now, 60, newCardStyles()), "\n"))
	if !strings.Contains(full, "scheduled") || !strings.Contains(full, "nightly-triage · fired 3h ago") {
		t.Errorf("card missing schedule:\n%s", full)
	}
}

// TestRoleTagRender: a sub-agent row shows its role before the title and a
// row without a role shows only its title; neither shows the agent name, and
// width budgets hold.
func TestRoleTagRender(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	for _, width := range []int{44, 56, 120} {
		t.Run(fmt.Sprintf("cw%d", width), func(t *testing.T) {
			with := sampleRow("s", "proj", "@1", "lead:1", "lead", "idle", false, fleet.LogInfo{Recap: "x"}, nil)
			with.ShortRole, with.ShortTitle = "review", "Fixing Hub Sort Order"
			with.Recap = fleet.RecapLine{Text: "full recap", Source: fleet.RecapSummary, Title: "Fixing Hub Sort Order", Role: "review"}
			without := sampleRow("s", "proj", "@2", "lead:2", "lead", "idle", false, fleet.LogInfo{Recap: "x"}, nil)
			without.Recap = fleet.RecapLine{Text: "full recap", Source: fleet.RecapSummary, Title: "Fixing Hub Sort Order"}
			m := newFleetModel()
			m.termWidth, m.termHeight, m.liveLeft = width, 30, width
			m.liveShown = "x"
			m.configureSplitPane()
			m.loaded = true
			m.groups = []fleet.Group{{Session: "s", Project: "proj", Rows: []fleet.Row{with, without}}}
			m.rebuildLines()
			cw := m.contentWidth()
			for _, l := range m.lines {
				if l.kind != fleetLineRow {
					continue
				}
				first := ansi.Strip(strings.Split(m.renderTreeRow(l.row, l.treePrefix, false, cw), "\n")[0])
				if w := lipgloss.Width(first); w > cw {
					t.Errorf("%s width %d > cw %d", l.row.WindowID, w, cw)
				}
				if !strings.Contains(first, "Fixing Hub") {
					t.Errorf("row %s lost its title: %q", l.row.WindowID, first)
				}
				title := strings.Index(first, "Fixing Hub")
				left, rest := first[:title], first[title:]
				if l.row.WindowID == "@2" {
					if strings.Contains(first, "review") || strings.Contains(first, "lead") {
						t.Errorf("bare row should show its title only, no role or name: %q", first)
					}
					continue
				}
				if !strings.Contains(left, "review") || strings.Contains(first, "lead") {
					t.Errorf("role row should show the role, not the name, before the title: %q", first)
				}
				if strings.Count(first, "review") != 1 {
					t.Errorf("role drawn twice: %q", first)
				}
				if strings.Contains(rest, "lead") {
					t.Errorf("role row at %d cols should not show its agent name: %q", width, first)
				}
			}
		})
	}
}
