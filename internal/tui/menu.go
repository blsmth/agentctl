package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/theme"
)

// Internal width for content inside the outer box.
const menuContentWidth = 88

// Column widths for the agent table.
const (
	colStatus = 4  // "● " or "○ "
	colName   = 24 // agent name
	colCount  = 5  // "(3)" or blank
	// description fills the rest
)

// MenuAgent represents an agent entry in the launch menu.
type MenuAgent struct {
	Name        string
	Description string
	Instances   []string // running instance window names
	Standalone  bool     // true = fixed dir, single instance
}

// MenuResult is the output of the menu popup.
type MenuResult struct {
	Cancelled bool
	Action    string // "switch", "launch", "create"
	Agent     string // base agent name
	Instance  string // specific instance to switch to
}

// menuTab is a named group of agents shown in one tab.
type menuTab struct {
	label  string
	agents []MenuAgent
}

// Menu-specific styles.
var (
	menuBoxStyle           lipgloss.Style
	menuTitleStyle         lipgloss.Style
	tabActiveStyle         lipgloss.Style
	tabInactiveStyle       lipgloss.Style
	sectionRuleStyle       lipgloss.Style
	colHeaderStyle         lipgloss.Style
	agentRowStyle          lipgloss.Style
	agentRowSelectedStyle  lipgloss.Style
	agentNameStyle         lipgloss.Style
	agentNameSelectedStyle lipgloss.Style
	menuDescStyle          lipgloss.Style
	runningDotStyle        lipgloss.Style
	stoppedDotStyle        lipgloss.Style
	instanceCntStyle       lipgloss.Style
	createStyle            lipgloss.Style
	createSelectedStyle    lipgloss.Style
	menuHintKeyStyle       lipgloss.Style
	menuHintLabelStyle     lipgloss.Style
	menuHintDotStyle       lipgloss.Style
	menuHintBarStyle       lipgloss.Style
	cursorActiveStyle      lipgloss.Style
)

// initMenuStyles initialises all menu-specific styles from the active theme.
func initMenuStyles() {
	t := theme.Current()

	menuBoxStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.Accent)).
		Padding(1, 2).
		Width(menuContentWidth + 6)

	menuTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Width(menuContentWidth).
		Align(lipgloss.Center)

	tabActiveStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Accent)).
		BorderStyle(lipgloss.Border{Bottom: "━"}).
		BorderBottom(true).
		BorderForeground(lipgloss.Color(t.Accent)).
		Padding(0, 2)

	tabInactiveStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		BorderStyle(lipgloss.Border{Bottom: "─"}).
		BorderBottom(true).
		BorderForeground(lipgloss.Color(t.Surface)).
		Padding(0, 2)

	sectionRuleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Surface))

	colHeaderStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Bold(true)

	agentRowStyle = lipgloss.NewStyle().Width(menuContentWidth)

	agentRowSelectedStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.Surface)).
		Width(menuContentWidth)

	agentNameStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Text)).
		Width(colName)

	agentNameSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true).
		Width(colName)

	menuDescStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))

	runningDotStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green))
	stoppedDotStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Overlay))
	instanceCntStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green))

	createStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle)).Italic(true)
	createSelectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Italic(true).Bold(true)

	menuHintKeyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true)
	menuHintLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	menuHintDotStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Surface))
	menuHintBarStyle = lipgloss.NewStyle().
		Width(menuContentWidth).
		Align(lipgloss.Center).
		MarginTop(1).
		Foreground(lipgloss.Color(t.Muted))

	cursorActiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true)
}

// MenuModel is the Bubble Tea model for the agent launch menu.
type MenuModel struct {
	tabs      []menuTab
	activeTab int
	cursor    int // row index within the current tab (agents + 1 for create)
	result    MenuResult
	quitting  bool
}

// itemCount returns the number of selectable items in the current tab.
// This is len(agents) + 1 for the "Create New Agent" row.
func (m MenuModel) itemCount() int {
	return len(m.tabs[m.activeTab].agents) + 1
}

// NewMenuModel creates the launch menu from a list of agents.
// defaultAgent is the name of the agent to pin at the top (matches config.DefaultAgent).
// If empty, no agent is pinned.
func NewMenuModel(agents []MenuAgent, defaultAgent string) MenuModel {
	var pinned, standalone, subagents []MenuAgent
	for _, a := range agents {
		if defaultAgent != "" && a.Name == defaultAgent {
			pinned = append(pinned, a)
		} else if a.Standalone {
			standalone = append(standalone, a)
		} else {
			subagents = append(subagents, a)
		}
	}

	var tabs []menuTab
	if len(pinned) > 0 {
		tabs = append(tabs, menuTab{label: "Pinned", agents: pinned})
	}
	if len(standalone) > 0 {
		tabs = append(tabs, menuTab{label: "Standalone", agents: standalone})
	}
	if len(subagents) > 0 {
		tabs = append(tabs, menuTab{label: "Sub-agents", agents: subagents})
	}

	// Fallback: if no tabs (shouldn't happen), add an empty one
	if len(tabs) == 0 {
		tabs = append(tabs, menuTab{label: "Agents", agents: nil})
	}

	return MenuModel{
		tabs:      tabs,
		activeTab: 0,
		cursor:    0,
	}
}

func (m MenuModel) Result() MenuResult { return m.result }
func (m MenuModel) Init() tea.Cmd      { return nil }

// PopupSize returns the tmux display-popup dimensions for the launch menu.
func (m MenuModel) PopupSize() PopupSize {
	return PopupDimensions(m.View().Content)
}

