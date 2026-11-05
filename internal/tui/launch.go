package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/theme"
)

// Internal width for the launch popup content.
const launchContentWidth = 88

// LaunchResult is the output of the launch session popup.
type LaunchResult struct {
	Cancelled bool
	Resume    bool
	SessionID string
	Dir       string // Working directory (may differ from default)
}

// LaunchSession is a session entry for the launch menu.
type LaunchSession struct {
	SessionID string
	Age       string // e.g. "5m ago", "2h ago"
	Preview   string // first user message preview
	Project   string // short project path
}

// Launch popup styles — consistent with the main menu.
var (
	launchBoxStyle             lipgloss.Style
	launchTitleStyle           lipgloss.Style
	launchRuleStyle            lipgloss.Style
	launchSectionStyle         lipgloss.Style
	launchColHeaderStyle       lipgloss.Style
	launchRowStyle             lipgloss.Style
	launchRowSelectedStyle     lipgloss.Style
	launchAgeStyle             lipgloss.Style
	launchPreviewStyle         lipgloss.Style
	launchPreviewSelectedStyle lipgloss.Style
	launchPreviewDimStyle      lipgloss.Style
	launchFreshStyle           lipgloss.Style
	launchHintBarStyle         lipgloss.Style
	launchDirLabelStyle        lipgloss.Style
	launchDirValueStyle        lipgloss.Style
	launchDirEditingStyle      lipgloss.Style
)

// initLaunchStyles initialises launch-popup styles from the active theme.
func initLaunchStyles() {
	t := theme.Current()

	launchBoxStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.Accent)).
		Padding(1, 2).
		Width(launchContentWidth + 6)

	launchTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Width(launchContentWidth).
		Align(lipgloss.Center)

	launchRuleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Surface))

	launchSectionStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Text))

	launchColHeaderStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Bold(true)

	launchRowStyle = lipgloss.NewStyle().Width(launchContentWidth)

	launchRowSelectedStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.Surface)).
		Width(launchContentWidth)

	launchAgeStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Width(12)

	launchPreviewStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))

	launchPreviewSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)

	launchPreviewDimStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Overlay)).
		Italic(true)

	launchFreshStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Green)).
		Bold(true)

	launchHintBarStyle = lipgloss.NewStyle().
		Width(launchContentWidth).
		Align(lipgloss.Center).
		MarginTop(1).
		Foreground(lipgloss.Color(t.Muted))

	launchDirLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	launchDirValueStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))
	launchDirEditingStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)
}

// LaunchModel is the Bubble Tea model for the launch session popup.
type LaunchModel struct {
	agent      string
	sessions   []LaunchSession
	cursor     int // 0 = "Start fresh", 1..N = sessions
	result     LaunchResult
	quitting   bool
	dir        string // current working directory
	defaultDir string // original default (for display)
	editingDir bool   // true when text input is active
	dirInput   textinput.Model
}

// NewLaunchModel creates the launch popup with a list of resumable sessions.
// "Start fresh" is always the first option (index 0).
// defaultDir is the working directory the agent will launch in.
func NewLaunchModel(agentName string, sessions []LaunchSession, defaultDir string) LaunchModel {
	ti := textinput.New()
	ti.CharLimit = 200
	ti.SetWidth(launchContentWidth - 10)
	ls := ti.Styles()
	ls.Focused.Prompt = launchDirEditingStyle
	ls.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	ti.SetStyles(ls)
	ti.Prompt = "dir: "
	ti.SetValue(defaultDir)

	return LaunchModel{
		agent:      agentName,
		sessions:   sessions,
		cursor:     0,
		dir:        defaultDir,
		defaultDir: defaultDir,
		dirInput:   ti,
	}
}

// Result returns the popup output after it quits.
func (m LaunchModel) Result() LaunchResult { return m.result }

func (m LaunchModel) Init() tea.Cmd { return nil }

// itemCount returns total selectable items ("Start fresh" + sessions).
func (m LaunchModel) itemCount() int {
	return 1 + len(m.sessions)
}

