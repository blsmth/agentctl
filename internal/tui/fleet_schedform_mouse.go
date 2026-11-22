package tui

import (
	"math"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Mouse support for the Schedules form. The form's renderer records a click
// target for every control it draws (schedFormLayout), so hit testing reads
// the very layout on screen and cannot drift from it. Every change a click
// makes goes through schedFormChanged, the tail of the key path.

// schedHitKind is what a click on a target does.
type schedHitKind int

const (
	schedHitFocus   schedHitKind = iota // focus the field
	schedHitNext                        // focus, then step the cycler forward
	schedHitPrev                        // focus, then step the cycler back
	schedHitSegment                     // focus, then choose segment arg
	schedHitDay                         // focus, then toggle day chip arg
	schedHitToggle                      // focus, then flip it (Advanced, Worktree)
	schedHitEditor                      // open the prompt in $EDITOR
	schedHitKey                         // press key (the review's y / n)
)

// schedHitEnd is an x1 that reaches the end of the line.
const schedHitEnd = math.MaxInt / 2

// schedHit is one click target: columns [x0, x1) of a screen line of the
// form, both relative to where the form is drawn.
type schedHit struct {
	line   int
	x0, x1 int
	stop   schedStop
	kind   schedHitKind
	arg    int
	key    string
}

// schedLayout is the form as drawn, one entry per screen line, with its
// click targets.
type schedLayout struct {
	lines []string
	hits  []schedHit
}

// add appends lines, one entry per screen line.
func (l *schedLayout) add(s ...string) {
	for _, v := range s {
		l.lines = append(l.lines, strings.Split(v, "\n")...)
	}
}

// scroll keeps line focus inside h lines, as scrollTo does, moving the
// targets with their lines and dropping the ones scrolled out.
func (l *schedLayout) scroll(focus, h int) {
	start := scrollStart(len(l.lines), focus, h)
	if start < 0 {
		return
	}
	l.lines = l.lines[start : start+h]
	hits := l.hits[:0]
	for _, ht := range l.hits {
		ht.line -= start
		if ht.line >= 0 && ht.line < h {
			hits = append(hits, ht)
		}
	}
	l.hits = hits
}

// at is the first target under column x of line y.
func (l schedLayout) at(x, y int) (schedHit, bool) {
	for _, ht := range l.hits {
		if ht.line == y && x >= ht.x0 && x < ht.x1 {
			return ht, true
		}
	}
	return schedHit{}, false
}

// schedFormArea is where the open form is drawn: the screen cell of its first
// line (x0, y0), the width and height it is rendered at, and how many of its
// lines fit. It mirrors withSchedPane (form beside the list) and renderList
// (form in place of the list), both of which draw it through fitPane: one
// blank line, then the form, indented.
func (m FleetModel) schedFormArea() (x0, y0, w, h, rows int, ok bool) {
	if m.schedForm == nil || !m.showSchedules {
		return 0, 0, 0, 0, 0, false
	}
	if m.schedFormReplacesList() {
		if _, isStatus := m.renderStatusOrEmpty(); isStatus {
			return 0, 0, 0, 0, 0, false
		}
		visible := m.visibleLines()
		return 2, m.fleetListTop() + 1, m.contentWidth() - 4, visible - 1, visible - 1, true
	}
	pw := m.schedPaneWidth()
	if pw == 0 || !m.schedPaneActive() {
		return 0, 0, 0, 0, 0, false
	}
	total := len(strings.Split(m.renderList(), "\n"))
	return m.contentWidth() + 2, 1, pw, total - 2, total - 1, true
}

// schedFormHitAt is the form target under screen cell (x, y).
func (m FleetModel) schedFormHitAt(x, y int) (schedHit, bool) {
	x0, y0, w, h, rows, ok := m.schedFormArea()
	if !ok {
		return schedHit{}, false
	}
	col, row := x-x0, y-y0
	if row < 0 || row >= rows || col < 0 || col >= w {
		return schedHit{}, false
	}
	return m.schedFormLayout(w, h).at(col, row)
}

// handleSchedFormClick is a mouse click while the form is open: a form
// target or a footer hint acts, anything else is ignored as before.
func (m *FleetModel) handleSchedFormClick(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	if key, ok := m.schedFooterHintAt(msg.X, msg.Y); ok {
		return m.handleSchedFormKey(schedSynthKey(key))
	}
	ht, ok := m.schedFormHitAt(msg.X, msg.Y)
	if !ok {
		return nil
	}
	return m.schedFormClick(ht)
}

// handleSchedFormWheel steps the focused cycler (or project) under the
// pointer; false when the wheel is not over one, so the list scrolls.
func (m *FleetModel) handleSchedFormWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	f := m.schedForm
	if f.review || f.saving || f.discard {
		return nil, false
	}
	ht, ok := m.schedFormHitAt(msg.X, msg.Y)
	if !ok || ht.stop != f.focus || (f.cyclerFor(ht.stop) == nil && ht.stop != stopDir) {
		return nil, false
	}
	switch msg.Button {
	case tea.MouseWheelUp:
		ht.kind = schedHitPrev
	case tea.MouseWheelDown:
		ht.kind = schedHitNext
	default:
		return nil, false
	}
	return m.schedFormClick(ht), true
}

