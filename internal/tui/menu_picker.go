package tui

import (
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
)

// pickItem is one selectable model of the picker. The price is computed once,
// when the picker opens, not per render.
type pickItem struct {
	value string
	note  string
	price string
	def   bool
	alias bool
}

// pickRowKind says what a displayed picker row is.
type pickRowKind int

const (
	pickHeader pickRowKind = iota
	pickModel
	pickTyped
)

// pickRow is one displayed line of the picker.
type pickRow struct {
	kind  pickRowKind
	text  string // header label
	item  int    // index into items (pickModel)
	typed string // value (pickTyped)
}

// modelPicker is the filterable model overlay.
type modelPicker struct {
	input  textinput.Model
	items  []pickItem
	rows   []pickRow
	cursor int // index into rows, always on a non-header row (-1 = none)
	err    string
}

// pickSource adapts items for fuzzy matching.
type pickSource []pickItem

func (p pickSource) String(i int) string { return strings.TrimSpace(p[i].value + " " + p[i].note) }
func (p pickSource) Len() int            { return len(p) }

// openPicker switches to the picker for the highlighted agent's backend.
func (m *MenuModel) openPicker() {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 120
	in.Focus()
	m.picker = modelPicker{input: in}
	m.picker.rebuild(*m)
	m.view = viewPicker
}

// rebuild recomputes items (with prices) from the menu's current choices and
// re-applies the query.
func (p *modelPicker) rebuild(m MenuModel) {
	choices := m.modelChoices()
	p.items = p.items[:0:0]
	for _, c := range choices {
		alias := strings.HasSuffix(c.Note, "alias")
		p.items = append(p.items, pickItem{
			value: c.Value,
			note:  c.Note,
			price: m.priceShort(m.opts.backend, c.Value),
			def:   c.Default,
			alias: alias,
		})
	}
	p.refilter(m, m.opts.model)
}

// refilter rebuilds the displayed rows from the query. When the query is empty
// the cursor lands on sel (the currently selected model) if listed.
func (p *modelPicker) refilter(m MenuModel, sel string) {
	q := strings.TrimSpace(p.input.Value())
	p.rows = p.rows[:0:0]
	p.err = ""
	if q == "" {
		p.group("DEFAULT", func(it pickItem) bool { return it.def })
		p.group("ALIASES", func(it pickItem) bool { return !it.def && it.alias })
		p.group("MODELS", func(it pickItem) bool { return !it.def && !it.alias })
	} else {
		matches := matchTerms(q, p.items)
		if len(matches) > 0 {
			p.rows = append(p.rows, pickRow{kind: pickHeader, text: "MATCHES"})
		}
		for _, idx := range matches {
			p.rows = append(p.rows, pickRow{kind: pickModel, item: idx})
		}
		exact := false
		for _, it := range p.items {
			exact = exact || it.value == q
		}
		if err := m.validateModel(m.opts.backend, q); err != nil {
			if len(matches) == 0 {
				p.err = err.Error()
			}
		} else if !exact {
			p.rows = append(p.rows, pickRow{kind: pickTyped, typed: q})
		}
	}
	p.cursor = -1
	for i, r := range p.rows {
		if r.kind == pickHeader {
			continue
		}
		if p.cursor < 0 {
			p.cursor = i
		}
		if q == "" && r.kind == pickModel && p.items[r.item].value == sel {
			p.cursor = i
			break
		}
	}
}

func (p *modelPicker) group(label string, in func(pickItem) bool) {
	start := len(p.rows)
	p.rows = append(p.rows, pickRow{kind: pickHeader, text: label})
	for i, it := range p.items {
		if in(it) {
			p.rows = append(p.rows, pickRow{kind: pickModel, item: i})
		}
	}
	if len(p.rows) == start+1 {
		p.rows = p.rows[:start]
	}
}

// matchTerms returns the indexes of items matching every whitespace-separated
// term of q (fuzzy, per term), best first by summed score. sahilm/fuzzy treats a
// space as a literal rune, so terms are matched separately.
func matchTerms(q string, items []pickItem) []int {
	var scores map[int]int
	for _, term := range strings.Fields(q) {
		hit := map[int]int{}
		for _, mt := range fuzzy.FindFrom(term, pickSource(items)) {
			if prev, ok := scores[mt.Index]; scores == nil || ok {
				hit[mt.Index] = prev + mt.Score
			}
		}
		scores = hit
	}
	out := make([]int, 0, len(scores))
	for idx := range scores {
		out = append(out, idx)
	}
	sort.Slice(out, func(a, b int) bool {
		if scores[out[a]] != scores[out[b]] {
			return scores[out[a]] > scores[out[b]]
		}
		return out[a] < out[b]
	})
	return out
}

