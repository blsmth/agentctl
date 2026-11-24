package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
)

// foldRow builds a row for the fold fixture.
func foldRow(sess, project, win, name, st string, done bool, parent string) fleet.Row {
	return fleet.Row{
		Session: sess,
		Project: project,
		Parent:  parent,
		AgentState: state.AgentState{
			Name: name, AgentName: name, WindowID: win, State: st, Done: done,
		},
	}
}

// foldGroups is the fold/filter fixture:
//
//	s1 alpha: p (idle) with child k (working), x (idle)
//	s2 beta:  d (done), e (idle)
//	s3 gamma: g (blocked)
//	s4 delta: no agents
func foldGroups() []fleet.Group {
	g := func(sess, project string, rows ...fleet.Row) fleet.Group {
		return fleet.Group{Session: sess, Project: project, Rows: rows,
			ProjectDir: "/src/" + project, RuntimeDir: "/src/" + project + "/.agentctl"}
	}
	return []fleet.Group{
		g("s1", "alpha",
			foldRow("s1", "alpha", "@1", "p", "idle", false, ""),
			foldRow("s1", "alpha", "@2", "k", "working", false, "@1"),
			foldRow("s1", "alpha", "@3", "x", "idle", false, "")),
		g("s2", "beta",
			foldRow("s2", "beta", "@4", "d", "idle", true, ""),
			foldRow("s2", "beta", "@5", "e", "idle", false, "")),
		g("s3", "gamma", foldRow("s3", "gamma", "@6", "g", "blocked", false, "")),
		g("s4", "delta"),
	}
}

// foldModel is a loaded hub over foldGroups.
func foldModel(t *testing.T) FleetModel {
	t.Helper()
	InitStyles()
	m := newFleetModel()
	m.termWidth, m.termHeight = 120, 40
	m.configureSplitPane()
	m.loaded = true
	m.groups = foldGroups()
	m.rebuildLines()
	m.resolveSelection()
	return m
}

// lineKeys describes m.lines compactly: "H:s1" header, "R:k" row ("R:p~"
// when dimmed), "P:s4" placeholder, "S" sub-section label, "-" divider, "_" group pad.
func lineKeys(m FleetModel) []string {
	var out []string
	for _, l := range m.lines {
		switch l.kind {
		case fleetLineHeader:
			out = append(out, "H:"+l.group.Session)
		case fleetLinePlaceholder:
			out = append(out, "P:"+l.group.Session)
		case fleetLineRow:
			k := "R:" + l.row.Name
			if l.dimmed {
				k += "~"
			}
			out = append(out, k)
		case fleetLineSubheader:
			out = append(out, "S")
		case fleetLineDivider:
			out = append(out, "-")
		case fleetLineGroupPad:
			out = append(out, "_")
		}
	}
	return out
}

// lineIndex finds the line with key (see lineKeys).
func lineIndex(t *testing.T, m FleetModel, key string) int {
	t.Helper()
	i := slices.Index(lineKeys(m), key)
	if i < 0 {
		t.Fatalf("no line %q in %v", key, lineKeys(m))
	}
	return i
}

func pressHub(m FleetModel, key string) (FleetModel, tea.Cmd) {
	code := []rune(key)[0]
	next, cmd := m.Update(tea.KeyPressMsg{Code: code, Text: key})
	return next.(FleetModel), cmd
}

