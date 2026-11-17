package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/state"
)

// sidebarNow is the fixed clock the sample sidebar is rendered against.
var sidebarNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

// fixedHerdNow pins herdNow for the test.
func fixedHerdNow(t *testing.T) {
	t.Helper()
	old := herdNow
	herdNow = func() time.Time { return sidebarNow }
	t.Cleanup(func() { herdNow = old })
}

// sampleRow builds a row and resolves its recap from info, as herd.TagLogs
// would from the session log.
func sampleRow(sess, project, win, name, agent, st string, done bool, info herd.LogInfo, mod func(*herd.Row)) herd.Row {
	r := herd.Row{
		Session: sess,
		Project: project,
		AgentState: state.AgentState{
			Name: name, AgentName: agent, WindowID: win, State: st, Done: done,
		},
	}
	if mod != nil {
		mod(&r)
	}
	r.Recap = herd.Recap(r, info)
	r.PRNumber, r.PRURL = info.PRNumber, info.PRURL
	return r
}

// sampleSidebar is the fixture behind the PR's before/after render: one row
// per recap source, a blocked row, a done row with a PR, and a second group.
func sampleSidebar(t *testing.T, width int) HerdModel {
	t.Helper()
	InitStyles()
	fixedHerdNow(t)
	ago := func(d time.Duration) time.Time { return sidebarNow.Add(-d) }
	rows := []herd.Row{
		sampleRow("agentctl-agentctl", "agentctl", "@1", "go-engineer-opus:2", "go-engineer-opus", "working", false,
			herd.LogInfo{Recap: "We are building the phone web app for agentctl (epic #52): PR 1 is merged, and PR 2 (read API, SSE events) is being built now."},
			func(r *herd.Row) {
				r.LastToolDetail = "Editing internal/serve/routes_read.go"
				r.StateSince = ago(4 * time.Minute)
			}),
		sampleRow("agentctl-agentctl", "agentctl", "@2", "go-engineer:3", "go-engineer", "blocked", false, herd.LogInfo{},
			func(r *herd.Row) {
				r.TicketRef, r.TicketTitle = "61", "Redesign the hub sidebar row"
				r.LastToolDetail = "Bash: go test ./internal/tui"
				r.StateSince = ago(2 * time.Minute)
			}),
		sampleRow("agentctl-agentctl", "agentctl", "@3", "reviewer:1", "reviewer", "idle", true,
			herd.LogInfo{AITitle: "Review mobile web app PR", PRNumber: 60, PRURL: "https://github.com/blsmth/agentctl/pull/60"},
			func(r *herd.Row) {
				r.LastText = "LGTM, approved with two nits."
				r.StateSince = ago(25 * time.Minute)
			}),
		sampleRow("agentctl-agentctl", "agentctl", "@4", "claude:4", "claude", "idle", false, herd.LogInfo{},
			func(r *herd.Row) {
				r.LastToolDetail = "Reading README.md"
				r.StateSince = ago(2 * time.Hour)
			}),
	}
	other := []herd.Row{
		sampleRow("agentctl-spacewalker", "spacewalker", "@7", "swift-engineer:1", "swift-engineer", "working", false, herd.LogInfo{},
			func(r *herd.Row) {
				r.Task = "Add offline mode to the sync engine"
				r.LastToolDetail = "Editing Sources/Sync/Engine.swift"
				r.StateSince = ago(40 * time.Second)
			}),
	}
	m := newHerdModel()
	m.termWidth, m.termHeight = width, 30
	m.liveLeft = width
	m.liveShown = "x" // suppress the "select an agent" hint
	m.configureSplitPane()
	m.loaded = true
	m.groups = []herd.Group{
		{Session: "agentctl-agentctl", Project: "agentctl", Rollup: herd.RollupBlocked, Rows: rows},
		{Session: "agentctl-spacewalker", Project: "spacewalker", Rollup: herd.RollupWorking, Rows: other},
	}
	m.rebuildLines()
	m.resolveSelection() // first agent row
	return m
}

