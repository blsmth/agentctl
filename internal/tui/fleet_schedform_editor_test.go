package tui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/cron"
)

// schedEdType types s into the hub one key at a time.
func schedEdType(t *testing.T, m FleetModel, s string) FleetModel {
	t.Helper()
	for _, r := range s {
		msg := tea.KeyPressMsg{Code: r, Text: string(r)}
		if r == '\n' {
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		}
		next, cmd := m.Update(msg)
		m = schedDrive(t, next.(FleetModel), cmd)
	}
	return m
}

func schedEdKey(t *testing.T, m FleetModel, k string) FleetModel {
	t.Helper()
	next, cmd := m.Update(edKey(k))
	return schedDrive(t, next.(FleetModel), cmd)
}

// settleDrawer finishes the drawer's slide, whose frames a slow (-race)
// run may have dropped.
func settleDrawer(m FleetModel) FleetModel {
	m.schedAnimOn, m.schedAnimP = false, 1
	return m
}

// schedPressSync presses k and runs the command it returns to the end,
// however long it takes.
func schedPressSync(t *testing.T, m FleetModel, k string) FleetModel {
	t.Helper()
	next, cmd := m.Update(edKey(k))
	m = next.(FleetModel)
	if cmd == nil {
		t.Fatalf("%s returned no command", k)
	}
	if msg := cmd(); msg != nil {
		next, _ = m.Update(msg)
		m = next.(FleetModel)
	}
	return m
}

func editorOf(t *testing.T, m FleetModel) *promptEditor {
	t.Helper()
	e := formOf(t, m).editor
	if e == nil {
		t.Fatal("the prompt editor is not open")
	}
	return e
}

func TestSchedEditorRoundTrip(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := openNewForm(t, f)
	m = schedEdType(t, m, "nightly")
	m = focusStopT(t, m, stopPrompt)
	m = settleDrawer(schedPress(t, m, "ctrl+e"))
	e := editorOf(t, m)
	out := ansi.Strip(m.render())
	for _, want := range []string{"Prompt — nightly", "1:1 · 1 line · 0 chars", "^s save"} {
		if !strings.Contains(out, want) {
			t.Errorf("editor lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Schedule") && strings.Contains(out, "Advanced") {
		t.Errorf("the editor should replace the form:\n%s", out)
	}
	x0, _, w, h, _, ok := m.schedFormArea()
	if !ok || e.w != w || e.h != h {
		t.Fatalf("editor sized %dx%d, form area %dx%d (ok %v)", e.w, e.h, w, h, ok)
	}
	_ = x0

	m = schedEdType(t, m, "Triage.\nThen\tlabel.")
	gen := formOf(t, m).gen
	m = schedPressSync(t, m, "ctrl+s")
	fm := formOf(t, m)
	if fm.editor != nil {
		t.Fatal("ctrl+s should close the editor")
	}
	if got := fm.prompt.Value(); got != "Triage.\nThen    label." {
		t.Errorf("prompt = %q", got)
	}
	if fm.focus != stopPrompt {
		t.Errorf("focus = %v, want the prompt", fm.focus)
	}
	if fm.gen == gen || fm.preview == nil || fm.preview.Job.Prompt != "Triage.\nThen    label." {
		t.Errorf("no preview of the new prompt: gen %d→%d preview %+v", gen, fm.gen, fm.preview)
	}
	if !strings.Contains(ansi.Strip(m.render()), "What") {
		t.Error("the form is not back")
	}
}

func TestSchedEditorEscDirtyAsks(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "upcoming")
	m = schedPress(t, m, "e", "ctrl+e")
	m = schedEdType(t, m, "X")
	m = settleDrawer(schedPress(t, m, "esc"))
	e := editorOf(t, m)
	if !e.Asking() || !strings.Contains(ansi.Strip(m.render()), "discard changes? ") {
		t.Fatalf("esc on a changed prompt should ask:\n%s", ansi.Strip(m.render()))
	}
	m = schedPress(t, m, "n")
	if editorOf(t, m).Asking() {
		t.Fatal("n should go back to editing")
	}
	m = schedPress(t, m, "esc", "y")
	fm := formOf(t, m)
	if fm.editor != nil || fm.prompt.Value() != "Triage new issues." {
		t.Errorf("discard: editor %v prompt %q", fm.editor != nil, fm.prompt.Value())
	}
	// Clean: esc closes at once, the prompt as it was.
	m = schedPress(t, m, "ctrl+e", "esc")
	if fm = formOf(t, m); fm.editor != nil || fm.focus != stopPrompt {
		t.Errorf("esc on a clean editor: open %v focus %v", fm.editor != nil, fm.focus)
	}
	if fm.dirty() {
		t.Error("opening and closing the editor dirtied the form")
	}
}

func TestSchedEditorUndoThroughHub(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := openNewForm(t, f)
	m = schedPress(t, m, "ctrl+e")
	m = schedEdType(t, m, "one two")
	m = schedEdKey(t, m, "ctrl+z")
	if got := editorOf(t, m).Value(); got != "one " {
		t.Fatalf("undo: %q", got)
	}
	m = schedEdKey(t, m, "ctrl+y")
	if got := editorOf(t, m).Value(); got != "one two" {
		t.Fatalf("redo: %q", got)
	}
}

func TestSchedEditorPromptKeyPrefill(t *testing.T) {
	f := newSchedFixture(t)
	f.promptKeys = map[string]string{"triage-prompt": "Text from the key."}
	f.create(t, cron.Job{Name: "keyed", Schedule: "0 7 * * *", PromptKey: "triage-prompt"})
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "keyed")
	m = schedPress(t, m, "e", "ctrl+e")
	if got := editorOf(t, m).Value(); got != "Text from the key." {
		t.Fatalf("editor opened with %q, want the key's text", got)
	}
	// esc with nothing changed keeps the key.
	m = schedPress(t, m, "esc")
	if fm := formOf(t, m); fm.promptKey != "triage-prompt" || fm.prompt.Value() != "" {
		t.Fatalf("clean esc converted the key: key %q prompt %q", fm.promptKey, fm.prompt.Value())
	}
	m = schedPress(t, m, "ctrl+e")
	m = schedEdKey(t, m, "ctrl+end")
	m = schedEdType(t, m, "\nSecond line.")
	m = schedPress(t, m, "ctrl+s")
	fm := formOf(t, m)
	if fm.prompt.Value() != "Text from the key.\nSecond line." || fm.promptKey != "" {
		t.Fatalf("prompt %q key %q", fm.prompt.Value(), fm.promptKey)
	}
	if job, _ := fm.job(); job.Prompt != "Text from the key.\nSecond line." || job.PromptKey != "" {
		t.Errorf("the job the form saves = %+v", job)
	}
}

