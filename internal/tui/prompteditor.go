package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// promptEditor is a full-pane text editor for prompts: a title line, the
// text with a line-number gutter and soft wrap, a status line and clickable
// key hints. It is generic (the schedule form is its first user) and owns
// no persistence: the caller opens it with a text, feeds it keys, pastes and
// clicks, and reads Value when an action says it is done.
//
// The text buffer, soft wrap, gutter, word movement, paging and scrolling
// that keeps the cursor visible are bubbles' textarea; this wraps it with
// undo/redo, tab-as-spaces, click-to-cursor and the chrome.
type promptEditor struct {
	title   string
	ta      textarea.Model
	initial string
	w, h    int // outer size, as last set
	textH   int // rows given to the text

	asking bool // "discard changes? y/n" is up

	undo, redo []editorSnap
	// run is the kind of the edit run the last key extended (typing or
	// deleting coalesce into one undo step); runEnd is where its cursor
	// ended, so a move in between starts a new step.
	run       editorRun
	runEnd    editorPos
	runSpaced bool // the typing run has reached whitespace
}

// editorAction is what a key or click asks of the caller.
type editorAction int

const (
	editorKeep    editorAction = iota // still editing
	editorSave                        // ctrl+s: apply the text and close
	editorClose                       // esc with nothing changed: close
	editorDiscard                     // y on "discard changes?": close, drop the text
)

type editorRun int

const (
	runNone editorRun = iota
	runType
	runDelete
)

type editorPos struct{ row, col int }

type editorSnap struct {
	text string
	pos  editorPos
}

const (
	editorUndoMax = 200
	editorTabStop = 4
	// editorMinFull is the smallest height that shows the title and a hint
	// line of its own; below it the hints share the status line.
	editorMinFull = 6
)

var (
	editorHints    = []fleetHint{{"^s", "save"}, {"esc", "cancel"}, {"^z", "undo"}, {"^y", "redo"}}
	editorAskHints = []fleetHint{{"y", "discard"}, {"n", "keep editing"}}
)

// newPromptEditor opens text for editing, the cursor at the top.
func newPromptEditor(title, text string) *promptEditor {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = true
	ta.CharLimit = 0
	ta.MaxWidth = 0
	// MaxContentHeight > 0 lifts textarea's legacy cap of MaxHeight logical
	// lines; MaxHeight itself only sizes the gutter here (setGutter).
	ta.MaxContentHeight = 1 << 30
	ta.Placeholder = "Write the prompt…"
	km := textarea.DefaultKeyMap()
	km.WordForward = key.NewBinding(key.WithKeys("alt+right", "alt+f", "ctrl+right"))
	km.WordBackward = key.NewBinding(key.WithKeys("alt+left", "alt+b", "ctrl+left"))
	km.Paste = key.NewBinding() // the terminal's bracketed paste, never the clipboard
	ta.KeyMap = km
	st := ta.Styles()
	st.Cursor.Blink = false
	ta.SetStyles(st)
	ta.Focus()
	e := &promptEditor{title: title, ta: ta, initial: text}
	e.ta.SetValue(text)
	e.SetSize(60, 12)
	e.ta.MoveToBegin()
	e.sync()
	return e
}

// Value is the text as edited.
func (e *promptEditor) Value() string { return e.ta.Value() }

// Dirty reports whether the text differs from the text it opened with.
func (e *promptEditor) Dirty() bool { return e.ta.Value() != e.initial }

// Asking reports whether the discard question is up.
func (e *promptEditor) Asking() bool { return e.asking }

// Hints are the key hints for the current state, for a caller's footer.
func (e *promptEditor) Hints() []fleetHint {
	if e.asking {
		return editorAskHints
	}
	return editorHints
}

// chrome is the rows above and below the text at height h.
func editorChrome(h int) (top, bottom int) {
	switch {
	case h >= editorMinFull:
		return 1, 2
	case h >= 2:
		return 0, 1
	}
	return 0, 0
}

// SetSize lays the editor out in w columns and h rows; a change reflows the
// text.
func (e *promptEditor) SetSize(w, h int) {
	w, h = max(w, 1), max(h, 1)
	if w == e.w && h == e.h {
		return
	}
	e.w, e.h = w, h
	top, bottom := editorChrome(h)
	e.textH = max(h-top-bottom, 1)
	e.setGutter()
	e.ta.SetWidth(w)
	e.ta.SetHeight(e.textH)
	e.sync()
}

// setGutter sizes the line-number gutter for the line count: textarea draws
// as many digits as MaxHeight has (two at least), and MaxHeight must not
// clamp the view's height.
func (e *promptEditor) setGutter() {
	d := max(2, len(fmt.Sprint(e.ta.LineCount())))
	mh := 1
	for range d {
		mh *= 10
	}
	mh = max(mh-1, e.textH)
	if mh != e.ta.MaxHeight {
		e.ta.MaxHeight = mh
		if e.w > 0 {
			e.ta.SetWidth(e.w)
			e.ta.SetHeight(e.textH)
		}
	}
}

