package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
)

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

func TestHerdLaunchEnv(t *testing.T) {
	g := herd.Group{Session: "agentctl-proj", RuntimeDir: "/p/proj/.agentctl", ProjectDir: "/p/proj"}
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
	got := herdLaunchEnv(g, parent)
	want := []string{"PATH=/usr/bin", "HOME=/h", "TMUX=/tmp/tmux-1/default,1,0", "AGENTCTL_INSTANCE_NAME=keep-me", "AGENTCTL_RUNTIME_DIR=/p/proj/.agentctl"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("env = %v, want %v", got, want)
	}
	if len(herdLaunchEnv(g, nil)) != 1 {
		t.Error("nil parent should yield only RUNTIME_DIR")
	}
}

func TestCloseConfirmStateMachine(t *testing.T) {
	m := mouseTestModel(t, 120, 40)
	m.selectLine(1) // row a (s1/@1)

	// start
	m.startClose(1)
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
	m.selectLine(1)
	var model tea.Model = m
	model, _ = model.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if !model.(HerdModel).closeArmed() {
		t.Fatal("x should arm")
	}
	model, cmd := model.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if model.(HerdModel).closeArmed() || cmd != nil {
		t.Error("n should cancel without launching")
	}
	model, _ = model.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	model, cmd = model.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if model.(HerdModel).closeArmed() || cmd == nil {
		t.Error("y should confirm: disarm and return close cmd")
	}
	if !strings.Contains(model.(HerdModel).infoMsg, "closing a") {
		t.Errorf("info = %q", model.(HerdModel).infoMsg)
	}
}

func TestRowCanCloseAndGroupCanLaunch(t *testing.T) {
	mk := func(sess, win string) herd.Row {
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
	if groupCanLaunch(herd.Group{Stale: true, ProjectDir: "/p", RuntimeDir: "/p/.agentctl"}) {
		t.Error("stale group must not launch")
	}
	if groupCanLaunch(herd.Group{ProjectDir: "/p"}) {
		t.Error("group without runtime dir must not launch")
	}
}

func TestNKeyLaunchesInSelectedGroup(t *testing.T) {
	m := buttonTestModel(t)
	m.selectLine(2) // row b in s1
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd == nil || !strings.Contains(next.(HerdModel).infoMsg, "s1") {
		t.Errorf("n on a row should launch in its group; info=%q", next.(HerdModel).infoMsg)
	}
	m.selectLine(5) // row c in stale s2
	next, cmd = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd != nil || next.(HerdModel).jumpErr == "" {
		t.Error("n in stale group should error without launching")
	}
}
