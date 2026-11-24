package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func edKey(k string) tea.KeyPressMsg {
	switch k {
	case "ctrl+z":
		return tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl}
	case "ctrl+y":
		return tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}
	case "ctrl+shift+z":
		return tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl | tea.ModShift}
	case "ctrl+end":
		return tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl}
	case "ctrl+home":
		return tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl}
	case "ctrl+right":
		return tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl}
	case "ctrl+left":
		return tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	}
	return schedKeyMsg(k)
}

// edType types s one key at a time.
func edType(e *promptEditor, s string) {
	for _, r := range s {
		if r == '\n' {
			e.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			continue
		}
		e.HandleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func edPress(e *promptEditor, keys ...string) editorAction {
	var act editorAction
	for _, k := range keys {
		act = e.HandleKey(edKey(k))
	}
	return act
}

func newTestEditor(t *testing.T, text string, w, h int) *promptEditor {
	t.Helper()
	InitStyles()
	e := newPromptEditor("Prompt — test", text)
	e.SetSize(w, h)
	return e
}

func TestPromptEditorUndoRedo(t *testing.T) {
	e := newTestEditor(t, "", 40, 10)
	edType(e, "hello world")
	steps := []string{"hello ", ""}
	for _, want := range steps {
		edPress(e, "ctrl+z")
		if got := e.Value(); got != want {
			t.Fatalf("undo: %q, want %q", got, want)
		}
	}
	edPress(e, "ctrl+z") // nothing left: no-op
	if e.Value() != "" {
		t.Fatalf("undo past the start changed the text: %q", e.Value())
	}
	edPress(e, "ctrl+y")
	if e.Value() != "hello " {
		t.Fatalf("redo: %q", e.Value())
	}
	edPress(e, "ctrl+shift+z")
	if e.Value() != "hello world" {
		t.Fatalf("redo (ctrl+shift+z): %q", e.Value())
	}
	if p := e.pos(); p != (editorPos{0, 11}) {
		t.Errorf("cursor after redo = %+v", p)
	}

	// A move ends a typing run, and a new edit drops the redo history.
	edPress(e, "home")
	edType(e, ">")
	edPress(e, "ctrl+z")
	if e.Value() != "hello world" {
		t.Fatalf("undo after a move took back too much: %q", e.Value())
	}
	edType(e, "!")
	edPress(e, "ctrl+y")
	if e.Value() != "!hello world" {
		t.Errorf("redo after a new edit should do nothing: %q", e.Value())
	}

	// Backspaces coalesce into one step.
	edPress(e, "end", "backspace", "backspace", "backspace")
	if e.Value() != "!hello wo" {
		t.Fatalf("backspace: %q", e.Value())
	}
	edPress(e, "ctrl+z")
	if e.Value() != "!hello world" {
		t.Errorf("undo of a delete run: %q", e.Value())
	}
}

func TestPromptEditorUndoMultiLineCursor(t *testing.T) {
	e := newTestEditor(t, "one\ntwo\nthree", 40, 10)
	edPress(e, "ctrl+end")
	edType(e, "!")
	edPress(e, "ctrl+home")
	edPress(e, "ctrl+z")
	if e.Value() != "one\ntwo\nthree" || e.pos() != (editorPos{2, 5}) {
		t.Errorf("undo: %q at %+v, want the cursor back on line 3", e.Value(), e.pos())
	}
}

func TestPromptEditorPaste(t *testing.T) {
	e := newTestEditor(t, "start\nend", 40, 10)
	edPress(e, "end") // end of "start"
	var b strings.Builder
	for i := range 500 {
		fmt.Fprintf(&b, "\r\npasted line %d\twith a tab", i)
	}
	e.Paste(b.String())
	lines := strings.Split(e.Value(), "\n")
	if len(lines) != 502 {
		t.Fatalf("lines = %d, want 502 (CRLF read as one newline)", len(lines))
	}
	if lines[1] != "pasted line 0    with a tab" || lines[501] != "end" {
		t.Errorf("pasted text: %q … %q", lines[1], lines[501])
	}
	if p := e.pos(); p.row != 500 {
		t.Errorf("cursor after paste at %+v, want the end of the paste", p)
	}
	// The gutter grew to three digits and the view follows the cursor.
	if e.gutterWidth() != 5 {
		t.Errorf("gutter width = %d", e.gutterWidth())
	}
	if !strings.Contains(ansi.Strip(strings.Join(e.View(), "\n")), "501 pasted line 499") {
		t.Errorf("cursor line not in view:\n%s", ansi.Strip(strings.Join(e.View(), "\n")))
	}
	edPress(e, "ctrl+z")
	if e.Value() != "start\nend" {
		t.Errorf("a paste is one undo step: %q", e.Value())
	}
}

func TestPromptEditorTabInsertsSpaces(t *testing.T) {
	e := newTestEditor(t, "", 40, 10)
	edType(e, "ab")
	edPress(e, "tab")
	edType(e, "c")
	if e.Value() != "ab  c" {
		t.Errorf("tab: %q, want spaces to the next stop", e.Value())
	}
}

func TestPromptEditorEsc(t *testing.T) {
	e := newTestEditor(t, "keep", 60, 10)
	if act := edPress(e, "esc"); act != editorClose {
		t.Fatalf("esc on a clean editor = %v, want close", act)
	}
	edType(e, "x")
	if act := edPress(e, "esc"); act != editorKeep || !e.Asking() {
		t.Fatalf("esc on a dirty editor should ask: act %v asking %v", act, e.Asking())
	}
	if !strings.Contains(ansi.Strip(strings.Join(e.View(), "\n")), "discard changes? y discard · n keep editing") {
		t.Errorf("no discard question:\n%s", ansi.Strip(strings.Join(e.View(), "\n")))
	}
	edType(e, "q") // ignored while asking
	if e.Value() != "xkeep" || !e.Asking() {
		t.Fatalf("typed while asking: %q asking %v", e.Value(), e.Asking())
	}
	if act := edPress(e, "n"); act != editorKeep || e.Asking() {
		t.Fatalf("n should return to editing")
	}
	edPress(e, "esc")
	if act := edPress(e, "y"); act != editorDiscard {
		t.Fatalf("y = %v, want discard", act)
	}
	if act := edPress(e, "ctrl+s"); act != editorSave {
		t.Fatalf("ctrl+s = %v, want save", act)
	}
}

func TestPromptEditorStatusAndTitle(t *testing.T) {
	e := newTestEditor(t, "ab\ncd", 60, 10)
	view := ansi.Strip(strings.Join(e.View(), "\n"))
	for _, want := range []string{"Prompt — test", "1:1 · 2 lines · 5 chars", "^s save", "esc cancel"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "modified") {
		t.Error("a clean editor shows modified")
	}
	edType(e, "é")
	view = ansi.Strip(strings.Join(e.View(), "\n"))
	if !strings.Contains(view, "1:2 · 2 lines · 6 chars · ● modified") {
		t.Errorf("status after typing:\n%s", view)
	}
	if len(e.View()) != 10 {
		t.Errorf("view is %d lines, want 10", len(e.View()))
	}
}

func TestPromptEditorWordAndPageMovement(t *testing.T) {
	var lines []string
	for i := range 50 {
		lines = append(lines, fmt.Sprintf("alpha beta %02d", i))
	}
	e := newTestEditor(t, strings.Join(lines, "\n"), 40, 10)
	edPress(e, "ctrl+right")
	if c := e.pos().col; c != 5 {
		t.Errorf("ctrl+right: col %d", c)
	}
	edPress(e, "ctrl+right", "ctrl+left")
	if c := e.pos().col; c != 6 {
		t.Errorf("ctrl+left: col %d", c)
	}
	edPress(e, "pgdown", "pgdown")
	if r := e.pos().row; r < 5 {
		t.Errorf("pgdown: row %d", r)
	}
	edPress(e, "ctrl+end")
	if p := e.pos(); p.row != 49 || e.ta.ScrollYOffset() == 0 {
		t.Errorf("ctrl+end: %+v offset %d", p, e.ta.ScrollYOffset())
	}
	edPress(e, "ctrl+home")
	if p := e.pos(); p != (editorPos{}) || e.ta.ScrollYOffset() != 0 {
		t.Errorf("ctrl+home: %+v offset %d", p, e.ta.ScrollYOffset())
	}
}

func TestPromptEditorClickWrapAndScroll(t *testing.T) {
	long := strings.Repeat("word ", 20) // wraps over several screen lines
	var lines []string
	for i := range 40 {
		lines = append(lines, fmt.Sprintf("l%02d", i))
	}
	lines[3] = strings.TrimSpace(long)
	e := newTestEditor(t, strings.Join(lines, "\n"), 30, 8) // 5 text rows
	g := e.gutterWidth()
	top := e.layout().textTop

	// Wrap: the second screen line of line 4 (the long one), lines 1-3 above.
	e.Click(g+2, top+4)
	li := e.ta.LineInfo()
	if e.pos().row != 3 || li.RowOffset != 1 || li.ColumnOffset != 2 {
		t.Fatalf("click on a wrapped line: %+v, row offset %d col offset %d", e.pos(), li.RowOffset, li.ColumnOffset)
	}

	// Scroll: at the bottom, the top screen row is the offset's line.
	edPress(e, "ctrl+end")
	off := e.ta.ScrollYOffset()
	if off == 0 {
		t.Fatal("ctrl+end did not scroll")
	}
	before := ansi.Strip(strings.Join(e.View(), "\n"))
	e.Click(g+1, top+1)
	if off != e.ta.ScrollYOffset() {
		t.Errorf("click scrolled the view: %d → %d", off, e.ta.ScrollYOffset())
	}
	// The screen line clicked: count back from the last line (39), each short
	// line one screen line.
	wantRow := 39 - (e.textH - 2)
	if p := e.pos(); p.row != wantRow || p.col != 1 {
		t.Errorf("click after scrolling: %+v, want row %d col 1\n%s", p, wantRow, before)
	}

	// Past the end of a line: its end. On the gutter: its start.
	e.Click(e.w-1, top)
	if p := e.pos(); p.row != wantRow-1 || p.col != 3 {
		t.Errorf("click past the end: %+v", p)
	}
	e.Click(0, top+2)
	if p := e.pos(); p.row != wantRow+1 || p.col != 0 {
		t.Errorf("click on the gutter: %+v", p)
	}
}

func TestPromptEditorClickWideRunes(t *testing.T) {
	e := newTestEditor(t, "日本語のテキスト 🙂 ok", 40, 8)
	g := e.gutterWidth()
	top := e.layout().textTop
	for _, tc := range []struct{ x, col int }{{0, 0}, {1, 0}, {2, 1}, {4, 2}, {5, 2}, {17, 9}, {19, 10}} {
		e.Click(g+tc.x, top)
		if p := e.pos(); p.col != tc.col {
			t.Errorf("click at x %d: col %d, want %d", tc.x, p.col, tc.col)
		}
	}
}

func TestPromptEditorHintClicks(t *testing.T) {
	e := newTestEditor(t, "", 60, 10)
	edType(e, "x")
	hint := func(key string) (int, int) {
		for _, ht := range e.layout().hits {
			if ht.key == key {
				return ht.x0, ht.line
			}
		}
		t.Fatalf("no %s hint in %+v", key, e.layout().hits)
		return 0, 0
	}
	x, y := hint("ctrl+z")
	e.Click(x, y)
	if e.Value() != "" {
		t.Errorf("undo button: %q", e.Value())
	}
	edType(e, "y")
	x, y = hint("esc")
	if e.Click(x, y); !e.Asking() {
		t.Fatal("cancel button on a dirty editor should ask")
	}
	x, y = hint("y")
	if act := e.Click(x, y); act != editorDiscard {
		t.Errorf("discard button = %v", act)
	}
	x, y = hint("ctrl+s")
	if act := e.Click(x, y); act != editorSave {
		t.Errorf("save button = %v", act)
	}
}

func TestPromptEditorWheel(t *testing.T) {
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprint(i))
	}
	e := newTestEditor(t, strings.Join(lines, "\n"), 30, 10)
	for range 10 {
		e.Wheel(3)
	}
	if e.ta.ScrollYOffset() == 0 {
		t.Error("wheel down did not scroll")
	}
	for range 20 {
		e.Wheel(-3)
	}
	if e.ta.ScrollYOffset() != 0 {
		t.Errorf("wheel up: offset %d", e.ta.ScrollYOffset())
	}
}

