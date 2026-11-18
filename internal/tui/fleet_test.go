package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
)

// ── Sidebar line construction ────────────────────────────────────────────────

func testRow(name, session, state_, activity string) fleet.Row {
	return fleet.Row{
		Session: session,
		Project: "myproj",
		AgentState: state.AgentState{
			Name:           name,
			WindowID:       "@5",
			State:          state_,
			LastToolDetail: activity,
		},
	}
}

func TestRenderRow_TruncatesAtNarrowWidth(t *testing.T) {
	InitStyles()

	r := testRow("a-very-long-instance-name-that-goes-on", "agentctl-myproj", "working",
		"Editing a file with a very long path that will not fit in a narrow sidebar column")

	m := FleetModel{}

	t.Run("wide width — no truncation of short content", func(t *testing.T) {
		short := testRow("go-engineer", "agentctl-myproj", "working", "Editing monitor.go")
		out := m.renderRow(short, false, 100)
		if lipgloss.Width(out) > 100 {
			t.Errorf("rendered width %d exceeds cw=100", lipgloss.Width(out))
		}
		if !strings.Contains(out, "go-engineer") {
			t.Errorf("expected full name in wide render, got %q", out)
		}
	})

	t.Run("narrow sidebar width — name and activity truncated", func(t *testing.T) {
		const cw = 24
		out := m.renderRow(r, false, cw)
		if got := lipgloss.Width(out); got > cw {
			t.Errorf("rendered row width = %d, want <= %d (row: %q)", got, cw, out)
		}
	})

	t.Run("selected row is padded to full width", func(t *testing.T) {
		const cw = 40
		out := m.renderRow(r, true, cw)
		if got := lipgloss.Width(out); got != cw {
			t.Errorf("selected row width = %d, want exactly %d", got, cw)
		}
	})

	t.Run("very narrow width does not panic", func(t *testing.T) {
		// Widths below this are narrower than any real sidebar can be
		// (fleetMinSidebarWidth is enforced well above this), but the
		// function must still not panic or produce a negative-width
		// truncation call.
		for _, cw := range []int{0, 1, 5, 10} {
			out := m.renderRow(r, false, cw)
			_ = out // must not panic
		}
	})

	t.Run("realistic narrow sidebar widths stay within budget", func(t *testing.T) {
		for _, cw := range []int{16, 20, 24, 28} {
			out := m.renderRow(r, false, cw)
			if got := lipgloss.Width(out); got > cw {
				t.Errorf("cw=%d: rendered width %d exceeds cw (row: %q)", cw, got, out)
			}
		}
	})
}

func TestRenderGroupHeader_TruncatesAndMarksSelected(t *testing.T) {
	InitStyles()
	m := FleetModel{}

	g := fleet.Group{
		Session: "agentctl-a-very-long-project-directory-name",
		Project: "a-very-long-project-directory-name",
		Rollup:  fleet.RollupWorking,
		Rows:    []fleet.Row{testRow("a", "s", "working", "")},
	}

	t.Run("narrow width truncates the composed header line", func(t *testing.T) {
		const cw = 20
		out := m.renderGroupHeader(g, false, cw)
		if got := lipgloss.Width(out); got > cw {
			t.Errorf("header width = %d, want <= %d", got, cw)
		}
	})

	t.Run("selected header is padded to full width", func(t *testing.T) {
		const cw = 60
		out := m.renderGroupHeader(g, true, cw)
		if got := lipgloss.Width(out); got != cw {
			t.Errorf("selected header width = %d, want exactly %d", got, cw)
		}
	})
}

