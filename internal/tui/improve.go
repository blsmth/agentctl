package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/theme"
)

// ImproveResult is the output of the improve popup.
type ImproveResult struct {
	Cancelled bool
	// Theme is the optional improvement focus; empty asks for a general review.
	Theme string
}

// selectedStyle is the style applied to the focused improve-popup prompt.
var selectedStyle lipgloss.Style

// initImproveStyles initialises improve-popup styles from the active theme.
func initImproveStyles() {
	t := theme.Current()
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true)
}

// ImproveModel is the Bubble Tea model for the improve popup: one optional
// focus line. Enter on an empty line is a general review.
type ImproveModel struct {
	agent     string
	textInput textinput.Model
	result    ImproveResult
}

// NewImproveModel creates the model for the improve popup.
func NewImproveModel(agentName string) ImproveModel {
	ti := textinput.New()
	ti.Placeholder = "optional, e.g. better commit messages"
	ti.CharLimit = 200
	ti.SetWidth(34)
	s := ti.Styles()
	s.Focused.Prompt = selectedStyle
	s.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	ti.SetStyles(s)
	ti.Focus()

	return ImproveModel{
		agent:     agentName,
		textInput: ti,
		result:    ImproveResult{Cancelled: true},
	}
}

// PopupSize returns the tmux display-popup dimensions for the improve popup.
func (m ImproveModel) PopupSize() PopupSize {
	return PopupDimensions(m.View().Content)
}

// Result returns the popup's output after it quits.
func (m ImproveModel) Result() ImproveResult {
	return m.result
}

func (m ImproveModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m ImproveModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc", "ctrl+c":
			return m, tea.Quit
		case "enter":
			m.result = ImproveResult{Theme: strings.TrimSpace(m.textInput.Value())}
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m ImproveModel) View() tea.View {
	var b strings.Builder

	b.WriteString(popupTitleStyle.Render("Improve: " + m.agent))
	b.WriteString("\n\n")
	b.WriteString(popupDimStyle.Render("Focus area (leave empty for a general review)"))
	b.WriteString("\n")
	b.WriteString(m.textInput.View())
	b.WriteString("\n")
	b.WriteString(renderPopupHints("enter", "start", "esc", "cancel"))

	return tea.NewView("\n" + popupBoxStyle.Render(b.String()) + "\n")
}
