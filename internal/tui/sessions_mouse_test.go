package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/sessions"
)

// sessTestModel is a loaded model of n sessions at w x h. The first has an
// absurdly long project and agent name to exercise truncation.
func sessTestModel(t *testing.T, w, h, n int) SessionsModel {
	t.Helper()
	now := time.Now()
	var entries []SessionEntry
	for i := 0; i < n; i++ {
		agent, project := fmt.Sprintf("agent-%d", i%3), fmt.Sprintf("proj-%d", i%2)
		if i == 0 {
			agent = "an-extremely-long-agent-name-that-keeps-going"
			project = "日本語のとても長いプロジェクト名-with-a-very-long-suffix"
		}
		entries = append(entries, SessionEntry{
			Session: sessions.Session{ID: fmt.Sprintf("id-%d", i), Agent: agent, Project: "/work/" + project,
				StartTime: now.Add(-time.Duration(i) * 90 * time.Minute), LogFile: fmt.Sprintf("/log/%d", i)},
			Summary: &sessions.Summary{
				TurnCount: 4, TotalInputTokens: 1000, TotalOutputTokens: 200, ModelName: "claude-sonnet-4",
				GitBranch: "agent/go-engineer/issue-12-a-rather-long-branch-name-indeed",
				UserMsgs:  []string{strings.Repeat("a long user message ", 12)},
			},
			Age: "5m ago", Agent: agent, Project: project, Turns: 4, InputTokens: 1000, OutputTokens: 200,
			Duration: 125 * time.Minute, CostUSD: 1.5, ModelShort: "sonnet", CtxPct: 62, TokPerMin: 99,
		})
	}
	m := NewSessionsModel("")
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = next.(SessionsModel)
	m.loading = false
	m.allEntries, m.showAll = entries, true
	m.agentOptions = []string{"agent-0", "agent-1", "agent-2", "an-extremely-long-agent-name-that-keeps-going"}
	m.projectOptions = []string{"proj-0", "proj-1"}
	m.applyFilters()
	return m
}

func sessRender(m SessionsModel) SessionsModel {
	_ = m.View()
	return m
}

func sessZonePoint(t *testing.T, m SessionsModel, id string) (int, int) {
	t.Helper()
	m = sessRender(m)
	r, ok := m.currentZones().find(id)
	if !ok {
		var ids []string
		for _, z := range m.currentZones() {
			ids = append(ids, z.id)
		}
		t.Fatalf("zone %q not rendered; have %v", id, ids)
	}
	return (r.x0 + r.x1 - 1) / 2, r.y
}

func sessPressRelease(m SessionsModel, px, py, rx, ry int) SessionsModel {
	m = sessRender(m)
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: px, Y: py})
	m = next.(SessionsModel)
	next, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: rx, Y: ry})
	return next.(SessionsModel)
}

func sessClick(t *testing.T, m SessionsModel, id string) SessionsModel {
	t.Helper()
	x, y := sessZonePoint(t, m, id)
	return sessPressRelease(m, x, y, x, y)
}

func sessFrozenClock(t *testing.T) *time.Time {
	t.Helper()
	clock := time.Now()
	prev := tkNow
	tkNow = func() time.Time { return clock }
	t.Cleanup(func() { tkNow = prev })
	return &clock
}

func TestSessionsRenderFitsPane(t *testing.T) {
	sizes := []struct{ w, h int }{{80, 24}, {110, 30}, {140, 30}, {100, 12}, {60, 15}, {40, 10}}
	for _, sz := range sizes {
		for _, mode := range []string{"list", "detail", "turns", "help", "empty", "search"} {
			t.Run(fmt.Sprintf("%dx%d/%s", sz.w, sz.h, mode), func(t *testing.T) {
				n := 12
				if mode == "empty" {
					n = 0
				}
				m := sessTestModel(t, sz.w, sz.h, n)
				switch mode {
				case "detail":
					m.viewMode = 1
				case "turns":
					m.viewMode, m.detailTab = 1, 1
					m.entries[0].Summary.TurnMetrics = []sessions.TurnMetric{{Turn: 1, InputTokens: 5}, {Turn: 2, InputTokens: 9}}
				case "help":
					m.helpOpen = true
				case "search":
					m.searchMode = true
				}
				out := m.View().Content
				lines := strings.Split(out, "\n")
				if len(lines) != sz.h {
					t.Errorf("%d lines, want %d", len(lines), sz.h)
				}
				for i, l := range lines {
					if w := lipgloss.Width(l); w > sz.w {
						t.Errorf("line %d is %d wide (> %d): %q", i, w, sz.w, stripANSI(l))
					}
				}
			})
		}
	}
}