// sidebarListText renders just the list area as plain text.
func sidebarListText(m HerdModel) string {
	out := ansi.Strip(m.renderList())
	lines := strings.Split(out, "\n")
	top := m.herdListTop()
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
		"#60 ✓",                                 // done PR badge replaces the age
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
				if l.kind == herdLineRow {
					out = m.renderTreeRow(l.row, l.treePrefix, sel, cw)
				} else {
					out = m.renderLine(i, cw)
				}
				for _, line := range strings.Split(out, "\n") {
					w := lipgloss.Width(line)
					if w > cw {
						t.Errorf("width=%d line %d selected=%v: %d cols > cw %d: %q", width, i, sel, w, cw, ansi.Strip(line))
					}
					if sel && l.kind == herdLineRow && w != cw {
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
	out := m.renderTreeRow(r.row, r.treePrefix, true, cw)
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasSuffix(line, strings.Repeat(" ", herdSidebarRightGutter)) {
			t.Fatalf("selected line must end in %d plain gutter columns: %q", herdSidebarRightGutter, line)
		}
		head := line[:len(line)-herdSidebarRightGutter]
		if !strings.HasSuffix(head, "m") { // the highlight's closing SGR, not a padded space
			t.Errorf("highlight should end right before the gutter: %q", line)
		}
	}
	// The close button is the last visible cell before the gutter.
	plain := ansi.Strip(strings.Split(out, "\n")[0])
	if want := herdCloseLabel + strings.Repeat(" ", herdSidebarRightGutter); !strings.HasSuffix(plain, want) {
		t.Errorf("row should end in %q, got %q", want, plain)
	}
}

func TestSidebarMouse_ExpandedSelectedRow(t *testing.T) {
	m := sampleSidebar(t, 56)
	top := m.herdListTop()
	cw := m.listColWidth()
	sel := m.splitPane.Cursor() // line 1: first agent row (line 0 is the header)
	h := m.lineHeight(sel, cw)
	if h < 2 {
		t.Fatalf("selected row height = %d, want expanded (>1)", h)
	}
	closeX := cw - herdSidebarRightGutter - 1
	tests := []struct {
		name       string
		x, y       int
		wantIdx    int
		wantAction herdClickAction
	}{
		{"header", 5, top, 0, herdClickSelect},
		{"selected row", 10, top + 1, sel, herdClickSelect},
		{"selected row close", closeX, top + 1, sel, herdClickClose},
		{"detail line selects", 10, top + 2, sel, herdClickSelect},
		{"detail line has no close", closeX, top + 2, sel, herdClickSelect},
		{"last detail line", 10, top + h, sel, herdClickSelect},
		{"row after expansion", 10, top + 1 + h, sel + 1, herdClickSelect},
		{"close after expansion", closeX, top + 1 + h, sel + 1, herdClickClose},
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
	m2 := next.(HerdModel)
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
	m.termHeight = herdChromeRows + m.liveHintRows() + 4 // 4 list rows
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
	fixedHerdNow(t)
	since := sidebarNow.Add(-4 * time.Minute)
	tests := []struct {
		name string
		row  herd.Row
		want string
	}{
		{"no age no pr", herd.Row{}, ""},
		{"age", herd.Row{StateSince: since}, "4m"},
		{"pr and age", herd.Row{StateSince: since, PRNumber: 60}, "#60 4m"},
		{"done pr replaces age", herd.Row{StateSince: since, PRNumber: 60, AgentState: state.AgentState{Done: true}}, "#60 ✓"},
		{"done without pr keeps age", herd.Row{StateSince: since, AgentState: state.AgentState{Done: true}}, "4m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ansi.Strip(rowRight(tt.row)); got != tt.want {
				t.Errorf("rowRight = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRowRight_BlockedUsesBlockedStyle(t *testing.T) {
	InitStyles()
	fixedHerdNow(t)
	r := herd.Row{StateSince: sidebarNow.Add(-2 * time.Minute), AgentState: state.AgentState{State: "blocked"}}
	if got, want := rowRight(r), herdBlockedStyle.Render("2m"); got != want {
		t.Errorf("blocked age = %q, want %q", got, want)
	}
}

func TestRowDetail(t *testing.T) {
	long := strings.Repeat("word ", 60)
	tests := []struct {
		name      string
		row       herd.Row
		wantLines int
		wantLast  string // substring of the last line, "" = skip
	}{
		{"nothing", herd.Row{}, 0, ""},
		{"activity only", herd.Row{AgentState: state.AgentState{LastToolDetail: "Reading a.go"}}, 1, "Reading a.go"},
		{"recap plus activity", herd.Row{
			Recap:      herd.RecapLine{Text: "Short recap", Source: herd.RecapSummary},
			AgentState: state.AgentState{LastToolDetail: "Reading a.go"},
		}, 2, "Reading a.go"},
		{"long recap capped with activity", herd.Row{
			Recap:      herd.RecapLine{Text: long, Source: herd.RecapSummary},
			AgentState: state.AgentState{LastToolDetail: "Reading a.go"},
		}, 3, "Reading a.go"},
		{"long recap alone uses three lines", herd.Row{
			Recap: herd.RecapLine{Text: long, Source: herd.RecapSummary},
		}, 3, "…"},
		{"activity equal to recap not repeated", herd.Row{
			Recap:      herd.RecapLine{Text: "same", Source: herd.RecapTitle},
			AgentState: state.AgentState{LastToolDetail: "same"},
		}, 1, "same"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rowDetail(tt.row, 52)
			if len(got) != tt.wantLines {
				t.Fatalf("rowDetail lines = %d, want %d: %+v", len(got), tt.wantLines, got)
			}
			for _, l := range got {
				if w := lipgloss.Width(l.text); w > 52-herdSidebarRightGutter-herdDetailIndent {
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
	out := ansi.Strip(m.renderTreeRow(l.row, "", false, m.contentWidth()))
	if !strings.Contains(out, "close go-engineer-opus:2? y/n") {
		t.Errorf("close prompt must name the full instance: %q", out)
	}
}

func TestOpenSelectedPR(t *testing.T) {
	m := sampleSidebar(t, 56)
	var opened []string
	old := herdOpenURL
	herdOpenURL = func(url string) tea.Cmd { opened = append(opened, url); return nil }
	t.Cleanup(func() { herdOpenURL = old })

	// Row without a PR: nothing, and no menu entry.
	m.handleKey(tea.KeyPressMsg{Code: 'p', Text: "p"})
	for _, it := range m.herdMenuItems(m.splitPane.Cursor()) {
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
	for _, it := range m.herdMenuItems(3) {
		found = found || it.key == "p"
	}
	if !found {
		t.Error("menu should offer open PR for a row with one")
	}
}