func TestFoldToggle(t *testing.T) {
	tests := []struct {
		name    string
		cursor  string // line key the cursor starts on
		session string // group expected to fold
	}{
		{"on header", "H:s1", "s1"},
		// A root with children collapses instead (TestCollapseToggle).
		{"on childless root row", "R:x", "s1"},
		{"on child row", "R:k", "s1"},
		{"on other group's row", "R:g", "s3"},
		{"on empty group's placeholder", "P:s4", "s4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := foldModel(t)
			m.selectLine(lineIndex(t, m, tt.cursor))
			m, _ = pressHub(m, "z")
			if !m.isFolded(tt.session) {
				t.Fatalf("%s not folded", tt.session)
			}
			head := lineIndex(t, m, "H:"+tt.session)
			if cursorOf(m) != head || !m.selIsHeader || m.selSession != tt.session {
				t.Errorf("cursor = %d (%s header=%v), want header %d", cursorOf(m), m.selSession, m.selIsHeader, head)
			}
			for _, l := range m.lines {
				if (l.kind == fleetLineRow && l.row.Session == tt.session) || (l.kind == fleetLinePlaceholder && l.group.Session == tt.session) {
					t.Errorf("folded group still shows %v", lineKeys(m))
					break
				}
			}
			m, _ = pressHub(m, "z")
			if m.isFolded(tt.session) {
				t.Fatal("second z should unfold")
			}
			if got, want := lineKeys(m), lineKeys(foldModel(t)); !slices.Equal(got, want) {
				t.Errorf("unfolded lines = %v, want %v", got, want)
			}
			if cursorOf(m) != lineIndex(t, m, "H:"+tt.session) {
				t.Errorf("unfold should leave the cursor on the header, at %d", cursorOf(m))
			}
		})
	}
}

func TestFocusMode(t *testing.T) {
	m := foldModel(t)
	m.selectLine(lineIndex(t, m, "R:d")) // a row in a group focus mode folds
	m, _ = pressHub(m, "Z")
	var folded []string
	for s := range m.folded {
		folded = append(folded, s)
	}
	slices.Sort(folded)
	// s1 has a working agent, s3 a blocked one; s2 (done+idle) and s4
	// (empty) are quiet.
	if want := []string{"s2", "s4"}; !slices.Equal(folded, want) {
		t.Errorf("focus mode folded %v, want %v", folded, want)
	}
	if !m.focusMode {
		t.Error("focusMode not set")
	}
	if cursorOf(m) != lineIndex(t, m, "H:s2") {
		t.Errorf("cursor on a now-hidden row should land on its header, at %d", cursorOf(m))
	}
	want := []string{"H:s1", "R:p", "R:k", "R:x", "-", "H:s2", "-", "H:s3", "R:g", "_", "_", "-", "H:s4"}
	if got := lineKeys(m); !slices.Equal(got, want) {
		t.Errorf("lines = %v, want %v", got, want)
	}
	m, _ = pressHub(m, "Z")
	if len(m.folded) != 0 || m.focusMode {
		t.Errorf("second Z should unfold all: folded=%v focus=%v", m.folded, m.focusMode)
	}
}

func TestFoldSurvivesReload(t *testing.T) {
	m := foldModel(t)
	m.selectLine(lineIndex(t, m, "R:e"))
	m, _ = pressHub(m, "z")
	m.filter = fleetFilterNeeds
	m.relayout()
	before := lineKeys(m)
	next, _ := m.Update(fleetLoadedMsg{groups: foldGroups()})
	m = next.(FleetModel)
	if !m.isFolded("s2") || m.filter != fleetFilterNeeds {
		t.Fatalf("reload lost view state: folded=%v filter=%v", m.folded, m.filter)
	}
	if got := lineKeys(m); !slices.Equal(got, before) {
		t.Errorf("lines after reload = %v, want %v", got, before)
	}
	if !m.selIsHeader || m.selSession != "s2" {
		t.Errorf("selection after reload = %s header=%v, want s2 header", m.selSession, m.selIsHeader)
	}
	// A reload where the selected row is still there but filtered out keeps
	// the cursor on its group's header rather than jumping to the top.
	m.filter = fleetFilterAll
	m.rebuildLines()
	m.selectLine(lineIndex(t, m, "R:x"))
	m.filter = fleetFilterNeeds
	next, _ = m.Update(fleetLoadedMsg{groups: foldGroups()})
	m = next.(FleetModel)
	if !m.selIsHeader || m.selSession != "s1" {
		t.Errorf("filtered-out selection = %s header=%v, want s1 header", m.selSession, m.selIsHeader)
	}
}

