package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// QuitResult is the output of the quit confirmation popup.
type QuitResult struct {
	Confirmed bool
}

// QuitModel is the Bubble Tea model for the quit confirmation popup.
type QuitModel struct {
	cursor   int // 0 = yes, 1 = no
	result   QuitResult
	agents   []string
	quitting bool
}

// NewQuitModel creates the quit confirmation popup.
// agents is the list of currently running agent names (for display).
func NewQuitModel(agents []string) QuitModel {
	return QuitModel{
		cursor: 1, // default to "No" for safety
		agents: agents,
	}
}

// PopupSize returns the tmux display-popup dimensions for the quit popup.
func (m QuitModel) PopupSize() PopupSize {
	return PopupDimensions(m.View().Content)
}

// Result returns the popup output after it quits.
func (m QuitModel) Result() QuitResult {
	return m.result
}

func (m QuitModel) Init() tea.Cmd {
	return nil
}

func (m QuitModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "n", "q":
			m.quitting = true
			return m, tea.Quit
		case "y":
			m.result.Confirmed = true
			m.quitting = true
			return m, tea.Quit
		case "left", "h":
			if m.cursor > 0 {
				m.cursor--
			}
		case "right", "l":
			if m.cursor < 1 {
				m.cursor++
			}
		case "tab":
			m.cursor = (m.cursor + 1) % 2
		case "enter":
			if m.cursor == 0 {
				m.result.Confirmed = true
			}
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m QuitModel) View() tea.View {
	var b strings.Builder

	// Title
	b.WriteString(popupTitleStyle.Render("Quit agentctl"))
	b.WriteString("\n\n")

	// Agent list or "no agents" message
	if len(m.agents) == 0 {
		b.WriteString(popupDimStyle.Render("No agents currently running."))
	} else {
		b.WriteString(
			popupBodyStyle.Render("Kill ") +
				popupAgentStyle.Render(fmt.Sprintf("%d", len(m.agents))) +
				popupBodyStyle.Render(" running agent(s):"))
		b.WriteString("\n")
		for _, name := range m.agents {
			b.WriteString("  " + popupDimStyle.Render("· ") + popupAgentStyle.Render(name))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")

	// Warning
	b.WriteString(popupWarningStyle.Render("The entire session will be destroyed."))
	b.WriteString("\n\n")

	// Rule
	b.WriteString(popupRuleStyle.Render(strings.Repeat("─", popupContentWidth)))
	b.WriteString("\n\n")

	// Buttons
	var yesBtn, noBtn string
	if m.cursor == 0 {
		yesBtn = btnDangerStyle.Render("Yes, quit")
		noBtn = btnNormalStyle.Render("No, cancel")
	} else {
		yesBtn = btnNormalStyle.Render("Yes, quit")
		noBtn = btnSelectedStyle.Render("No, cancel")
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, yesBtn, "   ", noBtn))
	b.WriteString("\n")

	// Hint bar
	b.WriteString(renderPopupHints("y", "confirm", "n/esc", "cancel", "tab", "switch"))

	return tea.NewView("\n" + popupBoxStyle.Render(b.String()) + "\n")
}