func TestPromptEditorResizeReflows(t *testing.T) {
	text := strings.Repeat("reflow me ", 30)
	e := newTestEditor(t, text, 80, 12)
	count := func() int {
		n := 0
		for _, l := range e.View()[1 : 1+e.textH] {
			if strings.Contains(ansi.Strip(l), "reflow") {
				n++
			}
		}
		return n
	}
	wide := count()
	e.SetSize(30, 12)
	narrow := count()
	if narrow <= wide {
		t.Errorf("narrowing did not reflow: %d lines wide, %d narrow", wide, narrow)
	}
	for _, l := range e.View() {
		if w := ansi.StringWidth(l); w > 30 {
			t.Errorf("line wider than the pane (%d): %q", w, ansi.Strip(l))
		}
	}
	if e.Value() != text {
		t.Error("resize changed the text")
	}
}

func TestPromptEditorLongSingleLine(t *testing.T) {
	text := strings.Repeat("x", 5000)
	e := newTestEditor(t, text, 40, 10)
	edPress(e, "ctrl+end")
	if p := e.pos(); p != (editorPos{0, 5000}) {
		t.Fatalf("end of a long line: %+v", p)
	}
	view := ansi.Strip(strings.Join(e.View(), "\n"))
	if !strings.Contains(view, "1:5001 · 1 line · 5000 chars") {
		t.Errorf("status:\n%s", view)
	}
	e.Click(e.gutterWidth()+3, e.layout().textTop)
	if e.pos().row != 0 {
		t.Errorf("click: %+v", e.pos())
	}
}