func TestSchedEditorPaste(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := openNewForm(t, f)
	m = schedPress(t, m, "ctrl+e")
	text := strings.Repeat("a pasted line of a long prompt\n", 300)
	next, cmd := m.Update(tea.PasteMsg{Content: text})
	m = schedDrive(t, next.(FleetModel), cmd)
	if got := editorOf(t, m).Value(); got != text {
		t.Fatalf("paste: %d bytes, want %d", len(got), len(text))
	}
	if formOf(t, m).prompt.Value() != "" {
		t.Error("the paste reached the form's prompt before a save")
	}
	m = schedPress(t, m, "ctrl+s")
	if formOf(t, m).prompt.Value() != text {
		t.Error("save did not apply the pasted text")
	}
}

func TestSchedEditorClickPlacesCursor(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := settleDrawer(openNewForm(t, f))
	m = schedPress(t, m, "ctrl+e")
	var lines []string
	for i := range 80 {
		lines = append(lines, "line "+strings.Repeat("z", i%7))
	}
	lines[78] = strings.Repeat("wrap me please ", 20)
	next, _ := m.Update(tea.PasteMsg{Content: strings.Join(lines, "\n")})
	m = next.(FleetModel)
	m.render() // lay out at the frame's size
	e := editorOf(t, m)
	off := e.ta.ScrollYOffset()
	if off == 0 {
		t.Fatal("the paste should have scrolled the view")
	}
	x0, y0, _, _, _, ok := m.schedFormArea()
	if !ok {
		t.Fatal("form area not ready")
	}
	top := e.layout().textTop
	// The last text row is the cursor's line (the end of the paste); the one
	// above it is the last wrapped screen line of line 79.
	last := top + e.textH - 1
	m = schedClickAt(t, m, x0+e.gutterWidth()+2, y0+last-1)
	e = editorOf(t, m)
	li := e.ta.LineInfo()
	if e.pos().row != 78 || li.RowOffset != li.Height-1 || li.ColumnOffset != 2 {
		t.Errorf("click on the wrapped line: %+v row offset %d/%d col offset %d", e.pos(), li.RowOffset, li.Height, li.ColumnOffset)
	}
	if e.ta.ScrollYOffset() != off {
		t.Errorf("the click scrolled: %d → %d", off, e.ta.ScrollYOffset())
	}
	// The editor's save button.
	hints := ansi.Strip(m.render())
	if !strings.Contains(hints, "^s save") {
		t.Fatal("no save button")
	}
	for _, ht := range e.layout().hits {
		if ht.key == "ctrl+s" {
			m = schedClickAt(t, m, x0+ht.x0, y0+ht.line)
		}
	}
	if fm := formOf(t, m); fm.editor != nil || !strings.HasPrefix(fm.prompt.Value(), "line ") {
		t.Errorf("the save button did not save: open %v", fm.editor != nil)
	}
}

func TestSchedEditorResizeReflows(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := settleDrawer(openNewForm(t, f))
	m = schedPress(t, m, "ctrl+e")
	m.render()
	w := editorOf(t, m).w
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = schedDrive(t, next.(FleetModel), cmd)
	m.liveLeft, m.liveRight = 100, 0
	m.configureSplitPane()
	out := m.render()
	e := editorOf(t, m)
	_, _, aw, ah, _, ok := m.schedFormArea()
	if !ok || e.w != aw || e.h != ah || e.w == w {
		t.Errorf("after a resize the editor is %dx%d, the area %dx%d (was %d wide)", e.w, e.h, aw, ah, w)
	}
	if !strings.Contains(ansi.Strip(out), "Prompt — ") {
		t.Errorf("editor gone after a resize:\n%s", ansi.Strip(out))
	}
}

func TestSchedEditorNotOfferedForTemplates(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := openNewForm(t, f)
	formOf(t, m).source = schedSourceTemplate
	m = schedPress(t, m, "ctrl+e")
	if formOf(t, m).editor != nil {
		t.Error("a template-source job opened the prompt editor")
	}
}

// The form no longer hands the prompt to an external editor.
func TestSchedFormHasNoExecPath(t *testing.T) {
	for _, file := range []string{"fleet_schedform.go", "fleet_schedform_mouse.go", "prompteditor.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"tea.ExecProcess", "tkEditorCmd", "$EDITOR", "os.CreateTemp"} {
			if strings.Contains(string(src), bad) {
				t.Errorf("%s still mentions %s", file, bad)
			}
		}
	}
}
