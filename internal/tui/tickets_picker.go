package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/charmbracelet/x/ansi"
)

// Pickers: a checklist with type-to-filter for an issue's labels (l) and
// assignees (a), applied at once on enter, and a single-choice list for the
// label filter (f). Typing filters, ↑/↓ move, space toggles, enter applies,
// esc cancels; a click on a row is the same as moving there and pressing
// space (checklist) or enter (filter).

// Picker kinds.
const (
	pickLabels    = "labels"
	pickAssignees = "assignees"
	pickFilter    = "filter"
)

// tkPickerAny is the filter picker's "no filter" row.
const tkPickerAny = "any label"

// tkMeOption is the assignee row for the signed-in user when their login
// could not be looked up.
const tkMeOption = "@me"

// tkPickerModel is the picker overlay content.
type tkPickerModel struct {
	kind     string
	number   int // issue the checklist applies to (0 for the filter)
	options  []string
	selected map[string]bool
	orig     map[string]bool
	query    string
	cursor   int
	loading  bool
	note     string // shown under the list (fetch errors)
	me       string // signed-in login, when known
	done     bool
	applied  bool
	hover    string
}

// newPicker builds a picker over options with current selected.
func newPicker(kind string, number int, options, current []string) tkPickerModel {
	sel := map[string]bool{}
	orig := map[string]bool{}
	for _, c := range current {
		sel[c], orig[c] = true, true
	}
	p := tkPickerModel{kind: kind, number: number, selected: sel, orig: orig}
	p.options = mergeStrings(nil, append(append([]string(nil), current...), options...))
	if kind == pickFilter {
		p.options = append([]string{tkPickerAny}, p.options...)
		for i, o := range p.options {
			if sel[o] {
				p.cursor = i
			}
		}
	}
	return p
}

// mergeStrings returns the sorted union of a and b without blanks.
func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func labelNames(ls []ghLabel) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Name)
	}
	return out
}

func userLogins(us []ghUser) []string {
	out := make([]string, 0, len(us))
	for _, u := range us {
		out = append(out, u.Login)
	}
	return out
}

// visible is the options matching the typed query.
func (p tkPickerModel) visible() []string {
	if p.query == "" {
		return p.options
	}
	q := strings.ToLower(p.query)
	var out []string
	for _, o := range p.options {
		if strings.Contains(strings.ToLower(o), q) {
			out = append(out, o)
		}
	}
	return out
}

func (p tkPickerModel) Init() tea.Cmd { return nil }

func (p tkPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		p.query += strings.TrimSpace(msg.Content)
		p.cursor = 0
	case tea.KeyPressMsg:
		vis := p.visible()
		switch msg.String() {
		case "esc":
			p.done = true
		case "enter":
			if p.kind == pickFilter && len(vis) > 0 {
				p.selected = map[string]bool{vis[p.cursor]: true}
			}
			p.done, p.applied = true, true
		case "up", "ctrl+p":
			if p.cursor > 0 {
				p.cursor--
			}
		case "down", "ctrl+n":
			if p.cursor < len(vis)-1 {
				p.cursor++
			}
		case "space":
			if p.kind != pickFilter && len(vis) > 0 {
				o := vis[p.cursor]
				p.selected[o] = !p.selected[o]
			}
		case "backspace":
			if p.query != "" {
				r := []rune(p.query)
				p.query = string(r[:len(r)-1])
				p.cursor = 0
			}
		default:
			if msg.Text != "" && msg.Mod == 0 {
				p.query += msg.Text
				p.cursor = 0
			}
		}
		if n := len(p.visible()); p.cursor >= n {
			p.cursor = n - 1
		}
		if p.cursor < 0 {
			p.cursor = 0
		}
	}
	return p, nil
}

// withHover implements tkHoverable.
func (p tkPickerModel) withHover(id string) tea.Model {
	p.hover = id
	return p
}

