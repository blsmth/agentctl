package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/charmbracelet/x/ansi"
)

// In-place editing of an issue's title and body in the detail view.
//
// The draft holds only title and body; labels and assignees apply at once
// through their pickers. While a field has focus every single-letter
// binding is dead: keys go to the field, except esc (cancel, asking first
// when dirty), tab (switch field), ctrl+s (save), enter in the title (save)
// and ctrl+e in the body ($EDITOR).
//
// Saving re-reads the issue first. If a field being saved changed on GitHub
// since the detail loaded, the save is blocked with View theirs / Overwrite /
// Keep editing: a remote edit is never overwritten silently. A failed save
// keeps the draft and shows gh's error under the box.

// tkEditField is the field that has focus.
type tkEditField int

const (
	editTitle tkEditField = iota + 1
	editBody
)

// tkEdit is the draft of an in-place edit.
type tkEdit struct {
	active bool
	number int
	field  tkEditField
	title  textinput.Model
	body   textarea.Model

	origTitle, origBody string
	// baseUpdatedAt is the issue's updatedAt when the detail loaded; the
	// stale check compares the remote copy against it before saving.
	baseUpdatedAt time.Time

	saving bool
	err    string // inline error under the edit box (red)
}

// Messages of the edit flow.
type (
	// tkEditRemoteMsg is the issue re-read just before a save.
	tkEditRemoteMsg struct {
		remote tkDetailFetchedMsg
		force  bool
	}
	// tkEditSavedMsg reports the gh issue edit result.
	tkEditSavedMsg struct {
		number      int
		title, body string
		err         error
	}
	// tkEditorDoneMsg reports the $EDITOR handoff.
	tkEditorDoneMsg struct {
		path string
		err  error
	}
)

// normBody is the body compared for dirtiness: trailing whitespace on each
// line and at the end does not count as a change.
func normBody(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func (e tkEdit) titleDirty() bool { return strings.TrimSpace(e.title.Value()) != e.origTitle }
func (e tkEdit) bodyDirty() bool  { return normBody(e.body.Value()) != normBody(e.origBody) }

// dirty reports whether the draft differs from what was loaded.
func (e tkEdit) dirty() bool { return e.active && (e.titleDirty() || e.bodyDirty()) }

// newTitleInput builds the one-line title editor.
func newTitleInput(value string) textinput.Model {
	t := theme.Current()
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 256
	ti.Placeholder = "Issue title"
	bg := lipgloss.NewStyle().Background(lipgloss.Color(t.Surface))
	st := ti.Styles()
	st.Focused.Text = bg.Foreground(lipgloss.Color(t.Bright)).Bold(true)
	st.Blurred.Text = bg.Foreground(lipgloss.Color(t.Text))
	st.Focused.Placeholder = bg.Foreground(lipgloss.Color(t.Muted))
	st.Blurred.Placeholder = bg.Foreground(lipgloss.Color(t.Muted))
	ti.SetStyles(st)
	ti.SetValue(value)
	return ti
}

// newBodyArea builds the body editor holding the raw markdown.
func newBodyArea(value string) textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.MaxContentHeight = 100000
	ta.Placeholder = "Describe the issue (markdown)…"
	ta.Prompt = ""
	ta.SetValue(value)
	return ta
}

// startEdit begins editing the detail issue with field focused.
func (m TicketsModel) startEdit(field tkEditField) (tea.Model, tea.Cmd) {
	if m.repoSlug == "" {
		return m, m.setFlash("✗ editing needs a GitHub remote for this directory", true)
	}
	d := m.detail
	if d == nil {
		return m, nil
	}
	if m.edit.active && m.edit.number == d.number {
		return m, m.focusEditField(field)
	}
	m.edit = tkEdit{
		active:        true,
		number:        d.number,
		title:         newTitleInput(d.title),
		body:          newBodyArea(d.body),
		origTitle:     d.title,
		origBody:      d.body,
		baseUpdatedAt: d.updatedAt,
	}
	m.sizeEditor()
	m.edit.body.MoveToBegin()
	return m, m.focusEditField(field)
}

// focusEditField moves focus to field.
func (m *TicketsModel) focusEditField(field tkEditField) tea.Cmd {
	m.edit.field = field
	if field == editTitle {
		m.edit.body.Blur()
		m.edit.title.CursorEnd()
		return m.edit.title.Focus()
	}
	m.edit.title.Blur()
	return m.edit.body.Focus()
}

