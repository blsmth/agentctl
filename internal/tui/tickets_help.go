package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/theme"
)

// tkHelpModel is the ? overlay: every key and mouse gesture of the tickets
// browser. Any of esc, ?, q or enter closes it.
type tkHelpModel struct {
	done  bool
	hover string
}

func (h tkHelpModel) Init() tea.Cmd { return nil }

func (h tkHelpModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc", "?", "q", "enter":
			h.done = true
		}
	}
	return h, nil
}

// withHover implements tkHoverable.
func (h tkHelpModel) withHover(id string) tea.Model {
	h.hover = id
	return h
}

// HintKeys implements ModalContent.
func (h tkHelpModel) HintKeys() []string { return []string{"esc / ?", "close"} }

// tkHelpKeys are the keyboard bindings, as key/description pairs. The
// overlay lays them out two per row, so descriptions stay short.
var tkHelpKeys = [][2]string{
	{"j/k ↑↓", "move / scroll"},
	{"J/K", "scroll preview"},
	{"enter", "open detail"},
	{"esc", "back / clear / quit"},
	{"e", "edit in place"},
	{"E", "edit in browser"},
	{"l", "edit labels"},
	{"a", "edit assignees"},
	{"f", "filter by label"},
	{"/", "search titles"},
	{"s", "cycle sort"},
	{"S", "open/closed/all"},
	{"c", "new issue"},
	{"d", "close (confirms)"},
	{"o", "open in browser"},
	{"p", "open linked PR"},
	{"t", "triage agent"},
	{"T", "dispatch agent…"},
	{"y", "copy issue URL"},
	{"P", "autopilot on/off"},
	{"tab", "list / preview"},
	{"q", "quit"},
}

// tkHelpEdit are the keys while a title or body field has focus.
var tkHelpEdit = [][2]string{
	{"ctrl+s", "save"},
	{"enter", "save (title)"},
	{"tab", "title ⇄ body"},
	{"ctrl+e", "body in $EDITOR"},
	{"esc", "cancel (asks)"},
}

// tkHelpMouse are the mouse gestures.
var tkHelpMouse = [][2]string{
	{"click", "select / press"},
	{"dbl-click", "open a row"},
	{"wheel", "scroll that pane"},
	{"label", "filter by it"},
	{"# / AGE", "sort by column"},
	{"#N↗", "open the PR"},
	{"✎ / dbl", "edit title/body"},
	{"outside", "cancel a dialog"},
}

// helpGrid renders pairs two per row.
func helpGrid(pairs [][2]string, keySt, descSt lipgloss.Style, colW int) string {
	var b strings.Builder
	for i := 0; i < len(pairs); i += 2 {
		left := keySt.Render(pairs[i][0]) + descSt.Render(pairs[i][1])
		line := padTo(left, colW, lipgloss.NewStyle())
		if i+1 < len(pairs) {
			line += keySt.Render(pairs[i+1][0]) + descSt.Render(pairs[i+1][1])
		}
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

func (h tkHelpModel) View() tea.View {
	t := theme.Current()
	cw := modalContentWidth(ModalMedium) - 4
	colW := (cw - 2) / 2
	keySt := fgStyle(t.Accent).Bold(true).Width(10)
	descSt := fgStyle(t.Text)
	section := fgStyle(t.Sky).Bold(true)

	var b strings.Builder
	b.WriteString(section.Render("Keyboard") + "\n")
	b.WriteString(helpGrid(tkHelpKeys, keySt, descSt, colW))
	b.WriteString(section.Render("While editing") + "\n")
	b.WriteString(helpGrid(tkHelpEdit, keySt, descSt, colW))
	b.WriteString(section.Render("Mouse") + "\n")
	b.WriteString(helpGrid(tkHelpMouse, keySt, descSt, colW))
	note := "Native text selection: hold Shift (Option in iTerm2) while dragging — the app has the mouse."
	b.WriteString("\n" + lipgloss.NewStyle().Width(cw).Foreground(lipgloss.Color(t.Muted)).Italic(true).Render(note) + "\n\n")

	// Close is the only button, so it has the focus.
	b.WriteString(zoneMark("mkey:esc@help", tkButton("Close", tkButtonText(), false, true)))
	return tea.NewView(b.String())
}