// HintKeys implements ModalContent.
func (p tkPickerModel) HintKeys() []string {
	if p.kind == pickFilter {
		return []string{"↑↓", "move", "enter", "pick", "esc", "cancel"}
	}
	return []string{"space", "toggle", "enter", "apply", "esc", "cancel"}
}

// tkPickerRows is how many option rows the picker shows at once.
const tkPickerRows = 12

func (p tkPickerModel) View() tea.View {
	t := theme.Current()
	cw := modalContentWidth(ModalSmall) - 4
	var b strings.Builder

	q := p.query
	if q == "" {
		q = popupDimStyle.Render("type to filter…")
	} else {
		q = fgStyle(t.Bright).Render(q) + fgStyle(t.Accent).Render("▏")
	}
	b.WriteString(fgStyle(t.Accent).Render("› ") + q + "\n\n")

	vis := p.visible()
	start := 0
	if p.cursor >= tkPickerRows {
		start = p.cursor - tkPickerRows + 1
	}
	end := start + tkPickerRows
	if end > len(vis) {
		end = len(vis)
	}
	if len(vis) == 0 {
		msg := "no matches"
		if p.loading {
			msg = "loading…"
		}
		b.WriteString(popupDimStyle.Render("  "+msg) + "\n")
	}
	for i := start; i < end; i++ {
		o := vis[i]
		id := fmt.Sprintf("mrow:%d@picker", i)
		box := "  "
		if p.kind != pickFilter {
			box = "☐ "
			if p.selected[o] {
				box = fgStyle(t.Green).Render("☑ ")
			}
		} else if p.selected[o] || (o == tkPickerAny && len(p.selected) == 0) {
			box = fgStyle(t.Green).Render("● ")
		}
		name := o
		st := fgStyle(t.Text)
		// Unchecked labels keep their colour; checked rows read in the
		// normal foreground next to their green tick.
		if (p.kind == pickLabels && !p.selected[o]) || (p.kind == pickFilter && o != tkPickerAny) {
			st = fgStyle(labelColor(o))
		}
		if p.kind == pickAssignees && o != tkMeOption {
			name = "@" + o
			if o == p.me {
				name += popupDimStyle.Render(" (you)")
			}
		}
		cur := "  "
		if i == p.cursor {
			cur = cursorActiveStyle.Render("▸ ")
			st = st.Bold(true)
		}
		if p.hover == id {
			st = st.Underline(true)
		}
		row := cur + box + st.Render(ansi.Truncate(name, cw-6, "…"))
		b.WriteString(zoneMark(id, padTo(row, cw, lipgloss.NewStyle())) + "\n")
	}
	if len(vis) > end {
		b.WriteString(popupDimStyle.Render(fmt.Sprintf("  … %d more", len(vis)-end)) + "\n")
	}
	if p.loading && len(vis) > 0 {
		b.WriteString(popupDimStyle.Render("  loading more…") + "\n")
	}
	if p.note != "" {
		b.WriteString(fgStyle(t.Red).Render(ansi.Truncate(p.note, cw, "…")) + "\n")
	}
	b.WriteString("\n")
	apply := "Apply"
	if p.kind == pickFilter {
		apply = "Pick"
	}
	// enter applies, so Apply is the focused button.
	b.WriteString(zoneMark("mkey:enter@picker", tkButton(apply, tkButtonText(), false, true)) + "  " +
		zoneMark("mkey:esc@picker", tkButton("Cancel", tkButtonText(), false, p.hover == "mkey:esc@picker")))
	return tea.NewView(b.String())
}

// changes returns what to add and remove relative to the starting set.
func (p tkPickerModel) changes() (add, remove []string) {
	for o, on := range p.selected {
		if on && !p.orig[o] {
			add = append(add, o)
		}
	}
	for o := range p.orig {
		if !p.selected[o] {
			remove = append(remove, o)
		}
	}
	sort.Strings(add)
	sort.Strings(remove)
	return add, remove
}