func TestHubViewReexecRoundTrip(t *testing.T) {
	m := foldModel(t)
	m.setFolded("s2", true)
	m.setFolded("s4", true)
	m.focusMode = true
	m.filter = fleetFilterActive
	var val string
	for _, e := range m.ReexecEnv() {
		if v, ok := strings.CutPrefix(e, HubViewEnv+"="); ok {
			val = v
		}
	}
	if val == "" {
		t.Fatalf("ReexecEnv %v lacks %s", m.ReexecEnv(), HubViewEnv)
	}

	// The env wins over the state file.
	path := filepath.Join(t.TempDir(), hubViewFile)
	if err := writeHubView(path, hubViewState{Folded: []string{"s1"}}); err != nil {
		t.Fatal(err)
	}
	withHubViewPath(t, path)
	t.Setenv(HubViewEnv, val)
	got := NewFleetModel()
	if !got.isFolded("s2") || !got.isFolded("s4") || got.isFolded("s1") || !got.focusMode || got.filter != fleetFilterActive {
		t.Errorf("round trip = folded %v focus %v filter %v", got.folded, got.focusMode, got.filter)
	}
	if _, ok := os.LookupEnv(HubViewEnv); ok {
		t.Error("env var not consumed")
	}
	if got.viewPath != path {
		t.Errorf("viewPath = %q, want %q", got.viewPath, path)
	}
}

// withHubViewPath points the state file seam at path for one test.
func withHubViewPath(t *testing.T, path string) {
	t.Helper()
	old := hubViewPath
	hubViewPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { hubViewPath = old })
}

func TestHubViewFile(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name  string
		setup func(path string)
		want  hubViewState
	}{
		{"missing", func(string) {}, hubViewState{}},
		{"corrupt", func(p string) { _ = os.WriteFile(p, []byte("{not json"), 0o600) }, hubViewState{}},
		{"wrong shape", func(p string) { _ = os.WriteFile(p, []byte(`{"folded": 3}`), 0o600) }, hubViewState{}},
		{"valid", func(p string) {
			_ = os.WriteFile(p, []byte(`{"folded":["a","b"],"focus":true,"filter":"needs"}`), 0o600)
		}, hubViewState{Folded: []string{"a", "b"}, Focus: true, Filter: "needs"}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, string(rune('a'+i)), hubViewFile)
			_ = os.MkdirAll(filepath.Dir(path), 0o700)
			tt.setup(path)
			got := readHubView(path)
			if !slices.Equal(got.Folded, tt.want.Folded) || got.Focus != tt.want.Focus || got.Filter != tt.want.Filter {
				t.Errorf("readHubView = %+v, want %+v", got, tt.want)
			}
			// NewFleetModel never errors on it either.
			withHubViewPath(t, path)
			m := NewFleetModel()
			if len(m.folded) != len(tt.want.Folded) {
				t.Errorf("model folded = %v", m.folded)
			}
		})
	}

	t.Run("write round trip is 0600 in a new dir", func(t *testing.T) {
		path := filepath.Join(dir, "new", "sub", hubViewFile)
		want := hubViewState{Folded: []string{"s2"}, Filter: "active"}
		if err := writeHubView(path, want); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("perm = %o, want 600", perm)
		}
		if got := readHubView(path); !slices.Equal(got.Folded, want.Folded) || got.Filter != want.Filter {
			t.Errorf("read back %+v, want %+v", got, want)
		}
		entries, _ := os.ReadDir(filepath.Dir(path))
		if len(entries) != 1 {
			t.Errorf("temp files left behind: %v", entries)
		}
	})

	t.Run("toggle saves and drops vanished groups lazily", func(t *testing.T) {
		path := filepath.Join(dir, "save", hubViewFile)
		m := foldModel(t)
		m.viewPath = path
		m.setFolded("gone-session", true) // a group that no longer exists
		m.selectLine(lineIndex(t, m, "H:s3"))
		m, cmd := pressHub(m, "z")
		if cmd == nil {
			t.Fatal("z should return a save cmd")
		}
		cmd()
		got := readHubView(path)
		if !slices.Equal(got.Folded, []string{"s3"}) {
			t.Errorf("saved folded = %v, want [s3]", got.Folded)
		}
		if !m.isFolded("gone-session") {
			t.Error("pruning is for the file only; never error or drop live state")
		}
		m, cmd = pressHub(m, "f")
		cmd()
		if got := readHubView(path); got.Filter != "active" {
			t.Errorf("saved filter = %q, want active", got.Filter)
		}
	})

	t.Run("no prune before the first load", func(t *testing.T) {
		m := newFleetModel()
		m.setFolded("s9", true)
		if got := m.viewState().Folded; !slices.Equal(got, []string{"s9"}) {
			t.Errorf("unloaded hub pruned folds: %v", got)
		}
	})
}

