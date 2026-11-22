package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
)

// collapseGroups is the collapse fixture:
//
//	s1 alpha: p (idle) ├─ k (working) │ └─ gg (idle)
//	                   └─ b (blocked)
//	          x (idle, no children)
//	s2 beta:  d (idle)
func collapseGroups() []fleet.Group {
	g := func(sess, project string, rows ...fleet.Row) fleet.Group {
		return fleet.Group{Session: sess, Project: project, Rows: rows,
			ProjectDir: "/src/" + project, RuntimeDir: "/src/" + project + "/.agentctl"}
	}
	child := func(r fleet.Row) fleet.Row {
		r.Dispatched = true
		return r
	}
	return []fleet.Group{
		g("s1", "alpha",
			foldRow("s1", "alpha", "@1", "p", "idle", false, ""),
			child(foldRow("s1", "alpha", "@2", "k", "working", false, "@1")),
			child(foldRow("s1", "alpha", "@4", "gg", "idle", false, "@2")),
			child(foldRow("s1", "alpha", "@3", "b", "blocked", false, "@1")),
			foldRow("s1", "alpha", "@5", "x", "idle", false, "")),
		g("s2", "beta", foldRow("s2", "beta", "@6", "d", "idle", false, "")),
	}
}

func collapseModel(t *testing.T) FleetModel {
	t.Helper()
	InitStyles()
	fixedFleetNow(t)
	m := newFleetModel()
	m.termWidth, m.termHeight = 120, 40
	m.configureSplitPane()
	m.loaded = true
	m.groups = collapseGroups()
	m.rebuildLines()
	m.resolveSelection()
	return m
}

func TestCollapseToggle(t *testing.T) {
	tests := []struct {
		name      string
		cursor    string   // line the cursor is on when z is pressed
		collapsed string   // window expected collapsed ("" = none)
		folded    string   // session expected folded ("" = none)
		hidden    []string // row lines expected gone
	}{
		{"parent collapses its whole subtree", "R:p", "@1", "", []string{"R:k", "R:gg", "R:b"}},
		{"nested parent collapses its own subtree", "R:k", "@2", "", []string{"R:gg"}},
		{"header folds the group", "H:s1", "", "s1", []string{"R:p", "R:x"}},
		{"childless row folds the group", "R:x", "", "s1", []string{"R:p", "R:x"}},
		{"leaf child folds the group", "R:b", "", "s1", []string{"R:p", "R:b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := collapseModel(t)
			m.selectLine(lineIndex(t, m, tt.cursor))
			m, _ = pressHub(m, "z")
			if tt.collapsed != "" && !m.isCollapsed("s1", tt.collapsed) {
				t.Errorf("%s not collapsed (collapsed = %v)", tt.collapsed, m.collapsed)
			}
			if tt.collapsed == "" && len(m.collapsed) != 0 {
				t.Errorf("collapsed = %v, want none", m.collapsed)
			}
			if got := m.isFolded("s1"); got != (tt.folded == "s1") {
				t.Errorf("folded s1 = %v, want %v", got, tt.folded == "s1")
			}
			keys := lineKeys(m)
			for _, h := range tt.hidden {
				if slices.Contains(keys, h) {
					t.Errorf("%s still shown: %v", h, keys)
				}
			}
			if tt.collapsed != "" {
				// The cursor stays on the parent; z again expands.
				if r := m.selectedRow(); r == nil || r.WindowID != tt.collapsed {
					t.Errorf("selected = %+v, want %s", r, tt.collapsed)
				}
				m, _ = pressHub(m, "z")
				if m.isCollapsed("s1", tt.collapsed) {
					t.Error("second z should expand")
				}
				for _, h := range tt.hidden {
					lineIndex(t, m, h)
				}
			}
		})
	}
}

func TestCollapseMarkerAlignment(t *testing.T) {
	m := collapseModel(t)
	cw := m.listColWidth()
	plain := func(key string) string {
		return ansi.Strip(m.renderLine(lineIndex(t, m, key), cw))
	}
	p, x, b := plain("R:p"), plain("R:x"), plain("R:b")
	if !strings.Contains(p, fleetUnfoldedGlyph+" p") {
		t.Errorf("parent row lacks the %s marker: %q", fleetUnfoldedGlyph, p)
	}
	if strings.Contains(x, fleetUnfoldedGlyph) || strings.Contains(b, fleetUnfoldedGlyph) {
		t.Errorf("childless rows draw a marker: %q / %q", x, b)
	}
	// A childless root's name sits where the parent's marker does: no
	// column shift down the list.
	col := func(s, sub string) int { return lipgloss.Width(s[:strings.Index(s, sub)]) }
	if col(p, fleetUnfoldedGlyph) != col(x, "x ") {
		t.Errorf("marker at %d, childless name at %d", col(p, fleetUnfoldedGlyph), col(x, "x "))
	}
	// Titles still line up.
	if col(p, "no prompt") != col(x, "no prompt") || col(p, "no prompt") != col(b, "no prompt") {
		t.Errorf("titles misaligned:\n%q\n%q\n%q", p, x, b)
	}

	m.selectLine(lineIndex(t, m, "R:p"))
	m, _ = pressHub(m, "z")
	if p := plain("R:p"); !strings.Contains(p, fleetFoldedGlyph+" p") {
		t.Errorf("collapsed parent lacks the %s marker: %q", fleetFoldedGlyph, p)
	}
}

