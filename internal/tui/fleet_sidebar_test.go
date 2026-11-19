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
)

// sidebarNow is the fixed clock the sample sidebar is rendered against.
var sidebarNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

// fixedFleetNow pins fleetNow for the test.
func fixedFleetNow(t *testing.T) {
	t.Helper()
	old := fleetNow
	fleetNow = func() time.Time { return sidebarNow }
	t.Cleanup(func() { fleetNow = old })
}

// sampleRow builds a row and resolves its recap from info, as fleet.TagLogs
// would from the session log.
func sampleRow(sess, project, win, name, agent, st string, done bool, info fleet.LogInfo, mod func(*fleet.Row)) fleet.Row {
	r := fleet.Row{
		Session: sess,
		Project: project,
		AgentState: state.AgentState{
			Name: name, AgentName: agent, WindowID: win, State: st, Done: done,
		},
	}
	if mod != nil {
		mod(&r)
	}
	r.Recap = fleet.Recap(r, info)
	r.PRNumber, r.PRURL = info.PRNumber, info.PRURL
	return r
}

// sampleSidebar is the fixture behind the PR's before/after render: one row
// per recap source, a blocked row, a done row with a PR, and a second group.
func sampleSidebar(t *testing.T, width int) FleetModel {
	t.Helper()
	InitStyles()
	fixedFleetNow(t)
	ago := func(d time.Duration) time.Time { return sidebarNow.Add(-d) }
	rows := []fleet.Row{
		sampleRow("agentctl-agentctl", "agentctl", "@1", "go-engineer-opus:2", "go-engineer-opus", "working", false,
			fleet.LogInfo{Recap: "We are building the phone web app for agentctl (epic #52): PR 1 is merged, and PR 2 (read API, SSE events) is being built now."},
			func(r *fleet.Row) {
				r.LastToolDetail = "Editing internal/serve/routes_read.go"
				r.StateSince = ago(4 * time.Minute)
			}),
		sampleRow("agentctl-agentctl", "agentctl", "@2", "go-engineer:3", "go-engineer", "blocked", false, fleet.LogInfo{},
			func(r *fleet.Row) {
				r.TicketRef, r.TicketTitle = "61", "Redesign the hub sidebar row"
				r.LastToolDetail = "Bash: go test ./internal/tui"
				r.StateSince = ago(2 * time.Minute)
			}),
		sampleRow("agentctl-agentctl", "agentctl", "@3", "reviewer:1", "reviewer", "idle", true,
			fleet.LogInfo{AITitle: "Review mobile web app PR", PRNumber: 60, PRURL: "https://github.com/blsmth/agentctl/pull/60"},
			func(r *fleet.Row) {
				r.LastText = "LGTM, approved with two nits."
				r.StateSince = ago(25 * time.Minute)
			}),
		sampleRow("agentctl-agentctl", "agentctl", "@4", "claude:4", "claude", "idle", false, fleet.LogInfo{},
			func(r *fleet.Row) {
				r.LastToolDetail = "Reading README.md"
				r.StateSince = ago(2 * time.Hour)
			}),
	}
	other := []fleet.Row{
		sampleRow("agentctl-spacewalker", "spacewalker", "@7", "swift-engineer:1", "swift-engineer", "working", false, fleet.LogInfo{},
			func(r *fleet.Row) {
				r.Task = "Add offline mode to the sync engine"
				r.LastToolDetail = "Editing Sources/Sync/Engine.swift"
				r.StateSince = ago(40 * time.Second)
			}),
	}
	m := newFleetModel()
	m.termWidth, m.termHeight = width, 30
	m.liveLeft = width
	m.liveShown = "x" // suppress the "select an agent" hint
	m.configureSplitPane()
	m.loaded = true
	m.groups = []fleet.Group{
		{Session: "agentctl-agentctl", Project: "agentctl", Rollup: fleet.RollupBlocked, Rows: rows},
		{Session: "agentctl-spacewalker", Project: "spacewalker", Rollup: fleet.RollupWorking, Rows: other},
	}
	m.rebuildLines()
	m.resolveSelection() // first agent row
	return m
}

