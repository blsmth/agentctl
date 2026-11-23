package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
)

func statusTestRow() fleet.Row {
	r := testRow("go-engineer~2", "agentctl-p", "working", "Bash: go test ./internal/tui/")
	r.WindowID = "@1"
	r.Project = "agentctl"
	r.Ticket = 42
	r.Recap = fleet.RecapLine{
		Title:  "Status pane",
		Text:   "Moving the selected row's expanded detail out of the hub list into a new status pane stacked above the live viewer pane, following the hub selection file, and keeping the inline expansion as the fallback when there is no viewer.",
		Source: fleet.RecapSummary,
	}
	return r
}

func TestHubStatusFor(t *testing.T) {
	InitStyles()
	st := hubStatusFor(statusTestRow(), fleetDisplay{})
	if st.Glyph != "●" || st.Title != "Status pane" || st.Project != "agentctl" || st.Note != "" {
		t.Errorf("status = %+v", st)
	}
	if st.Detail.Recap == "" || st.Detail.Activity != "" || st.Detail.Marker != "" || st.Detail.RecapIsActivity {
		t.Errorf("detail = %+v; the status pane carries the recap only", st.Detail)
	}
	act := statusTestRow()
	act.Recap = fleet.RecapLine{Source: fleet.RecapActivity, Text: "Bash: ls"}
	if d := hubStatusFor(act, fleetDisplay{}).Detail; d != (rowDetailParts{}) {
		t.Errorf("activity recap leaked into the status: %+v", d)
	}
	if len(st.Chips) == 0 || st.Chips[0] != "#42" {
		t.Errorf("chips = %v", st.Chips)
	}

	empty := testRow("go-engineer", "agentctl-p", "idle", "")
	st = hubStatusFor(empty, fleetDisplay{})
	if st.Title != "go-engineer" || !strings.HasPrefix(st.Note, "no prompt yet") || st.Detail.Recap != "" {
		t.Errorf("empty row status = %+v", st)
	}
}

func TestRenderHubStatus(t *testing.T) {
	InitStyles()
	sel := HubSelection{Session: "agentctl-p", WindowID: "@1", RuntimeDir: "/p/.agentctl"}
	sel.Status = hubStatusFor(statusTestRow(), fleetDisplay{})
	const w, h = 44, fleetStatusRows
	out := renderHubStatus(sel, false, w, h)
	lines := strings.Split(out, "\n")
	if len(lines) != h {
		t.Fatalf("%d lines, want %d:\n%s", len(lines), h, out)
	}
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			t.Errorf("line %d is %d wide, over %d: %q", i, lipgloss.Width(l), w, ansi.Strip(l))
		}
	}
	plain := ansi.Strip(out)
	for _, want := range []string{"● Status pane", "agentctl", "Moving the selected row", "[#42]"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "go test") {
		t.Errorf("activity line shown:\n%s", plain)
	}
	// Title, four recap lines, chips: the recap wraps over every row left,
	// cut with an ellipsis.
	if got := strings.TrimSpace(ansi.Strip(lines[4])); !strings.HasSuffix(got, "…") {
		t.Errorf("last recap line %q, want an ellipsis", got)
	}
	if got := ansi.Strip(lines[h-1]); !strings.Contains(got, "[#42]") {
		t.Errorf("last line %q, want the chips", got)
	}

	// A taller pane fits the whole recap.
	if plain := ansi.Strip(renderHubStatus(sel, false, w, 12)); strings.Contains(plain, "…") {
		t.Errorf("tall pane still cut the recap:\n%s", plain)
	}
}

func TestRenderHubStatusIgnoresActivityFromOlderFile(t *testing.T) {
	InitStyles()
	st := &hubStatus{Glyph: "●", Title: "T", Detail: rowDetailParts{Recap: "ls", RecapIsActivity: true, Marker: "$ "}}
	plain := ansi.Strip(renderHubStatus(HubSelection{Session: "s", WindowID: "@1", RuntimeDir: "/p", Status: st}, false, 60, 6))
	if strings.Contains(plain, "$ ") || strings.Contains(plain, "ls") || !strings.Contains(plain, "nothing to summarise") {
		t.Errorf("want a placeholder, got:\n%s", plain)
	}
}

