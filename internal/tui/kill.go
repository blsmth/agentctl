package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// KillResult is the output of the kill confirmation popup.
type KillResult struct {
	Confirmed bool
}

// KillModel is the Bubble Tea model for the kill agent confirmation popup.
type KillModel struct {
	agent    string
	cursor   int // 0 = yes, 1 = no
	result   KillResult
	quitting bool
	// children are the dispatched sessions the kill will also close.
	children []string
}

// NewKillModel creates the kill confirmation popup for the given agent.
func NewKillModel(agentName string) KillModel {
	return KillModel{
		agent:  agentName,
		cursor: 1, // default to "No" for safety
	}
}

// WithChildren records the dispatched sessions that closing the agent will
// also close (close_children), so the confirmation can say so.
func (m KillModel) WithChildren(names []string) KillModel {
	m.children = names
	return m
}

// maxKillChildNames caps how many child names the confirmation lists.
const maxKillChildNames = 3

// childrenLine is the confirmation's "also closes" note, or "" when none.
func (m KillModel) childrenLine() string {
	n := len(m.children)
	if n == 0 {
		return ""
	}
	noun := "session"
	if n > 1 {
		noun = "sessions"
	}
	shown := m.children
	if n > maxKillChildNames {
		shown = shown[:maxKillChildNames]
	}
	list := strings.Join(shown, ", ")
	if n > maxKillChildNames {
		list += fmt.Sprintf(" +%d more", n-maxKillChildNames)
	}
	return fmt.Sprintf("Also closes %d dispatched %s: %s", n, noun, list)
}

// PopupSize returns the tmux display-popup dimensions for the kill popup.
func (m KillModel) PopupSize() PopupSize {
	return PopupDimensions(m.View().Content)
}

// Result returns the popup output after it quits.
func (m KillModel) Result() KillResult {
	return m.result
}

func (m KillModel) Init() tea.Cmd {
	return nil
}

func (m KillModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

func (m KillModel) View() tea.View {
	var b strings.Builder

	// Title
	b.WriteString(popupTitleStyle.Render("Kill Agent"))
	b.WriteString("\n\n")

	// Body
	b.WriteString(
		popupBodyStyle.Render("Stop agent ") +
			popupAgentStyle.Render(m.agent) +
			popupBodyStyle.Render("?"),
	)
	b.WriteString("\n")
	if line := m.childrenLine(); line != "" {
		b.WriteString(popupDimStyle.Width(popupContentWidth).Render(line))
		b.WriteString("\n")
	}

	// Rule
	b.WriteString(popupRuleStyle.Render(strings.Repeat("─", popupContentWidth)))
	b.WriteString("\n\n")

	// Buttons: yes on left, no on right
	var yesBtn, noBtn string
	if m.cursor == 0 {
		yesBtn = btnDangerStyle.Render("Yes, kill")
		noBtn = btnNormalStyle.Render("No, cancel")
	} else {
		yesBtn = btnNormalStyle.Render("Yes, kill")
		noBtn = btnSelectedStyle.Render("No, cancel")
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, yesBtn, "   ", noBtn))
	b.WriteString("\n")

	// Hint bar
	b.WriteString(renderPopupHints("y", "confirm", "n/esc", "cancel", "tab", "switch"))

	return tea.NewView(popupBoxStyle.Render(b.String()))
}