func TestSessionsPreviewCollapses(t *testing.T) {
	wide := stripANSI(sessTestModel(t, 130, 30, 5).View().Content)
	narrow := stripANSI(sessTestModel(t, 80, 24, 5).View().Content)
	if !strings.Contains(wide, "Session Details") || !strings.Contains(wide, "Resume") {
		t.Error("130 cols should show the preview panel with its buttons")
	}
	if strings.Contains(narrow, "Session Details") {
		t.Error("80 cols should collapse the preview panel")
	}
}

func TestSessionsZeroSessions(t *testing.T) {
	m := sessTestModel(t, 110, 30, 0)
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "No sessions found") || !strings.Contains(out, "0 sessions") {
		t.Errorf("empty state missing:\n%s", out)
	}
	// Keys with nothing selected do nothing, and clicks don't panic.
	for _, k := range []string{"r", "l", "t", "enter"} {
		next, cmd := m.Update(tkKey(k))
		if cmd != nil || next.(SessionsModel).quitting || next.(SessionsModel).viewMode != 0 {
			t.Errorf("key %q with no sessions should be a no-op", k)
		}
	}
	sessPressRelease(m, 5, 5, 5, 5)
}

func TestSessionsClickSelectsRow(t *testing.T) {
	m := sessTestModel(t, 110, 30, 6)
	m = sessClick(t, m, "row:3")
	if m.cursor != 3 {
		t.Errorf("cursor = %d, want 3", m.cursor)
	}
	if m.viewMode != 0 {
		t.Error("a single click must not open the detail")
	}
}

func TestSessionsDoubleClickOpensDetail(t *testing.T) {
	clock := sessFrozenClock(t)
	m := sessTestModel(t, 110, 30, 6)
	m = sessClick(t, m, "row:2")
	*clock = clock.Add(200 * time.Millisecond)
	m = sessClick(t, m, "row:2")
	if m.viewMode != 1 {
		t.Fatalf("double-click did not open the detail (viewMode %d)", m.viewMode)
	}

	// Past the window it is two single clicks.
	m = sessTestModel(t, 110, 30, 6)
	m = sessClick(t, m, "row:2")
	*clock = clock.Add(time.Second)
	m = sessClick(t, m, "row:2")
	if m.viewMode != 0 {
		t.Error("slow second click must not open the detail")
	}

	// Two different rows is not a double-click either.
	m = sessTestModel(t, 110, 30, 6)
	m = sessClick(t, m, "row:1")
	m = sessClick(t, m, "row:2")
	if m.viewMode != 0 || m.cursor != 2 {
		t.Errorf("clicks on two rows: viewMode %d cursor %d", m.viewMode, m.cursor)
	}
}

func TestSessionsReleaseElsewhereCancels(t *testing.T) {
	m := sessTestModel(t, 110, 30, 6)
	x1, y1 := sessZonePoint(t, m, "row:1")
	x4, y4 := sessZonePoint(t, m, "row:4")
	m = sessPressRelease(m, x1, y1, x4, y4)
	if m.cursor != 0 {
		t.Errorf("drag between rows moved the cursor to %d", m.cursor)
	}
}

