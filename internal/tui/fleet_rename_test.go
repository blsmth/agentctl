package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/windowstate"
)

const (
	renameTitled   = "Old Title"
	renameUntitled = "Fixing the flaky hub test" // the recap's first sentence
)

// renameFixture is a hub with one titled row (@1), one untitled row (@2) and
// a shell row (@9) in group s1, whose runtime dir holds records for @1 and @2.
type renameFixture struct {
	m  FleetModel
	rt string
}

func writeRecord(t *testing.T, rt, id string, ws map[string]any) {
	t.Helper()
	dir := windowstate.WindowsDirFor(rt)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readRecord(t *testing.T, rt, id string) windowstate.ShortTitle {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(windowstate.WindowsDirFor(rt), id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var ws struct {
		ShortTitle windowstate.ShortTitle `json:"short_title"`
	}
	if err := json.Unmarshal(data, &ws); err != nil {
		t.Fatal(err)
	}
	return ws.ShortTitle
}

func renameRows() []fleet.Row {
	a := testRow("alpha", "s1", "idle", "")
	a.WindowID = "@1"
	a.ShortTitle, a.ShortRole = renameTitled, "build"
	a.Recap = fleet.RecapLine{Text: "Worked on the hub. Then more.", Source: fleet.RecapSummary, Title: renameTitled}
	b := testRow("beta", "s1", "idle", "")
	b.WindowID = "@2"
	b.Recap = fleet.RecapLine{Text: renameUntitled + ". It keeps failing on CI.", Source: fleet.RecapSummary}
	sh := testRow("shell", "s1", "", "")
	sh.WindowID, sh.Shell = "@9", true
	return []fleet.Row{a, b, sh}
}

func newRenameFixture(t *testing.T, width int) *renameFixture {
	t.Helper()
	InitStyles()
	rt := t.TempDir()
	writeRecord(t, rt, "@1", map[string]any{"instance": "alpha", "short_title": map[string]any{"title": renameTitled, "kind": "fix"}})
	writeRecord(t, rt, "@2", map[string]any{"instance": "beta"})
	m := NewFleetModel()
	m.termWidth, m.termHeight = width, 40
	m.configureSplitPane()
	m.loaded = true
	m.showShells = true
	m.groups = []fleet.Group{{Session: "s1", Project: "myproj", ProjectDir: filepath.Dir(rt), RuntimeDir: rt, Rows: renameRows()}}
	m.rebuildLines()
	m.resolveSelection()
	return &renameFixture{m: m, rt: rt}
}

// lineOf is the line index of window id's row.
func (f *renameFixture) lineOf(t *testing.T, id string) int {
	t.Helper()
	for i, l := range f.m.lines {
		if l.kind == fleetLineRow && l.row.WindowID == id {
			return i
		}
	}
	t.Fatalf("no line for %s", id)
	return -1
}

// yOf is the screen row of line idx.
func (f *renameFixture) yOf(idx int) int {
	y := f.m.fleetListTop()
	cw := f.m.listColWidth()
	for i := f.m.scrollOffset; i < idx; i++ {
		y += f.m.lineHeight(i, cw)
	}
	return y
}

func (f *renameFixture) span(idx int) [2]int {
	l := f.m.lines[idx]
	_, sp := f.m.treeRowLayout(l.row, l.treePrefix, idx == f.m.splitPane.Cursor(), fleetHoverBody, f.m.listColWidth())
	return sp
}

func (f *renameFixture) send(t *testing.T, msg tea.Msg) tea.Cmd {
	t.Helper()
	got, cmd := f.m.Update(msg)
	f.m = got.(FleetModel)
	return cmd
}

func (f *renameFixture) typeText(t *testing.T, s string) {
	t.Helper()
	for _, r := range s {
		f.send(t, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func (f *renameFixture) leftClick(t *testing.T, x, y int) tea.Cmd {
	t.Helper()
	return f.send(t, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
}

// doubleClick sends two left clicks 100ms apart on a fake clock.
func (f *renameFixture) doubleClick(t *testing.T, x, y int) {
	t.Helper()
	f.withClock(t, func(advance func(time.Duration)) {
		f.leftClick(t, x, y)
		advance(100 * time.Millisecond)
		f.leftClick(t, x, y)
	})
}

// withClock runs fn with fleetNow replaced by a clock fn advances.
func (f *renameFixture) withClock(t *testing.T, fn func(advance func(time.Duration))) {
	t.Helper()
	orig := fleetNow
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fleetNow = func() time.Time { return now }
	defer func() { fleetNow = orig }()
	fn(func(d time.Duration) { now = now.Add(d) })
}

func (f *renameFixture) rowLine(t *testing.T, id string) string {
	t.Helper()
	idx := f.lineOf(t, id)
	return ansi.Strip(f.m.renderLine(idx, f.m.listColWidth()))
}

// TestRowTitleSpanMatchesRender pins the hit-test span to the drawn text, at
// a wide and at narrow widths where the title is truncated.
func TestRowTitleSpanMatchesRender(t *testing.T) {
	for _, width := range []int{120, 60, 44} {
		for _, tc := range []struct{ id, text string }{{"@1", renameTitled}, {"@2", renameUntitled}} {
			f := newRenameFixture(t, width)
			idx := f.lineOf(t, tc.id)
			sp := f.span(idx)
			if sp[1] <= sp[0] {
				t.Fatalf("width %d %s: empty span %v", width, tc.id, sp)
			}
			line := ansi.Strip(f.m.renderLine(idx, f.m.listColWidth()))
			got := ansi.Cut(line, sp[0], sp[1])
			if strings.TrimSpace(got) != got || got == "" {
				t.Errorf("width %d %s: span %v covers %q, want the title text exactly", width, tc.id, sp, got)
			}
			if !strings.HasPrefix(tc.text, strings.TrimSuffix(got, "…")) {
				t.Errorf("width %d %s: span text %q is not the title %q", width, tc.id, got, tc.text)
			}
			if sp[0] > 0 && ansi.Cut(line, sp[0]-1, sp[0]) != " " {
				t.Errorf("width %d %s: span starts mid-word in %q", width, tc.id, line)
			}
		}
	}
}

func TestRightClickNeverRenames(t *testing.T) {
	f := newRenameFixture(t, 120)
	a := f.lineOf(t, "@1")
	sh := f.lineOf(t, "@9")
	sp := f.span(a)
	tests := []struct {
		name   string
		x, y   int
		button tea.MouseButton
		want   fleetClickAction
	}{
		{"title start", sp[0], f.yOf(a), tea.MouseRight, fleetClickMenu},
		{"title end", sp[1] - 1, f.yOf(a), tea.MouseRight, fleetClickMenu},
		{"agent name", 5, f.yOf(a), tea.MouseRight, fleetClickMenu},
		{"after title", sp[1] + 1, f.yOf(a), tea.MouseRight, fleetClickMenu},
		{"header", 5, f.yOf(0), tea.MouseRight, fleetClickMenu},
		{"shell row", sp[0], f.yOf(sh), tea.MouseRight, fleetClickMenu},
		{"left click on title selects", sp[0], f.yOf(a), tea.MouseLeft, fleetClickSelect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, got := f.m.buttonDecision(tt.x, tt.y, tt.button); got != tt.want {
				t.Errorf("buttonDecision = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("detail line", func(t *testing.T) {
		g := newRenameFixture(t, 120)
		g.m.selectLine(a)
		if g.m.lineHeight(a, g.m.listColWidth()) < 2 {
			t.Skip("selected row has no detail line")
		}
		sp := g.span(a)
		if _, got := g.m.buttonDecision(sp[0], g.yOf(a)+1, tea.MouseRight); got != fleetClickMenu {
			t.Errorf("detail line: %v, want menu", got)
		}
	})
}

func TestRenameEditorSaves(t *testing.T) {
	f := newRenameFixture(t, 120)
	a := f.lineOf(t, "@1")
	sp := f.span(a)
	f.doubleClick(t, sp[0]+1, f.yOf(a))
	if !f.m.renameOpen() || f.m.menuOpen {
		t.Fatalf("double click on title: rename=%v menu=%v", f.m.renameOpen(), f.m.menuOpen)
	}
	if got := f.m.rename.input.Value(); got != renameTitled {
		t.Fatalf("prefill = %q, want %q", got, renameTitled)
	}
	if f.m.rename.input.Position() != len([]rune(renameTitled)) {
		t.Errorf("cursor at %d, want end", f.m.rename.input.Position())
	}
	// Hub bindings do not fire while editing: r (refresh), q (leave), x (close).
	f.send(t, tea.KeyPressMsg{Code: tea.KeyBackspace})
	f.typeText(t, "rqx")
	if f.m.closeArmed() || !f.m.renameOpen() {
		t.Fatal("a typed key ran a hub binding")
	}
	if got := f.m.rename.input.Value(); got != "Old Titlrqx" {
		t.Fatalf("value = %q", got)
	}
	if !strings.Contains(f.rowLine(t, "@1"), "Old Titlrqx") {
		t.Errorf("editor not drawn in place of the title: %q", f.rowLine(t, "@1"))
	}
	f.send(t, tea.PasteMsg{Content: " pasted\n"})
	f.send(t, tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.m.renameOpen() {
		t.Fatal("enter should close the editor")
	}
	st := readRecord(t, f.rt, "@1")
	want := "Old Titlrqx pasted"
	if st.Title != want || !st.Manual || !st.Final || st.Kind != "fix" {
		t.Errorf("record = %+v, want manual final %q kind go", st, want)
	}
	if line := f.rowLine(t, "@1"); !strings.Contains(line, want) {
		t.Errorf("row not updated at once: %q", line)
	}

	// A load that started before the write must not bring the old title back.
	stale := f.m.groups[0]
	stale.Rows = renameRows()
	f.send(t, fleetLoadedMsg{groups: []fleet.Group{stale}})
	if line := f.rowLine(t, "@1"); !strings.Contains(line, want) {
		t.Errorf("stale load replaced the saved title: %q", line)
	}
	// Once a load shows it, the hold is gone.
	fresh := f.m.groups[0]
	f.send(t, fleetLoadedMsg{groups: []fleet.Group{fresh}})
	if len(f.m.titleHolds) != 0 {
		t.Errorf("hold kept after a load reflected it: %v", f.m.titleHolds)
	}
}

func TestRenameEditorCancels(t *testing.T) {
	t.Run("esc", func(t *testing.T) {
		f := newRenameFixture(t, 120)
		f.m.selectLine(f.lineOf(t, "@1"))
		f.send(t, tea.KeyPressMsg{Code: 'R', Text: "R"})
		f.typeText(t, "zzz")
		f.send(t, tea.KeyPressMsg{Code: tea.KeyEscape})
		if f.m.renameOpen() {
			t.Fatal("esc should close")
		}
		if st := readRecord(t, f.rt, "@1"); st.Title != renameTitled || st.Manual {
			t.Errorf("esc wrote %+v", st)
		}
	})
	t.Run("click elsewhere", func(t *testing.T) {
		f := newRenameFixture(t, 120)
		a := f.lineOf(t, "@1")
		f.m.openRename(a)
		sp := f.span(a)
		f.send(t, tea.MouseClickMsg{Button: tea.MouseLeft, X: sp[0] + 1, Y: f.yOf(a)})
		if !f.m.renameOpen() {
			t.Fatal("a click on the editor itself must keep it open")
		}
		f.send(t, tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: f.yOf(f.lineOf(t, "@2"))})
		if f.m.renameOpen() {
			t.Fatal("a click elsewhere should cancel")
		}
		if st := readRecord(t, f.rt, "@1"); st.Manual {
			t.Errorf("click-away wrote %+v", st)
		}
	})
	t.Run("unchanged enter writes nothing", func(t *testing.T) {
		f := newRenameFixture(t, 120)
		f.m.openRename(f.lineOf(t, "@1"))
		f.send(t, tea.KeyPressMsg{Code: tea.KeyEnter})
		if st := readRecord(t, f.rt, "@1"); st.Manual {
			t.Errorf("unchanged save pinned the auto title: %+v", st)
		}
	})
}

func TestRenameEditorSurvivesPoll(t *testing.T) {
	f := newRenameFixture(t, 120)
	f.m.openRename(f.lineOf(t, "@2"))
	f.typeText(t, "New")
	// A reload that reorders the rows: the editor stays on @2.
	g := f.m.groups[0]
	rows := renameRows()
	g.Rows = []fleet.Row{rows[1], rows[0], rows[2]}
	f.send(t, fleetLoadedMsg{groups: []fleet.Group{g}})
	if !f.m.renameOpen() || f.m.rename.windowID != "@2" || f.m.rename.input.Value() != "New" {
		t.Fatalf("poll dropped or moved the editor: %+v", f.m.rename)
	}
	if !strings.Contains(f.rowLine(t, "@2"), "New") || strings.Contains(f.rowLine(t, "@1"), "New") {
		t.Errorf("editor drawn on the wrong row")
	}
	// The window disappears: the editor closes.
	g.Rows = []fleet.Row{rows[0], rows[2]}
	f.send(t, fleetLoadedMsg{groups: []fleet.Group{g}})
	if f.m.renameOpen() {
		t.Fatal("editor should close when its window is gone")
	}
}

func TestRenameUntitledAndClear(t *testing.T) {
	f := newRenameFixture(t, 120)
	b := f.lineOf(t, "@2")
	sp := f.span(b)
	f.doubleClick(t, sp[0], f.yOf(b))
	if !f.m.renameOpen() || f.m.rename.input.Value() != "" {
		t.Fatalf("untitled row: rename=%v prefill=%q", f.m.renameOpen(), f.m.rename.input.Value())
	}
	f.typeText(t, "  My\x01 Name  ")
	f.send(t, tea.KeyPressMsg{Code: tea.KeyEnter})
	if st := readRecord(t, f.rt, "@2"); st.Title != "My Name" || !st.Manual {
		t.Fatalf("record = %+v", st)
	}
	if !strings.Contains(f.rowLine(t, "@2"), "My Name") {
		t.Errorf("row = %q", f.rowLine(t, "@2"))
	}

	// Saving empty clears it: the recap sentence is back, auto titles resume.
	f.m.openRename(f.lineOf(t, "@2"))
	if f.m.rename.input.Value() != "My Name" {
		t.Fatalf("prefill = %q", f.m.rename.input.Value())
	}
	for range len("My Name") {
		f.send(t, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	f.send(t, tea.KeyPressMsg{Code: tea.KeyEnter})
	if st := readRecord(t, f.rt, "@2"); st != (windowstate.ShortTitle{}) {
		t.Errorf("clear left %+v", st)
	}
	if line := f.rowLine(t, "@2"); !strings.Contains(line, "Fixing the flaky") || strings.Contains(line, "My Name") {
		t.Errorf("row after clear = %q", line)
	}
}

func TestRenameLongTitleCapped(t *testing.T) {
	f := newRenameFixture(t, 120)
	f.m.openRename(f.lineOf(t, "@2"))
	f.send(t, tea.PasteMsg{Content: strings.Repeat("abcdefghij", 10)})
	f.send(t, tea.KeyPressMsg{Code: tea.KeyEnter})
	if st := readRecord(t, f.rt, "@2"); len([]rune(st.Title)) != 60 {
		t.Errorf("title is %d runes, want 60", len([]rune(st.Title)))
	}
}

func TestRenameRecordGone(t *testing.T) {
	f := newRenameFixture(t, 120)
	f.m.openRename(f.lineOf(t, "@2"))
	if err := os.Remove(filepath.Join(windowstate.WindowsDirFor(f.rt), "@2.json")); err != nil {
		t.Fatal(err)
	}
	f.typeText(t, "x")
	f.send(t, tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.m.renameOpen() || f.m.jumpErr == "" {
		t.Errorf("rename=%v err=%q, want closed with an error", f.m.renameOpen(), f.m.jumpErr)
	}
	if _, err := os.Stat(filepath.Join(windowstate.WindowsDirFor(f.rt), "@2.json")); !os.IsNotExist(err) {
		t.Error("save must not create a record")
	}
}

func TestRenameMenuAndShell(t *testing.T) {
	f := newRenameFixture(t, 120)
	has := func(idx int) bool {
		for _, it := range f.m.fleetMenuItems(idx) {
			if it.key == fleetRenameKey {
				return true
			}
		}
		return false
	}
	a, sh := f.lineOf(t, "@1"), f.lineOf(t, "@9")
	if !has(a) {
		t.Error("agent row menu lacks rename")
	}
	if has(sh) || has(0) {
		t.Error("shell row and header menus must not offer rename")
	}
	f.m.openMenu(a)
	f.send(t, tea.KeyPressMsg{Code: 'R', Text: "R"})
	if !f.m.renameOpen() || f.m.menuOpen {
		t.Errorf("menu rename: rename=%v menu=%v", f.m.renameOpen(), f.m.menuOpen)
	}
	f.m.cancelRename()

	f.m.selectLine(sh)
	f.send(t, tea.KeyPressMsg{Code: 'R', Text: "R"})
	if f.m.renameOpen() {
		t.Error("R must not rename a shell row")
	}

	// Bare windows (not dispatched) rename too.
	g := newRenameFixture(t, 120)
	g.m.groups[0].Rows[0].Dispatched = false
	g.m.rebuildLines()
	g.m.openRename(g.lineOf(t, "@1"))
	if !g.m.renameOpen() {
		t.Error("bare window should be renamable")
	}
}

func TestRenameFooterHints(t *testing.T) {
	f := newRenameFixture(t, 120)
	f.m.openRename(f.lineOf(t, "@1"))
	out := ansi.Strip(f.m.render())
	if !strings.Contains(out, "⏎ save") || !strings.Contains(out, "esc cancel") {
		t.Errorf("footer lacks rename hints:\n%s", out)
	}
}

func TestRenamePrefillAutoShortTitle(t *testing.T) {
	f := newRenameFixture(t, 120)
	g := f.m.groups[0]
	g.Rows[1].Recap = fleet.RecapLine{Text: "Auto Short Title", Source: fleet.RecapTitle}
	f.m.groups = []fleet.Group{g}
	f.m.rebuildLines()
	f.m.openRename(f.lineOf(t, "@2"))
	if got := f.m.rename.input.Value(); got != "Auto Short Title" {
		t.Errorf("prefill = %q", got)
	}
}

func TestRenameDefersHotReload(t *testing.T) {
	f := newRenameFixture(t, 120)
	f.m.exeChanged = func() bool { return true }
	f.m.openRename(f.lineOf(t, "@1"))
	cmd := f.send(t, fleetTickMsg{})
	if f.m.Reexec() {
		t.Fatal("reload fired while the editor was open")
	}
	if cmd == nil {
		t.Fatal("tick should keep polling")
	}
	f.send(t, tea.KeyPressMsg{Code: tea.KeyEscape})
	if f.m.renameOpen() {
		t.Fatal("editor still open")
	}
	cmd = f.send(t, fleetTickMsg{})
	if !f.m.Reexec() {
		t.Fatal("reload did not resume")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("want tea.Quit")
	}
}

func TestDoubleClickRenameWindow(t *testing.T) {
	f := newRenameFixture(t, 120)
	a, b := f.lineOf(t, "@1"), f.lineOf(t, "@2")
	spA, spB := f.span(a), f.span(b)
	closeX := f.m.listColWidth() - fleetSidebarRightGutter - 1

	t.Run("single click only selects", func(t *testing.T) {
		g := newRenameFixture(t, 120)
		g.leftClick(t, spA[0], g.yOf(a))
		if g.m.renameOpen() || g.m.splitPane.Cursor() != a {
			t.Errorf("rename=%v cursor=%d", g.m.renameOpen(), g.m.splitPane.Cursor())
		}
	})
	t.Run("two clicks too far apart", func(t *testing.T) {
		g := newRenameFixture(t, 120)
		g.withClock(t, func(advance func(time.Duration)) {
			g.leftClick(t, spA[0], g.yOf(a))
			advance(fleetDoubleClickWindow + time.Millisecond)
			g.leftClick(t, spA[0], g.yOf(a))
		})
		if g.m.renameOpen() {
			t.Error("slow second click opened the editor")
		}
	})
	t.Run("different rows", func(t *testing.T) {
		g := newRenameFixture(t, 120)
		g.withClock(t, func(advance func(time.Duration)) {
			g.leftClick(t, spA[0], g.yOf(a))
			advance(50 * time.Millisecond)
			g.leftClick(t, spB[0], g.yOf(b))
		})
		if g.m.renameOpen() {
			t.Error("clicks on two rows opened the editor")
		}
	})
	t.Run("third click does not reopen", func(t *testing.T) {
		g := newRenameFixture(t, 120)
		g.doubleClick(t, spA[0], g.yOf(a))
		if !g.m.renameOpen() || g.m.lastClick != nil {
			t.Fatalf("rename=%v lastClick=%v", g.m.renameOpen(), g.m.lastClick)
		}
	})
	t.Run("close button", func(t *testing.T) {
		g := newRenameFixture(t, 120)
		g.doubleClick(t, closeX, g.yOf(a))
		if g.m.renameOpen() {
			t.Error("double click on the close button opened the editor")
		}
	})
	t.Run("off the title", func(t *testing.T) {
		g := newRenameFixture(t, 120)
		g.doubleClick(t, 5, g.yOf(a))
		if g.m.renameOpen() {
			t.Error("double click off the title opened the editor")
		}
	})
}

func TestRenameBlinks(t *testing.T) {
	f := newRenameFixture(t, 120)
	cmd := f.m.openRename(f.lineOf(t, "@1"))
	if cmd == nil {
		t.Fatal("openRename returned no blink command")
	}
	if !f.m.rename.input.Styles().Cursor.Blink {
		t.Error("cursor blink disabled")
	}
	// The blink command's message reaches the input and keeps the loop going.
	next := f.send(t, cmd())
	if next == nil {
		t.Error("blink message was not routed to the editor")
	}
	// Once closed, blink messages go nowhere: no tick loop.
	f.m.cancelRename()
	if got := f.send(t, cmd()); got != nil {
		t.Error("blink continued after the editor closed")
	}
}
