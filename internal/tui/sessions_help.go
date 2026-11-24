package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/theme"
)

// sessHelpModel is the ? overlay: every key and mouse gesture of the sessions
// browser. Any of esc, ?, q or enter closes it.
type sessHelpModel struct {
	hover string
	// height is the terminal height: short panes drop the note, then the
	// mouse section, so the Close button always fits on screen.
	height int
}

func (h sessHelpModel) Init() tea.Cmd { return nil }

func (h sessHelpModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return h, nil }

// HintKeys implements ModalContent.
func (h sessHelpModel) HintKeys() []string { return []string{"esc / ?", "close"} }

var sessHelpKeys = [][2]string{
	{"j/k ↑↓", "move / scroll"},
	{"J/K", "scroll preview"},
	{"enter", "open detail"},
	{"r", "resume (forks)"},
	{"l", "open log"},
	{"t", "turn metrics"},
	{"a", "cycle agent"},
	{"p", "cycle project"},
	{"/", "search"},
	{"esc", "back / clear / quit"},
	{"?", "this help"},
	{"q", "quit"},
}

var sessHelpMouse = [][2]string{
	{"click", "select / press"},
	{"dbl-click", "open detail"},
	{"wheel", "scroll that pane"},
	{"chips", "cycle a filter"},
	{"×", "clear a filter"},
	{"hints", "run that key"},
	{"outside", "close this"},
}

func (h sessHelpModel) View() tea.View {
	t := theme.Current()
	cw := modalContentWidth(ModalMedium) - 4
	colW := (cw - 2) / 2
	keySt := fgStyle(t.Accent).Bold(true).Width(10)
	descSt := fgStyle(t.Text)
	section := fgStyle(t.Sky).Bold(true)

	var b strings.Builder
	b.WriteString(section.Render("Keyboard") + "\n")
	b.WriteString(helpGrid(sessHelpKeys, keySt, descSt, colW))
	if h.height == 0 || h.height >= 22 {
		b.WriteString("\n" + section.Render("Mouse") + "\n")
		b.WriteString(helpGrid(sessHelpMouse, keySt, descSt, colW))
	}
	if h.height == 0 || h.height >= 28 {
		note := "Native text selection: hold Shift (Option in iTerm2) while dragging — the app has the mouse."
		b.WriteString("\n" + lipgloss.NewStyle().Width(cw).Foreground(lipgloss.Color(t.Muted)).Italic(true).Render(note) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(zoneMark("mkey:esc@help", tkButton("Close", tkButtonText(), false, true)))
	return tea.NewView(b.String())
}

// helpModal is the overlay as a Modal, with the pointer's hover applied.
func (m SessionsModel) helpModal() Modal {
	return NewModal("Sessions: keys & gestures", sessHelpModel{hover: m.hover, height: m.termHeight}, ModalMedium)
}