// sizeEditor fits the body editor to the detail layout.
func (m *TicketsModel) sizeEditor() {
	w, h := m.editorSize()
	m.edit.body.SetWidth(w)
	m.edit.body.SetHeight(h)
	// The field is a bar, a space and the input; keep one column for the
	// cursor past the last character.
	m.edit.title.SetWidth(m.titleInputWidth() - 2)
}

// tkModifiedMark is the dirty marker in the header; its width is always
// reserved so the title field does not jump when the draft turns dirty.
const tkModifiedMark = "  ● modified"

// titleInputWidth is the width of the title field in the edit header.
func (m TicketsModel) titleInputWidth() int {
	w := m.contentWidth() - lipgloss.Width("← back") - 2 -
		lipgloss.Width(fmt.Sprintf("#%d ", m.edit.number)) - 1 - // the field's left bar
		lipgloss.Width(tkModifiedMark) - 2 - lipgloss.Width(m.renderPill(true))
	if w < 10 {
		w = 10
	}
	return w
}

// tkEditChrome is the rows the edit box spends around the textarea: its
// top and bottom border and the message line. The box takes the body
// column's "Body ✎" line plus its body rows.
const tkEditChrome = 3

// editorSize is the textarea's inner width and height.
func (m TicketsModel) editorSize() (int, int) {
	lay := m.detailLayout()
	w := lay.leftW - 4 // border + one column of padding each side
	if w < 20 {
		w = 20
	}
	h := m.detailBodyRows() + 1 - tkEditChrome
	if h < 3 {
		h = 3
	}
	return w, h
}

// endEdit leaves edit mode, dropping the draft.
func (m *TicketsModel) endEdit() {
	m.edit = tkEdit{}
}

// updateEdit handles a key while a field has focus.
func (m TicketsModel) updateEdit(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.edit.dirty() {
			m.choiceModal = NewModal("Quit?", tkDiscardChoice("quit"), ModalSmall)
			return m, nil
		}
		return m.quit()
	case "esc":
		return m.cancelEdit()
	case "tab", "shift+tab":
		if m.edit.field == editTitle {
			return m, m.focusEditField(editBody)
		}
		return m, m.focusEditField(editTitle)
	case "ctrl+s":
		return m.saveEdit(false)
	case "ctrl+e":
		if m.edit.field == editBody {
			return m.openExternalEditor()
		}
	case "enter":
		if m.edit.field == editTitle {
			return m.saveEdit(false)
		}
	}
	return m.forwardToEditor(msg)
}

// forwardToEditor sends msg (a key or a paste) to the focused field.
func (m TicketsModel) forwardToEditor(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.edit.field == editTitle {
		m.edit.title, cmd = m.edit.title.Update(msg)
	} else {
		m.edit.body, cmd = m.edit.body.Update(msg)
	}
	if m.edit.err != "" && !m.edit.saving {
		if _, isKey := msg.(tea.KeyPressMsg); isKey && strings.Contains(m.edit.err, "title") {
			m.edit.err = "" // typing clears the empty-title complaint
		}
	}
	return m, cmd
}

// cancelEdit is esc: leave at once when clean, ask first when dirty.
func (m TicketsModel) cancelEdit() (tea.Model, tea.Cmd) {
	if m.edit.saving {
		return m, nil
	}
	if m.edit.dirty() {
		m.choiceModal = NewModal("Discard changes?", tkDiscardChoice("discard"), ModalSmall)
		return m, nil
	}
	m.endEdit()
	return m, nil
}

// tkDiscardChoice is the dirty-draft prompt; Keep editing is the default.
func tkDiscardChoice(kind string) tkChoiceModel {
	label, line := "Discard", "Your edits to this issue have not been saved."
	if kind == "quit" {
		label, line = "Discard & quit", "Quit and lose your unsaved edits?"
	}
	return tkChoiceModel{
		kind:  kind,
		lines: []string{popupBodyStyle.Render(line)},
		choices: []tkChoice{
			{label: "Keep editing", key: "k"},
			{label: label, key: "d", danger: true},
		},
		cancel: 0,
		chosen: -1,
	}
}