// clickRow is a click on visible row i: space on it, or enter for the filter.
func (p tkPickerModel) clickRow(i int) tkPickerModel {
	vis := p.visible()
	if i < 0 || i >= len(vis) {
		return p
	}
	p.cursor = i
	if p.kind == pickFilter {
		p.selected = map[string]bool{vis[i]: true}
		p.done, p.applied = true, true
		return p
	}
	p.selected[vis[i]] = !p.selected[vis[i]]
	return p
}

// ── Messages and commands ────────────────────────────────────────────────────

// tkPickerOptionsMsg delivers fetched options for an open picker.
type tkPickerOptionsMsg struct {
	kind    string
	options []string
	me      string
	err     error
}

// tkPickerAppliedMsg reports an applied label/assignee change.
type tkPickerAppliedMsg struct {
	kind   string
	number int
	err    error
}

// tkFetchRepoLabelsCmd lists the repo's labels.
func tkFetchRepoLabelsCmd(slug string) tea.Cmd {
	return func() tea.Msg {
		out, err := tkExec("gh", "label", "list", "--repo", slug, "--limit", "200", "--json", "name")
		if err != nil {
			return tkPickerOptionsMsg{kind: pickLabels, err: err}
		}
		var ls []ghLabel
		if err := json.Unmarshal(out, &ls); err != nil {
			return tkPickerOptionsMsg{kind: pickLabels, err: err}
		}
		return tkPickerOptionsMsg{kind: pickLabels, options: labelNames(ls)}
	}
}

// tkFetchAssigneesCmd lists the repo's assignable users and the signed-in
// login.
func tkFetchAssigneesCmd(slug string) tea.Cmd {
	return func() tea.Msg {
		out, err := tkExec("gh", "repo", "view", slug, "--json", "assignableUsers")
		if err != nil {
			return tkPickerOptionsMsg{kind: pickAssignees, err: err}
		}
		var v struct {
			AssignableUsers []ghUser `json:"assignableUsers"`
		}
		if err := json.Unmarshal(out, &v); err != nil {
			return tkPickerOptionsMsg{kind: pickAssignees, err: err}
		}
		me := ""
		if who, err := tkExec("gh", "api", "user", "--jq", ".login"); err == nil {
			me = strings.TrimSpace(string(who))
		}
		return tkPickerOptionsMsg{kind: pickAssignees, options: userLogins(v.AssignableUsers), me: me}
	}
}

// tkApplyPickerCmd applies a checklist's changes with gh issue edit.
func tkApplyPickerCmd(slug string, p tkPickerModel) tea.Cmd {
	add, remove := p.changes()
	flagAdd, flagRemove := "--add-label", "--remove-label"
	if p.kind == pickAssignees {
		flagAdd, flagRemove = "--add-assignee", "--remove-assignee"
	}
	return func() tea.Msg {
		args := []string{"issue", "edit", strconv.Itoa(p.number), "--repo", slug}
		if len(add) > 0 {
			args = append(args, flagAdd, strings.Join(add, ","))
		}
		if len(remove) > 0 {
			args = append(args, flagRemove, strings.Join(remove, ","))
		}
		_, err := tkExec("gh", args...)
		return tkPickerAppliedMsg{kind: p.kind, number: p.number, err: err}
	}
}

// ── Model integration ───────────────────────────────────────────────────────

// pickerTarget is the issue the l/a pickers act on and its current values.
func (m TicketsModel) pickerTarget() (n int, labels []ghLabel, assignees []ghUser, ok bool) {
	if m.viewMode == tkViewDetail && m.detail != nil {
		return m.detail.number, m.detail.labels, m.detail.assignees, true
	}
	iss, ok := m.selectedIssue()
	if !ok {
		return 0, nil, nil, false
	}
	if d, cached := m.detailCache[iss.Number]; cached {
		return iss.Number, d.labels, d.assignees, true
	}
	return iss.Number, iss.Labels, iss.Assignees, true
}