// gutterWidth is the columns the line numbers take.
func (e *promptEditor) gutterWidth() int {
	return len(fmt.Sprint(e.ta.MaxHeight)) + 2
}

// sync lets textarea scroll the cursor into view after a direct cursor
// move (its Update does this; the cursor setters do not).
func (e *promptEditor) sync() {
	e.ta, _ = e.ta.Update(nil)
}

func (e *promptEditor) pos() editorPos { return editorPos{e.ta.Line(), e.ta.Column()} }

func (e *promptEditor) snap() editorSnap { return editorSnap{e.ta.Value(), e.pos()} }

// push records s as an undo step and drops the redo history.
func (e *promptEditor) push(s editorSnap) {
	e.undo = append(e.undo, s)
	if len(e.undo) > editorUndoMax {
		e.undo = e.undo[len(e.undo)-editorUndoMax:]
	}
	e.redo = nil
}

// restore puts a snapshot back.
func (e *promptEditor) restore(s editorSnap) {
	e.ta.SetValue(s.text)
	e.setGutter()
	e.setCursor(s.pos)
	e.run = runNone
}

// setCursor moves to row, col (textarea has no row setter: walk down).
func (e *promptEditor) setCursor(p editorPos) {
	e.ta.MoveToBegin()
	for e.ta.Line() < p.row {
		before := e.pos()
		e.ta.CursorDown()
		if e.pos() == before {
			break
		}
	}
	e.ta.SetCursorColumn(p.col)
	e.sync()
}

// HandleKey runs one key press.
func (e *promptEditor) HandleKey(msg tea.KeyPressMsg) editorAction {
	k := msg.String()
	if e.asking {
		switch k {
		case "y", "Y":
			e.asking = false
			return editorDiscard
		case "n", "N", "esc":
			e.asking = false
		}
		return editorKeep
	}
	switch k {
	case "ctrl+s":
		return editorSave
	case "esc":
		if e.Dirty() {
			e.asking = true
			return editorKeep
		}
		return editorClose
	case "ctrl+z":
		e.Undo()
		return editorKeep
	case "ctrl+y", "ctrl+shift+z":
		e.Redo()
		return editorKeep
	case "tab":
		n := editorTabStop - e.ta.Column()%editorTabStop
		e.insert(strings.Repeat(" ", n), runNone)
		return editorKeep
	case "shift+tab":
		return editorKeep
	}
	run := runNone
	switch {
	case k == "backspace" || k == "delete":
		run = runDelete
	case msg.Text != "" && msg.Mod&(tea.ModCtrl|tea.ModAlt) == 0:
		run = runType
	}
	before := e.snap()
	e.ta, _ = e.ta.Update(msg)
	if e.ta.Value() == before.text {
		e.run = runNone // a move ends the run
		return editorKeep
	}
	e.recordEdit(before, run, msg.Text)
	e.setGutter()
	return editorKeep
}

// recordEdit pushes before as an undo step unless the edit extends the
// current typing or deleting run. Typing starts a new step at the first
// word after whitespace, so undo takes back a word or so at a time.
func (e *promptEditor) recordEdit(before editorSnap, run editorRun, typed string) {
	space := typed != "" && strings.TrimFunc(typed, unicode.IsSpace) == ""
	extends := run != runNone && run == e.run && before.pos == e.runEnd
	if extends && run == runType && e.runSpaced && !space {
		extends = false
	}
	if !extends {
		e.push(before)
		e.runSpaced = false
	}
	if run == runType && space {
		e.runSpaced = true
	}
	e.run = run
	e.runEnd = e.pos()
}

// insert puts s at the cursor as one undo step (run runNone) or as part of
// a run.
func (e *promptEditor) insert(s string, run editorRun) {
	if s == "" {
		return
	}
	before := e.snap()
	e.ta.InsertString(s)
	e.sync()
	if e.ta.Value() == before.text {
		return
	}
	e.recordEdit(before, run, s)
	e.setGutter()
}

