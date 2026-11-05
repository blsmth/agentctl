package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/theme"
)

// Internal width for the settings popup content.
const settingsContentWidth = 60

// Settings popup styles — wider than the default popup to prevent description wrapping.
var (
	settingsBoxStyle         lipgloss.Style
	settingsTitleStyle       lipgloss.Style
	settingsRowStyle         lipgloss.Style
	settingsRowSelectedStyle lipgloss.Style
	settingsHintBarStyle     lipgloss.Style
)

// initSettingsStyles initialises settings-popup styles from the active theme.
func initSettingsStyles() {
	t := theme.Current()

	settingsBoxStyle = lipgloss.NewStyle().
		Padding(1, 2).
		Width(settingsContentWidth + 4)

	settingsTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Width(settingsContentWidth).
		Align(lipgloss.Center)

	settingsRowStyle = lipgloss.NewStyle().Width(settingsContentWidth)

	settingsRowSelectedStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.Surface)).
		Width(settingsContentWidth)

	settingsHintBarStyle = lipgloss.NewStyle().
		Width(settingsContentWidth).
		Align(lipgloss.Center).
		MarginTop(1).
		Foreground(lipgloss.Color(t.Muted))
}

// SettingsResult is the output of the settings popup.
type SettingsResult struct {
	Cancelled bool
	// Toggles maps setting keys to their new values.
	Toggles map[string]bool
}

type settingItem struct {
	key   string // config key: "alerts", "auto_improve", "cross_context"
	label string // display label
	desc  string // short description shown dimly
	on    bool
}

// SettingsModel is the Bubble Tea model for the agent settings popup.
type SettingsModel struct {
	agent    string
	items    []settingItem
	cursor   int
	result   SettingsResult
	quitting bool
}

// NewSettingsModel creates the settings popup for the given agent with current values.
func NewSettingsModel(agentName string, alerts, autoImprove, crossContext, alertBlocked, alertIdle, alertGone, autoAccept, workJournal bool) SettingsModel {
	return SettingsModel{
		agent: agentName,
		items: []settingItem{
			{key: "alerts", label: "Alerts", desc: "macOS notifications on activity", on: alerts},
			{key: "alert_blocked", label: "  Blocked", desc: "needs approval", on: alertBlocked},
			{key: "alert_idle", label: "  Idle", desc: "done, waiting for input", on: alertIdle},
			{key: "alert_gone", label: "  Gone", desc: "session ended", on: alertGone},
			{key: "auto_improve", label: "Auto-improve", desc: "self-review after sessions", on: autoImprove},
			{key: "cross_context", label: "Cross-context", desc: "share context across agents", on: crossContext},
			{key: "auto_accept", label: "Auto-accept", desc: "approve all permission prompts", on: autoAccept},
			{key: "work_journal", label: "Work journal", desc: "summarize sessions on exit (needs llm config)", on: workJournal},
		},
		cursor: 0,
		result: SettingsResult{
			Cancelled: true,
			Toggles:   make(map[string]bool),
		},
	}
}

// PopupSize returns the tmux display-popup dimensions for the settings popup.
func (m SettingsModel) PopupSize() PopupSize {
	return PopupDimensions(m.View().Content)
}

// Result returns the popup output after it quits.
func (m SettingsModel) Result() SettingsResult {
	return m.result
}

func (m SettingsModel) Init() tea.Cmd {
	return nil
}

func (m SettingsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "enter", " ":
			// Toggle the current setting
			m.items[m.cursor].on = !m.items[m.cursor].on
			// Build result with all current values
			m.result.Cancelled = false
			for _, item := range m.items {
				m.result.Toggles[item.key] = item.on
			}
		case "s":
			// Save and quit
			m.result.Cancelled = false
			for _, item := range m.items {
				m.result.Toggles[item.key] = item.on
			}
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m SettingsModel) View() tea.View {
	var b strings.Builder

	b.WriteString(settingsTitleStyle.Render("Settings: " + m.agent))
	b.WriteString("\n\n")

	for i, item := range m.items {
		// Toggle indicator
		var toggle string
		if item.on {
			toggle = toggleOnStyle.Render("●")
		} else {
			toggle = toggleOffStyle.Render("○")
		}

		// Cursor arrow
		cursor := "  "
		if i == m.cursor {
			cursor = cursorActiveStyle.Render("") + " "
		}

		// Extra indentation for alert sub-items
		indent := ""
		if strings.HasPrefix(item.key, "alert_") {
			indent = "    "
		}

		// Label + dim description
		label := popupBodyStyle.Render(fmt.Sprintf("%-16s", item.label))
		if i == m.cursor {
			label = popupAgentStyle.Render(fmt.Sprintf("%-16s", item.label))
		}
		desc := popupDimStyle.Render(item.desc)

		inner := indent + cursor + toggle + "  " + label + desc

		var row string
		if i == m.cursor {
			row = settingsRowSelectedStyle.Render(inner)
		} else {
			row = settingsRowStyle.Render(inner)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}

	b.WriteString(renderHintsInStyle(settingsHintBarStyle, "space", "toggle", "s", "save", "esc", "cancel"))

	return tea.NewView("\n" + settingsBoxStyle.Render(b.String()) + "\n")
}