// sidebarListText renders just the list area as plain text.
func sidebarListText(m FleetModel) string {
	out := ansi.Strip(m.renderList())
	lines := strings.Split(out, "\n")
	top := m.fleetListTop()
	end := top + m.visibleLines()
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[top:end], "\n")
}

func TestSidebarSample_Render(t *testing.T) {
	m := sampleSidebar(t, 56)
	got := sidebarListText(m)
	t.Logf("sample sidebar (cw=%d):\n%s", m.contentWidth(), got)

	for _, want := range []string{
		"go-engineer-opus ",                     // base name, no ":2"
		"We are building",                       // recap wins
		"#61 Redesign",                          // ticket task
		"#60 25m",                               // done PR badge keeps the age
		"4m",                                    // state age
		"Reading README.md",                     // activity fallback
		"Add offline mode",                      // ad-hoc task
		"Editing internal/serve/routes_read.go", // expanded selected row: activity
	} {
		if !strings.Contains(got, want) {
			t.Errorf("sample sidebar missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "go-engineer-opus:2") || strings.Contains(got, "go-engineer:3") {
		t.Errorf("row names must drop the instance suffix:\n%s", got)
	}
	// The selected row expands; the others stay one line.
	if n := strings.Count(got, "We are building"); n != 2 {
		t.Errorf("recap shown %d times, want 2 (row + expanded detail)", n)
	}
}

func TestSidebarRows_NeverExceedWidth(t *testing.T) {
	for _, width := range []int{44, 56, 120} {
		m := sampleSidebar(t, width)
		cw := m.contentWidth()
		for i, l := range m.lines {
			if !l.selectable() {
				continue
			}
			m.selectLine(i)
			for _, sel := range []bool{false, true} {
				var out string
				if l.kind == fleetLineRow {
					out = m.renderTreeRow(l.row, l.treePrefix, l.nameCol, sel, cw)
				} else {
					out = m.renderLine(i, cw)
				}
				for _, line := range strings.Split(out, "\n") {
					w := lipgloss.Width(line)
					if w > cw {
						t.Errorf("width=%d line %d selected=%v: %d cols > cw %d: %q", width, i, sel, w, cw, ansi.Strip(line))
					}
					if sel && l.kind == fleetLineRow && w != cw {
						t.Errorf("width=%d line %d: selected line is %d cols, want exactly %d", width, i, w, cw)
					}
				}
			}
		}
	}
}

func TestSidebarSelectedHighlightStopsBeforeGutter(t *testing.T) {
	m := sampleSidebar(t, 56)
	cw := m.contentWidth()
	r := m.lines[m.splitPane.Cursor()]
	out := m.renderTreeRow(r.row, r.treePrefix, r.nameCol, true, cw)
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasSuffix(line, strings.Repeat(" ", fleetSidebarRightGutter)) {
			t.Fatalf("selected line must end in %d plain gutter columns: %q", fleetSidebarRightGutter, line)
		}
		head := line[:len(line)-fleetSidebarRightGutter]
		if !strings.HasSuffix(head, "m") { // the highlight's closing SGR, not a padded space
			t.Errorf("highlight should end right before the gutter: %q", line)
		}
	}
	// The close button is the last visible cell before the gutter.
	plain := ansi.Strip(strings.Split(out, "\n")[0])
	if want := fleetCloseLabel + strings.Repeat(" ", fleetSidebarRightGutter); !strings.HasSuffix(plain, want) {
		t.Errorf("row should end in %q, got %q", want, plain)
	}
}

func TestSidebarMouse_ExpandedSelectedRow(t *testing.T) {
	m := sampleSidebar(t, 56)
	top := m.fleetListTop()
	cw := m.listColWidth()
	sel := m.splitPane.Cursor() // line 1: first agent row (line 0 is the header)
	h := m.lineHeight(sel, cw)
	if h < 2 {
		t.Fatalf("selected row height = %d, want expanded (>1)", h)
	}
	closeX := cw - fleetSidebarRightGutter - 1
	tests := []struct {
		name       string
		x, y       int
		wantIdx    int
		wantAction fleetClickAction
	}{
		{"header", 1, top, 0, fleetClickSelect},
		{"header name", 8, top, 0, fleetClickFold},
		{"selected row", 10, top + 1, sel, fleetClickSelect},
		{"selected row close", closeX, top + 1, sel, fleetClickClose},
		{"detail line selects", 10, top + 2, sel, fleetClickSelect},
		{"detail line has no close", closeX, top + 2, sel, fleetClickSelect},
		{"last detail line", 10, top + h, sel, fleetClickSelect},
		{"row after expansion", 10, top + 1 + h, sel + 1, fleetClickSelect},
		{"close after expansion", closeX, top + 1 + h, sel + 1, fleetClickClose},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, action := m.clickDecision(tt.x, tt.y)
			if idx != tt.wantIdx || action != tt.wantAction {
				t.Errorf("clickDecision(%d,%d) = (%d,%v), want (%d,%v)", tt.x, tt.y, idx, action, tt.wantIdx, tt.wantAction)
			}
		})
	}

	// Clicking the row below moves the expansion there: the old row shrinks
	// back to one line and the new one expands in place.
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 10, Y: top + 1 + h})
	m2 := next.(FleetModel)
	if m2.splitPane.Cursor() != sel+1 {
		t.Fatalf("cursor = %d, want %d", m2.splitPane.Cursor(), sel+1)
	}
	if got := m2.lineHeight(sel, cw); got != 1 {
		t.Errorf("deselected row height = %d, want 1", got)
	}
	if idx, ok := m2.lineAt(10, top+2); !ok || idx != sel+1 {
		t.Errorf("after reselect, row below the old selection is at y=%d: got (%d,%v)", top+2, idx, ok)
	}
}