func TestJumpToBlockedUnfolds(t *testing.T) {
	old := focusLivePane
	focusLivePane = func() error { return nil }
	t.Cleanup(func() { focusLivePane = old })

	m := foldModel(t)
	m.selectLine(lineIndex(t, m, "H:s3"))
	m, _ = pressHub(m, "z")
	m.selectLine(0)
	if !strings.Contains(ansi.Strip(m.renderLine(lineIndex(t, m, "H:s3"), 120)), "⏸") {
		t.Error("folded group must still show the ⏸ flag")
	}
	m, _ = pressHub(m, "b")
	if m.isFolded("s3") {
		t.Fatal("b should unfold the target group")
	}
	if r := m.selectedRow(); r == nil || r.WindowID != "@6" {
		t.Errorf("selected = %+v, want blocked row @6", r)
	}
	if m.jumpErr != "" {
		t.Errorf("jumpErr = %q", m.jumpErr)
	}
}

func TestPendingFocusReveals(t *testing.T) {
	t.Run("folded group unfolds", func(t *testing.T) {
		m := foldModel(t)
		m.setFolded("s2", true)
		m.rebuildLines()
		m.pendingFocus = &fleetPendingFocus{session: "s2", windowID: "@5"}
		next, _ := m.Update(fleetLoadedMsg{groups: foldGroups()})
		m = next.(FleetModel)
		if m.isFolded("s2") {
			t.Error("pending focus should unfold its group")
		}
		if r := m.selectedRow(); r == nil || r.WindowID != "@5" {
			t.Errorf("selected = %+v, want @5", r)
		}
	})
	t.Run("filtered row clears the filter", func(t *testing.T) {
		m := foldModel(t)
		m.filter = fleetFilterNeeds
		m.rebuildLines()
		m.pendingFocus = &fleetPendingFocus{session: "s1", windowID: "@3"}
		next, _ := m.Update(fleetLoadedMsg{groups: foldGroups()})
		m = next.(FleetModel)
		if m.filter != fleetFilterAll {
			t.Errorf("filter = %v, want all", m.filter)
		}
		if r := m.selectedRow(); r == nil || r.WindowID != "@3" {
			t.Errorf("selected = %+v, want @3", r)
		}
	})
}

func TestFilterModes(t *testing.T) {
	tests := []struct {
		filter fleetFilter
		want   []string
	}{
		{fleetFilterAll, []string{"H:s1", "R:p", "R:k", "R:x", "-", "H:s2", "R:d", "R:e", "_", "-", "H:s3", "R:g", "_", "_", "-", "H:s4", "P:s4"}},
		// p is idle but kept, dimmed, as k's parent; s2 has nothing active.
		{fleetFilterActive, []string{"H:s1", "R:p~", "R:k", "_", "-", "H:s2", "-", "H:s3", "R:g", "_", "_", "-", "H:s4"}},
		// Nothing in s1 needs you: header only, and no divider between two
		// header-only groups.
		{fleetFilterNeeds, []string{"H:s1", "-", "H:s2", "R:d", "_", "_", "-", "H:s3", "R:g", "_", "_", "-", "H:s4"}},
	}
	for _, tt := range tests {
		t.Run(tt.filter.label(), func(t *testing.T) {
			m := foldModel(t)
			m.filter = tt.filter
			m.relayout()
			if got := lineKeys(m); !slices.Equal(got, tt.want) {
				t.Errorf("lines = %v\nwant    %v", got, tt.want)
			}
		})
	}
}