// saveEdit validates the draft and re-reads the issue before saving.
func (m TicketsModel) saveEdit(force bool) (tea.Model, tea.Cmd) {
	if m.edit.saving {
		return m, nil
	}
	if strings.TrimSpace(m.edit.title.Value()) == "" {
		m.edit.err = "✗ the title can't be empty"
		return m, m.focusEditField(editTitle)
	}
	if !m.edit.dirty() {
		m.endEdit()
		return m, m.setFlash("no changes to save", false)
	}
	m.edit.saving = true
	m.edit.err = ""
	slug, n := m.repoSlug, m.edit.number
	return m, func() tea.Msg { return tkEditRemoteMsg{remote: viewIssue(slug, n), force: force} }
}

// onEditRemote decides, from the re-read issue, whether the save may go on.
func (m TicketsModel) onEditRemote(msg tkEditRemoteMsg) (tea.Model, tea.Cmd) {
	if !m.edit.active || !m.edit.saving || msg.remote.number != m.edit.number {
		return m, nil
	}
	r := msg.remote
	if r.err != nil {
		m.edit.saving = false
		m.edit.err = fmt.Sprintf("✗ save failed: %s · ctrl+s to retry", firstNonBlankLine(r.err.Error()))
		return m, nil
	}
	if strings.EqualFold(r.state, "closed") {
		m.edit.saving = false
		m.edit.err = fmt.Sprintf("✗ #%d was closed on GitHub · your draft is kept", r.number)
		return m, nil
	}
	if !msg.force && m.editIsStale(r) {
		m.edit.saving = false
		m.choiceModal = NewModal("Edit conflict", m.staleChoice(r), ModalMedium)
		return m, nil
	}
	return m, m.saveCmd()
}

// editIsStale reports whether a field the draft would save changed on
// GitHub since the detail loaded.
func (m TicketsModel) editIsStale(r tkDetailFetchedMsg) bool {
	if r.updatedAt.Equal(m.edit.baseUpdatedAt) {
		return false
	}
	if m.edit.titleDirty() && r.title != m.edit.origTitle {
		return true
	}
	return m.edit.bodyDirty() && normBody(r.body) != normBody(m.edit.origBody)
}

// staleChoice is the conflict prompt for a remote change.
func (m TicketsModel) staleChoice(r tkDetailFetchedMsg) tkChoiceModel {
	t := theme.Current()
	var fields []string
	if m.edit.titleDirty() && r.title != m.edit.origTitle {
		fields = append(fields, "title")
	}
	if m.edit.bodyDirty() && normBody(r.body) != normBody(m.edit.origBody) {
		fields = append(fields, "body")
	}
	ago := "since you opened it"
	if !r.updatedAt.IsZero() {
		ago = fmtAge(tkNow().Sub(r.updatedAt))
	}
	c := tkChoiceModel{
		kind: "stale",
		lines: []string{
			fgStyle(t.Yellow).Bold(true).Render(fmt.Sprintf("Changed on GitHub %s:", ago)),
			popupBodyStyle.Render(fmt.Sprintf("Someone else edited the %s of #%d while you were editing.", strings.Join(fields, " and "), r.number)),
			popupDimStyle.Render("Overwrite replaces their version with yours."),
		},
		choices: []tkChoice{
			{label: "View theirs", key: "v"},
			{label: "Overwrite", key: "o", danger: true},
			{label: "Keep editing", key: "k"},
		},
		cancel: 2,
		chosen: -1,
	}
	c.focus = 2
	theirs := r
	c.theirs = &theirs
	return c
}

// saveCmd runs gh issue edit with the dirty fields of the draft.
func (m TicketsModel) saveCmd() tea.Cmd {
	slug, n := m.repoSlug, m.edit.number
	title := strings.TrimSpace(m.edit.title.Value())
	body := m.edit.body.Value()
	setTitle, setBody := m.edit.titleDirty(), m.edit.bodyDirty()
	return func() tea.Msg {
		args := []string{"issue", "edit", strconv.Itoa(n), "--repo", slug}
		if setTitle {
			args = append(args, "--title", title)
		}
		if setBody {
			f, err := os.CreateTemp("", "agentctl-issue-*.md")
			if err != nil {
				return tkEditSavedMsg{number: n, err: fmt.Errorf("writing the body: %w", err)}
			}
			defer func() { _ = os.Remove(f.Name()) }()
			if _, err := f.WriteString(body); err != nil {
				_ = f.Close()
				return tkEditSavedMsg{number: n, err: fmt.Errorf("writing the body: %w", err)}
			}
			if err := f.Close(); err != nil {
				return tkEditSavedMsg{number: n, err: fmt.Errorf("writing the body: %w", err)}
			}
			args = append(args, "--body-file", f.Name())
		}
		_, err := tkExec("gh", args...)
		return tkEditSavedMsg{number: n, title: title, body: body, err: err}
	}
}