func TestRebuildLines_HeadersRowsAndPlaceholders(t *testing.T) {
	m := &FleetModel{
		groups: []fleet.Group{
			{
				Session: "agentctl-a",
				Project: "a",
				Rows: []fleet.Row{
					testRow("agent-1", "agentctl-a", "blocked", ""),
					testRow("agent-2", "agentctl-a", "idle", ""),
				},
			},
			{
				Session: "agentctl-b",
				Project: "b",
				Rows:    nil, // no agents -> placeholder
			},
		},
	}
	m.rebuildLines()

	// group a: header + 2 rows (3 lines); a divider between the two groups;
	// group b: header + placeholder (2 lines) = 6 total.
	if len(m.lines) != 6 {
		t.Fatalf("len(lines) = %d, want 6 (header, 2 rows, divider, header+placeholder)", len(m.lines))
	}
	if m.lines[0].kind != fleetLineHeader || m.lines[0].group.Session != "agentctl-a" {
		t.Errorf("lines[0] = %+v, want header for agentctl-a", m.lines[0])
	}
	if m.lines[1].kind != fleetLineRow || m.lines[1].row.Name != "agent-1" {
		t.Errorf("lines[1] = %+v, want row agent-1", m.lines[1])
	}
	if m.lines[2].kind != fleetLineRow || m.lines[2].row.Name != "agent-2" {
		t.Errorf("lines[2] = %+v, want row agent-2", m.lines[2])
	}
	if m.lines[3].kind != fleetLineDivider {
		t.Errorf("lines[3] = %+v, want divider between agentctl-a and agentctl-b", m.lines[3])
	}
	if m.lines[4].kind != fleetLineHeader || m.lines[4].group.Session != "agentctl-b" {
		t.Errorf("lines[4] = %+v, want header for agentctl-b", m.lines[4])
	}
	if m.lines[5].kind != fleetLinePlaceholder {
		t.Errorf("lines[5] = %+v, want placeholder for agentctl-b (zero agents)", m.lines[5])
	}
}

// ── Divider construction ─────────────────────────────────────────────────────

func TestRebuildLines_DividerCounts(t *testing.T) {
	oneGroup := []fleet.Group{
		{Session: "agentctl-a", Project: "a", Rows: []fleet.Row{testRow("agent-1", "agentctl-a", "idle", "")}},
	}
	twoGroups := append(append([]fleet.Group{}, oneGroup...), fleet.Group{
		Session: "agentctl-b", Project: "b", Rows: []fleet.Row{testRow("agent-2", "agentctl-b", "idle", "")},
	})
	threeGroups := append(append([]fleet.Group{}, twoGroups...), fleet.Group{
		Session: "agentctl-c", Project: "c", Rows: []fleet.Row{testRow("agent-3", "agentctl-c", "idle", "")},
	})

	tests := []struct {
		name          string
		groups        []fleet.Group
		wantDividers  int
		wantNoLeadTrl bool
	}{
		{"zero groups", nil, 0, true},
		{"one group", oneGroup, 0, true},
		{"two groups", twoGroups, 1, true},
		{"three groups", threeGroups, 2, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &FleetModel{groups: tt.groups}
			m.rebuildLines()

			dividers := 0
			for _, l := range m.lines {
				if l.kind == fleetLineDivider {
					dividers++
				}
			}
			if dividers != tt.wantDividers {
				t.Errorf("dividers = %d, want %d (lines: %+v)", dividers, tt.wantDividers, m.lines)
			}
			if tt.wantNoLeadTrl && len(m.lines) > 0 {
				if m.lines[0].kind == fleetLineDivider {
					t.Errorf("first line must never be a divider: %+v", m.lines[0])
				}
				if m.lines[len(m.lines)-1].kind == fleetLineDivider {
					t.Errorf("last line must never be a divider: %+v", m.lines[len(m.lines)-1])
				}
			}
		})
	}
}

// TestRenderFleetDivider_IsEmptyRow pins the divider between groups as an
// empty row (rhythm comes from the gap, not a rule) at every width, so it can
// never overflow or draw a border.
func TestRenderFleetDivider_IsEmptyRow(t *testing.T) {
	InitStyles()
	for _, cw := range []int{0, 1, 3, 10, 40, 80, 120} {
		if out := renderFleetDivider(cw); out != "" {
			t.Errorf("renderFleetDivider(%d) = %q, want an empty row", cw, out)
		}
	}
	m := sampleSidebar(t, 56)
	for i, l := range m.lines {
		if l.kind == fleetLineDivider {
			if out := m.renderLine(i, m.contentWidth()); out != "" {
				t.Errorf("divider line %d renders %q, want empty", i, out)
			}
			if l.selectable() {
				t.Errorf("divider line %d must stay non-selectable", i)
			}
		}
	}
}