func TestRenderHubStatusPlaceholder(t *testing.T) {
	InitStyles()
	tests := []struct {
		name    string
		sel     HubSelection
		readErr bool
		want    string
	}{
		{"no selection (header, closed, schedule)", HubSelection{}, false, "Select an agent in the hub"},
		{"unreadable", HubSelection{}, true, "Hub selection unavailable"},
		{"valid but no status (older hub)", HubSelection{Session: "s", WindowID: "@1", RuntimeDir: "/p"}, false, "Select an agent in the hub"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderHubStatus(tt.sel, tt.readErr, 60, 5)
			lines := strings.Split(ansi.Strip(out), "\n")
			if len(lines) != 5 || !strings.Contains(lines[0], tt.want) {
				t.Errorf("got %q, want %q on the first of 5 lines", lines, tt.want)
			}
		})
	}
}

func TestHubStatusModelFollowsSelection(t *testing.T) {
	InitStyles()
	sel := HubSelection{Session: "s", WindowID: "@1", RuntimeDir: "/p", Status: &hubStatus{Glyph: "●", Title: "Doing it"}}
	var readErr error
	focused := 0
	m := HubStatusModel{
		read:        func() (HubSelection, error) { return sel, readErr },
		focusViewer: func() error { focused++; return nil },
		width:       40, height: 5,
	}
	next, _ := m.Update(hubStatusTickMsg{})
	m = next.(HubStatusModel)
	if !strings.Contains(ansi.Strip(m.View().Content), "Doing it") {
		t.Errorf("view = %q", m.View().Content)
	}
	readErr = errors.New("corrupt")
	next, _ = m.Update(hubStatusTickMsg{})
	m = next.(HubStatusModel)
	if !strings.Contains(ansi.Strip(m.View().Content), "Hub selection unavailable") {
		t.Errorf("view after a read error = %q", m.View().Content)
	}
	// Input hands focus to the viewer below.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if cmd == nil {
		t.Fatal("key press returned no command")
	}
	cmd()
	if focused != 1 {
		t.Errorf("focusViewer called %d times, want 1", focused)
	}
}

// statusPaneModel is a hub showing row a of liveTestModel in the viewer,
// with the status pane open.
func statusPaneModel(t *testing.T) FleetModel {
	t.Helper()
	m := liveTestModel(t, 120, 40)
	m.selectLine(m.firstRowLine())
	m.liveShown = m.selSession + "|" + m.selWindowID
	m.statusPane = true
	return m
}

func TestFleetStatusPaneDropsInlineDetail(t *testing.T) {
	m := statusPaneModel(t)
	i := m.splitPane.Cursor()
	cw := m.contentWidth()
	if got := strings.Count(m.renderLine(i, cw), "\n"); got != 0 {
		t.Errorf("selected row rendered %d detail lines with the status pane open", got)
	}
	if got := m.lineHeight(i, cw); got != 1 {
		t.Errorf("lineHeight = %d, want 1", got)
	}
	// Row b sits right below row a now: a click there selects it.
	if idx, ok := m.lineAt(5, 5); !ok || idx != i+1 {
		t.Errorf("lineAt(5,5) = (%d,%v), want (%d,true)", idx, ok, i+1)
	}

	// No viewer (the activity pane in its place, a failed show): the inline expansion is back.
	for name, mod := range map[string]func(*FleetModel){
		"viewer hidden":     func(m *FleetModel) { m.liveShown = "" },
		"show failed":       func(m *FleetModel) { m.liveFailed = m.liveShown },
		"activity in place": func(m *FleetModel) { m.activity.hidesViewer = true },
		"no status pane":    func(m *FleetModel) { m.statusPane = false },
	} {
		t.Run(name, func(t *testing.T) {
			m := statusPaneModel(t)
			mod(&m)
			if got := m.lineHeight(i, cw); got < 2 {
				t.Errorf("lineHeight = %d, want the inline detail back", got)
			}
			if strings.Count(m.renderLine(i, cw), "\n")+1 != m.lineHeight(i, cw) {
				t.Errorf("render and lineHeight disagree")
			}
		})
	}
}