func (m MenuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			if m.cursor < m.itemCount()-1 {
				m.cursor++
			}
		case "tab", "l", "right":
			m.activeTab = (m.activeTab + 1) % len(m.tabs)
			m.clampCursor()
		case "shift+tab", "h", "left":
			m.activeTab = (m.activeTab - 1 + len(m.tabs)) % len(m.tabs)
			m.clampCursor()
		case "enter":
			tab := m.tabs[m.activeTab]
			if m.cursor >= len(tab.agents) {
				// "Create New Agent" row
				m.result.Action = "create"
				m.quitting = true
				return m, tea.Quit
			}
			a := tab.agents[m.cursor]
			m.result.Action = "launch"
			m.result.Agent = a.Name
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// clampCursor ensures the cursor is within bounds after a tab switch.
func (m *MenuModel) clampCursor() {
	max := m.itemCount() - 1
	if m.cursor > max {
		m.cursor = max
	}
}

// renderTabBar renders the horizontal tab bar.
func (m MenuModel) renderTabBar() string {
	var tabs []string
	for i, t := range m.tabs {
		label := t.label
		// Show count of running agents in tab
		running := 0
		for _, a := range t.agents {
			if len(a.Instances) > 0 {
				running++
			}
		}
		if running > 0 {
			label = fmt.Sprintf("%s (%d)", t.label, running)
		}

		if i == m.activeTab {
			tabs = append(tabs, tabActiveStyle.Render(label))
		} else {
			tabs = append(tabs, tabInactiveStyle.Render(label))
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Bottom, tabs...)
}

// renderTableHeader renders the column header row for the agent table.
func renderTableHeader() string {
	return fmt.Sprintf("   %s%s%s%s",
		colHeaderStyle.Render(pad("", colStatus)),
		colHeaderStyle.Render(pad("AGENT", colName)),
		colHeaderStyle.Render(pad("", colCount)),
		colHeaderStyle.Render("DESCRIPTION"),
	)
}

// pad right-pads s to width with spaces.
func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func renderAgentRow(a *MenuAgent, selected bool) string {
	// Cursor (3 chars)
	cursor := "   "
	if selected {
		cursor = " " + cursorActiveStyle.Render("") + " "
	}

	// Status dot (colStatus chars)
	var dot string
	if len(a.Instances) > 0 {
		dot = runningDotStyle.Render("●") + "  "
	} else {
		dot = stoppedDotStyle.Render("·") + "  "
	}

	// Agent name
	nameS := agentNameStyle
	if selected {
		nameS = agentNameSelectedStyle
	}
	nameStr := nameS.Render(a.Name)

	// Instance count badge
	countStr := strings.Repeat(" ", colCount)
	if len(a.Instances) > 1 {
		c := fmt.Sprintf("(%d)", len(a.Instances))
		countStr = instanceCntStyle.Render(c) + strings.Repeat(" ", colCount-len(c))
	}

	// Description
	desc := ""
	if a.Description != "" {
		d := a.Description
		maxDesc := menuContentWidth - colName - colStatus - colCount - 6
		if maxDesc < 10 {
			maxDesc = 10
		}
		if len(d) > maxDesc {
			d = d[:maxDesc-1] + "…"
		}
		desc = menuDescStyle.Render(d)
	}

	inner := cursor + dot + nameStr + countStr + desc

	if selected {
		return agentRowSelectedStyle.Render(inner)
	}
	return agentRowStyle.Render(inner)
}

func (m MenuModel) View() tea.View {
	var content strings.Builder

	// Title banner
	content.WriteString(menuTitleStyle.Render("Launch Agent"))
	content.WriteString("\n\n")

	// Tab bar
	content.WriteString("  " + m.renderTabBar())
	content.WriteString("\n\n")

	// Column headers
	content.WriteString(renderTableHeader())
	content.WriteString("\n")
	content.WriteString("   " + sectionRuleStyle.Render(strings.Repeat("╌", menuContentWidth-3)))
	content.WriteString("\n")

	// Agent rows for current tab
	tab := m.tabs[m.activeTab]
	for i := range tab.agents {
		content.WriteString(renderAgentRow(&tab.agents[i], i == m.cursor))
		content.WriteString("\n")
	}

	// Create row
	content.WriteString("\n")
	content.WriteString(sectionRuleStyle.Render(strings.Repeat("─", menuContentWidth)))
	content.WriteString("\n\n")
	createIdx := len(tab.agents)
	if m.cursor == createIdx {
		inner := " " + cursorActiveStyle.Render("") + " " + createSelectedStyle.Render("+ Create New Agent")
		content.WriteString(agentRowSelectedStyle.Render(inner))
	} else {
		content.WriteString(agentRowStyle.Render("   " + createStyle.Render("+ Create New Agent")))
	}
	content.WriteString("\n")

	// Hint bar
	hints := []struct{ key, label string }{
		{"j/k", "navigate"},
		{"tab", "switch tab"},
		{"enter", "launch"},
		{"esc", "cancel"},
	}
	var hintParts []string
	for _, h := range hints {
		hintParts = append(hintParts, menuHintKeyStyle.Render(h.key)+" "+menuHintLabelStyle.Render(h.label))
	}
	content.WriteString(menuHintBarStyle.Render(strings.Join(hintParts, "  "+menuHintDotStyle.Render("·")+"  ")))

	return tea.NewView("\n" + menuBoxStyle.Render(content.String()) + "\n")
}