func TestSessionsButtonsRunKeyPaths(t *testing.T) {
	cases := []struct {
		zone   string
		action string
		view   int
	}{
		{"key:r@preview", "resume", 0},
		{"key:l@preview", "log", 0},
		{"key:enter@preview", "", 1},
		{"key:t@preview", "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.zone, func(t *testing.T) {
			m := sessTestModel(t, 130, 30, 4)
			m.cursor = 1
			m = sessClick(t, m, tc.zone)
			if m.result.Action != tc.action || m.viewMode != tc.view {
				t.Errorf("result %+v viewMode %d, want action %q view %d", m.result, m.viewMode, tc.action, tc.view)
			}
			if tc.action != "" && (m.result.SessionID != "id-1" || !m.quitting) {
				t.Errorf("wrong session or not quitting: %+v", m.result)
			}
			if tc.action == "log" && m.result.LogFile != "/log/1" {
				t.Errorf("log file = %q", m.result.LogFile)
			}
		})
	}
}

func TestSessionsDetailButtonsAndBack(t *testing.T) {
	m := sessTestModel(t, 100, 30, 3)
	m.viewMode = 1
	m = sessClick(t, m, "key:r@detail")
	if m.result.Action != "resume" {
		t.Errorf("detail Resume: %+v", m.result)
	}
	m = sessTestModel(t, 100, 30, 3)
	m.viewMode = 1
	m = sessClick(t, m, "key:esc@back")
	if m.viewMode != 0 {
		t.Error("← back should return to the list")
	}
}

func TestSessionsHintsClickable(t *testing.T) {
	m := sessTestModel(t, 130, 30, 4)
	for _, h := range m.listHints() {
		send := h.send
		if send == "" {
			send = h.key
		}
		if !m.renderZones().has("key:" + send + "@hint") {
			t.Errorf("hint %q is not a click zone at 130 cols", h.key)
		}
	}
	// Each hint runs its key.
	if got := sessClick(t, m, "key:j@hint"); got.cursor != 1 {
		t.Errorf("j hint: cursor %d", got.cursor)
	}
	if got := sessClick(t, m, "key:a@hint"); got.filterAgent != "agent-0" {
		t.Errorf("a hint: filterAgent %q", got.filterAgent)
	}
	if got := sessClick(t, m, "key:?@hint"); !got.helpOpen {
		t.Error("? hint should open the overlay")
	}
	if got := sessClick(t, m, "key:q@hint"); !got.quitting || !got.result.Cancelled {
		t.Error("q hint should quit")
	}
	// At 80 cols pinned hints stay clickable.
	narrow := sessTestModel(t, 80, 24, 4)
	for _, id := range []string{"key:j@hint", "key:q@hint", "key:?@hint", "key:r@hint"} {
		if !narrow.renderZones().has(id) {
			t.Errorf("pinned hint %s missing at 80 cols", id)
		}
	}
}

func TestSessionsChips(t *testing.T) {
	m := sessTestModel(t, 110, 30, 6)
	m = sessClick(t, m, "key:a@chip")
	if m.filterAgent != "agent-0" || m.showAll {
		t.Fatalf("agent chip: filter %q", m.filterAgent)
	}
	m = sessClick(t, m, "chip:agent-clear")
	if m.filterAgent != "" || !m.showAll || len(m.entries) != 6 {
		t.Errorf("agent ×: filter %q entries %d", m.filterAgent, len(m.entries))
	}
	m = sessClick(t, m, "key:p@chip")
	if m.filterProject != "proj-0" {
		t.Fatalf("project chip: %q", m.filterProject)
	}
	m = sessClick(t, m, "chip:project-clear")
	if m.filterProject != "" {
		t.Error("project × did not clear")
	}
	m.searchQuery = "agent-1"
	m.applyFilters()
	m = sessClick(t, m, "chip:search-clear")
	if m.searchQuery != "" || len(m.entries) != 6 {
		t.Errorf("search ×: %q, %d entries", m.searchQuery, len(m.entries))
	}
	m = sessClick(t, m, "key:/@chip")
	if !m.searchMode {
		t.Error("search chip should open the search box")
	}
}

