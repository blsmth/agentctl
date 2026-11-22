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
// keeps its state glyph, and its recap column lines up with a plain row's.
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
				out := m.renderTreeRow(l.row, l.treePrefix, l.nameCol, false, cw)
				first := ansi.Strip(strings.Split(out, "\n")[0])
				if w := lipgloss.Width(first); w > cw {
					t.Errorf("%s width %d > cw %d", l.row.WindowID, w, cw)
				}
				// The recap column starts after the name column's two-space gap.
				start := strings.Index(first, l.row.AgentName) + len(l.row.AgentName)
				rest := first[start:]
				col := lipgloss.Width(first[:start]) + len(rest) - len(strings.TrimLeft(rest, " "))
				cols[l.row.WindowID] = col
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
			if cols["@1"] != cols["@2"] {
				t.Errorf("recap column: plain %d, cron %d", cols["@1"], cols["@2"])
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

// TestRoleTagRender: a sub-agent row shows its dim role tag before the title,
// a row without a role shows the bare title, the tag is dropped before the
// title is squeezed, and width budgets hold.
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
				first := ansi.Strip(strings.Split(m.renderTreeRow(l.row, l.treePrefix, l.nameCol, false, cw), "\n")[0])
				if w := lipgloss.Width(first); w > cw {
					t.Errorf("%s width %d > cw %d", l.row.WindowID, w, cw)
				}
				if !strings.Contains(first, "Fixing Hub") {
					t.Errorf("row %s lost its title: %q", l.row.WindowID, first)
				}
				hasTag := strings.Contains(first, "review  Fixing")
				if l.row.WindowID == "@2" && strings.Contains(first, "review") {
					t.Errorf("bare row shows a role: %q", first)
				}
				if l.row.WindowID == "@1" && width == 120 && !hasTag {
					t.Errorf("child row at %d cols should show the role tag: %q", width, first)
				}
			}
		})
	}
}