// onEditSaved finishes a save: success leaves edit mode and refreshes the
// detail and list row; failure keeps the draft and shows gh's error.
func (m TicketsModel) onEditSaved(msg tkEditSavedMsg) (tea.Model, tea.Cmd) {
	if !m.edit.active || msg.number != m.edit.number {
		return m, nil
	}
	m.edit.saving = false
	if msg.err != nil {
		m.edit.err = fmt.Sprintf("✗ save failed: %s · ctrl+s to retry", firstNonBlankLine(msg.err.Error()))
		return m, nil
	}
	n := msg.number
	if m.detail != nil && m.detail.number == n {
		d := *m.detail
		d.title, d.body = msg.title, msg.body
		m.detail = &d
	}
	for i := range m.allIssues {
		if m.allIssues[i].Number == n {
			m.allIssues[i].Title = msg.title
		}
	}
	m.applyFilters()
	m.forgetPreview(n)
	m.endEdit()
	return m, tea.Batch(
		m.setFlash(fmt.Sprintf("saved #%d", n), false),
		tkFetchDetailRefreshCmd(m.repoSlug, n),
	)
}

// tkDetailRefreshedMsg is a detail re-read after a change (it updates the
// detail in place instead of opening it).
type tkDetailRefreshedMsg tkDetailFetchedMsg

func tkFetchDetailRefreshCmd(slug string, n int) tea.Cmd {
	return func() tea.Msg { return tkDetailRefreshedMsg(viewIssue(slug, n)) }
}

// onDetailRefreshed installs a re-read issue in the caches and views.
func (m TicketsModel) onDetailRefreshed(msg tkDetailRefreshedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, nil
	}
	d := tkDetailFetchedMsg(msg)
	m.cachePreview(&d)
	if m.detail != nil && m.detail.number == d.number && !m.edit.active {
		m.detail = &d
	}
	if m.previewDetail != nil && m.previewDetail.number == d.number {
		m.previewDetail = &d
	}
	for i := range m.allIssues {
		if m.allIssues[i].Number == d.number {
			m.allIssues[i].Title = d.title
			m.allIssues[i].Labels = d.labels
			m.allIssues[i].Assignees = d.assignees
		}
	}
	m.labelOptions = mergeStrings(m.labelOptions, labelNames(d.labels))
	m.applyFilters()
	return m, nil
}

// onStaleChoice acts on the conflict prompt.
func (m TicketsModel) onStaleChoice(c tkChoiceModel) (tea.Model, tea.Cmd) {
	switch c.picked() {
	case "o":
		m.edit.saving = true
		return m, m.saveCmd()
	case "v":
		if c.theirs != nil {
			m.helpModal = NewModal("Their version on GitHub", tkTheirsModel{theirs: *c.theirs, prompt: c}, ModalMedium)
		}
	}
	return m, nil
}

// tkEditorCmd builds the $EDITOR command for path, or nil when $EDITOR is
// unset. Tests replace it.
var tkEditorCmd = func(path string) *exec.Cmd {
	editor := strings.Fields(os.Getenv("EDITOR"))
	if len(editor) == 0 {
		return nil
	}
	return exec.Command(editor[0], append(editor[1:], path)...)
}