func TestSessionsWheel(t *testing.T) {
	m := sessTestModel(t, 130, 30, 20)
	x, y := sessZonePoint(t, m, "row:2")
	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: x, Y: y})
	m = next.(SessionsModel)
	if m.cursor != 1 {
		t.Errorf("wheel over the list: cursor %d, want 1", m.cursor)
	}
	next, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: x, Y: y})
	if next.(SessionsModel).cursor != 0 {
		t.Error("wheel up should move the cursor back")
	}

	// Over the preview it scrolls the preview, not the list.
	m = sessRender(m)
	var px, py int
	for _, z := range m.currentZones() {
		if z.id == "pane:preview" && z.y > 10 {
			px, py = z.x0+1, z.y
			break
		}
	}
	next, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: px, Y: py})
	got := next.(SessionsModel)
	if got.cursor != m.cursor || got.previewScroll != tkWheelStep {
		t.Errorf("wheel over preview: cursor %d scroll %d", got.cursor, got.previewScroll)
	}
}

func TestSessionsListScrollsToCursor(t *testing.T) {
	m := sessTestModel(t, 80, 24, 40)
	for i := 0; i < 30; i++ {
		next, _ := m.Update(tkKey("j"))
		m = next.(SessionsModel)
	}
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "31 of 40") || m.listTop == 0 {
		t.Errorf("list did not scroll with the cursor (listTop %d)", m.listTop)
	}
	if !m.renderZones().has("row:30") {
		t.Error("the selected row must be on screen")
	}
}

func TestSessionsHelpOverlayOwnsMouse(t *testing.T) {
	m := sessTestModel(t, 110, 30, 6)
	m = sessClick(t, m, "key:?@hint")
	if !m.helpOpen {
		t.Fatal("overlay not open")
	}
	z := m.renderZones()
	if z.has("row:1") || z.has("key:r@hint") {
		t.Error("the view behind the overlay must lose its zones")
	}
	// The wheel does nothing behind it.
	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 5, Y: 5})
	if next.(SessionsModel).cursor != m.cursor {
		t.Error("wheel moved the list behind the overlay")
	}
	// A resize while it is open keeps it open and in bounds.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})
	m = next.(SessionsModel)
	for _, l := range strings.Split(m.View().Content, "\n") {
		if lipgloss.Width(l) > 70 {
			t.Fatalf("overlay overflows after resize: %d wide", lipgloss.Width(l))
		}
	}
	// Close button closes; so does a click outside.
	closed := sessClick(t, m, "mkey:esc@help")
	if closed.helpOpen {
		t.Error("Close button did not close the overlay")
	}
	m = sessPressRelease(m, 0, 0, 0, 0)
	if m.helpOpen {
		t.Error("a click outside should close the overlay")
	}
}

func TestSessionsHoverTint(t *testing.T) {
	m := sessTestModel(t, 110, 30, 6)
	x, y := sessZonePoint(t, m, "row:3")
	next, _ := m.Update(tea.MouseMotionMsg{X: x, Y: y})
	m = next.(SessionsModel)
	if m.hoverRow != "row:3" {
		t.Fatalf("hoverRow = %q", m.hoverRow)
	}
	plain := m.View().Content
	m.hoverRow = ""
	if plain == m.View().Content {
		t.Error("a hovered row should render differently")
	}
}

func TestSessionsKeyboardUnchanged(t *testing.T) {
	// No mouse message ever arrives: every key path still works.
	m := sessTestModel(t, 110, 30, 4)
	for _, k := range []string{"j", "j", "k", "enter"} {
		next, _ := m.Update(tkKey(k))
		m = next.(SessionsModel)
	}
	if m.cursor != 1 || m.viewMode != 1 {
		t.Errorf("cursor %d viewMode %d", m.cursor, m.viewMode)
	}
	next, _ := m.Update(tkKey("?"))
	if !next.(SessionsModel).helpOpen {
		t.Error("? should open the overlay")
	}
}
