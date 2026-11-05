package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// KillAllResult is the output of the kill-all confirmation popup.
type KillAllResult struct {
	Confirmed bool
}

// KillAllModel is the Bubble Tea model for the kill-all-agents confirmation popup.
type KillAllModel struct {
	count    int
	cursor   int // 0 = yes, 1 = no
	result   KillAllResult
	quitting bool
}

// NewKillAllModel creates the kill-all confirmation popup for the given agent count.
func NewKillAllModel(count int) KillAllModel {
	return KillAllModel{
		count:  count,
		cursor: 1, // default to "No" for safety
	}
}

// PopupSize returns the tmux display-popup dimensions for the kill-all popup.
func (m KillAllModel) PopupSize() PopupSize {
	return PopupDimensions(m.View().Content)
}

// Result returns the popup output after it quits.
func (m KillAllModel) Result() KillAllResult {
	return m.result
}

func (m KillAllModel) Init() tea.Cmd {
	return nil
}

func (m KillAllModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

func (m KillAllModel) View() tea.View {
	var b strings.Builder

	// Title
	b.WriteString(popupTitleStyle.Render("Kill All Agents"))
	b.WriteString("\n\n")

	// Body
	var body string
	if m.count == 0 {
		body = popupBodyStyle.Render("No agent windows are running.")
	} else {
		countStr := popupAgentStyle.Render(fmt.Sprintf("%d", m.count))
		noun := "agents"
		if m.count == 1 {
			noun = "agent"
		}
		body = popupBodyStyle.Render("Kill all ") + countStr + popupBodyStyle.Render(fmt.Sprintf(" running %s?", noun))
	}
	b.WriteString(body)
	b.WriteString("\n")

	// Rule
	b.WriteString(popupRuleStyle.Render(strings.Repeat("─", popupContentWidth)))
	b.WriteString("\n\n")

	// Buttons
	if m.count == 0 {
		// No agents — only show cancel.
		b.WriteString(btnSelectedStyle.Render("OK"))
	} else {
		var yesBtn, noBtn string
		if m.cursor == 0 {
			yesBtn = btnDangerStyle.Render("Yes, kill all")
			noBtn = btnNormalStyle.Render("No, cancel")
		} else {
			yesBtn = btnNormalStyle.Render("Yes, kill all")
			noBtn = btnSelectedStyle.Render("No, cancel")
		}
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, yesBtn, "   ", noBtn))
	}
	b.WriteString("\n")

	// Hint bar
	b.WriteString(renderPopupHints("y", "confirm", "n/esc", "cancel", "tab", "switch"))

	return tea.NewView(popupBoxStyle.Render(b.String()))
}