func TestRebuildLines_EmptyGroupYieldsPlaceholder(t *testing.T) {
	m := &FleetModel{
		groups: []fleet.Group{
			{Session: "agentctl-empty", Project: "empty", Rows: nil},
		},
	}
	m.rebuildLines()

	if len(m.lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2 (header + placeholder)", len(m.lines))
	}
	if m.lines[1].kind != fleetLinePlaceholder {
		t.Errorf("lines[1].kind = %v, want fleetLinePlaceholder", m.lines[1].kind)
	}
}

// ── Selection resolution survives reload (regression) ────────────────────────

func TestResolveSelection_PrefersFirstRowByDefault(t *testing.T) {
	m := &FleetModel{
		groups: []fleet.Group{
			{Session: "agentctl-a", Project: "a", Rows: []fleet.Row{testRow("agent-1", "agentctl-a", "idle", "")}},
		},
	}
	m.rebuildLines()
	m.resolveSelection()

	if m.selIsHeader {
		t.Errorf("expected the default selection to land on the first row, not the header")
	}
	if m.selSession != "agentctl-a" || m.selWindowID != "@5" {
		t.Errorf("selection = (%q, %q), want (agentctl-a, @5)", m.selSession, m.selWindowID)
	}
}

func TestResolveSelection_MatchesByWindowIDAfterReorder(t *testing.T) {
	agent1 := testRow("agent-1", "agentctl-a", "idle", "")
	agent1.WindowID = "@5"
	agent2 := testRow("agent-2", "agentctl-a", "blocked", "")
	agent2.WindowID = "@6"

	m := &FleetModel{
		groups: []fleet.Group{
			{Session: "agentctl-a", Project: "a", Rows: []fleet.Row{agent1, agent2}},
		},
	}
	m.rebuildLines()
	// Select "agent-1" explicitly (it's not blocked, so it won't be first
	// after a re-sort that floats blocked agents up).
	m.selSession, m.selWindowID, m.selIsHeader = "agentctl-a", "@5", false

	// Simulate a reload where the row order changed (e.g. agent-2 now
	// sorts first because it's blocked) — resolveSelection must still find
	// agent-1 by (session, windowID), not lose it to the reorder.
	m.groups[0].Rows = []fleet.Row{agent2, agent1}
	m.rebuildLines()
	m.resolveSelection()

	got := m.selectedRow()
	if got == nil || got.Name != "agent-1" {
		t.Fatalf("selected row = %+v, want agent-1", got)
	}
}

func TestResolveSelection_HeaderSelectionSurvivesReload(t *testing.T) {
	m := &FleetModel{
		groups: []fleet.Group{
			{Session: "agentctl-a", Project: "a", Rows: []fleet.Row{testRow("agent-1", "agentctl-a", "idle", "")}},
		},
	}
	m.rebuildLines()
	m.selSession, m.selWindowID, m.selIsHeader = "agentctl-a", "", true

	m.rebuildLines() // reload with the same session
	m.resolveSelection()

	if !m.selIsHeader {
		t.Errorf("expected header selection to survive reload")
	}
	if g := m.selectedGroup(); g == nil || g.Session != "agentctl-a" {
		t.Errorf("selectedGroup() = %+v, want agentctl-a", g)
	}
}

// TestResolveSelection_SkipsDividerWhenMatchingByWindowID verifies that
// resolveSelection's (session, windowID) match still finds the right row
// once dividers have shifted every subsequent line's index — the second
// group's row is no longer at index 1, it's at index 4 (header, row,
// divider, header, row).
func TestResolveSelection_SkipsDividerWhenMatchingByWindowID(t *testing.T) {
	agentA := testRow("agent-a", "agentctl-a", "idle", "")
	agentA.WindowID = "@5"
	agentB := testRow("agent-b", "agentctl-b", "idle", "")
	agentB.WindowID = "@9"

	m := &FleetModel{
		groups: []fleet.Group{
			{Session: "agentctl-a", Project: "a", Rows: []fleet.Row{agentA}},
			{Session: "agentctl-b", Project: "b", Rows: []fleet.Row{agentB}},
		},
	}
	m.rebuildLines()
	m.selSession, m.selWindowID, m.selIsHeader = "agentctl-b", "@9", false

	m.resolveSelection()

	got := m.selectedRow()
	if got == nil || got.Name != "agent-b" {
		t.Fatalf("selected row = %+v, want agent-b", got)
	}
	idx := m.splitPane.Cursor()
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != fleetLineRow {
		t.Fatalf("cursor at %d landed on a non-row line: %+v", idx, m.lines)
	}
}