func TestPromptEditorTinyPanes(t *testing.T) {
	for h := 1; h <= 6; h++ {
		for _, w := range []int{1, 2, 5, 12} {
			e := newTestEditor(t, "some text\nmore 日本", w, h)
			edType(e, "ab\ncd")
			e.Paste("x\ny")
			for y := -1; y <= h; y++ {
				for x := -1; x <= w; x++ {
					e.Click(x, y)
				}
			}
			e.Wheel(3)
			if got := len(e.View()); got != h {
				t.Errorf("w %d h %d: %d lines", w, h, got)
			}
		}
	}
	e := newTestEditor(t, "", 40, 4)
	view := ansi.Strip(strings.Join(e.View(), "\n"))
	if !strings.Contains(view, "1:1") || !strings.Contains(view, "^s save") {
		t.Errorf("a short pane keeps status and hints on one line:\n%s", view)
	}
}

func TestPromptEditorEmpty(t *testing.T) {
	e := newTestEditor(t, "", 40, 10)
	if e.Dirty() || e.Value() != "" {
		t.Fatal("an empty editor is clean")
	}
	edPress(e, "backspace", "delete", "ctrl+z", "ctrl+y", "up", "ctrl+end")
	if e.Value() != "" {
		t.Errorf("keys on an empty editor changed it: %q", e.Value())
	}
	if act := edPress(e, "esc"); act != editorClose {
		t.Errorf("esc = %v", act)
	}
}
