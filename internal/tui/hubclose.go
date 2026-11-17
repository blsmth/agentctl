package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// HubCloseModel is the confirmation popup for closing the hub (prefix+q in
// the hub). It lists no agents: they keep running in their project sessions.
type HubCloseModel struct {
	cursor int // 0 = yes, 1 = no
	result QuitResult
}

// NewHubCloseModel creates the popup, defaulting to "No".
func NewHubCloseModel() HubCloseModel { return HubCloseModel{cursor: 1} }

// Result returns the popup output after it quits.
func (m HubCloseModel) Result() QuitResult { return m.result }

func (m HubCloseModel) Init() tea.Cmd { return nil }

func (m HubCloseModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "ctrl+c", "esc", "n", "q":
			return m, tea.Quit
		case "y":
			m.result.Confirmed = true
			return m, tea.Quit
		case "left", "h":
			m.cursor = 0
		case "right", "l":
			m.cursor = 1
		case "tab":
			m.cursor = (m.cursor + 1) % 2
		case "enter":
			m.result.Confirmed = m.cursor == 0
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m HubCloseModel) View() tea.View {
	var b strings.Builder
	b.WriteString(popupTitleStyle.Render("Close the hub?"))
	b.WriteString("\n\n")
	b.WriteString(popupBodyStyle.Render("Agents keep running in their project sessions."))
	b.WriteString("\n\n")
	b.WriteString(popupRuleStyle.Render(strings.Repeat("─", popupContentWidth)))
	b.WriteString("\n\n")
	yes, no := btnNormalStyle.Render("Yes, close"), btnSelectedStyle.Render("No, cancel")
	if m.cursor == 0 {
		yes, no = btnDangerStyle.Render("Yes, close"), btnNormalStyle.Render("No, cancel")
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, yes, "   ", no))
	b.WriteString("\n")
	b.WriteString(renderPopupHints("y", "confirm", "n/esc", "cancel", "tab", "switch"))
	return tea.NewView("\n" + popupBoxStyle.Render(b.String()) + "\n")
}