func TestFilterCycle(t *testing.T) {
	m := foldModel(t)
	want := []fleetFilter{fleetFilterActive, fleetFilterNeeds, fleetFilterAll}
	for _, w := range want {
		m, _ = pressHub(m, "f")
		if m.filter != w {
			t.Fatalf("filter = %v, want %v", m.filter, w)
		}
	}
}

func TestFilterRowsAncestors(t *testing.T) {
	rows := []fleet.Row{
		foldRow("s", "p", "@1", "root", "idle", false, ""),
		foldRow("s", "p", "@2", "mid", "idle", false, "@1"),
		foldRow("s", "p", "@3", "leaf", "blocked", false, "@2"),
		foldRow("s", "p", "@4", "other", "idle", false, ""),
		// A parent cycle must terminate.
		foldRow("s", "p", "@5", "c1", "working", false, "@6"),
		foldRow("s", "p", "@6", "c2", "idle", false, "@5"),
	}
	kept, ctx := filterRows(rows, fleetFilterActive)
	var names []string
	for _, r := range kept {
		names = append(names, r.Name)
	}
	if want := []string{"root", "mid", "leaf", "c1", "c2"}; !slices.Equal(names, want) {
		t.Errorf("kept = %v, want %v", names, want)
	}
	if !ctx["@1"] || !ctx["@2"] || ctx["@3"] || !ctx["@6"] || ctx["@5"] {
		t.Errorf("context = %v, want @1 @2 @6", ctx)
	}
}

func TestFoldAndFilterCompose(t *testing.T) {
	m := foldModel(t)
	m.selectLine(lineIndex(t, m, "H:s3"))
	m, _ = pressHub(m, "z")
	for _, f := range []fleetFilter{fleetFilterActive, fleetFilterNeeds, fleetFilterAll} {
		m, _ = pressHub(m, "f")
		if m.filter != f {
			t.Fatalf("filter = %v, want %v", m.filter, f)
		}
		if !m.isFolded("s3") {
			t.Fatalf("filter %v unfolded s3", f)
		}
		if slices.Contains(lineKeys(m), "R:g") {
			t.Errorf("filter %v shows a folded group's row: %v", f, lineKeys(m))
		}
	}
	m.filter = fleetFilterNeeds
	m.relayout()
	want := []string{"H:s1", "-", "H:s2", "R:d", "_", "_", "-", "H:s3", "H:s4"}
	if got := lineKeys(m); !slices.Equal(got, want) {
		t.Errorf("needs you + folded s3 = %v, want %v", got, want)
	}
}

func TestNavigationSkipsHiddenRows(t *testing.T) {
	m := foldModel(t)
	m.setFolded("s2", true)
	m.relayout()
	m.selectLine(lineIndex(t, m, "R:x"))
	var path []string
	for range 4 {
		m, _ = pressHub(m, "j")
		path = append(path, lineKeys(m)[cursorOf(m)])
	}
	if want := []string{"H:s2", "H:s3", "R:g", "H:s4"}; !slices.Equal(path, want) {
		t.Errorf("j path = %v, want %v", path, want)
	}
	for range 3 {
		m, _ = pressHub(m, "k")
	}
	if got := lineKeys(m)[cursorOf(m)]; got != "H:s2" {
		t.Errorf("k back = %s, want H:s2", got)
	}
}