func (m LaunchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// When editing the directory, delegate to text input
	if m.editingDir {
		switch msg := msg.(type) {
		case tea.KeyPressMsg:
			switch msg.String() {
			case "enter":
				val := strings.TrimSpace(m.dirInput.Value())
				if val != "" {
					// Expand ~ prefix
					if strings.HasPrefix(val, "~/") {
						home, _ := os.UserHomeDir()
						val = filepath.Join(home, val[2:])
					}
					m.dir = val
				}
				m.editingDir = false
				m.dirInput.Blur()
				return m, nil
			case "esc":
				// Cancel edit, restore previous value
				m.dirInput.SetValue(m.dir)
				m.editingDir = false
				m.dirInput.Blur()
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.dirInput, cmd = m.dirInput.Update(msg)
		return m, cmd
	}

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
		case "d":
			// Edit working directory
			m.editingDir = true
			return m, m.dirInput.Focus()
		case "f":
			// Quick key: start fresh
			m.result.Resume = false
			m.result.Dir = m.dir
			m.quitting = true
			return m, tea.Quit
		case "enter":
			if m.cursor == 0 {
				// Start fresh
				m.result.Resume = false
			} else {
				// Resume session (cursor-1 maps to sessions index)
				m.result.Resume = true
				m.result.SessionID = m.sessions[m.cursor-1].SessionID
			}
			m.result.Dir = m.dir
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// shortenDir returns a display-friendly directory path, replacing $HOME with ~.
func shortenDir(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return dir
	}
	if strings.HasPrefix(dir, home) {
		return "~" + dir[len(home):]
	}
	return dir
}

func (m LaunchModel) View() tea.View {
	var content strings.Builder

	// Title banner
	content.WriteString(launchTitleStyle.Render(fmt.Sprintf("Launch: %s", m.agent)))
	content.WriteString("\n")

	// --- Working directory ---
	content.WriteString("\n")
	if m.editingDir {
		content.WriteString("  " + m.dirInput.View())
	} else {
		dirDisplay := shortenDir(m.dir)
		modified := ""
		if m.dir != m.defaultDir {
			modified = launchDirEditingStyle.Render(" (custom)")
		}
		content.WriteString("  " + launchDirLabelStyle.Render("dir: ") + launchDirValueStyle.Render(dirDisplay) + modified)
	}
	content.WriteString("\n")

	// --- Start fresh option ---
	content.WriteString("\n")
	freshCursor := "   "
	if m.cursor == 0 {
		freshCursor = " " + cursorActiveStyle.Render("") + " "
	}
	freshLabel := launchFreshStyle.Render("Start fresh session")
	if m.cursor == 0 {
		content.WriteString(launchRowSelectedStyle.Render(freshCursor + freshLabel))
	} else {
		content.WriteString(launchRowStyle.Render(freshCursor + freshLabel))
	}
	content.WriteString("\n")

	// --- Recent sessions ---
	if len(m.sessions) > 0 {
		content.WriteString("\n")
		content.WriteString(launchRuleStyle.Render(strings.Repeat("─", launchContentWidth)))
		content.WriteString("\n\n")

		content.WriteString("  " + launchSectionStyle.Render("Resume a recent session"))
		content.WriteString("\n\n")

		// Column headers
		content.WriteString(fmt.Sprintf("   %s%s",
			launchColHeaderStyle.Render(fmt.Sprintf("%-12s", "AGE")),
			launchColHeaderStyle.Render("PREVIEW"),
		))
		content.WriteString("\n")
		content.WriteString("   " + launchRuleStyle.Render(strings.Repeat("╌", launchContentWidth-3)))
		content.WriteString("\n")

		for i, s := range m.sessions {
			itemIdx := i + 1 // offset by 1 because "Start fresh" is 0
			selected := m.cursor == itemIdx

			cursor := "   "
			if selected {
				cursor = " " + cursorActiveStyle.Render("") + " "
			}

			age := launchAgeStyle.Render(fmt.Sprintf("%-12s", s.Age))

			preview := s.Preview
			if preview == "" {
				preview = "(no preview)"
			}
			maxPreview := launchContentWidth - 18
			if len(preview) > maxPreview {
				preview = preview[:maxPreview-1] + "…"
			}

			var previewStr string
			if preview == "(no preview)" {
				previewStr = launchPreviewDimStyle.Render(preview)
			} else if selected {
				previewStr = launchPreviewSelectedStyle.Render(preview)
			} else {
				previewStr = launchPreviewStyle.Render(preview)
			}

			inner := cursor + age + previewStr

			if selected {
				content.WriteString(launchRowSelectedStyle.Render(inner))
			} else {
				content.WriteString(launchRowStyle.Render(inner))
			}
			content.WriteString("\n")
		}
	}

	// Hint bar
	hints := []struct{ key, label string }{
		{"j/k", "navigate"},
		{"enter", "select"},
		{"d", "change dir"},
		{"f", "fresh"},
		{"esc", "cancel"},
	}
	var hintParts []string
	for _, h := range hints {
		hintParts = append(hintParts, menuHintKeyStyle.Render(h.key)+" "+menuHintLabelStyle.Render(h.label))
	}
	content.WriteString(launchHintBarStyle.Render(strings.Join(hintParts, "  "+menuHintDotStyle.Render("·")+"  ")))

	return tea.NewView("\n" + launchBoxStyle.Render(content.String()) + "\n")
}