func TestFleetLiveShowEnsuresStatusPane(t *testing.T) {
	installActivityStub(t, 200)
	var wants []bool
	open := true
	orig := hubStatusSync
	hubStatusSync = func(want bool, rows int) (bool, error) {
		if rows != fleetStatusRows {
			t.Errorf("rows = %d", rows)
		}
		wants = append(wants, want)
		return want && open, nil
	}
	t.Cleanup(func() { hubStatusSync = orig })

	m := liveTestModel(t, 56, 40)
	m.selectLine(m.firstRowLine())
	m.liveShown = ""
	m = activityPump(t, m, activityRun(m.maybeShowCmd())...)
	if len(wants) != 1 || !wants[0] || !m.statusPaneOpen() {
		t.Fatalf("wants = %v, open = %v", wants, m.statusPaneOpen())
	}
	// The open status pane follows the selection file, detail included.
	path, _ := hubSelectionPath()
	got, err := readHubSelection(path)
	if err != nil || got.WindowID != m.selWindowID || got.Status == nil {
		t.Errorf("published %+v, %v", got, err)
	}

	// A short window keeps every row for the viewer.
	short := liveTestModel(t, 56, fleetStatusMinWindowRows-1)
	short.selectLine(short.firstRowLine())
	short.liveShown = ""
	short = activityPump(t, short, activityRun(short.maybeShowCmd())...)
	if wants[len(wants)-1] || short.statusPaneOpen() {
		t.Errorf("short window: wants = %v, open = %v", wants, short.statusPaneOpen())
	}
}

func TestFleetStatusPaneResize(t *testing.T) {
	var pins []int
	var wants []bool
	origSync, origPin, origWidth := hubStatusSync, hubStatusPin, hubWindowWidth
	hubStatusSync = func(want bool, _ int) (bool, error) { wants = append(wants, want); return want, nil }
	hubStatusPin = func(h int) error { pins = append(pins, h); return nil }
	t.Cleanup(func() { hubStatusSync, hubStatusPin, hubWindowWidth = origSync, origPin, origWidth })

	m := statusPaneModel(t)
	hubWindowWidth = func() (int, error) { return 0, errors.New("no tmux") }
	m = activityPump(t, m, tea.WindowSizeMsg{Width: 120, Height: 50})
	if len(wants) != 1 || !wants[0] || len(pins) != 1 || pins[0] != fleetStatusRows {
		t.Errorf("taller: wants = %v pins = %v", wants, pins)
	}
	m = activityPump(t, m, tea.WindowSizeMsg{Width: 130, Height: 50})
	if len(wants) != 1 {
		t.Errorf("a width-only change re-synced the status pane: %v", wants)
	}
	m = activityPump(t, m, tea.WindowSizeMsg{Width: 130, Height: 20})
	if len(wants) != 2 || wants[1] || m.statusPaneOpen() {
		t.Errorf("short: wants = %v open = %v", wants, m.statusPaneOpen())
	}
}

func TestDetailPartsMatchRowDetail(t *testing.T) {
	// rowDetailText is detailPartsFor + textLines: the list and the status
	// pane lay out the same parts.
	r := statusTestRow()
	for _, viewed := range []bool{false, true} {
		got := detailPartsFor(r, viewed).textLines(30, fleetDetailMaxLines)
		want := rowDetailText(r, 30, viewed)
		if len(got) != len(want) {
			t.Fatalf("viewed=%v: %d lines vs %d", viewed, len(got), len(want))
		}
		if viewed && got[len(got)-1].marker != "" {
			t.Errorf("viewed row kept its activity line")
		}
	}
	act := testRow("a", "s", "working", "Bash: ls")
	act.AgentState = state.AgentState{Name: "a", WindowID: "@1", State: "working", LastToolDetail: "Bash: ls"}
	if p := detailPartsFor(act, true); p.Recap != "" && p.RecapIsActivity {
		t.Errorf("viewed activity recap kept: %+v", p)
	}
}