func TestOverflowCountsWithHiddenRows(t *testing.T) {
	m := foldModel(t)
	m.setFolded("s1", true)
	m.setFolded("s2", true)
	m.relayout()
	// H:s1 H:s2 - H:s3 R:g - H:s4 P:s4; three visible list rows.
	m.termHeight = fleetChromeRows + 3
	m.configureSplitPane()
	m.selectLine(0)
	cw := m.contentWidth()
	if got := ansi.Strip(m.renderOverflowBelow(cw)); strings.TrimSpace(got) != "↓ 1 more" {
		t.Errorf("below = %q, want ↓ 1 more (folded rows do not count)", got)
	}
	m.selectLine(lineIndex(t, m, "P:s4"))
	if got := ansi.Strip(m.renderOverflowAbove(cw)); strings.TrimSpace(got) != "↑ 1 more" {
		t.Errorf("above = %q, want ↑ 1 more", got)
	}
}

func TestClickProjectNameTogglesFold(t *testing.T) {
	m := foldModel(t)
	top := m.fleetListTop()
	span := m.headerNameSpan(m.groups[0], m.listColWidth())
	if span[0] != fleetHeaderNameCol || span[1] != fleetHeaderNameCol+2+len("alpha") {
		t.Fatalf("name span = %v", span)
	}
	hdr := ansi.Strip(m.renderLine(0, m.listColWidth()))
	if got := string([]rune(hdr)[span[0]:span[1]]); got != fleetUnfoldedGlyph+" alpha" {
		t.Errorf("span covers %q, want the fold marker and name", got)
	}
	for _, x := range []int{span[0], span[1] - 1} {
		if h := m.hoverAt(x, top); h.reg != fleetHoverFold {
			t.Errorf("hover at %d = %v, want fold", x, h.reg)
		}
	}
	click := func(m FleetModel, x int) FleetModel {
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: top})
		return next.(FleetModel)
	}
	m = click(m, span[0]+3)
	if !m.isFolded("s1") || cursorOf(m) != 0 {
		t.Fatalf("click on name: folded=%v cursor=%d", m.isFolded("s1"), cursorOf(m))
	}
	if hdr := ansi.Strip(m.renderLine(0, m.listColWidth())); !strings.Contains(hdr, fleetFoldedGlyph+" alpha") {
		t.Errorf("folded header = %q, want %s marker", hdr, fleetFoldedGlyph)
	}
	m = click(m, span[0])
	if m.isFolded("s1") {
		t.Error("second click should unfold")
	}
	// The "+ new" button and the gap before the counts keep their behaviour.
	newX := m.listColWidth() - fleetSidebarRightGutter - 2
	if _, act := m.clickDecision(newX, top); act != fleetClickNew {
		t.Errorf("+ new click = %v, want new", act)
	}
	if _, act := m.clickDecision(span[1]+2, top); act != fleetClickSelect {
		t.Errorf("click past the name = %v, want select", act)
	}
}

func TestTitleSpacing(t *testing.T) {
	m := foldModel(t)
	if top := m.fleetListTop(); top != 2 {
		t.Fatalf("list top = %d, want 2", top)
	}
	out := strings.Split(ansi.Strip(m.render()), "\n")
	if !strings.Contains(out[0], "agentctl hub") {
		t.Errorf("row 0 = %q, want the title", out[0])
	}
	if strings.TrimSpace(out[1]) != "" {
		t.Errorf("row 1 = %q, want one blank row", out[1])
	}
	if !strings.Contains(out[2], "alpha") {
		t.Errorf("row 2 = %q, want the first group header", out[2])
	}
	// The footer message line follows the list area directly.
	if y := m.footerMessageY(); y != 2+m.fleetListRows()+1 {
		t.Errorf("footerMessageY = %d", y)
	}
}

