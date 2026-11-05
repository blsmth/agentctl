package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/theme"
)

// ─────────────────────────────────────────────────────────────────────────────
// ConfirmCloseModel — close-confirmation dialog
// ─────────────────────────────────────────────────────────────────────────────

// confirmCloseState tracks whether the dialog is still open or has resolved.
type confirmCloseState int

const (
	confirmCloseOpen      confirmCloseState = iota // waiting for input
	confirmCloseConfirmed                          // user chose Yes
	confirmCloseCancelled                          // user chose No / esc
)

// ConfirmCloseModel is the content model for the close-confirmation overlay.
// It renders only the dialog body; the wrapping Modal provides the border,
// title ("Close Ticket"), and hint bar.
type ConfirmCloseModel struct {
	issueNumber int
	cursorIdx   int // 0=Yes, 1=No; defaults to 1 (No) for safety
	state       confirmCloseState
}

// newConfirmCloseModel creates a ConfirmCloseModel for the given issue number.
// cursorIdx defaults to 1 (No) for safety.
func newConfirmCloseModel(issueNumber int) ConfirmCloseModel {
	return ConfirmCloseModel{issueNumber: issueNumber, cursorIdx: 1}
}

func (m ConfirmCloseModel) Init() tea.Cmd { return nil }

func (m ConfirmCloseModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch keyMsg.String() {
	case "y":
		m.state = confirmCloseConfirmed
	case "enter":
		if m.cursorIdx == 0 {
			m.state = confirmCloseConfirmed
		} else {
			m.state = confirmCloseCancelled
		}
	case "n", "esc":
		m.state = confirmCloseCancelled
	case "tab", "left", "right":
		m.cursorIdx = (m.cursorIdx + 1) % 2
	}
	return m, nil
}

func (m ConfirmCloseModel) View() tea.View {
	var b strings.Builder

	b.WriteString(
		popupBodyStyle.Render("Close ") +
			popupAgentStyle.Render(fmt.Sprintf("#%d", m.issueNumber)) +
			popupBodyStyle.Render("?"),
	)
	b.WriteString("\n")
	b.WriteString(popupRuleStyle.Render(strings.Repeat("─", popupContentWidth)))
	b.WriteString("\n\n")

	var yesBtn, noBtn string
	if m.cursorIdx == 0 {
		yesBtn = btnDangerStyle.Render("Yes, close")
		noBtn = btnNormalStyle.Render("No, cancel")
	} else {
		yesBtn = btnNormalStyle.Render("Yes, close")
		noBtn = btnSelectedStyle.Render("No, cancel")
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, yesBtn, "   ", noBtn))

	return tea.NewView(b.String())
}

// HintKeys implements ModalContent.
func (m ConfirmCloseModel) HintKeys() []string {
	return []string{"y", "confirm", "n/esc", "cancel", "tab", "switch"}
}

// Done returns true when the dialog has been resolved (confirmed or cancelled).
func (m ConfirmCloseModel) Done() bool { return m.state != confirmCloseOpen }

// Confirmed returns true when the user chose to confirm the close.
func (m ConfirmCloseModel) Confirmed() bool { return m.state == confirmCloseConfirmed }

// ─────────────────────────────────────────────────────────────────────────────
// AgentSelectModel — agent-selection overlay
// ─────────────────────────────────────────────────────────────────────────────

// agentSelectState tracks whether the dialog is still open or has resolved.
type agentSelectState int

const (
	agentSelectOpen      agentSelectState = iota // waiting for input
	agentSelectSelected                          // user selected an agent
	agentSelectCancelled                         // user pressed esc
)

// AgentSelectModel is the content model for the agent-selection overlay.
// It renders only the list body; the wrapping Modal provides the border,
// title ("Select Agent  #N"), and hint bar.
type AgentSelectModel struct {
	agents             []agent.Agent
	cursor             int
	pendingTicketNum   int
	pendingTicketTitle string
	pendingTicketBody  string
	state              agentSelectState
}

// newAgentSelectModel creates an AgentSelectModel for the given ticket.
func newAgentSelectModel(agents []agent.Agent, ticketNum int, ticketTitle, ticketBody string) AgentSelectModel {
	return AgentSelectModel{
		agents:             agents,
		pendingTicketNum:   ticketNum,
		pendingTicketTitle: ticketTitle,
		pendingTicketBody:  ticketBody,
	}
}