// openExternalEditor hands the body draft to $EDITOR on a temp file.
func (m TicketsModel) openExternalEditor() (tea.Model, tea.Cmd) {
	f, err := os.CreateTemp("", "agentctl-issue-*.md")
	if err != nil {
		m.edit.err = "✗ $EDITOR: " + err.Error()
		return m, nil
	}
	path := f.Name()
	_, werr := f.WriteString(m.edit.body.Value())
	cerr := f.Close()
	cmd := tkEditorCmd(path)
	if werr != nil || cerr != nil || cmd == nil {
		_ = os.Remove(path)
		if cmd == nil {
			return m, m.setFlash("set $EDITOR to edit the body in your editor", false)
		}
		m.edit.err = "✗ $EDITOR: couldn't write the draft"
		return m, nil
	}
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return tkEditorDoneMsg{path: path, err: err} })
}

// onEditorDone loads the file $EDITOR wrote back into the body draft.
func (m TicketsModel) onEditorDone(msg tkEditorDoneMsg) (tea.Model, tea.Cmd) {
	defer func() { _ = os.Remove(msg.path) }()
	if !m.edit.active {
		return m, nil
	}
	if msg.err != nil {
		m.edit.err = "✗ $EDITOR: " + firstNonBlankLine(msg.err.Error())
		return m, nil
	}
	b, err := os.ReadFile(msg.path)
	if err != nil {
		m.edit.err = "✗ $EDITOR: " + err.Error()
		return m, nil
	}
	m.edit.body.SetValue(strings.TrimRight(string(b), "\n"))
	return m, m.focusEditField(editBody)
}

// ── Rendering ────────────────────────────────────────────────────────────────

// renderEditTitleLine replaces the detail header while editing: the title
// as a full-width field on a subtle background, with a left bar that is the
// accent while it has focus, and "● modified" beside it only when the title
// changed.
func (m TicketsModel) renderEditTitleLine(width int) string {
	t := theme.Current()
	back := m.link("edit:cancel@back", "← back", fgStyle(t.Accent).Bold(true))
	num := fgStyle(t.Accent).Bold(true).Render(fmt.Sprintf("#%d ", m.edit.number))
	mark := strings.Repeat(" ", lipgloss.Width(tkModifiedMark))
	if m.edit.titleDirty() {
		mark = "  " + fgStyle(t.Yellow).Bold(true).Render("● modified")
	}
	pill := m.renderPill(true)
	inputW := m.titleInputWidth()
	bar := fgStyle(t.Overlay)
	if m.edit.field == editTitle {
		bar = fgStyle(t.Accent)
	}
	bg := lipgloss.NewStyle().Background(lipgloss.Color(t.Surface))
	input := ansi.Truncate(m.edit.title.View(), inputW-1, "")
	field := zoneMark("edit:focus-title", bar.Render("▌")+withBackground(" "+padTo(input, inputW-1, bg), t.Surface))
	line := back + "  " + num + field + mark + "  " + pill
	return ansi.Truncate(line, width, "")
}

// withBackground paints color behind s, including the cells its own
// styles reset (the text input's cursor cell renders unstyled).
func withBackground(s, color string) string {
	on, _, _ := strings.Cut(lipgloss.NewStyle().Background(lipgloss.Color(color)).Render("x"), "x")
	if on == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+on)
	s = strings.ReplaceAll(s, "\x1b[m", "\x1b[m"+on)
	return on + s + "\x1b[m"
}

// editButtons is [ Save ] [ Cancel ], shown in place of the actions while
// editing.
func (m TicketsModel) editButtons() string {
	t := theme.Current()
	return m.button("edit:save", "Save", fgStyle(t.Green).Bold(true)) + " " +
		m.button("edit:cancel", "Cancel", tkButtonText())
}