func TestHeaderNewGap(t *testing.T) {
	m := foldModel(t)
	for _, w := range []int{44, 56, 120} {
		// "+ new" is drawn on the selected header only (or a hovered one).
		hdr := strings.TrimRight(ansi.Strip(m.renderGroupHeader(m.groups[1], true, w)), " ")
		before, ok := strings.CutSuffix(hdr, fleetNewLabel)
		if !ok {
			t.Fatalf("w=%d: selected header %q lacks %q", w, hdr, fleetNewLabel)
		}
		// The sweep button sits between them: counts, two spaces, "sweep N", two spaces.
		sweep := sweepLabel(groupSweepable(m.groups[1]))
		if !strings.HasSuffix(before, "○ 1  "+sweep+"  ") || strings.HasSuffix(before, "○ 1   "+sweep+"  ") {
			t.Errorf("w=%d: want exactly two spaces between the counts and %q: %q", w, sweep, hdr)
		}
		if plain := strings.TrimRight(ansi.Strip(m.renderGroupHeader(m.groups[1], false, w)), " "); strings.Contains(plain, fleetNewLabel) {
			t.Errorf("w=%d: unselected header shows %q: %q", w, fleetNewLabel, plain)
		}
	}
}

func TestTitleFilterTag(t *testing.T) {
	tests := []struct {
		filter fleetFilter
		width  int
		want   string
		counts bool
	}{
		{fleetFilterAll, 120, "", true},
		{fleetFilterActive, 120, "· active", true},
		{fleetFilterNeeds, 120, "· needs you", true},
		{fleetFilterNeeds, 44, "· needs", true},
	}
	for _, tt := range tests {
		t.Run(tt.filter.label(), func(t *testing.T) {
			m := foldModel(t)
			m.filter = tt.filter
			got := ansi.Strip(m.renderTitle(tt.width))
			if tt.want == "" && strings.Contains(got, "· a") {
				t.Errorf("unfiltered title has a tag: %q", got)
			}
			if !strings.Contains(got, "agentctl hub"+" "+tt.want) && tt.want != "" {
				t.Errorf("title = %q, want tag %q", got, tt.want)
			}
			if tt.counts && !strings.Contains(got, "⏸ 1") {
				t.Errorf("title lost its counts: %q", got)
			}
			if ansi.StringWidth(got) > tt.width {
				t.Errorf("title wider than %d: %q", tt.width, got)
			}
		})
	}
}

func TestHelpListsFoldKeys(t *testing.T) {
	var keys []string
	for _, it := range fleetHelpItems() {
		keys = append(keys, it.keys)
	}
	for _, k := range []string{"z", "Z", "f"} {
		if !slices.Contains(keys, k) {
			t.Errorf("? reference lacks %q", k)
		}
	}
	// The one-line footer is already full at 77 columns: z/Z/f stay in "? more".
	if w := ansi.StringWidth(strings.TrimSpace(ansi.Strip(fitHints(fleetFooterHints, 200)))); w > 77 {
		t.Errorf("footer hints are %d columns, over 77", w)
	}
}

func TestViewerSwitchToHiddenRowUnfolds(t *testing.T) {
	m := foldModel(t)
	m.setFolded("s2", true)
	m.relayout()
	next, _ := m.Update(fleetViewerMsg{home: "s2", window: "@4"})
	m = next.(FleetModel)
	if m.isFolded("s2") {
		t.Error("a live-pane switch to a folded row should unfold its group")
	}
	if r := m.selectedRow(); r == nil || r.WindowID != "@4" {
		t.Errorf("selected = %+v, want @4", r)
	}
}

func TestDimmedContextRow(t *testing.T) {
	m := foldModel(t)
	m.filter = fleetFilterActive
	m.relayout()
	m.selectLine(lineIndex(t, m, "R:g"))
	i := lineIndex(t, m, "R:p~")
	got := m.renderLine(i, 120)
	if ansi.Strip(got) != ansi.Strip(m.renderRowLine(m.lines[i], false, fleetHoverNone, 120)) {
		t.Errorf("dimming changed the text: %q", ansi.Strip(got))
	}
	if !strings.HasPrefix(got, fleetProjectBar("alpha")) || !strings.Contains(got, fleetDimStyle.Render(strings.TrimPrefix(ansi.Strip(got), "▎"))) {
		t.Errorf("context row should keep its bar and dim the rest: %q", got)
	}
}