func TestSidebarScroll_KeepsExpandedRowVisible(t *testing.T) {
	m := sampleSidebar(t, 56)
	m.termHeight = fleetChromeRows + m.liveHintRows() + 4 // 4 list rows
	cw := m.contentWidth()
	last := len(m.lines) - 1
	m.selectLine(last)
	visible := m.visibleLines()
	if got := m.rowsBetween(m.scrollOffset, last, cw); got > visible {
		t.Errorf("rows from scrollOffset %d through cursor = %d, exceeds visible %d", m.scrollOffset, got, visible)
	}
	// The whole expanded row is on screen.
	text := sidebarListText(m)
	if !strings.Contains(text, "Editing Sources/Sync/Engine.swift") {
		t.Errorf("expanded last row's detail should be visible:\n%s", text)
	}
	if got := strings.Count(text, "\n") + 1; got != visible {
		t.Errorf("list area renders %d rows, want %d", got, visible)
	}
}

func TestRowRight(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	since := sidebarNow.Add(-4 * time.Minute)
	tests := []struct {
		name string
		row  fleet.Row
		want string
	}{
		{"no age no pr", fleet.Row{}, "            " + "         "},
		{"age", fleet.Row{StateSince: since}, "            " + "       4m"},
		{"pr and age", fleet.Row{StateSince: since, PRNumber: 60}, "            " + "  #60  4m"},
		{"done pr keeps age, no check", fleet.Row{StateSince: since, PRNumber: 60, AgentState: state.AgentState{Done: true}}, "            " + "  #60  4m"},
		{"done without pr keeps age", fleet.Row{StateSince: since, AgentState: state.AgentState{Done: true}}, "            " + "       4m"},
		{"four-digit pr", fleet.Row{StateSince: since, PRNumber: 1025}, "            " + "#1025  4m"},
		{"model and ctx", fleet.Row{StateSince: since, PRNumber: 60, AgentState: state.AgentState{ModelName: "claude-opus-4-7", ContextPct: 42}}, "  opus  42% " + "  #60  4m"},
		{"model, no ctx yet", fleet.Row{StateSince: since, AgentState: state.AgentState{ModelName: "claude-sonnet-4-5-20250929"}}, "sonnet      " + "       4m"},
		{"ctx 0 is blank", fleet.Row{AgentState: state.AgentState{ModelName: "claude-haiku-4-5", ContextPct: 0}}, " haiku      " + "         "},
		{"full context", fleet.Row{AgentState: state.AgentState{ModelName: "claude-opus-4-7[1m]", ContextPct: 100}}, "  opus 100% " + "         "},
		{"shell has no model", fleet.Row{Shell: true, AgentState: state.AgentState{ModelName: "claude-opus-4-7", ContextPct: 5}}, "            " + "         "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ansi.Strip(rowRight(tt.row, fleetRightFull, fleetDisplay{})); got != tt.want {
				t.Errorf("rowRight = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRowRight_BlockedUsesBlockedStyle(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	r := fleet.Row{StateSince: sidebarNow.Add(-2 * time.Minute), AgentState: state.AgentState{State: "blocked"}}
	if got, want := rowRight(r, fleetRightFull, fleetDisplay{}), fleetBlockedStyle.Render("2m"); !strings.HasSuffix(got, want) {
		t.Errorf("blocked age = %q, want %q", got, want)
	}
}

func TestRowDetail(t *testing.T) {
	long := strings.Repeat("word ", 60)
	tests := []struct {
		name      string
		row       fleet.Row
		wantLines int
		wantLast  string // substring of the last line, "" = skip
	}{
		{"nothing", fleet.Row{}, 0, ""},
		{"activity only", fleet.Row{AgentState: state.AgentState{LastToolDetail: "Reading a.go"}}, 1, "Reading a.go"},
		{"recap plus activity", fleet.Row{
			Recap:      fleet.RecapLine{Text: "Short recap", Source: fleet.RecapSummary},
			AgentState: state.AgentState{LastToolDetail: "Reading a.go"},
		}, 2, "Reading a.go"},
		{"long recap capped with activity", fleet.Row{
			Recap:      fleet.RecapLine{Text: long, Source: fleet.RecapSummary},
			AgentState: state.AgentState{LastToolDetail: "Reading a.go"},
		}, 3, "Reading a.go"},
		{"long recap alone uses three lines", fleet.Row{
			Recap: fleet.RecapLine{Text: long, Source: fleet.RecapSummary},
		}, 3, "…"},
		{"activity equal to recap not repeated", fleet.Row{
			Recap:      fleet.RecapLine{Text: "same", Source: fleet.RecapTitle},
			AgentState: state.AgentState{LastToolDetail: "same"},
		}, 1, "same"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rowDetail(tt.row, 0, 52, fleetDisplay{})
			if len(got) != tt.wantLines {
				t.Fatalf("rowDetail lines = %d, want %d: %+v", len(got), tt.wantLines, got)
			}
			for _, l := range got {
				if w := lipgloss.Width(l.text); w > 52-fleetSidebarRightGutter-fleetDetailIndent {
					t.Errorf("detail line %q is %d cols, too wide", l.text, w)
				}
			}
			if tt.wantLast != "" && !strings.Contains(got[len(got)-1].text, tt.wantLast) {
				t.Errorf("last line = %q, want it to contain %q", got[len(got)-1].text, tt.wantLast)
			}
		})
	}
}

func TestRenderTreeRow_ConfirmKeepsInstanceName(t *testing.T) {
	m := sampleSidebar(t, 120)
	l := m.lines[m.splitPane.Cursor()]
	m.confirmSession, m.confirmWindow = l.row.Session, l.row.WindowID
	out := ansi.Strip(m.renderTreeRow(l.row, "", 0, false, m.contentWidth()))
	if !strings.Contains(out, "close go-engineer-opus:2? y/n") {
		t.Errorf("close prompt must name the full instance: %q", out)
	}
}

func TestOpenSelectedPR(t *testing.T) {
	m := sampleSidebar(t, 56)
	var opened []string
	old := fleetOpenURL
	fleetOpenURL = func(url string) tea.Cmd { opened = append(opened, url); return nil }
	t.Cleanup(func() { fleetOpenURL = old })

	// Row without a PR: nothing, and no menu entry.
	m.handleKey(tea.KeyPressMsg{Code: 'p', Text: "p"})
	for _, it := range m.fleetMenuItems(m.splitPane.Cursor()) {
		if it.key == "p" {
			t.Error("menu offers open PR for a row without one")
		}
	}
	// The reviewer row (line 3) has PR #60.
	m.selectLine(3)
	m.handleKey(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if len(opened) != 1 || opened[0] != "https://github.com/blsmth/agentctl/pull/60" {
		t.Errorf("opened = %v, want the reviewer's PR once", opened)
	}
	found := false
	for _, it := range m.fleetMenuItems(3) {
		found = found || it.key == "p"
	}
	if !found {
		t.Error("menu should offer open PR for a row with one")
	}
}

func TestSidebarCloseButtonOneColumnFromPaneEdge(t *testing.T) {
	for _, n := range []int{56, 80, 106} {
		m := sampleSidebar(t, n)
		if cw := m.contentWidth(); cw != n {
			t.Fatalf("termWidth %d: contentWidth = %d, want %d", n, cw, n)
		}
		found := false
		for i, l := range m.lines {
			if l.kind != fleetLineRow || i == m.splitPane.Cursor() {
				continue
			}
			found = true
			// The ✕ shows on a hovered (or selected) row only.
			line := ansi.Strip(m.renderTreeRowHover(l.row, l.treePrefix, l.nameCol, false, fleetHoverBody, n))
			cols := []rune(line)
			if len(cols) != n {
				t.Fatalf("width %d: line is %d cols, want %d", n, len(cols), n)
			}
			if got := string(cols[n-2]); got != fleetCloseLabel {
				t.Errorf("width %d: col %d = %q, want %q in %q", n, n-2, got, fleetCloseLabel, line)
			}
			if !m.onCloseButton(n - 2) {
				t.Errorf("width %d: onCloseButton(%d) = false", n, n-2)
			}
			break
		}
		if !found {
			t.Fatal("no unselected row")
		}
	}
}

// hoverTarget finds the screen row of line i and returns it.
func hoverRowY(m FleetModel, i int) int {
	y := m.fleetListTop()
	cw := m.listColWidth()
	for j := m.scrollOffset; j < i; j++ {
		y += m.lineHeight(j, cw)
	}
	return y
}

func TestFleetHover_CloseAndNew(t *testing.T) {
	m := sampleSidebar(t, 56)
	cw := m.listColWidth()
	closeX := cw - fleetSidebarRightGutter - 1

	// Pick an unselected row.
	rowIdx := -1
	for i, l := range m.lines {
		if l.kind == fleetLineRow && i != m.splitPane.Cursor() {
			rowIdx = i
			break
		}
	}
	if rowIdx < 0 {
		t.Fatal("no unselected row")
	}
	nm, cmd := m.Update(tea.MouseMotionMsg{X: closeX, Y: hoverRowY(m, rowIdx)})
	if cmd != nil {
		t.Fatal("motion should not return a cmd")
	}
	m = nm.(FleetModel)
	if got := m.hover.region(rowIdx); got != fleetHoverClose {
		t.Fatalf("region = %v, want close", got)
	}
	l := m.lines[rowIdx]
	want := m.renderTreeRowHover(l.row, l.treePrefix, l.nameCol, false, fleetHoverClose, cw)
	if got := m.renderLine(rowIdx, cw); got != want {
		t.Error("hovered row not rendered with the close-hover style")
	}
	if !strings.Contains(want, fleetCloseHoverStyle.Render(fleetCloseLabel)) {
		t.Error("rendered row lacks the styled ✕")
	}
	if plain := m.renderTreeRowHover(l.row, l.treePrefix, l.nameCol, false, fleetHoverNone, cw); plain == want {
		t.Error("hover must change the rendering")
	}

	// Same region: no change, nil cmd.
	before := m.hover
	nm, cmd = m.Update(tea.MouseMotionMsg{X: closeX, Y: hoverRowY(m, rowIdx)})
	if cmd != nil || nm.(FleetModel).hover != before {
		t.Error("motion within the same region must be a no-op")
	}

	// Off the list (header rows): cleared.
	nm, _ = m.Update(tea.MouseMotionMsg{X: closeX, Y: 0})
	if nm.(FleetModel).hover.region(rowIdx) != fleetHoverNone {
		t.Error("moving off the list should clear hover")
	}

}

func TestFleetHover_New(t *testing.T) {
	m := sampleSidebar(t, 56)
	cw := m.listColWidth()
	for i := range m.lines {
		if m.lines[i].kind == fleetLineHeader {
			m.lines[i].group.ProjectDir, m.lines[i].group.RuntimeDir = "/p", "/r"
		}
	}
	hdr := -1
	for i, l := range m.lines {
		if l.kind == fleetLineHeader && groupCanLaunch(l.group) {
			hdr = i
			break
		}
	}
	if hdr < 0 {
		t.Fatal("no launchable header in sample")
	}
	nm, _ := m.Update(tea.MouseMotionMsg{X: cw - fleetSidebarRightGutter - 1, Y: hoverRowY(m, hdr)})
	m = nm.(FleetModel)
	if m.hover.region(hdr) != fleetHoverNew {
		t.Fatalf("region = %v, want new", m.hover.region(hdr))
	}
	if !strings.Contains(m.renderLine(hdr, cw), fleetNewHoverStyle.Render(fleetNewLabel)) {
		t.Error("header lacks hovered + new")
	}
}

func TestFleetHover_WidthInvariants(t *testing.T) {
	for _, width := range []int{44, 56, 120} {
		m := sampleSidebar(t, width)
		cw := m.contentWidth()
		for i, l := range m.lines {
			if l.kind != fleetLineRow {
				continue
			}
			for _, reg := range []fleetHoverRegion{fleetHoverBody, fleetHoverClose} {
				for _, sel := range []bool{false, true} {
					out := m.renderTreeRowHover(l.row, l.treePrefix, l.nameCol, sel, reg, cw)
					for _, line := range strings.Split(out, "\n") {
						w := lipgloss.Width(line)
						if w != cw { // every case here is highlighted (selected or hover tint)
							t.Errorf("width=%d line %d reg=%v sel=%v: %d cols, want exactly %d", width, i, reg, sel, w, cw)
						}
						if !strings.HasSuffix(line, strings.Repeat(" ", fleetSidebarRightGutter)) {
							t.Errorf("gutter not plain: %q", line)
						}
					}
				}
			}
		}
	}
}

func TestFleetHover_NameBoldRecapBright(t *testing.T) {
	m := sampleSidebar(t, 120)
	cw := m.contentWidth()
	fg := func(s lipgloss.Style) string {
		pre, _, _ := strings.Cut(s.Render("x"), "x")
		return pre
	}
	for i, l := range m.lines {
		if l.kind != fleetLineRow {
			continue
		}
		w := rowWeight(l.row)
		base, _ := splitAgentName(truncateToWidth(fleet.BaseName(l.row), fleetNameBudget(cw)))
		rs := fleetRecapHoverStyle
		if rowRecap(l.row).Source == fleet.RecapActivity {
			rs = fleetActivityHoverStyle
		}
		for _, sel := range []bool{false, true} {
			hov := m.renderTreeRowHover(l.row, l.treePrefix, l.nameCol, sel, fleetHoverBody, cw)
			plain := m.renderTreeRowHover(l.row, l.treePrefix, l.nameCol, sel, fleetHoverNone, cw)
			if !strings.Contains(hov, renderAgentNameHover(base, w, true)) {
				t.Errorf("row %d sel=%v: hovered name not bold", i, sel)
			}
			if !strings.Contains(plain, renderAgentName(base, w)) {
				t.Errorf("row %d sel=%v: unhovered name changed", i, sel)
			}
			if w != fleetWeightBlocked && w != fleetWeightWorking && strings.Contains(plain, renderAgentNameHover(base, w, true)) {
				t.Errorf("row %d sel=%v: unhovered name is bold", i, sel)
			}
			if !strings.Contains(hov, fg(rs)) {
				t.Errorf("row %d sel=%v: bright recap foreground missing", i, sel)
			}
		}
	}
}

func TestFleetHover_BackgroundContinuous(t *testing.T) {
	m := sampleSidebar(t, 120)
	cw := m.contentWidth()
	for _, tc := range []struct {
		name string
		sel  bool
		reg  fleetHoverRegion
		hl   lipgloss.Style
	}{
		{"hover", false, fleetHoverBody, fleetHoverStyle},
		{"selected", true, fleetHoverNone, fleetSelectedStyle},
	} {
		seq, _, _ := strings.Cut(lipgloss.NewStyle().Background(tc.hl.GetBackground()).Render("x"), "x")
		for i, l := range m.lines {
			if l.kind != fleetLineRow {
				continue
			}
			first := strings.Split(m.renderTreeRowHover(l.row, l.treePrefix, l.nameCol, tc.sel, tc.reg, cw), "\n")[0]
			body := strings.TrimSuffix(first, strings.Repeat(" ", fleetSidebarRightGutter))
			// Every reset must be immediately followed by the background
			// again, except the final one that closes the highlight.
			for _, reset := range []string{"\x1b[0m", "\x1b[m"} {
				rest := body
				for {
					idx := strings.Index(rest, reset)
					if idx < 0 {
						break
					}
					rest = rest[idx+len(reset):]
					if rest != "" && !strings.HasPrefix(rest, seq) {
						t.Fatalf("%s row %d: text after reset without background: %q", tc.name, i, rest)
					}
				}
			}
		}
	}
}

// A row with a generated short title shows it on the list line; the
// selected row's detail lines keep the full recap.
func TestRenderTreeRow_ShortTitle(t *testing.T) {
	m := sampleSidebar(t, 120)
	l := m.lines[m.splitPane.Cursor()]
	const full = "Moved the photo library into albums by year and started deduplicating"
	r := l.row
	r.Recap = fleet.RecapLine{Text: full, Source: fleet.RecapSummary, Title: "Organizing your Photos"}
	cw := m.contentWidth()

	row := ansi.Strip(m.renderTreeRow(r, l.treePrefix, l.nameCol, false, cw))
	if !strings.Contains(row, "Organizing your Photos") || strings.Contains(row, "photo library") {
		t.Errorf("list row should show the short title only: %q", row)
	}
	lines := strings.Split(ansi.Strip(m.renderTreeRow(r, l.treePrefix, l.nameCol, true, cw)), "\n")
	if !strings.Contains(lines[0], "Organizing your Photos") {
		t.Errorf("selected row line = %q", lines[0])
	}
	if detail := strings.Join(lines[1:], " "); !strings.Contains(detail, "photo library into albums") {
		t.Errorf("selected detail should carry the full recap: %q", detail)
	}

	r.Recap.Title = ""
	if row := ansi.Strip(m.renderTreeRow(r, l.treePrefix, l.nameCol, false, cw)); !strings.Contains(row, "Moved the photo") {
		t.Errorf("without a title the row shows the recap text: %q", row)
	}
}
