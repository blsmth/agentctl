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
	Mode      string // "full" or "quick"
	Theme     string // only set for "quick"
}

type view int

const (
	viewMenu view = iota
	viewInput
)

// selectedStyle is the style applied to the selected improve-popup menu item.
var selectedStyle lipgloss.Style

// initImproveStyles initialises improve-popup styles from the active theme.
func initImproveStyles() {
	t := theme.Current()
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true)
}

type menuItem struct {
	label string
	mode  string
}

var menuItems = []menuItem{
	{label: "Full Review", mode: "full"},
	{label: "Quick Improve", mode: "quick"},
}

// ImproveModel is the Bubble Tea model for the improve popup.
type ImproveModel struct {
	agent     string
	view      view
	cursor    int
	textInput textinput.Model
	result    ImproveResult
	quitting  bool
}

// NewImproveModel creates the model for the improve popup.
func NewImproveModel(agentName string) ImproveModel {
	ti := textinput.New()
	ti.Placeholder = "e.g., better commit messages"
	ti.CharLimit = 200
	ti.SetWidth(34)
	s := ti.Styles()
	s.Focused.Prompt = selectedStyle
	s.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	ti.SetStyles(s)

	return ImproveModel{
		agent:     agentName,
		view:      viewMenu,
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
	return nil
}

func (m ImproveModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch m.view {
		case viewMenu:
			return m.updateMenu(msg)
		case viewInput:
			return m.updateInput(msg)
		}
	}
	return m, nil
}

func (m ImproveModel) updateMenu(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(menuItems)-1 {
			m.cursor++
		}
	case "enter":
		item := menuItems[m.cursor]
		if item.mode == "full" {
			m.result = ImproveResult{Mode: "full"}
			m.quitting = true
			return m, tea.Quit
		}
		if item.mode == "quick" {
			m.view = viewInput
			m.textInput.Focus()
			return m, textinput.Blink
		}
	}
	return m, nil
}

func (m ImproveModel) updateInput(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Back to menu
		m.view = viewMenu
		m.textInput.Reset()
		return m, nil
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "enter":
		theme := m.textInput.Value()
		if theme != "" {
			m.result = ImproveResult{Mode: "quick", Theme: theme}
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m ImproveModel) View() tea.View {
	switch m.view {
	case viewMenu:
		return tea.NewView(m.viewMenu())
	case viewInput:
		return tea.NewView(m.viewInput())
	}
	return tea.NewView("")
}

func (m ImproveModel) viewMenu() string {
	var b strings.Builder

	b.WriteString(popupTitleStyle.Render("Improve: " + m.agent))
	b.WriteString("\n\n")

	for i, item := range menuItems {
		cursor := "  "
		if i == m.cursor {
			cursor = cursorActiveStyle.Render("") + " "
		}

		var row string
		label := popupBodyStyle.Render(item.label)
		if i == m.cursor {
			label = popupAgentStyle.Render(item.label)
			row = popupRowSelectedStyle.Render(cursor + label)
		} else {
			row = popupRowStyle.Render(cursor + label)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}

	b.WriteString(renderPopupHints("j/k", "navigate", "enter", "select", "esc", "cancel"))

	return "\n" + popupBoxStyle.Render(b.String()) + "\n"
}

func (m ImproveModel) viewInput() string {
	var b strings.Builder

	b.WriteString(popupTitleStyle.Render("Quick Improve: " + m.agent))
	b.WriteString("\n\n")

	b.WriteString(popupDimStyle.Render("What should be improved?"))
	b.WriteString("\n")
	b.WriteString(m.textInput.View())
	b.WriteString("\n")

	b.WriteString(renderPopupHints("enter", "submit", "esc", "back"))

	return "\n" + popupBoxStyle.Render(b.String()) + "\n"
}
