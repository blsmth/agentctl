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

// tkHelpKeys are the keyboard bindings, as key/description pairs.
var tkHelpKeys = [][2]string{
	{"j/k ↑/↓", "move / scroll"},
	{"J/K", "scroll the preview"},
	{"enter", "open detail"},
	{"esc", "back / clear search / quit"},
	{"e", "edit in the browser"},
	{"d", "close issue (confirms)"},
	{"a", "assign yourself"},
	{"c", "new issue"},
	{"l", "cycle label filter"},
	{"s", "cycle sort"},
	{"S", "cycle open / closed / all"},
	{"/", "search titles"},
	{"o", "open issue in browser"},
	{"p", "open linked PR"},
	{"y", "copy issue URL (tmux buffer)"},
	{"t / T", "triage / dispatch an agent"},
	{"tab", "focus list / preview"},
	{"P", "autopilot on (confirms) / off"},
	{"q", "quit"},
}

// tkHelpMouse are the mouse gestures.
var tkHelpMouse = [][2]string{
	{"click", "select a row, press a button or chip"},
	{"double-click", "open a row's detail"},
	{"wheel", "scroll the pane under the pointer"},
	{"click a label", "filter by that label"},
	{"click # / AGE", "sort by that column"},
	{"click #N↗", "open the linked PR"},
	{"click outside", "cancel a dialog"},
}

func (h tkHelpModel) View() tea.View {
	t := theme.Current()
	keySt := fgStyle(t.Accent).Bold(true).Width(16)
	descSt := fgStyle(t.Text)
	section := fgStyle(t.Sky).Bold(true)

	var b strings.Builder
	b.WriteString(section.Render("Keyboard") + "\n")
	for _, kv := range tkHelpKeys {
		b.WriteString("  " + keySt.Render(kv[0]) + descSt.Render(kv[1]) + "\n")
	}
	b.WriteString("\n" + section.Render("Mouse") + "\n")
	for _, kv := range tkHelpMouse {
		b.WriteString("  " + keySt.Render(kv[0]) + descSt.Render(kv[1]) + "\n")
	}
	note := "To select text natively, hold Shift (Option in iTerm2) while dragging: the app has the mouse."
	b.WriteString("\n" + lipgloss.NewStyle().Width(modalContentWidth(ModalMedium)).Foreground(lipgloss.Color(t.Muted)).Italic(true).Render(note) + "\n\n")

	st := btnNormalStyle
	if h.hover == "mkey:esc@help" {
		st = st.Reverse(true)
	}
	b.WriteString(zoneMark("mkey:esc@help", st.Render("[ Close ]")))
	return tea.NewView(b.String())
}