// restore moves the cursor to the model row with the given value, if shown.
func (p *modelPicker) restore(value string) {
	for i, r := range p.rows {
		if r.kind == pickModel && p.items[r.item].value == value {
			p.cursor = i
			return
		}
	}
}

// move shifts the cursor by delta selectable rows.
func (p *modelPicker) move(delta int) {
	for i := p.cursor + delta; i >= 0 && i < len(p.rows); i += delta {
		if p.rows[i].kind != pickHeader {
			p.cursor = i
			return
		}
	}
}

// selected returns the value under the cursor.
func (p modelPicker) selected() (string, bool) {
	if p.cursor < 0 || p.cursor >= len(p.rows) {
		return "", false
	}
	r := p.rows[p.cursor]
	switch r.kind {
	case pickModel:
		return p.items[r.item].value, true
	case pickTyped:
		return r.typed, true
	}
	return "", false
}

// keyPicker handles keys in the model picker.
func (m MenuModel) keyPicker(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.view = viewSheet
		return m, nil
	case "up", "ctrl+p", "ctrl+k":
		m.picker.move(-1)
		return m, nil
	case "down", "ctrl+n", "ctrl+j":
		m.picker.move(1)
		return m, nil
	case "enter":
		if v, ok := m.picker.selected(); ok {
			m.opts.model = v
			m.view, m.errMsg = viewSheet, ""
		}
		return m, nil
	}
	in, cmd := m.picker.input.Update(msg)
	m.picker.input = in
	m.picker.refilter(m, m.opts.model)
	return m, cmd
}

// pickerLines renders the picker as exactly height lines of width cells.
func (m MenuModel) pickerLines(width, height int) []string {
	p := m.picker
	out := []string{
		truncW(menuHintKeyStyle.Render("> ")+p.input.View(), width),
	}
	for _, n := range m.modelNotes() {
		out = append(out, truncW(n, width))
	}
	if p.err != "" {
		out = append(out, mnWarnStyle.Render(truncW("can't use as typed: "+p.err, width)))
	}
	room := height - len(out)
	if room < 1 {
		return fitLines(out, height)
	}
	start := 0
	if len(p.rows) > room {
		at := max(p.cursor, 0)
		if at > 0 && p.rows[at-1].kind == pickHeader {
			at--
		}
		start = max(0, min(at-room/2, len(p.rows)-room))
	}
	priceW := 12
	if width < 50 {
		priceW = 0
	}
	for i := start; i < len(p.rows) && i < start+room; i++ {
		out = append(out, m.pickerRow(p, i, width, priceW))
	}
	if len(p.rows) == 0 {
		out = append(out, mnNoteStyle.Render("no models match"))
	}
	return fitLines(out, height)
}

// pickerRow renders one picker row, exactly width cells.
func (m MenuModel) pickerRow(p modelPicker, i, width, priceW int) string {
	r := p.rows[i]
	if r.kind == pickHeader {
		return mnSectionStyle.Render(truncW(r.text, width))
	}
	sel := i == p.cursor
	nameStyle := mnNameStyle
	if sel {
		nameStyle = mnNameSelStyle
	}
	bar := "  "
	if sel {
		bar = "▌ "
	}
	var value, note, price string
	switch r.kind {
	case pickTyped:
		value = "use " + r.typed + " as typed"
	default:
		it := p.items[r.item]
		value, note, price = it.value, it.note, it.price
		if it.def {
			note = strings.TrimSpace("default " + note)
		}
	}
	gap := 0
	if priceW > 0 {
		gap = priceW + 1
	}
	body := width - 2 - gap
	valW := ansi.StringWidth(value)
	noteTxt := ""
	if note != "" && valW+2 < body {
		noteTxt = " " + note
	}
	line := onSel(cursorActiveStyle, sel).Render(bar) +
		onSel(nameStyle, sel).Render(truncW(value, body)) +
		onSel(mnNoteStyle, sel).Render(truncW(noteTxt, max(0, body-valW)))
	if priceW > 0 {
		pad := width - ansi.StringWidth(line) - priceW
		line += onSel(mnDescStyle, sel).Render(strings.Repeat(" ", max(0, pad)) + padLeftW(price, priceW))
	}
	if g := width - ansi.StringWidth(line); g > 0 {
		line += onSel(mnDescStyle, sel).Render(strings.Repeat(" ", g))
	}
	return line
}

// padLeftW right-aligns s in w cells (truncating if longer).
func padLeftW(s string, w int) string {
	s = truncW(s, w)
	if n := w - ansi.StringWidth(s); n > 0 {
		s = strings.Repeat(" ", n) + s
	}
	return s
}

// fitLines pads or truncates lines to exactly n entries.
func fitLines(lines []string, n int) []string {
	if len(lines) > n {
		return lines[:max(n, 0)]
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return lines
}