func (m AgentSelectModel) Init() tea.Cmd { return nil }

func (m AgentSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch keyMsg.String() {
	case "esc":
		m.state = agentSelectCancelled
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.agents)-1 {
			m.cursor++
		}
	case "enter":
		m.state = agentSelectSelected
	}
	return m, nil
}

// agentSelectContentWidth is the usable content width for the agent-select
// view, matching ModalMedium's content width.
const agentSelectContentWidth = 68

func (m AgentSelectModel) View() tea.View {
	const cw = agentSelectContentWidth
	var b strings.Builder

	ticketLine := m.pendingTicketTitle
	if len(ticketLine) > cw-4 {
		ticketLine = ticketLine[:cw-5] + "…"
	}
	b.WriteString(tkSubtitleStyle.Render("  " + ticketLine))
	b.WriteString("\n\n")
	b.WriteString("  " + tkRuleStyle.Render(strings.Repeat("╌", cw-2)))
	b.WriteString("\n")

	const maxVisible = 10
	start := 0
	if m.cursor >= maxVisible {
		start = m.cursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(m.agents) {
		end = len(m.agents)
	}

	for i := start; i < end; i++ {
		a := m.agents[i]
		selected := i == m.cursor

		cur := "   "
		if selected {
			cur = " " + cursorActiveStyle.Render("▸") + " "
		}

		nameStyle := tkTitleColStyle.Width(28)
		descStyle := tkDimStyle
		if selected {
			nameStyle = tkTitleSelectedStyle.Width(28)
			descStyle = tkDetailValueStyle
		}

		desc := a.Description
		maxDesc := cw - 34
		if maxDesc < 0 {
			maxDesc = 0
		}
		if len(desc) > maxDesc {
			desc = desc[:maxDesc-1] + "…"
		}

		b.WriteString(cur + nameStyle.Render(a.Name) + descStyle.Render(desc))
		b.WriteString("\n")
	}

	return tea.NewView(b.String())
}

// HintKeys implements ModalContent.
func (m AgentSelectModel) HintKeys() []string {
	return []string{"j/k", "navigate", "enter", "select", "esc", "back"}
}

// Done returns true when the dialog has been resolved.
func (m AgentSelectModel) Done() bool { return m.state != agentSelectOpen }

// Cancelled returns true when the user pressed esc.
func (m AgentSelectModel) Cancelled() bool { return m.state == agentSelectCancelled }

// Selected returns the agent at the current cursor and true, or a zero value
// and false if no agent has been selected.
func (m AgentSelectModel) Selected() (agent.Agent, bool) {
	if m.state != agentSelectSelected {
		return agent.Agent{}, false
	}
	if m.cursor >= 0 && m.cursor < len(m.agents) {
		return m.agents[m.cursor], true
	}
	return agent.Agent{}, false
}

// ─────────────────────────────────────────────────────────────────────────────
// PromptSelectModel — prompt-mode selection overlay
// ─────────────────────────────────────────────────────────────────────────────

// promptSelectState tracks whether the dialog is still open or has resolved.
type promptSelectState int

const (
	promptSelectOpen     promptSelectState = iota // waiting for input
	promptSelectSelected                          // user selected a prompt mode
	promptSelectGoBack                            // user pressed esc → return to agent select
)

// PromptSelectModel is the content model for the prompt-mode selection overlay.
// It renders only the list body; the wrapping Modal provides the border,
// title ("Prompt Mode  #N"), and hint bar.
type PromptSelectModel struct {
	cursor              int
	pendingTicketNum    int
	pendingTicketTitle  string
	pendingTicketBody   string
	pendingAgentName    string
	customPromptInput   textinput.Model
	editingCustomPrompt bool
	state               promptSelectState
	selectedMode        string
	selectedCustom      string
}

// newPromptSelectModel creates a PromptSelectModel for the given ticket/agent.
func newPromptSelectModel(ticketNum int, ticketTitle, ticketBody, agentName string) PromptSelectModel {
	ci := textinput.New()
	ci.CharLimit = 500
	ciS := ci.Styles()
	ciS.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	ci.SetStyles(ciS)
	ci.Placeholder = "Describe what you want the agent to do..."

	return PromptSelectModel{
		pendingTicketNum:   ticketNum,
		pendingTicketTitle: ticketTitle,
		pendingTicketBody:  ticketBody,
		pendingAgentName:   agentName,
		customPromptInput:  ci,
	}
}

func (m PromptSelectModel) Init() tea.Cmd { return nil }

func (m PromptSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// When the custom prompt text input is active, delegate to it first.
	if m.editingCustomPrompt {
		if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
			switch keyMsg.String() {
			case "enter":
				customPrompt := strings.TrimSpace(m.customPromptInput.Value())
				m.editingCustomPrompt = false
				m.customPromptInput.Blur()
				m.state = promptSelectSelected
				m.selectedMode = "custom"
				m.selectedCustom = customPrompt
				return m, nil
			case "esc":
				m.editingCustomPrompt = false
				m.customPromptInput.Blur()
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.customPromptInput, cmd = m.customPromptInput.Update(msg)
		return m, cmd
	}

	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch keyMsg.String() {
	case "esc":
		m.state = promptSelectGoBack
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(tlPromptOptions)-1 {
			m.cursor++
		}
	case "w":
		m.cursor = 0
		m.state = promptSelectSelected
		m.selectedMode = "work"
	case "t":
		m.cursor = 1
		m.state = promptSelectSelected
		m.selectedMode = "triage"
	case "n":
		m.cursor = 2
		m.state = promptSelectSelected
		m.selectedMode = "none"
	case "c":
		m.cursor = 3
		m.editingCustomPrompt = true
		m.customPromptInput.SetValue("")
		m.customPromptInput.Focus()
		return m, textinput.Blink
	case "enter":
		switch m.cursor {
		case 0:
			m.state = promptSelectSelected
			m.selectedMode = "work"
		case 1:
			m.state = promptSelectSelected
			m.selectedMode = "triage"
		case 2:
			m.state = promptSelectSelected
			m.selectedMode = "none"
		case 3:
			m.editingCustomPrompt = true
			m.customPromptInput.SetValue("")
			m.customPromptInput.Focus()
			return m, textinput.Blink
		}
	}
	return m, nil
}

// promptSelectContentWidth is the usable content width for the prompt-select
// view, matching ModalMedium's content width.
const promptSelectContentWidth = 68

func (m PromptSelectModel) View() tea.View {
	const cw = promptSelectContentWidth
	var b strings.Builder

	ticketLine := m.pendingTicketTitle
	if len(ticketLine) > cw-4 {
		ticketLine = ticketLine[:cw-5] + "…"
	}
	b.WriteString(tkSubtitleStyle.Render("  " + ticketLine))
	if m.pendingAgentName != "" {
		b.WriteString("  " + tkDimStyle.Render("→ "+m.pendingAgentName))
	}
	b.WriteString("\n\n")
	b.WriteString("  " + tkRuleStyle.Render(strings.Repeat("╌", cw-2)))
	b.WriteString("\n")

	for i, opt := range tlPromptOptions {
		selected := i == m.cursor

		cur := "   "
		if selected {
			cur = " " + cursorActiveStyle.Render("▸") + " "
		}

		nameStyle := tkTitleColStyle.Width(24)
		descStyle := tkDimStyle
		if selected {
			nameStyle = tkTitleSelectedStyle.Width(24)
		}

		b.WriteString(cur + nameStyle.Render(opt.label) + descStyle.Render(opt.description))
		b.WriteString("\n")

		// Show custom prompt input below the "Custom" option when editing.
		if i == 3 && m.editingCustomPrompt {
			b.WriteString("   " + m.customPromptInput.View())
			b.WriteString("\n")
		}
	}

	return tea.NewView(b.String())
}

// HintKeys implements ModalContent.
func (m PromptSelectModel) HintKeys() []string {
	return []string{"j/k", "navigate", "enter", "select", "esc", "back"}
}

// Done returns true when the dialog has been resolved.
func (m PromptSelectModel) Done() bool { return m.state != promptSelectOpen }

// GoBack returns true when the user pressed esc to return to agent selection.
func (m PromptSelectModel) GoBack() bool { return m.state == promptSelectGoBack }

// Selected returns the selected prompt mode and custom prompt string.
// Only meaningful when Done() is true and GoBack() is false.
func (m PromptSelectModel) Selected() (mode, custom string) {
	return m.selectedMode, m.selectedCustom
}