// ── Cursor movement skips dividers ───────────────────────────────────────────

// TestMoveCursor_SkipsDividers builds a two-group, one-row-each layout
// (header-a, dispatched-a, row-a1, divider, header-b, dispatched-b, row-b1 — indices
// 0..6) and drives the cursor across the divider and sub-section labels in
// both directions, including from the boundary positions at either end.
func TestMoveCursor_SkipsDividers(t *testing.T) {
	// Rows are parentless dispatched rows so each group has a label to skip.
	a1 := testRow("agent-a1", "agentctl-a", "idle", "")
	a1.Dispatched = true
	b1 := testRow("agent-b1", "agentctl-b", "idle", "")
	b1.Dispatched = true
	m := &FleetModel{
		groups: []fleet.Group{
			{Session: "agentctl-a", Project: "a", Rows: []fleet.Row{a1}},
			{Session: "agentctl-b", Project: "b", Rows: []fleet.Row{b1}},
		},
	}
	m.rebuildLines()
	if len(m.lines) != 7 {
		t.Fatalf("setup: len(lines) = %d, want 7", len(m.lines))
	}
	if m.lines[3].kind != fleetLineDivider {
		t.Fatalf("setup: lines[3] = %+v, want the divider", m.lines[3])
	}

	m.splitPane.SetListContent("", len(m.lines))
	m.splitPane.SetCursor(2) // row-a1

	for _, step := range []struct{ delta, want int }{
		{1, 4},  // header-b, skipping the divider
		{1, 6},  // row-b1, skipping the label
		{1, 6},  // boundary: unchanged
		{-1, 4}, // header-b, skipping the label
		{-1, 2}, // row-a1, skipping the divider
		{-1, 0}, // header-a, skipping the label
		{-1, 0}, // boundary: unchanged
	} {
		m.moveCursor(step.delta)
		if got := m.splitPane.Cursor(); got != step.want {
			t.Fatalf("moveCursor(%+d) = %d, want %d", step.delta, got, step.want)
		}
	}
}

// TestNextSelectable_NeverReturnsADivider is a narrower, table-driven check
// of the skip logic itself, independent of SplitPane's cursor state.
func TestNextSelectable_NeverReturnsADivider(t *testing.T) {
	m := &FleetModel{
		lines: []fleetLine{
			{kind: fleetLineHeader},
			{kind: fleetLineRow},
			{kind: fleetLineDivider},
			{kind: fleetLineHeader},
			{kind: fleetLineRow},
		},
	}
	tests := []struct {
		name   string
		cursor int
		delta  int
		want   int
	}{
		{"forward over the divider", 1, 1, 3},
		{"backward over the divider", 3, -1, 1},
		{"forward at the last line stays put", 4, 1, 4},
		{"backward at the first line stays put", 0, -1, 0},
		{"delta zero is a no-op", 2, 0, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.nextSelectable(tt.cursor, tt.delta)
			if got != tt.want {
				t.Errorf("nextSelectable(%d, %d) = %d, want %d", tt.cursor, tt.delta, got, tt.want)
			}
			// delta == 0 is a pure no-op (moveCursor never calls it that
			// way in practice) — it returns cursor unchanged even if
			// cursor itself started on a divider, so only the moving cases
			// need the "never lands on a divider" guarantee.
			if tt.delta != 0 && m.lines[got].kind == fleetLineDivider {
				t.Errorf("nextSelectable(%d, %d) returned a divider index %d", tt.cursor, tt.delta, got)
			}
		})
	}
}

