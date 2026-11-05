package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/theme"
)

// LogsMenuResult is the output of the logs agent-picker popup.
type LogsMenuResult struct {
	Cancelled bool
	Agent     string
}

// LogsMenuModel is the Bubble Tea model for picking an agent to view logs.
type LogsMenuModel struct {
	agents   []string
	cursor   int
	result   LogsMenuResult
	quitting bool
}

// NewLogsMenuModel creates the logs agent-picker popup.
func NewLogsMenuModel(agents []string) LogsMenuModel {
	return LogsMenuModel{
		agents: agents,
		cursor: 0,
	}
}

// PopupSize returns the tmux display-popup dimensions for the logs menu.
func (m LogsMenuModel) PopupSize() PopupSize {
	return PopupDimensions(m.View().Content)
}

// Result returns the popup output after it quits.
func (m LogsMenuModel) Result() LogsMenuResult {
	return m.result
}

func (m LogsMenuModel) Init() tea.Cmd { return nil }

func (m LogsMenuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.result.Cancelled = true
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.agents)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.agents) > 0 {
				m.result.Agent = m.agents[m.cursor]
			}
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m LogsMenuModel) View() tea.View {
	var b strings.Builder

	b.WriteString(popupTitleStyle.Render("View Logs"))
	b.WriteString("\n\n")

	if len(m.agents) == 0 {
		b.WriteString(popupDimStyle.Render("No agents found."))
		b.WriteString("\n")
	} else {
		for i, name := range m.agents {
			cursor := "  "
			if i == m.cursor {
				cursor = cursorActiveStyle.Render("") + " "
			}
			label := popupBodyStyle.Render(name)
			var row string
			if i == m.cursor {
				label = lipgloss.NewStyle().
					Foreground(lipgloss.Color(theme.Current().Accent)).
					Bold(true).
					Render(name)
				row = popupRowSelectedStyle.Render(cursor + label)
			} else {
				row = popupRowStyle.Render(cursor + label)
			}
			b.WriteString(row)
			b.WriteString("\n")
		}
	}

	b.WriteString(renderPopupHints("j/k", "navigate", "enter", "view logs", "esc", "cancel"))

	return tea.NewView("\n" + popupBoxStyle.Render(b.String()) + "\n")
}
