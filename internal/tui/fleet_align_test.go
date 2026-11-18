package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
)

func TestRecapColumnAligned(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	for _, width := range []int{44, 56, 120} {
		t.Run(fmt.Sprintf("cw%d", width), func(t *testing.T) {
			mk := func(win, parent, agent, recap string) fleet.Row {
				r := sampleRow("s", "proj", win, agent+":1", agent, "idle", false, fleet.LogInfo{Recap: recap}, func(r *fleet.Row) {
					r.Parent = parent
					r.Dispatched = parent != ""
				})
				return r
			}
			rows := []fleet.Row{
				mk("@1", "", "brandon", "ROOTRECAP sweep"),
				mk("@2", "@1", "go-engineer", "CHILDRECAP launch"),
				mk("@3", "@1", "a-very-long-agent-name-that-overflows", "LONGRECAP phone"),
			}
			m := newFleetModel()
			m.termWidth, m.termHeight, m.liveLeft = width, 30, width
			m.liveShown = "x"
			m.configureSplitPane()
			m.loaded = true
			m.groups = []fleet.Group{{Session: "s", Project: "proj", Rows: rows}}
			m.rebuildLines()
			cw := m.contentWidth()

			col := -1
			for i, l := range m.lines {
				if l.kind != fleetLineRow {
					continue
				}
				for _, sel := range []bool{false, true} {
					out := m.renderTreeRow(l.row, l.treePrefix, l.nameCol, sel, cw)
					first := strings.Split(out, "\n")[0]
					if w := lipgloss.Width(first); w > cw || (sel && w != cw) {
						t.Errorf("line %d sel=%v width %d, cw %d", i, sel, w, cw)
					}
					plain := ansi.Strip(first)
					idx := strings.Index(plain, "RECAP")
					if idx < 0 {
						continue
					}
					// column of the recap word's first letter
					word := strings.LastIndex(plain[:idx], "  ") + 2
					c := lipgloss.Width(plain[:word])
					if col == -1 {
						col = c
					} else if c != col {
						t.Errorf("line %d sel=%v recap at col %d, want %d: %q", i, sel, c, col, plain)
					}
				}
			}
			if col == -1 {
				t.Fatal("no recap found")
			}
		})
	}
}
