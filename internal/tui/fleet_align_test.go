package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

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
					out := m.renderRowLine(l, sel, fleetHoverNone, cw)
					first := strings.Split(out, "\n")[0]
					if w := lipgloss.Width(first); w > cw || (sel && w != cw) {
						t.Errorf("line %d sel=%v width %d, cw %d", i, sel, w, cw)
					}
					plain := ansi.Strip(first)
					idx := strings.Index(plain, "RECAP")
					if idx < 0 {
						continue
					}
					// With no name column the recap follows the tree prefix and the
					// collapse marker directly, and the agent name is not drawn.
					want := 5 + lipgloss.Width(fleetKeptPrefix(l.treePrefix, cw))
					if l.hasKids {
						want += fleetCollapseMarkerW
					}
					c := lipgloss.Width(plain[:strings.LastIndex(plain[:idx], " ")+1])
					if c != want {
						t.Errorf("line %d sel=%v recap at col %d, want %d: %q", i, sel, c, want, plain)
					}
					if strings.Contains(plain, l.row.AgentName) {
						t.Errorf("line %d: row shows the agent name: %q", i, plain)
					}
					col = c
				}
			}
			if col == -1 {
				t.Fatal("no recap found")
			}
		})
	}
}

// TestRoleRowColumns: a role row draws its role before the title, and no row
// draws the agent name (the selected row's chips name it); the PR and age
// columns still line up down the list at every width.
func TestRoleRowColumns(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	type rowSpec struct {
		win, parent, agent, title, role string
		pr                              int
	}
	specs := []rowSpec{
		{"@1", "", "brandon", "ROOT Polish Pass", "", 12},
		{"@2", "@1", "go-engineer", "FIXER Collapse Rows", "fix", 345},
		{"@3", "@1", "code-reviewer", "REVIEWER Pass", "review", 0},
		{"@4", "@1", "python-engineer", "PLAIN Child Work", "", 7},
	}
	for _, width := range []int{44, 56, 120} {
		t.Run(fmt.Sprintf("cw%d", width), func(t *testing.T) {
			var rows []fleet.Row
			for _, s := range specs {
				s := s
				rows = append(rows, sampleRow("s", "proj", s.win, s.agent+":1", s.agent, "idle", false, fleet.LogInfo{PRNumber: s.pr}, func(r *fleet.Row) {
					r.Parent, r.Dispatched = s.parent, s.parent != ""
					r.ShortTitle, r.ShortRole = s.title, s.role
					r.StateSince = sidebarNow.Add(-90 * time.Second)
				}))
				rows[len(rows)-1].Recap = fleet.RecapLine{Text: s.title, Source: fleet.RecapTitle, Role: s.role}
			}
			m := newFleetModel()
			m.termWidth, m.termHeight, m.liveLeft = width, 30, width
			m.liveShown = "x"
			m.configureSplitPane()
			m.loaded = true
			m.groups = []fleet.Group{{Session: "s", Project: "proj", Rows: rows}}
			m.rebuildLines()
			cw := m.contentWidth()
			ageCol, prCol := -1, -1
			same := func(what string, col *int, c int, line string) {
				if *col == -1 {
					*col = c
				} else if c != *col {
					t.Errorf("%s at col %d, want %d: %q", what, c, *col, line)
				}
			}
			for i, l := range m.lines {
				if l.kind != fleetLineRow {
					continue
				}
				spec := specs[slices.IndexFunc(specs, func(s rowSpec) bool { return s.win == l.row.WindowID })]
				plain := ansi.Strip(m.renderLine(i, cw))
				if w := lipgloss.Width(plain); w > cw {
					t.Errorf("%s width %d > %d", spec.win, w, cw)
				}
				word := strings.Fields(spec.title)[0]
				ti := strings.Index(plain, word)
				if ti < 0 {
					t.Fatalf("%s lost its title: %q", spec.win, plain)
				}
				left, right := plain[:ti], plain[ti:]
				wantTitle := 5 + lipgloss.Width(fleetKeptPrefix(l.treePrefix, cw))
				if l.hasKids {
					wantTitle += fleetCollapseMarkerW
				}
				if spec.role != "" {
					wantTitle += lipgloss.Width(spec.role) + 2
				}
				if got := lipgloss.Width(left); got != wantTitle {
					t.Errorf("%s title at col %d, want %d: %q", spec.win, got, wantTitle, plain)
				}
				// The agent name is on no row: a role row shows its role, the
				// others just their title. The chip line has the name.
				if strings.Contains(plain, spec.agent) {
					t.Errorf("%s at %d: row should not show the agent name: %q", spec.win, width, plain)
				}
				if spec.role != "" && !strings.Contains(left, spec.role) {
					t.Errorf("%s: role row should show the role on the left: %q", spec.win, plain)
				}
				chips := strings.Join(rowDetailStyled(l.row, l.treePrefix, cw, m.display, false), "\n")
				if !strings.Contains(ansi.Strip(chips), "["+spec.agent+"]") {
					t.Errorf("%s at %d: agent chip missing", spec.win, width)
				}
				if age := strings.LastIndex(plain, "1m"); age >= 0 {
					same("age", &ageCol, lipgloss.Width(plain[:age]), plain)
				}
				if spec.pr > 0 {
					badge := fmt.Sprintf("#%d", spec.pr)
					if p := strings.Index(right, badge); p >= 0 {
						same("PR end", &prCol, lipgloss.Width(left)+lipgloss.Width(right[:p+len(badge)]), plain)
					} else if width == 120 {
						t.Errorf("%s: PR missing at %d: %q", spec.win, width, plain)
					}
				}
			}
			if width == 120 && (ageCol < 0 || prCol < 0) {
				t.Errorf("columns not found: age %d pr %d", ageCol, prCol)
			}
		})
	}
}