// Paste inserts pasted text (CRLF and CR read as newlines; textarea turns
// tabs into spaces) as one undo step.
func (e *promptEditor) Paste(content string) {
	if e.asking {
		return
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	e.insert(content, runNone)
	e.run = runNone
}

// Undo takes back the last step.
func (e *promptEditor) Undo() {
	if len(e.undo) == 0 {
		return
	}
	s := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.redo = append(e.redo, e.snap())
	e.restore(s)
}

// Redo puts back the last undone step.
func (e *promptEditor) Redo() {
	if len(e.redo) == 0 {
		return
	}
	s := e.redo[len(e.redo)-1]
	e.redo = e.redo[:len(e.redo)-1]
	e.undo = append(e.undo, e.snap())
	e.restore(s)
}

// Wheel scrolls by delta screen lines (negative is up) by moving the
// cursor, which textarea keeps in view.
func (e *promptEditor) Wheel(delta int) {
	if e.asking {
		return
	}
	for range absInt(delta) {
		if delta < 0 {
			e.ta.CursorUp()
		} else {
			e.ta.CursorDown()
		}
	}
	e.run = runNone
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Click is a left click at column x, row y of the editor as last laid out:
// on the text it places the cursor, on a hint it presses that key.
func (e *promptEditor) Click(x, y int) editorAction {
	l := e.layout()
	for _, ht := range l.hits {
		if ht.line == y && x >= ht.x0 && x < ht.x1 {
			return e.HandleKey(schedSynthKey(ht.key))
		}
	}
	if e.asking || y < l.textTop || y >= l.textTop+e.textH {
		return editorKeep
	}
	e.placeCursor(e.ta.ScrollYOffset()+y-l.textTop, x-e.gutterWidth())
	e.run = runNone
	return editorKeep
}

// placeCursor moves the cursor to screen line vline of the wrapped text
// (0 = the first) at column x of the text, keeping the scroll offset.
// textarea exposes no viewport setter, so this walks the cursor: down to the
// bottom of the current view (which leaves the offset where it was), then up
// to the clicked line, every step landing at the start of a screen line.
func (e *promptEditor) placeCursor(vline, x int) {
	top := e.ta.ScrollYOffset()
	e.ta.MoveToBegin()
	at := 0
	step := func(down bool) bool {
		before := e.pos()
		if down {
			e.ta.CursorDown()
		} else {
			e.ta.CursorUp()
		}
		return e.pos() != before
	}
	for at < top+e.textH-1 && step(true) {
		at++
	}
	for at > vline && step(false) {
		at--
	}
	li := e.ta.LineInfo()
	row := []rune(strings.Split(e.ta.Value(), "\n")[e.ta.Line()])
	start := min(li.StartColumn, len(row))
	end := min(li.StartColumn+li.Width, len(row))
	last := li.RowOffset >= li.Height-1
	col := start
	for col < end && ansi.StringWidth(string(row[start:col+1])) <= x {
		col++
	}
	if !last && col >= end && end > start {
		col = end - 1 // the wrap point shows at the start of the next line
	}
	e.ta.SetCursorColumn(col)
	e.sync()
}

// editorLayout is the editor as drawn: its lines, the row the text starts
// on, and the hint buttons.
type editorLayout struct {
	lines   []string
	textTop int
	hits    []schedHit
}

// View renders the editor at its size, one entry per screen line.
func (e *promptEditor) View() []string { return e.layout().lines }

func (e *promptEditor) layout() editorLayout {
	top, bottom := editorChrome(e.h)
	var l editorLayout
	if top > 0 {
		l.lines = append(l.lines, fleetTitleStyle.Render(truncateToWidth(e.title, e.w)))
	}
	l.textTop = len(l.lines)
	text := strings.Split(strings.TrimRight(e.ta.View(), "\n"), "\n")
	for len(text) < e.textH {
		text = append(text, "")
	}
	l.lines = append(l.lines, text[:e.textH]...)
	status := e.status()
	switch bottom {
	case 2:
		l.lines = append(l.lines, fleetDimStyle.Render(truncateToWidth(status, e.w)))
		l.addHints(e, "")
	case 1:
		l.addHints(e, e.shortStatus()+"  ")
	}
	return l
}

// addHints appends the hint line after prefix, recording a target per hint.
func (l *editorLayout) addHints(e *promptEditor, prefix string) {
	line := len(l.lines)
	var b strings.Builder
	col := 0
	if e.asking {
		prefix = "discard changes? "
		b.WriteString(fleetBlockedStyle.Render(prefix))
	} else {
		b.WriteString(fleetDimStyle.Render(prefix))
	}
	col = lipgloss.Width(prefix)
	sep := fleetDimStyle.Render(" · ")
	for i, h := range e.Hints() {
		r := renderHint(h)
		rw := lipgloss.Width(r)
		gap := 0
		if i > 0 {
			gap = lipgloss.Width(sep)
		}
		if col+gap+rw > e.w {
			break
		}
		if i > 0 {
			b.WriteString(sep)
			col += gap
		}
		l.hits = append(l.hits, schedHit{line: line, x0: col, x1: col + rw, kind: schedHitKey, key: editorHintKeys[h.key]})
		b.WriteString(r)
		col += rw
	}
	l.lines = append(l.lines, truncateToWidth(b.String(), e.w))
}

// editorHintKeys maps a hint to the key it presses.
var editorHintKeys = map[string]string{
	"^s": "ctrl+s", "esc": "esc", "^z": "ctrl+z", "^y": "ctrl+y", "y": "y", "n": "n",
}

// status is the full status line: line:col, lines, chars, dirty marker.
func (e *promptEditor) status() string {
	n := e.ta.LineCount()
	lines := "lines"
	if n == 1 {
		lines = "line"
	}
	s := fmt.Sprintf("%d:%d · %d %s · %d chars", e.ta.Line()+1, e.ta.Column()+1, n, lines, utf8.RuneCountInString(e.ta.Value()))
	if e.Dirty() {
		s += " · ● modified"
	}
	return s
}

// shortStatus is the status for a pane too short for its own line.
func (e *promptEditor) shortStatus() string {
	s := fmt.Sprintf("%d:%d", e.ta.Line()+1, e.ta.Column()+1)
	if e.Dirty() {
		s += " ●"
	}
	return s
}