// renderEditBox renders the body editor and its message line (saving, or
// an error in red) in a column of the given width and height. "● modified"
// sits on the border only when the body changed.
func (m TicketsModel) renderEditBox(width, height int) []string {
	t := theme.Current()
	border := fgStyle(t.Overlay)
	if m.edit.field == editBody {
		border = fgStyle(t.Accent)
	}
	label := " Body · markdown "
	if m.edit.field == editBody {
		label = " Body · markdown · ctrl+e $EDITOR "
	}
	mark := ""
	if m.edit.bodyDirty() {
		mark = " ● modified "
	}
	inner := width - 2
	fill := inner - 1 - lipgloss.Width(label) - lipgloss.Width(mark) - 1
	if fill < 0 {
		fill = 0
	}
	top := border.Render("╭─") + border.Bold(true).Render(label) + border.Render(strings.Repeat("─", fill))
	if mark != "" {
		top += fgStyle(t.Yellow).Bold(true).Render(mark)
	}
	top += border.Render("─╮")

	lines := []string{top}
	areaLines := strings.Split(m.edit.body.View(), "\n")
	_, h := m.editorSize()
	for i := 0; i < h; i++ {
		l := ""
		if i < len(areaLines) {
			l = areaLines[i]
		}
		l = padTo(ansi.Truncate(l, inner-2, ""), inner-2, lipgloss.NewStyle())
		lines = append(lines, zoneMark("edit:focus-body", border.Render("│")+" "+l+" "+border.Render("│")))
	}
	lines = append(lines, border.Render("╰"+strings.Repeat("─", inner)+"╯"))

	switch {
	case m.edit.saving:
		lines = append(lines, fgStyle(t.Sky).Render("saving…"))
	case m.edit.err != "":
		lines = append(lines, ansi.Truncate(fgStyle(t.Red).Render(m.edit.err), width, "…"))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// editHintBar is the hint bar while editing.
func (m TicketsModel) editHintBar() string {
	hints := []tkHint{{key: "ctrl+s", label: "save", pin: true}}
	if m.edit.field == editTitle {
		hints = append(hints, tkHint{key: "enter", label: "save"})
	}
	hints = append(hints,
		tkHint{key: "tab", label: "switch field"},
		tkHint{key: "esc", label: "cancel", pin: true},
	)
	return m.renderHintBar(hints)
}

// clickEdit handles a click while editing. Edit controls work; anything
// else cancels a clean edit first and is ignored over a dirty one.
func (m TicketsModel) clickEdit(zone string, double bool) (tea.Model, tea.Cmd) {
	kind, arg := zoneParts(zone)
	if kind == "edit" {
		switch arg {
		case "save":
			return m.saveEdit(false)
		case "cancel":
			return m.cancelEdit()
		case "focus-title":
			return m, m.focusEditField(editTitle)
		case "focus-body":
			return m, m.focusEditField(editBody)
		}
	}
	if kind == "key" && strings.HasSuffix(zone, "@hint") {
		return m.handleKey(tkKey(arg)) // the edit hint bar: same as the key
	}
	if zone == "" || isRegionZone(zone) {
		return m, nil
	}
	if m.edit.dirty() || m.edit.saving {
		return m, nil // never discard a draft on a stray click
	}
	m.endEdit()
	return m.activate(zone, double)
}

// ── Their version ────────────────────────────────────────────────────────────

// tkTheirsModel shows the remote copy during a conflict; closing it returns
// to the conflict prompt.
type tkTheirsModel struct {
	theirs tkDetailFetchedMsg
	prompt tkChoiceModel
	done   bool
	hover  string
}

func (v tkTheirsModel) Init() tea.Cmd { return nil }

func (v tkTheirsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc", "enter", "q", "b":
			v.done = true
		}
	}
	return v, nil
}

func (v tkTheirsModel) withHover(id string) tea.Model {
	v.hover = id
	return v
}

// HintKeys implements ModalContent.
func (v tkTheirsModel) HintKeys() []string { return []string{"esc", "back to the choice"} }

func (v tkTheirsModel) View() tea.View {
	t := theme.Current()
	cw := modalContentWidth(ModalMedium) - 4
	var b strings.Builder
	b.WriteString(fgStyle(t.Bright).Bold(true).Render(ansi.Truncate(v.theirs.title, cw, "…")) + "\n")
	if !v.theirs.updatedAt.IsZero() {
		b.WriteString(popupDimStyle.Render("updated "+fmtAge(tkNow().Sub(v.theirs.updatedAt))) + "\n")
	}
	b.WriteString("\n")
	body := strings.Split(lipgloss.NewStyle().Width(cw).Render(v.theirs.body), "\n")
	const maxLines = 18
	if len(body) > maxLines {
		body = append(body[:maxLines], popupDimStyle.Render(fmt.Sprintf("… %d more lines", len(body)-maxLines)))
	}
	b.WriteString(fgStyle(t.Text).Render(strings.Join(body, "\n")) + "\n\n")
	// Back is the only button, so it has the focus.
	b.WriteString(zoneMark("mkey:esc@theirs", tkButton("Back", tkButtonText(), false, true)))
	return tea.NewView(b.String())
}