// openLabelPicker is l: edit the current issue's labels.
func (m TicketsModel) openLabelPicker() (tea.Model, tea.Cmd) {
	if m.repoSlug == "" {
		return m, m.setFlash("✗ editing needs a GitHub remote for this directory", true)
	}
	n, labels, _, ok := m.pickerTarget()
	if !ok {
		return m, nil
	}
	p := newPicker(pickLabels, n, m.labelOptions, labelNames(labels))
	p.loading = true
	m.pickerModal = NewModal(fmt.Sprintf("Labels  #%d", n), p, ModalSmall)
	return m, tkFetchRepoLabelsCmd(m.repoSlug)
}

// openAssignPicker is a: edit the current issue's assignees.
func (m TicketsModel) openAssignPicker() (tea.Model, tea.Cmd) {
	if m.repoSlug == "" {
		return m, m.setFlash("✗ editing needs a GitHub remote for this directory", true)
	}
	n, _, assignees, ok := m.pickerTarget()
	if !ok {
		return m, nil
	}
	p := newPicker(pickAssignees, n, nil, userLogins(assignees))
	p.loading = true
	m.pickerModal = NewModal(fmt.Sprintf("Assignees  #%d", n), p, ModalSmall)
	return m, tkFetchAssigneesCmd(m.repoSlug)
}

// openFilterPicker is f and the label chip: pick the label to filter by.
func (m TicketsModel) openFilterPicker() (tea.Model, tea.Cmd) {
	var cur []string
	if m.filterLabel != "" {
		cur = []string{m.filterLabel}
	}
	p := newPicker(pickFilter, 0, m.labelOptions, cur)
	m.pickerModal = NewModal("Filter by label", p, ModalSmall)
	return m, nil
}

// onPickerOptions merges fetched options into the open picker.
func (m TicketsModel) onPickerOptions(msg tkPickerOptionsMsg) (tea.Model, tea.Cmd) {
	p, ok := m.pickerModal.Content.(tkPickerModel)
	if !ok || !m.pickerModal.Visible || p.kind != msg.kind {
		return m, nil
	}
	p.loading = false
	if msg.err != nil {
		p.note = "✗ couldn't load the full list: " + firstNonBlankLine(msg.err.Error())
		if p.kind == pickAssignees && !containsString(p.options, tkMeOption) {
			p.options = append([]string{tkMeOption}, p.options...)
		}
	} else {
		p.options = mergeStrings(p.options, msg.options)
		p.me = msg.me
	}
	m.pickerModal.Content = p
	return m, nil
}

// checkPickerDone acts on a closed picker.
func (m TicketsModel) checkPickerDone() (tea.Model, tea.Cmd) {
	p, ok := m.pickerModal.Content.(tkPickerModel)
	if !ok || !p.done {
		return m, nil
	}
	m.pickerModal.Visible = false
	if !p.applied {
		return m, nil
	}
	if p.kind == pickFilter {
		label := ""
		for o, on := range p.selected {
			if on && o != tkPickerAny {
				label = o
			}
		}
		m.setLabelFilter(label)
		if m.viewMode == tkViewDetail && !m.edit.active {
			m.backToList()
		}
		return m, m.previewCmdForCursor()
	}
	add, remove := p.changes()
	if len(add) == 0 && len(remove) == 0 {
		return m, nil
	}
	return m, tkApplyPickerCmd(m.repoSlug, p)
}

// onPickerApplied refreshes the issue after a label/assignee change.
func (m TicketsModel) onPickerApplied(msg tkPickerAppliedMsg) (tea.Model, tea.Cmd) {
	what := "labels"
	if msg.kind == pickAssignees {
		what = "assignees"
	}
	if msg.err != nil {
		return m, m.setFlash(fmt.Sprintf("✗ updating %s of #%d failed: %s", what, msg.number, firstNonBlankLine(msg.err.Error())), true)
	}
	m.forgetPreview(msg.number)
	return m, tea.Batch(
		m.setFlash(fmt.Sprintf("updated %s of #%d", what, msg.number), false),
		tkFetchDetailRefreshCmd(m.repoSlug, msg.number),
	)
}
