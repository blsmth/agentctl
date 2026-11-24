package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/theme"
)

func TestRowStyleWeightDoneDecay(t *testing.T) {
	now := sidebarNow
	tests := []struct {
		name string
		row  fleet.Row
		want fleetWeight
	}{
		{"fresh done", fleet.Row{StateSince: now.Add(-time.Hour), AgentState: state.AgentState{Done: true}}, fleetWeightDone},
		{"done at the edge", fleet.Row{StateSince: now.Add(-fleetDoneDecay), AgentState: state.AgentState{Done: true}}, fleetWeightDone},
		{"stale done", fleet.Row{StateSince: now.Add(-fleetDoneDecay - time.Minute), AgentState: state.AgentState{Done: true}}, fleetWeightIdle},
		{"done, unknown age", fleet.Row{AgentState: state.AgentState{Done: true}}, fleetWeightDone},
		{"old working stays working", fleet.Row{StateSince: now.Add(-72 * time.Hour), AgentState: state.AgentState{State: "working"}}, fleetWeightWorking},
		{"old blocked stays blocked", fleet.Row{StateSince: now.Add(-72 * time.Hour), AgentState: state.AgentState{State: "blocked"}}, fleetWeightBlocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rowStyleWeight(tt.row, now); got != tt.want {
				t.Errorf("rowStyleWeight = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStaleDoneRowRendersIdleButKeepsCheck(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	stale := sampleRow("s", "proj", "@1", "worker:1", "worker", "idle", true, fleet.LogInfo{Recap: "Shipped the thing"}, func(r *fleet.Row) {
		r.StateSince = sidebarNow.Add(-30 * time.Hour)
	})
	fresh := stale
	fresh.StateSince = sidebarNow.Add(-time.Hour)
	m := newFleetModel()
	m.loaded = true
	m.groups = []fleet.Group{{Session: "s", Project: "proj", Rows: []fleet.Row{stale, fresh}}}
	m.rebuildLines()

	out := m.renderRow(stale, false, 80)
	faded := lipgloss.NewStyle().Foreground(lipgloss.Color(midColor(theme.Current().Base, theme.Current().Green)))
	if !strings.Contains(out, faded.Render("✓")) {
		t.Errorf("stale done row should keep a faded green ✓: %q", out)
	}
	if !strings.Contains(out, fleetRecapIdleStyle.Render("Shipped the thing")) {
		t.Errorf("stale done row should render at idle weight: %q", out)
	}
	if out := m.renderRow(fresh, false, 80); !strings.Contains(out, fleetRecapStyle.Render("Shipped the thing")) {
		t.Errorf("fresh done row should keep done weight: %q", out)
	}
	// Both still count as done.
	if _, _, _, done, _ := m.fleetCounts(); done != 2 {
		t.Errorf("title done count = %d, want 2", done)
	}
	if c := groupStateCounts(m.groups[0], 1, 2); c.done != 2 {
		t.Errorf("header done count = %d, want 2", c.done)
	}
}

func TestSelectedRowDropsActivityLineWhenViewed(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	recap := sampleRow("s", "proj", "@1", "worker:1", "worker", "working", false, fleet.LogInfo{Recap: "Short recap text"}, func(r *fleet.Row) {
		r.LastToolDetail = "Bash: go test ./..."
	})
	activity := sampleRow("s", "proj", "@2", "other:1", "other", "working", false, fleet.LogInfo{}, func(r *fleet.Row) {
		r.LastToolDetail = "Bash: go vet ./..."
	})
	tests := []struct {
		name      string
		row       fleet.Row
		liveShown string
		failed    bool
		wantLines int
		wantCmd   bool
	}{
		{"no viewer keeps the activity", recap, "", false, 4, true},
		{"viewer on another window keeps it", recap, "s|@9", false, 4, true},
		{"viewer on this window drops it", recap, "s|@1", false, 3, false},
		{"failed viewer keeps it", recap, "s|@1", true, 4, true},
		{"activity recap, no viewer", activity, "", false, 3, true},
		{"activity recap, viewed", activity, "s|@2", false, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newFleetModel()
			m.termWidth, m.termHeight, m.liveLeft = 80, 30, 80
			m.configureSplitPane()
			m.loaded = true
			m.groups = []fleet.Group{{Session: "s", Project: "proj", Rows: []fleet.Row{tt.row}}}
			m.rebuildLines()
			m.liveShown = tt.liveShown
			if tt.failed {
				m.liveFailed = tt.liveShown
			}
			cw := m.contentWidth()
			for i, l := range m.lines {
				if l.kind != fleetLineRow {
					continue
				}
				m.splitPane.SetCursor(i)
				out := m.renderTreeRow(l.row, l.treePrefix, true, cw)
				lines := strings.Split(out, "\n")
				if len(lines) != tt.wantLines || m.lineHeight(i, cw) != tt.wantLines {
					t.Fatalf("lines = %d (lineHeight %d), want %d: %q", len(lines), m.lineHeight(i, cw), tt.wantLines, ansi.Strip(out))
				}
				detail := ansi.Strip(strings.Join(lines[1:], "\n"))
				if got := strings.Contains(detail, "$ go "); got != tt.wantCmd {
					t.Errorf("activity line shown = %v, want %v: %q", got, tt.wantCmd, detail)
				}
				for _, ln := range lines {
					if w := lipgloss.Width(ln); w != cw {
						t.Errorf("line width %d, want %d", w, cw)
					}
				}
			}
		})
	}
}

func TestViewerFailureTracking(t *testing.T) {
	m := newFleetModel()
	m.liveShown = "s|@1"
	next, _ := m.Update(fleetLiveMsg{key: "s|@1", err: errTest})
	m = next.(FleetModel)
	r := fleet.Row{Session: "s", AgentState: state.AgentState{WindowID: "@1"}}
	if m.viewerShows(r) {
		t.Error("a failed hand-off is not showing the window")
	}
	next, _ = m.Update(fleetLiveMsg{key: "s|@1", left: 40, right: 80})
	m = next.(FleetModel)
	if !m.viewerShows(r) {
		t.Error("a successful hand-off shows the window")
	}
}

func TestRenderRecapTitleDimsLeadingIssue(t *testing.T) {
	InitStyles()
	rs := fleetRecapWorkingStyle
	tests := []struct {
		in, want string
	}{
		{"#21 Crotch drops into skin crease", fleetDimStyle.Render("#21") + rs.Render(" Crotch drops into skin crease")},
		{"Fixing #21 later", rs.Render("Fixing #21 later")},
		{"summit-k2-envs#244 Provider Upgrade", rs.Render("summit-k2-envs#244 Provider Upgrade")},
		{"#9", fleetDimStyle.Render("#9") + rs.Render("")},
		{"#abc not an issue", rs.Render("#abc not an issue")},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := renderRecapTitle(tt.in, rs); got != tt.want {
				t.Errorf("renderRecapTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestGroupNameNeutralBarKeepsColour(t *testing.T) {
	InitStyles()
	m := newFleetModel()
	g := fleet.Group{Session: "agentctl-proj", Project: "proj", Rows: []fleet.Row{testRow("a", "agentctl-proj", "idle", "")}}
	m.groups = []fleet.Group{g}
	hdr := m.renderGroupHeader(g, false, 80)
	if !strings.Contains(hdr, fleetProjectChipStyle("proj", true).Render(" proj ")) {
		t.Errorf("group name should be a padded project-colour chip: %q", hdr)
	}
	if !strings.HasPrefix(hdr, fleetProjectBar("proj")) {
		t.Errorf("header should start with the project bar: %q", hdr)
	}
	narrow := m.renderGroupHeader(g, false, 44)
	if !strings.Contains(narrow, fleetProjectChipStyle("proj", false).Render("proj")) || !strings.Contains(ansi.Strip(narrow), "▾ proj") {
		t.Errorf("narrow header should drop the chip padding: %q", narrow)
	}
	if !strings.Contains(fleetProjectBar("proj"), lipgloss.NewStyle().Foreground(lipgloss.Color(fleetColorFor("proj"))).Render("▎")) {
		t.Error("the ▎ bar must keep the project colour")
	}
}

func TestTitleSortTag(t *testing.T) {
	m := foldModel(t)
	m.sortRecent = true
	got := ansi.Strip(m.renderTitle(120))
	if !strings.Contains(got, "· sort: recent") || strings.Contains(got, "↻") {
		t.Errorf("title = %q, want the \"sort: recent\" tag", got)
	}
}

func TestTitleSortTagClick(t *testing.T) {
	m := foldModel(t)
	m.liveLeft = 0
	for _, recent := range []bool{false, true} {
		m.sortRecent = recent
		want := fleetSortWindow
		if recent {
			want = fleetSortRecent
		}
		l := m.titleLayout(m.contentWidth())
		plain := ansi.Strip(l.line)
		if l.sort[1] == 0 || ansi.Cut(plain, l.sort[0], l.sort[1]) != want {
			t.Fatalf("recent=%v: sort span %v covers %q in %q", recent, l.sort, ansi.Cut(plain, l.sort[0], l.sort[1]), plain)
		}
		if !m.onSortTag(l.sort[0], 0) || m.onSortTag(l.sort[0], 1) || m.onSortTag(l.sort[1], 0) {
			t.Errorf("recent=%v: hit test wrong", recent)
		}
		next, _ := m.Update(tea.MouseClickMsg{X: l.sort[0] + 1, Y: 0, Button: tea.MouseLeft})
		if got := next.(FleetModel).sortRecent; got == recent {
			t.Errorf("recent=%v: click did not toggle", recent)
		}
	}
}

func TestTitleSortTagDropsWhenNarrow(t *testing.T) {
	m := foldModel(t)
	for _, w := range []int{40, 44, 50, 56, 80, 120} {
		m.liveLeft = 0
		with := m.titleLayout(w)
		plain := ansi.Strip(with.line)
		if ansi.StringWidth(plain) > w {
			t.Errorf("w=%d: title %d wide", w, ansi.StringWidth(plain))
		}
		if !strings.Contains(plain, "○") {
			t.Errorf("w=%d: counts dropped: %q", w, plain)
		}
		if with.sort[1] == 0 && strings.Contains(plain, "sort:") {
			t.Errorf("w=%d: tag shown without span: %q", w, plain)
		}
	}
	if l := m.titleLayout(120); l.sort[1] == 0 {
		t.Error("no sort tag at 120 columns")
	}
}

func TestTitleCountsSkipShells(t *testing.T) {
	m := foldModel(t)
	before := ansi.Strip(m.renderTitle(120))
	sh := testRow("shell", m.groups[0].Session, "", "")
	sh.WindowID, sh.Shell = "@99", true
	m.groups[0].Rows = append(append([]fleet.Row(nil), m.groups[0].Rows...), sh)
	if after := ansi.Strip(m.renderTitle(120)); after != before {
		t.Errorf("a shell row changed the title counts:\n%q\n%q", before, after)
	}
}

func TestNoPromptRowShowsNoModel(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	r := sampleRow("s", "proj", "@1", "tickets:1", "tickets", "idle", false, fleet.LogInfo{}, func(r *fleet.Row) {
		r.ModelName = "claude-sonnet-4-5"
	})
	m := newFleetModel()
	m.except = fleetExceptions{common: "opus", modelW: 6}
	line := firstLine(m.renderRow(r, false, 100))
	if !strings.Contains(line, "no prompt yet") || strings.Contains(line, "sonnet") {
		t.Errorf("row = %q, want \"no prompt yet\" and no model", line)
	}
	if !strings.HasSuffix(strings.TrimRight(line, " "), fleetNoAge) {
		t.Errorf("unknown age should show a dash: %q", line)
	}
}