func TestFleetLaunchEnv(t *testing.T) {
	g := fleet.Group{Session: "agentctl-proj", RuntimeDir: "/p/proj/.agentctl", ProjectDir: "/p/proj"}
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/h",
		"AGENTCTL_RUNTIME_DIR=/hub/.agentctl",
		"AGENTCTL_INSTANCE=hub",
		"AGENTCTL_SESSION_ID=abc",
		"AGENTCTL_WINDOW_ID=@9",
		"TMUX_PANE=%3",
		"TMUX=/tmp/tmux-1/default,1,0",
		"AGENTCTL_INSTANCE_NAME=keep-me",
	}
	got := fleetLaunchEnv(g, parent)
	want := []string{"PATH=/usr/bin", "HOME=/h", "TMUX=/tmp/tmux-1/default,1,0", "AGENTCTL_INSTANCE_NAME=keep-me", "AGENTCTL_RUNTIME_DIR=/p/proj/.agentctl"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("env = %v, want %v", got, want)
	}
	if len(fleetLaunchEnv(g, nil)) != 1 {
		t.Error("nil parent should yield only RUNTIME_DIR")
	}
}

func TestCloseConfirmStateMachine(t *testing.T) {
	m := mouseTestModel(t, 120, 40)
	m.selectLine(2) // row a (s1/@1)

	// start
	m.startClose(2)
	if !m.closeArmed() || m.confirmWindow != "@1" {
		t.Fatalf("start: armed=%v win=%q", m.closeArmed(), m.confirmWindow)
	}
	// start on a header does nothing
	m2 := mouseTestModel(t, 120, 40)
	m2.startClose(0)
	if m2.closeArmed() {
		t.Error("header must not arm close")
	}
	// moving selection away cancels
	m.moveCursor(1)
	if m.closeArmed() {
		t.Error("moving selection should cancel")
	}
	// key flow: x arms, n cancels, x y confirms
	m.selectLine(2)
	var model tea.Model = m
	model, _ = model.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if !model.(FleetModel).closeArmed() {
		t.Fatal("x should arm")
	}
	model, cmd := model.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if model.(FleetModel).closeArmed() || cmd != nil {
		t.Error("n should cancel without launching")
	}
	model, _ = model.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	model, cmd = model.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if model.(FleetModel).closeArmed() || cmd == nil {
		t.Error("y should confirm: disarm and return close cmd")
	}
	if !strings.Contains(model.(FleetModel).infoMsg, "closing a") {
		t.Errorf("info = %q", model.(FleetModel).infoMsg)
	}
}

func TestRowCanCloseAndGroupCanLaunch(t *testing.T) {
	mk := func(sess, win string) fleet.Row {
		r := testRow("x", sess, "idle", "")
		r.WindowID = win
		return r
	}
	if rowCanClose(mk(tmux.HubSessionName, "@1")) {
		t.Error("hub rows must never be closable")
	}
	if rowCanClose(mk("s", "shell")) {
		t.Error("non-@ window ids rejected")
	}
	if !rowCanClose(mk("s", "@2")) {
		t.Error("normal row closable")
	}
	if groupCanLaunch(fleet.Group{RuntimeDir: "/p/.agentctl"}) {
		t.Error("group without project dir must not launch")
	}
	if groupCanLaunch(fleet.Group{ProjectDir: "/p"}) {
		t.Error("group without runtime dir must not launch")
	}
}

func TestNKeyArmsSelectorThenAgentLaunches(t *testing.T) {
	m := buttonTestModel(t)
	m.selectLine(2) // row b in s1
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd != nil || next.(FleetModel).newSession != "s1" {
		t.Fatalf("n on a row should arm the selector; armed=%q", next.(FleetModel).newSession)
	}
	next, cmd = next.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil || !strings.Contains(next.(FleetModel).infoMsg, "s1") {
		t.Errorf("a should launch in its group; info=%q", next.(FleetModel).infoMsg)
	}
	m.selectLine(5) // row c in s2 (no dirs)
	next, cmd = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd != nil || next.(FleetModel).newArmed() {
		t.Error("n in a group without dirs must not arm")
	}
}
