package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/state"
)

// ── Narrow-terminal fallback threshold ───────────────────────────────────────

func TestUseSplitLayout(t *testing.T) {
	tests := []struct {
		width int
		want  bool
	}{
		{40, false},
		{79, false},
		{herdMinSplitWidth, true},
		{80, true},
		{120, true},
		{200, true},
	}
	for _, tt := range tests {
		got := useSplitLayout(tt.width)
		if got != tt.want {
			t.Errorf("useSplitLayout(%d) = %v, want %v", tt.width, got, tt.want)
		}
	}
}

// ── Sidebar line construction ────────────────────────────────────────────────

func testRow(name, session, state_, activity string) herd.Row {
	return herd.Row{
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

	m := HerdModel{}

	t.Run("wide width — no truncation of short content", func(t *testing.T) {
		short := testRow("go-engineer", "agentctl-myproj", "working", "Editing monitor.go")
		out := m.renderRow(short, false, false, 100)
		if lipgloss.Width(out) > 100 {
			t.Errorf("rendered width %d exceeds cw=100", lipgloss.Width(out))
		}
		if !strings.Contains(out, "go-engineer") {
			t.Errorf("expected full name in wide render, got %q", out)
		}
	})

	t.Run("narrow sidebar width — name and activity truncated", func(t *testing.T) {
		const cw = 24
		out := m.renderRow(r, false, false, cw)
		if got := lipgloss.Width(out); got > cw {
			t.Errorf("rendered row width = %d, want <= %d (row: %q)", got, cw, out)
		}
	})

	t.Run("selected row is padded to full width", func(t *testing.T) {
		const cw = 40
		out := m.renderRow(r, true, false, cw)
		if got := lipgloss.Width(out); got != cw {
			t.Errorf("selected row width = %d, want exactly %d", got, cw)
		}
	})

	t.Run("very narrow width does not panic", func(t *testing.T) {
		// Widths below this are narrower than any real sidebar can be
		// (herdMinSidebarWidth is enforced well above this), but the
		// function must still not panic or produce a negative-width
		// truncation call.
		for _, cw := range []int{0, 1, 5, 10} {
			out := m.renderRow(r, false, false, cw)
			_ = out // must not panic
		}
	})

	t.Run("realistic narrow sidebar widths stay within budget", func(t *testing.T) {
		for _, cw := range []int{16, 20, 24, 28} {
			out := m.renderRow(r, false, false, cw)
			if got := lipgloss.Width(out); got > cw {
				t.Errorf("cw=%d: rendered width %d exceeds cw (row: %q)", cw, got, out)
			}
		}
	})
}

func TestRenderGroupHeader_TruncatesAndMarksSelected(t *testing.T) {
	InitStyles()
	m := HerdModel{}

	g := herd.Group{
		Session: "agentctl-a-very-long-project-directory-name",
		Project: "a-very-long-project-directory-name",
		Rollup:  herd.RollupWorking,
		Rows:    []herd.Row{testRow("a", "s", "working", "")},
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

	t.Run("stale group shows the stale marker", func(t *testing.T) {
		// Short names here so the composed line comfortably fits cw=80 —
		// the truncation behaviour itself is covered separately above.
		short := herd.Group{
			Session: "agentctl-a",
			Project: "a",
			Rollup:  herd.RollupWorking,
			Rows:    []herd.Row{testRow("a", "s", "working", "")},
			Stale:   true,
		}
		out := m.renderGroupHeader(short, false, 80)
		if !strings.Contains(out, herdStaleMarker) {
			t.Errorf("expected stale marker in header, got %q", out)
		}
	})
}

func TestRebuildLines_HeadersRowsAndPlaceholders(t *testing.T) {
	m := &HerdModel{
		groups: []herd.Group{
			{
				Session: "agentctl-a",
				Project: "a",
				Rows: []herd.Row{
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

	// group a: header + 2 rows (3 lines); a divider (1 line) between the two
	// groups; group b: header + placeholder (2 lines, since it has zero
	// agents) = 6 total.
	if len(m.lines) != 6 {
		t.Fatalf("len(lines) = %d, want 6 (header+2 rows, divider, header+placeholder)", len(m.lines))
	}
	if m.lines[0].kind != herdLineHeader || m.lines[0].group.Session != "agentctl-a" {
		t.Errorf("lines[0] = %+v, want header for agentctl-a", m.lines[0])
	}
	if m.lines[1].kind != herdLineRow || m.lines[1].row.Name != "agent-1" {
		t.Errorf("lines[1] = %+v, want row agent-1", m.lines[1])
	}
	if m.lines[2].kind != herdLineRow || m.lines[2].row.Name != "agent-2" {
		t.Errorf("lines[2] = %+v, want row agent-2", m.lines[2])
	}
	if m.lines[3].kind != herdLineDivider {
		t.Errorf("lines[3] = %+v, want divider between agentctl-a and agentctl-b", m.lines[3])
	}
	if m.lines[4].kind != herdLineHeader || m.lines[4].group.Session != "agentctl-b" {
		t.Errorf("lines[4] = %+v, want header for agentctl-b", m.lines[4])
	}
	if m.lines[5].kind != herdLinePlaceholder {
		t.Errorf("lines[5] = %+v, want placeholder for agentctl-b (zero agents)", m.lines[5])
	}
}

// ── Divider construction ─────────────────────────────────────────────────────

func TestRebuildLines_DividerCounts(t *testing.T) {
	oneGroup := []herd.Group{
		{Session: "agentctl-a", Project: "a", Rows: []herd.Row{testRow("agent-1", "agentctl-a", "idle", "")}},
	}
	twoGroups := append(append([]herd.Group{}, oneGroup...), herd.Group{
		Session: "agentctl-b", Project: "b", Rows: []herd.Row{testRow("agent-2", "agentctl-b", "idle", "")},
	})
	threeGroups := append(append([]herd.Group{}, twoGroups...), herd.Group{
		Session: "agentctl-c", Project: "c", Rows: []herd.Row{testRow("agent-3", "agentctl-c", "idle", "")},
	})

	tests := []struct {
		name          string
		groups        []herd.Group
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
			m := &HerdModel{groups: tt.groups}
			m.rebuildLines()

			dividers := 0
			for _, l := range m.lines {
				if l.kind == herdLineDivider {
					dividers++
				}
			}
			if dividers != tt.wantDividers {
				t.Errorf("dividers = %d, want %d (lines: %+v)", dividers, tt.wantDividers, m.lines)
			}
			if tt.wantNoLeadTrl && len(m.lines) > 0 {
				if m.lines[0].kind == herdLineDivider {
					t.Errorf("first line must never be a divider: %+v", m.lines[0])
				}
				if m.lines[len(m.lines)-1].kind == herdLineDivider {
					t.Errorf("last line must never be a divider: %+v", m.lines[len(m.lines)-1])
				}
			}
		})
	}
}

func TestRenderHerdDivider_SpansExactWidth(t *testing.T) {
	InitStyles()
	for _, cw := range []int{0, 1, 10, 40, 80, 120} {
		out := renderHerdDivider(cw)
		if got := lipgloss.Width(out); got != cw {
			t.Errorf("renderHerdDivider(%d): width = %d, want %d", cw, got, cw)
		}
	}
}

// TestRenderHerdDivider_MatchesContentInset asserts the divider's left
// offset and right gutter match the sidebar content block it sits between —
// a group header's left inset and a content row's right gutter — so it
// reads as part of the sidebar instead of starting at column 0 and
// colliding with the detail panel's border in a T-junction. This is a
// regression test: it fails if the sidebar's padding constants
// (herdSidebarLeftInset / herdSidebarRightGutter) and renderHerdDivider ever
// drift apart.
func TestRenderHerdDivider_MatchesContentInset(t *testing.T) {
	InitStyles()

	for _, cw := range []int{40, 80, 120} {
		plainRunes := []rune(ansi.Strip(renderHerdDivider(cw)))

		if got := lipgloss.Width(string(plainRunes)); got != cw {
			t.Fatalf("renderHerdDivider(%d): plain width = %d, want %d", cw, got, cw)
		}

		leading := 0
		for leading < len(plainRunes) && plainRunes[leading] == ' ' {
			leading++
		}
		if leading != herdSidebarLeftInset {
			t.Errorf("renderHerdDivider(%d): left inset = %d, want %d (herdSidebarLeftInset)", cw, leading, herdSidebarLeftInset)
		}

		trailing := 0
		for trailing < len(plainRunes) && plainRunes[len(plainRunes)-1-trailing] == ' ' {
			trailing++
		}
		if trailing != herdSidebarRightGutter {
			t.Errorf("renderHerdDivider(%d): right gutter = %d, want %d (herdSidebarRightGutter)", cw, trailing, herdSidebarRightGutter)
		}

		if strings.ContainsRune(string(plainRunes), '│') {
			t.Errorf("renderHerdDivider(%d): must not draw the panel border itself", cw)
		}
	}
}

// TestRenderHerdDivider_NarrowFallback covers widths too small to reserve
// both the left inset and the right gutter — the divider must still span
// exactly cw columns rather than disappearing or overflowing.
func TestRenderHerdDivider_NarrowFallback(t *testing.T) {
	InitStyles()

	for _, cw := range []int{1, 2, 3} {
		out := renderHerdDivider(cw)
		if got := lipgloss.Width(out); got != cw {
			t.Errorf("renderHerdDivider(%d): width = %d, want %d", cw, got, cw)
		}
	}
}

func TestRebuildLines_EmptyGroupYieldsPlaceholder(t *testing.T) {
	m := &HerdModel{
		groups: []herd.Group{
			{Session: "agentctl-empty", Project: "empty", Rows: nil},
		},
	}
	m.rebuildLines()

	if len(m.lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2 (header + placeholder)", len(m.lines))
	}
	if m.lines[1].kind != herdLinePlaceholder {
		t.Errorf("lines[1].kind = %v, want herdLinePlaceholder", m.lines[1].kind)
	}
}

// ── Selected-row → main-panel content mapping ────────────────────────────────

func TestMainPanelContent_AgentSelected(t *testing.T) {
	InitStyles()

	row := herd.Row{
		Session: "agentctl-myproj",
		Project: "myproj",
		AgentState: state.AgentState{
			Name:           "go-engineer~2",
			WindowID:       "@5",
			State:          "working",
			ModelName:      "claude-sonnet-4-20250514",
			ContextPct:     42,
			InputTokens:    12345,
			OutputTokens:   678,
			Ticket:         99,
			LastToolDetail: "Editing monitor.go",
		},
	}
	lines := []herdLine{{kind: herdLineRow, row: row}}

	out := mainPanelContent(lines, 0, "$ some terminal output\n", "", 80)

	for _, want := range []string{"go-engineer~2", "State:", "working", "Model:", "claude-sonnet-4-20250514", "Context:", "42%", "Tokens:", "Ticket:", "#99", "Terminal", "some terminal output"} {
		if !strings.Contains(out, want) {
			t.Errorf("agent detail missing %q; got:\n%s", want, out)
		}
	}
}

func TestMainPanelContent_AgentSelected_PreviewError(t *testing.T) {
	InitStyles()

	row := herd.Row{
		Session:    "agentctl-myproj",
		Project:    "myproj",
		AgentState: state.AgentState{Name: "go-engineer", WindowID: "@5", State: "idle"},
	}
	lines := []herdLine{{kind: herdLineRow, row: row}}

	out := mainPanelContent(lines, 0, "", "capture-pane agentctl-myproj:@5: exit status 1", 80)
	if !strings.Contains(out, "preview unavailable") {
		t.Errorf("expected preview-unavailable message, got:\n%s", out)
	}
}

func TestMainPanelContent_AgentSelected_NoOutputYet(t *testing.T) {
	InitStyles()

	row := herd.Row{
		Session:    "agentctl-myproj",
		Project:    "myproj",
		AgentState: state.AgentState{Name: "go-engineer", WindowID: "@5", State: "idle"},
	}
	lines := []herdLine{{kind: herdLineRow, row: row}}

	out := mainPanelContent(lines, 0, "", "", 80)
	if !strings.Contains(out, "no output yet") {
		t.Errorf("expected no-output placeholder, got:\n%s", out)
	}
}

func TestMainPanelContent_GroupHeaderSelected(t *testing.T) {
	InitStyles()

	g := herd.Group{
		Session: "agentctl-myproj",
		Project: "myproj",
		Rollup:  herd.RollupWorking,
		Rows: []herd.Row{
			testRow("a", "agentctl-myproj", "blocked", ""),
			testRow("b", "agentctl-myproj", "working", ""),
			testRow("c", "agentctl-myproj", "idle", ""),
		},
	}
	lines := []herdLine{
		{kind: herdLineHeader, group: g},
		{kind: herdLineRow, row: g.Rows[0]},
		{kind: herdLineRow, row: g.Rows[1]},
		{kind: herdLineRow, row: g.Rows[2]},
	}

	out := mainPanelContent(lines, 0, "", "", 80)

	for _, want := range []string{"agentctl-myproj", "Project:", "myproj", "Agents:", "3", "Blocked:", "1", "Working:", "1", "Idle:", "1"} {
		if !strings.Contains(out, want) {
			t.Errorf("group summary missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Terminal") {
		t.Errorf("group summary should not include a Terminal preview section; got:\n%s", out)
	}
}

func TestMainPanelContent_PlaceholderSelected(t *testing.T) {
	InitStyles()

	g := herd.Group{Session: "agentctl-empty", Project: "empty", Rollup: herd.RollupIdle}
	lines := []herdLine{
		{kind: herdLineHeader, group: g},
		{kind: herdLinePlaceholder, group: g},
	}

	out := mainPanelContent(lines, 1, "", "", 80)
	if !strings.Contains(out, "Agents:") || !strings.Contains(out, "0") {
		t.Errorf("expected a zero-agent summary for the placeholder line, got:\n%s", out)
	}
}

func TestMainPanelContent_NothingSelected(t *testing.T) {
	InitStyles()

	tests := []struct {
		name   string
		lines  []herdLine
		cursor int
	}{
		{"no lines at all", nil, 0},
		{"cursor negative", []herdLine{{kind: herdLineRow}}, -1},
		{"cursor past the end", []herdLine{{kind: herdLineRow}}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := mainPanelContent(tt.lines, tt.cursor, "", "", 80)
			if !strings.Contains(out, "nothing selected") {
				t.Errorf("expected nothing-selected placeholder, got:\n%s", out)
			}
		})
	}
}

// ── stateBreakdown ────────────────────────────────────────────────────────────

func TestStateBreakdown(t *testing.T) {
	rows := []herd.Row{
		testRow("a", "s", "blocked", ""),
		testRow("b", "s", "working", ""),
		testRow("c", "s", "working", ""),
		testRow("d", "s", "idle", ""),
	}
	// One more row marked Done (unseen) via the embedded AgentState.
	done := testRow("e", "s", "idle", "")
	done.Done = true
	rows = append(rows, done)

	blocked, working, doneCount, idle := stateBreakdown(rows)
	if blocked != 1 || working != 2 || doneCount != 1 || idle != 1 {
		t.Errorf("stateBreakdown = (%d,%d,%d,%d), want (1,2,1,1)", blocked, working, doneCount, idle)
	}
}

func TestStateBreakdown_Empty(t *testing.T) {
	blocked, working, done, idle := stateBreakdown(nil)
	if blocked != 0 || working != 0 || done != 0 || idle != 0 {
		t.Errorf("stateBreakdown(nil) = (%d,%d,%d,%d), want all zero", blocked, working, done, idle)
	}
}

// ── Selection resolution survives reload (regression) ────────────────────────

func TestResolveSelection_PrefersFirstRowByDefault(t *testing.T) {
	m := &HerdModel{
		groups: []herd.Group{
			{Session: "agentctl-a", Project: "a", Rows: []herd.Row{testRow("agent-1", "agentctl-a", "idle", "")}},
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

	m := &HerdModel{
		groups: []herd.Group{
			{Session: "agentctl-a", Project: "a", Rows: []herd.Row{agent1, agent2}},
		},
	}
	m.rebuildLines()
	// Select "agent-1" explicitly (it's not blocked, so it won't be first
	// after a re-sort that floats blocked agents up).
	m.selSession, m.selWindowID, m.selIsHeader = "agentctl-a", "@5", false

	// Simulate a reload where the row order changed (e.g. agent-2 now
	// sorts first because it's blocked) — resolveSelection must still find
	// agent-1 by (session, windowID), not lose it to the reorder.
	m.groups[0].Rows = []herd.Row{agent2, agent1}
	m.rebuildLines()
	m.resolveSelection()

	got := m.selectedRow()
	if got == nil || got.Name != "agent-1" {
		t.Fatalf("selected row = %+v, want agent-1", got)
	}
}

func TestResolveSelection_HeaderSelectionSurvivesReload(t *testing.T) {
	m := &HerdModel{
		groups: []herd.Group{
			{Session: "agentctl-a", Project: "a", Rows: []herd.Row{testRow("agent-1", "agentctl-a", "idle", "")}},
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

	m := &HerdModel{
		groups: []herd.Group{
			{Session: "agentctl-a", Project: "a", Rows: []herd.Row{agentA}},
			{Session: "agentctl-b", Project: "b", Rows: []herd.Row{agentB}},
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
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != herdLineRow {
		t.Fatalf("cursor at %d landed on a non-row line: %+v", idx, m.lines)
	}
}

// ── Cursor movement skips dividers ───────────────────────────────────────────

// TestMoveCursor_SkipsDividers builds a two-group, one-row-each layout
// (header-a, row-a1, divider, header-b, row-b1 — indices 0..4) and drives
// the cursor across the divider in both directions, including from the
// boundary positions at either end of the list.
func TestMoveCursor_SkipsDividers(t *testing.T) {
	m := &HerdModel{
		groups: []herd.Group{
			{Session: "agentctl-a", Project: "a", Rows: []herd.Row{testRow("agent-a1", "agentctl-a", "idle", "")}},
			{Session: "agentctl-b", Project: "b", Rows: []herd.Row{testRow("agent-b1", "agentctl-b", "idle", "")}},
		},
	}
	m.rebuildLines()
	if len(m.lines) != 5 {
		t.Fatalf("setup: len(lines) = %d, want 5 (header,row,divider,header,row)", len(m.lines))
	}
	if m.lines[2].kind != herdLineDivider {
		t.Fatalf("setup: lines[2] = %+v, want the divider", m.lines[2])
	}

	m.splitPane.SetListContent("", len(m.lines))
	m.splitPane.SetCursor(1) // row-a1

	m.moveCursor(1)
	if got := m.splitPane.Cursor(); got != 3 {
		t.Fatalf("moveCursor(+1) from row-a1 (1) = %d, want 3 (header-b, skipping the divider at 2)", got)
	}

	m.moveCursor(1)
	if got := m.splitPane.Cursor(); got != 4 {
		t.Fatalf("moveCursor(+1) from header-b (3) = %d, want 4 (row-b1)", got)
	}

	// Boundary: moving past the last line must stay put, not panic or wrap.
	m.moveCursor(1)
	if got := m.splitPane.Cursor(); got != 4 {
		t.Fatalf("moveCursor(+1) at the last line = %d, want 4 (unchanged)", got)
	}

	m.moveCursor(-1)
	if got := m.splitPane.Cursor(); got != 3 {
		t.Fatalf("moveCursor(-1) from row-b1 (4) = %d, want 3 (header-b)", got)
	}

	m.moveCursor(-1)
	if got := m.splitPane.Cursor(); got != 1 {
		t.Fatalf("moveCursor(-1) from header-b (3) = %d, want 1 (row-a1, skipping the divider at 2)", got)
	}

	m.moveCursor(-1)
	if got := m.splitPane.Cursor(); got != 0 {
		t.Fatalf("moveCursor(-1) from row-a1 (1) = %d, want 0 (header-a)", got)
	}

	// Boundary: moving before the first line must stay put, not panic or
	// go negative.
	m.moveCursor(-1)
	if got := m.splitPane.Cursor(); got != 0 {
		t.Fatalf("moveCursor(-1) at the first line = %d, want 0 (unchanged)", got)
	}
}

// TestNextSelectable_NeverReturnsADivider is a narrower, table-driven check
// of the skip logic itself, independent of SplitPane's cursor state.
func TestNextSelectable_NeverReturnsADivider(t *testing.T) {
	m := &HerdModel{
		lines: []herdLine{
			{kind: herdLineHeader},
			{kind: herdLineRow},
			{kind: herdLineDivider},
			{kind: herdLineHeader},
			{kind: herdLineRow},
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
			if tt.delta != 0 && m.lines[got].kind == herdLineDivider {
				t.Errorf("nextSelectable(%d, %d) returned a divider index %d", tt.cursor, tt.delta, got)
			}
		})
	}
}

// ── Link state marking ───────────────────────────────────────────────────────

func TestRowIsLinked(t *testing.T) {
	linked := map[string]bool{"@5": true, "@9": true}

	tests := []struct {
		name     string
		windowID string
		linked   map[string]bool
		want     bool
	}{
		{"linked window", "@5", linked, true},
		{"another linked window", "@9", linked, true},
		{"window not in the set", "@7", linked, false},
		{"nil map", "@5", nil, false},
		{"empty map", "@5", map[string]bool{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testRow("agent", "agentctl-a", "idle", "")
			r.WindowID = tt.windowID
			if got := rowIsLinked(r, tt.linked); got != tt.want {
				t.Errorf("rowIsLinked(windowID=%q) = %v, want %v", tt.windowID, got, tt.want)
			}
		})
	}
}

func TestRenderRow_ShowsLinkedGlyphWhenLinked(t *testing.T) {
	InitStyles()

	r := testRow("go-engineer", "agentctl-myproj", "working", "")
	r.WindowID = "@5"

	linked := HerdModel{linkedWindowIDs: map[string]bool{"@5": true}}
	if out := linked.renderRow(r, false, false, 60); !strings.Contains(out, herdLinkedGlyph) {
		t.Errorf("expected linked glyph %q in row output, got %q", herdLinkedGlyph, out)
	}

	unlinked := HerdModel{linkedWindowIDs: map[string]bool{"@9": true}}
	if out := unlinked.renderRow(r, false, false, 60); strings.Contains(out, herdLinkedGlyph) {
		t.Errorf("expected no linked glyph for an unlinked row, got %q", out)
	}

	noData := HerdModel{}
	if out := noData.renderRow(r, false, false, 60); strings.Contains(out, herdLinkedGlyph) {
		t.Errorf("expected no linked glyph before the first tick populates linkedWindowIDs, got %q", out)
	}
}