func TestCollapsedSummaryKeepsBlocked(t *testing.T) {
	for _, width := range []int{44, 56, 80, 120} {
		t.Run(fmt.Sprintf("cw%d", width), func(t *testing.T) {
			m := collapseModel(t)
			m.termWidth, m.liveLeft = width, width
			m.groups[0].Rows[0].Recap = fleet.RecapLine{Text: "Reworking the hub sub-agent rows", Source: fleet.RecapTitle}
			m.setCollapsed("s1", "@1", true)
			m.relayout()
			cw := m.listColWidth()
			out := m.renderLine(lineIndex(t, m, "R:p"), cw)
			if w := lipgloss.Width(out); w > cw {
				t.Errorf("width %d > cw %d", w, cw)
			}
			if !strings.Contains(out, fleetBlockedStyle.Render("⏸1")) {
				t.Errorf("collapsed parent must show its blocked child: %q", ansi.Strip(out))
			}
			if !strings.Contains(out, fleetWorkingStyle.Render("●1")) {
				t.Errorf("collapsed parent should show its working child: %q", ansi.Strip(out))
			}
			if width == 120 && !strings.Contains(ansi.Strip(out), "+3 ⏸1 ●1") {
				t.Errorf("wide summary = %q, want +3 ⏸1 ●1", ansi.Strip(out))
			}
		})
	}
}

func TestSubtreeSummary(t *testing.T) {
	InitStyles()
	c := fleetSubtreeCounts{total: 4, blocked: 1, working: 2, done: 1}
	tests := []struct {
		w    int
		want string
	}{
		{40, "+4 ⏸1 ●2 ✓1"},
		{10, "+4 ⏸1 ●2"},
		{6, "⏸1 ●2"},
		{3, "⏸1"},
		{0, ""},
	}
	for _, tt := range tests {
		if got := ansi.Strip(subtreeSummary(c, tt.w)); got != tt.want {
			t.Errorf("subtreeSummary(w=%d) = %q, want %q", tt.w, got, tt.want)
		}
	}
	if got := ansi.Strip(subtreeSummary(fleetSubtreeCounts{total: 2, idle: 2}, 10)); got != "+2" {
		t.Errorf("idle-only summary = %q, want +2", got)
	}
}

func TestCollapseMovesCursorToParent(t *testing.T) {
	m := collapseModel(t)
	m.selectLine(lineIndex(t, m, "R:gg"))
	m.toggleCollapseAt(lineIndex(t, m, "R:p"))
	if r := m.selectedRow(); r == nil || r.WindowID != "@1" {
		t.Errorf("selected = %+v, want the parent @1", r)
	}
	// A reload keeps it there.
	m.rebuildLines()
	m.resolveSelection()
	if r := m.selectedRow(); r == nil || r.WindowID != "@1" {
		t.Errorf("after reload selected = %+v, want @1", r)
	}
}

func TestCollapseSkippedByNavigation(t *testing.T) {
	m := collapseModel(t)
	m.setCollapsed("s1", "@1", true)
	m.relayout()
	m.selectLine(lineIndex(t, m, "R:p"))
	m, _ = pressHub(m, "j")
	if r := m.selectedRow(); r == nil || r.WindowID != "@5" {
		t.Errorf("j from collapsed parent = %+v, want x (@5)", r)
	}
}

func TestRevealRowExpandsAncestors(t *testing.T) {
	m := collapseModel(t)
	m.setCollapsed("s1", "@1", true)
	m.setCollapsed("s1", "@2", true)
	m.relayout()
	if !m.revealRow("s1", "@4") {
		t.Fatal("revealRow reported no change")
	}
	if m.isCollapsed("s1", "@1") || m.isCollapsed("s1", "@2") {
		t.Errorf("collapsed ancestors left: %v", m.collapsed)
	}
	lineIndex(t, m, "R:gg")
	if !m.viewDirty {
		t.Error("the expansion should be persisted (viewDirty)")
	}
}

func TestJumpToBlockedExpands(t *testing.T) {
	old := focusLivePane
	focusLivePane = func() error { return nil }
	t.Cleanup(func() { focusLivePane = old })

	m := collapseModel(t)
	m.setCollapsed("s1", "@1", true)
	m.relayout()
	m, _ = pressHub(m, "b")
	if m.isCollapsed("s1", "@1") {
		t.Error("b should expand the blocked row's parent")
	}
	if r := m.selectedRow(); r == nil || r.WindowID != "@3" {
		t.Errorf("selected = %+v, want blocked @3", r)
	}
}