// schedFormClick runs a target: it focuses the field, makes the change and
// hands it to schedFormChanged, as the key path does.
func (m *FleetModel) schedFormClick(ht schedHit) tea.Cmd {
	f := m.schedForm
	if ht.kind == schedHitKey {
		return m.handleSchedFormKey(schedSynthKey(ht.key))
	}
	if f.saving || f.review || f.discard {
		return nil
	}
	f.err = ""
	f.focusStop(ht.stop)
	if f.focus != ht.stop {
		return nil
	}
	before := f.state()
	switch ht.kind {
	case schedHitEditor:
		return m.schedOpenEditor()
	case schedHitNext, schedHitPrev:
		d := 1
		if ht.kind == schedHitPrev {
			d = -1
		}
		if ht.stop == stopDir {
			if n := len(f.dirs); n > 0 {
				f.dirIdx = (f.dirIdx + d + n) % n
			}
		} else if c := f.cyclerFor(ht.stop); c != nil {
			c.step(d)
		}
	case schedHitSegment:
		switch ht.stop {
		case stopKind:
			f.setKind(ht.arg)
		case stopOverlap:
			f.overlap = ht.arg
		case stopCatchup:
			f.catchup = ht.arg
		}
	case schedHitDay:
		f.dayCur = ht.arg
		f.days[ht.arg] = !f.days[ht.arg]
		f.schedTouched = true
	case schedHitToggle:
		switch ht.stop {
		case stopAdvanced:
			f.advOpen = !f.advOpen
		case stopWorktree:
			f.worktree = !f.worktree
		}
	}
	return m.schedFormChanged(before, nil)
}

// schedFooterKeys maps the form's footer hints to the key each one presses.
var schedFooterKeys = map[string]string{
	"^s": "ctrl+s", "tab": "tab", "esc": "esc", "y": "y", "n": "n",
}

// schedFooterHintAt is the key of the footer hint under (x, y) while the form
// is open. The hints are found in the rendered hint bar, so dropped or
// centred hints are hit where they are drawn.
func (m FleetModel) schedFooterHintAt(x, y int) (string, bool) {
	list := strings.Split(m.renderList(), "\n")
	if y != len(list)-1 || x >= m.contentWidth() {
		return "", false
	}
	plain := ansi.Strip(list[y])
	from := 0
	for _, h := range m.footerHints() {
		text := h.key + " " + h.label
		i := strings.Index(plain[from:], text)
		if i < 0 {
			continue
		}
		start := ansi.StringWidth(plain[:from+i])
		end := start + ansi.StringWidth(text)
		from += i + len(text)
		if key, ok := schedFooterKeys[h.key]; ok && x >= start && x < end {
			return key, true
		}
	}
	return "", false
}

// schedSynthKey is the key press a click stands for.
func schedSynthKey(k string) tea.KeyPressMsg {
	switch k {
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}