// lineY is the screen row line idx starts on.
func lineY(m FleetModel, idx int) int {
	y := m.fleetListTop()
	for i := m.scrollOffset; i < idx; i++ {
		y += m.lineHeight(i, m.listColWidth())
	}
	return y
}

func TestClickMarkerTogglesCollapse(t *testing.T) {
	m := collapseModel(t)
	m.selectLine(lineIndex(t, m, "R:x"))
	idx := lineIndex(t, m, "R:p")
	lay := m.rowLayout(m.lines[idx], false, fleetHoverBody, m.listColWidth())
	if lay.marker[1] == 0 {
		t.Fatal("parent row has no marker span")
	}
	plain := ansi.Strip(m.renderLine(idx, m.listColWidth()))
	if got := string([]rune(plain)[lay.marker[0]:lay.marker[1]]); got != fleetUnfoldedGlyph {
		t.Errorf("marker span covers %q", got)
	}
	y := lineY(m, idx)
	if h := m.hoverAt(lay.marker[0], y); h.reg != fleetHoverCollapse {
		t.Errorf("hover on marker = %v, want collapse", h.reg)
	}
	if _, act := m.clickDecision(lay.marker[0], y); act != fleetClickCollapse {
		t.Fatalf("click on marker = %v, want collapse", act)
	}
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: lay.marker[0], Y: y})
	m = next.(FleetModel)
	if !m.isCollapsed("s1", "@1") {
		t.Fatal("click on the marker should collapse")
	}
	if r := m.selectedRow(); r == nil || r.WindowID != "@1" {
		t.Errorf("selected = %+v, want the clicked parent", r)
	}
	// A click on the name past the marker just selects.
	if _, act := m.clickDecision(lay.marker[1]+3, lineY(m, lineIndex(t, m, "R:p"))); act != fleetClickSelect {
		t.Errorf("click past the marker = %v, want select", act)
	}
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: lay.marker[0], Y: lineY(m, lineIndex(t, m, "R:p"))})
	if next.(FleetModel).isCollapsed("s1", "@1") {
		t.Error("second click should expand")
	}
}

func TestCollapseViewState(t *testing.T) {
	t.Run("hot reload round trip", func(t *testing.T) {
		m := collapseModel(t)
		m.setCollapsed("s1", "@1", true)
		var val string
		for _, e := range m.ReexecEnv() {
			if v, ok := strings.CutPrefix(e, HubViewEnv+"="); ok {
				val = v
			}
		}
		if !strings.Contains(val, `"collapsed":["s1|@1"]`) {
			t.Fatalf("%s = %s, want the collapsed set", HubViewEnv, val)
		}
		withHubViewPath(t, filepath.Join(t.TempDir(), hubViewFile))
		t.Setenv(HubViewEnv, val)
		got := NewFleetModel()
		if !got.isCollapsed("s1", "@1") || len(got.collapsed) != 1 {
			t.Errorf("round trip collapsed = %v", got.collapsed)
		}
	})

	t.Run("file round trip", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), hubViewFile)
		m := collapseModel(t)
		m.viewPath = path
		m.selectLine(lineIndex(t, m, "R:p"))
		m, cmd := pressHub(m, "z")
		if cmd == nil {
			t.Fatal("collapse should return a save cmd")
		}
		cmd()
		if got := readHubView(path).Collapsed; !slices.Equal(got, []string{"s1|@1"}) {
			t.Errorf("saved collapsed = %v", got)
		}
		withHubViewPath(t, path)
		if got := NewFleetModel(); !got.isCollapsed("s1", "@1") {
			t.Errorf("restart lost the collapse: %v", got.collapsed)
		}
	})

	t.Run("prunes vanished windows only with a loaded list", func(t *testing.T) {
		m := collapseModel(t)
		m.setCollapsed("s1", "@1", true)
		m.setCollapsed("s1", "@99", true)
		m.setCollapsed("gone", "@1", true)
		if got := m.viewState().Collapsed; !slices.Equal(got, []string{"s1|@1"}) {
			t.Errorf("pruned collapsed = %v, want [s1|@1]", got)
		}
		if !m.isCollapsed("s1", "@99") {
			t.Error("pruning is for the saved state only")
		}
		empty := newFleetModel()
		empty.setCollapsed("s1", "@99", true)
		if got := empty.viewState().Collapsed; !slices.Equal(got, []string{"s1|@99"}) {
			t.Errorf("unloaded hub pruned: %v", got)
		}
	})

	t.Run("bad keys ignored", func(t *testing.T) {
		var m FleetModel
		m.applyViewState(hubViewState{Collapsed: []string{"nobar", "|@1", "s|", "s|@2"}})
		if len(m.collapsed) != 1 || !m.isCollapsed("s", "@2") {
			t.Errorf("collapsed = %v, want only s|@2", m.collapsed)
		}
	})
}
