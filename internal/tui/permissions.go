package tui

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/theme"
)

// PermissionPreset is a group of related permission patterns that can be toggled together.
type PermissionPreset struct {
	Key      string   // short key like "git"
	Label    string   // display label
	Patterns []string // permission patterns like "Bash(git *)"
}

// DefaultPresets returns the common permission groups.
func DefaultPresets() []PermissionPreset {
	return []PermissionPreset{
		{Key: "git", Label: "Git", Patterns: []string{"Bash(git *)"}},
		{Key: "gh", Label: "GitHub CLI", Patterns: []string{"Bash(gh *)"}},
		{Key: "go", Label: "Go toolchain", Patterns: []string{"Bash(go *)"}},
		{Key: "just", Label: "Just runner", Patterns: []string{"Bash(just *)"}},
		{Key: "make", Label: "Make", Patterns: []string{"Bash(make *)"}},
		{Key: "npm", Label: "npm / npx", Patterns: []string{"Bash(npm *)", "Bash(npx *)"}},
		{Key: "docker", Label: "Docker", Patterns: []string{"Bash(docker *)", "Bash(docker-compose *)"}},
		{Key: "python", Label: "Python / uv", Patterns: []string{"Bash(python *)", "Bash(uv *)"}},
		{Key: "terraform", Label: "Terraform", Patterns: []string{"Bash(terraform *)"}},
	}
}

// PermissionsResult is the output of the permissions popup.
type PermissionsResult struct {
	Cancelled  bool
	Presets    map[string]bool
	Deletions  []string // standalone perms the user deleted
	CleanedUp  bool     // user pressed 'c' to remove redundant
	Standalone []string // surviving standalone perms after deletions/cleanup
}

type permissionEntry struct {
	preset  PermissionPreset
	on      bool
	coversN int // how many other permissions this preset's patterns cover
}

// Content width inside the box (box padding + border eat some).
const permContentWidth = 72

// Permissions popup styles.
var (
	permBoxStyle                lipgloss.Style
	permTitleStyle              lipgloss.Style
	permProjectStyle            lipgloss.Style
	permRowStyle                lipgloss.Style
	permRowSelectedStyle        lipgloss.Style
	permToggleOnStyle           lipgloss.Style
	permToggleOffStyle          lipgloss.Style
	permLabelStyle              lipgloss.Style
	permLabelSelectedStyle      lipgloss.Style
	permPatternStyle            lipgloss.Style
	permCoversBadgeStyle        lipgloss.Style
	permDividerStyle            lipgloss.Style
	permStandaloneStyle         lipgloss.Style
	permStandaloneSelectedStyle lipgloss.Style
	permStandaloneDimStyle      lipgloss.Style
	permHintBarStyle            lipgloss.Style
	permTabActiveStyle          lipgloss.Style
	permTabInactiveStyle        lipgloss.Style
	permTabSepStyle             lipgloss.Style
)

// initPermissionStyles initialises permissions-popup styles from the active theme.
func initPermissionStyles() {
	t := theme.Current()

	permBoxStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.Accent)).
		Padding(1, 2).
		Width(permContentWidth + 6)

	permTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Width(permContentWidth).
		Align(lipgloss.Center)

	permProjectStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Italic(true)

	permRowStyle = lipgloss.NewStyle().Width(permContentWidth)

	permRowSelectedStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.Surface)).
		Width(permContentWidth)

	permToggleOnStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Green)).
		Bold(true)

	permToggleOffStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Overlay))

	permLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))

	permLabelSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)

	permPatternStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))

	permCoversBadgeStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Italic(true)

	permDividerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Surface))

	permStandaloneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))

	permStandaloneSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Text)).
		Bold(true)

	permStandaloneDimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Overlay))

	permHintBarStyle = lipgloss.NewStyle().
		Width(permContentWidth).
		Align(lipgloss.Center).
		MarginTop(1).
		Foreground(lipgloss.Color(t.Muted))

	permTabActiveStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Bold(true).
		Padding(0, 2)

	permTabInactiveStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Padding(0, 2)

	permTabSepStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Surface))
}

// PermissionsModel is the Bubble Tea model for the permissions popup.
type PermissionsModel struct {
	agent        string
	settingsFile string            // path to .claude/settings.local.json being edited
	globalPerms  []string          // read-only global permissions from ~/.claude/settings.json
	entries      []permissionEntry // preset toggles (project tab)
	standalone   []string          // uncovered permissions (project tab)
	covered      map[string]string // specific perm → covering preset label
	tab          int               // 0=project, 1=global
	cursor       int               // cursor within current tab
	scrollOff    int               // scroll offset for standalone (project) or global list
	result       PermissionsResult
	quitting     bool
}

// maxVisible is how many scrollable rows fit in the viewport.
// Project standalone gets fewer rows (presets take space above).
const (
	maxVisibleStandalone = 4
	maxVisibleGlobal     = 12
)

// projectItemCount returns the number of selectable items on the project tab.
func (m PermissionsModel) projectItemCount() int {
	return len(m.entries) + len(m.standalone)
}

// NewPermissionsModel creates the permissions popup.
func NewPermissionsModel(agentName, projectRoot, settingsPath string, currentAllow, globalAllow []string) PermissionsModel {
	presets := DefaultPresets()
	entries := make([]permissionEntry, len(presets))

	// Track which patterns are accounted for by presets (exact match on preset patterns)
	accountedFor := make(map[string]bool)
	// Track which patterns are covered by a preset's broad pattern
	covered := make(map[string]string) // specific → preset label

	for i, p := range presets {
		allPresent := true
		for _, pattern := range p.Patterns {
			found := false
			for _, a := range currentAllow {
				if a == pattern {
					found = true
					accountedFor[a] = true
					break
				}
			}
			if !found {
				allPresent = false
			}
		}
		entries[i] = permissionEntry{
			preset: p,
			on:     len(p.Patterns) > 0 && allPresent,
		}

		for _, pattern := range p.Patterns {
			for _, a := range currentAllow {
				if a == pattern {
					accountedFor[a] = true
				}
			}
		}
	}

	// Smart matching: find covered permissions
	for i, entry := range entries {
		if !entry.on {
			continue
		}
		coversCount := 0
		for _, pattern := range entry.preset.Patterns {
			for _, a := range currentAllow {
				if accountedFor[a] {
					continue
				}
				if claude.PermissionCovers(pattern, a) {
					covered[a] = entry.preset.Label
					coversCount++
				}
			}
		}
		entries[i].coversN = coversCount
	}

	// Standalone = not accounted for by a preset and not covered by one
	var standalone []string
	for _, a := range currentAllow {
		if !accountedFor[a] && covered[a] == "" {
			standalone = append(standalone, a)
		}
	}

	return PermissionsModel{
		agent:        agentName,
		settingsFile: settingsPath,
		globalPerms:  globalAllow,
		entries:      entries,
		standalone:   standalone,
		covered:      covered,
		tab:          0,
		cursor:       0,
		result: PermissionsResult{
			Cancelled: true,
			Presets:   make(map[string]bool),
		},
	}
}

// PopupSize returns the tmux display-popup dimensions for the permissions popup.
func (m PermissionsModel) PopupSize() PopupSize {
	return PopupDimensions(m.View().Content)
}

// Result returns the popup output after it quits.
func (m PermissionsModel) Result() PermissionsResult { return m.result }

func (m PermissionsModel) Init() tea.Cmd { return nil }

// inStandalone returns true if the cursor is in the standalone section (project tab).
func (m PermissionsModel) inStandalone() bool {
	return m.tab == 0 && m.cursor >= len(m.entries)
}

// standaloneIdx returns the index into m.standalone for the current cursor.
func (m PermissionsModel) standaloneIdx() int {
	return m.cursor - len(m.entries)
}

func (m PermissionsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.quitting = true
			return m, tea.Quit

		case "tab":
			// Switch tabs
			if m.tab == 0 {
				m.tab = 1
			} else {
				m.tab = 0
			}
			m.cursor = 0
			m.scrollOff = 0

		case "up", "k":
			m.moveCursorUp()

		case "down", "j":
			m.moveCursorDown()

		case "enter", " ":
			if m.tab == 0 && !m.inStandalone() {
				m.entries[m.cursor].on = !m.entries[m.cursor].on
			}

		case "d":
			if m.tab == 0 && m.inStandalone() && len(m.standalone) > 0 {
				idx := m.standaloneIdx()
				m.standalone = append(m.standalone[:idx], m.standalone[idx+1:]...)
				if m.cursor >= m.projectItemCount() && m.cursor > 0 {
					m.cursor--
				}
				m.adjustProjectScroll()
			}

		case "c":
			if m.tab == 0 {
				var kept []string
				for _, perm := range m.standalone {
					isCovered := false
					for _, entry := range m.entries {
						if !entry.on {
							continue
						}
						for _, pattern := range entry.preset.Patterns {
							if claude.PermissionCovers(pattern, perm) {
								isCovered = true
								break
							}
						}
						if isCovered {
							break
						}
					}
					if !isCovered {
						kept = append(kept, perm)
					}
				}
				m.result.CleanedUp = true
				m.standalone = kept
				if m.cursor >= m.projectItemCount() && m.cursor > 0 {
					m.cursor = m.projectItemCount() - 1
				}
				if m.cursor < 0 {
					m.cursor = 0
				}
				m.adjustProjectScroll()
			}

		case "a":
			if m.tab == 0 {
				for i := range m.entries {
					m.entries[i].on = true
				}
			}

		case "s":
			if m.tab == 0 {
				m.result.Cancelled = false
				for _, e := range m.entries {
					m.result.Presets[e.preset.Key] = e.on
				}
				m.result.Standalone = append([]string{}, m.standalone...)
				m.quitting = true
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m *PermissionsModel) moveCursorUp() {
	if m.cursor > 0 {
		m.cursor--
	}
	if m.tab == 0 {
		m.adjustProjectScroll()
	} else {
		m.adjustGlobalScroll()
	}
}

func (m *PermissionsModel) moveCursorDown() {
	max := 0
	if m.tab == 0 {
		max = m.projectItemCount() - 1
	} else {
		max = len(m.globalPerms) - 1
	}
	if max < 0 {
		max = 0
	}
	if m.cursor < max {
		m.cursor++
	}
	if m.tab == 0 {
		m.adjustProjectScroll()
	} else {
		m.adjustGlobalScroll()
	}
}

func (m *PermissionsModel) adjustProjectScroll() {
	if !m.inStandalone() {
		m.scrollOff = 0
		return
	}
	idx := m.standaloneIdx()
	if idx < m.scrollOff {
		m.scrollOff = idx
	}
	if idx >= m.scrollOff+maxVisibleStandalone {
		m.scrollOff = idx - maxVisibleStandalone + 1
	}
}

func (m *PermissionsModel) adjustGlobalScroll() {
	if m.cursor < m.scrollOff {
		m.scrollOff = m.cursor
	}
	if m.cursor >= m.scrollOff+maxVisibleGlobal {
		m.scrollOff = m.cursor - maxVisibleGlobal + 1
	}
}

// fixedContentLines is the target number of lines for the tab content area
// (between the tab bar and the hint bar). Both tabs pad to this height
// so the popup box stays stable when switching tabs.
const fixedContentLines = 18

func (m PermissionsModel) View() tea.View {
	var content strings.Builder

	// Title
	content.WriteString(permTitleStyle.Render("Permissions"))
	content.WriteString("\n")

	// Agent name + tab bar on same line to save space
	content.WriteString("\n")

	// Tab bar
	projectCount := len(m.entries) + len(m.standalone)
	for _, e := range m.entries {
		projectCount += e.coversN
	}
	globalCount := len(m.globalPerms)

	projectLabel := fmt.Sprintf(" Project (%d) ", projectCount)
	globalLabel := fmt.Sprintf(" Global (%d) ", globalCount)

	if m.tab == 0 {
		content.WriteString("  " +
			permTabActiveStyle.Render(projectLabel) +
			permTabSepStyle.Render(" │ ") +
			permTabInactiveStyle.Render(globalLabel) +
			"  " + permProjectStyle.Render(m.agent))
	} else {
		content.WriteString("  " +
			permTabInactiveStyle.Render(projectLabel) +
			permTabSepStyle.Render(" │ ") +
			permTabActiveStyle.Render(globalLabel) +
			"  " + permProjectStyle.Render(m.agent))
	}
	content.WriteString("\n")

	// File path for current tab
	if m.tab == 0 {
		content.WriteString("  " + permProjectStyle.Render(shortenPath(m.settingsFile)))
	} else {
		content.WriteString("  " + permProjectStyle.Render("~/.claude/settings.json"))
	}
	content.WriteString("\n\n")

	// Tab content — render into a separate buffer so we can pad to fixed height
	var tabContent strings.Builder
	if m.tab == 0 {
		m.renderProjectTab(&tabContent)
	} else {
		m.renderGlobalTab(&tabContent)
	}

	// Count lines and pad to fixedContentLines
	tabStr := tabContent.String()
	lineCount := strings.Count(tabStr, "\n")
	content.WriteString(tabStr)
	for i := lineCount; i < fixedContentLines; i++ {
		content.WriteString("\n")
	}

	// Hint bar
	content.WriteString(m.renderHints())

	return tea.NewView("\n" + permBoxStyle.Render(content.String()) + "\n")
}

func (m PermissionsModel) renderProjectTab(content *strings.Builder) {
	// Preset toggles
	for i, e := range m.entries {
		selected := m.cursor == i

		cursor := "   "
		if selected {
			cursor = " " + cursorActiveStyle.Render("") + " "
		}

		toggle := permToggleOffStyle.Render("○")
		if e.on {
			toggle = permToggleOnStyle.Render("●")
		}

		label := permLabelStyle.Render(fmt.Sprintf("%-16s", e.preset.Label))
		if selected {
			label = permLabelSelectedStyle.Render(fmt.Sprintf("%-16s", e.preset.Label))
		}

		patternStr := permPatternStyle.Render(strings.Join(e.preset.Patterns, ", "))

		badge := ""
		if e.on && e.coversN > 0 {
			badge = " " + permCoversBadgeStyle.Render(fmt.Sprintf("+%d covered", e.coversN))
		}

		inner := fmt.Sprintf("%s%s %s %s%s", cursor, toggle, label, patternStr, badge)

		if selected {
			content.WriteString(permRowSelectedStyle.Render(inner))
		} else {
			content.WriteString(permRowStyle.Render(inner))
		}
		content.WriteString("\n")
	}

	// Standalone section
	if len(m.standalone) > 0 {
		content.WriteString("\n")
		content.WriteString("  " + permDividerStyle.Render(strings.Repeat("┈", permContentWidth-4)))
		content.WriteString("\n")
		content.WriteString("  " + permPatternStyle.Render(fmt.Sprintf("%d standalone", len(m.standalone))))
		content.WriteString("\n\n")

		end := m.scrollOff + maxVisibleStandalone
		if end > len(m.standalone) {
			end = len(m.standalone)
		}

		if m.scrollOff > 0 {
			content.WriteString("  " + permStandaloneDimStyle.Render(fmt.Sprintf("  ↑ %d more", m.scrollOff)))
			content.WriteString("\n")
		}

		for i := m.scrollOff; i < end; i++ {
			perm := m.standalone[i]
			itemCursor := m.cursor == len(m.entries)+i
			cursorStr := "   "
			if itemCursor {
				cursorStr = " " + cursorActiveStyle.Render("") + " "
			}

			display := shortenPath(perm)
			maxLen := permContentWidth - 8
			if len(display) > maxLen {
				display = display[:maxLen-1] + "…"
			}

			var permStr string
			if itemCursor {
				permStr = permStandaloneSelectedStyle.Render(display)
			} else {
				permStr = permStandaloneStyle.Render(display)
			}

			inner := cursorStr + permStr
			if itemCursor {
				content.WriteString(permRowSelectedStyle.Render(inner))
			} else {
				content.WriteString(permRowStyle.Render(inner))
			}
			content.WriteString("\n")
		}

		remaining := len(m.standalone) - end
		if remaining > 0 {
			content.WriteString("  " + permStandaloneDimStyle.Render(fmt.Sprintf("  ↓ %d more", remaining)))
			content.WriteString("\n")
		}
	}
}

func (m PermissionsModel) renderGlobalTab(content *strings.Builder) {
	if len(m.globalPerms) == 0 {
		content.WriteString("  " + permStandaloneDimStyle.Render("No global permissions configured"))
		content.WriteString("\n")
		return
	}

	end := m.scrollOff + maxVisibleGlobal
	if end > len(m.globalPerms) {
		end = len(m.globalPerms)
	}

	if m.scrollOff > 0 {
		content.WriteString("  " + permStandaloneDimStyle.Render(fmt.Sprintf("  ↑ %d more", m.scrollOff)))
		content.WriteString("\n")
	}

	for i := m.scrollOff; i < end; i++ {
		perm := m.globalPerms[i]
		selected := m.cursor == i

		cursorStr := "   "
		if selected {
			cursorStr = " " + cursorActiveStyle.Render("") + " "
		}

		display := shortenPath(perm)
		maxLen := permContentWidth - 8
		if len(display) > maxLen {
			display = display[:maxLen-1] + "…"
		}

		var permStr string
		if selected {
			permStr = permStandaloneSelectedStyle.Render(display)
		} else {
			permStr = permStandaloneStyle.Render(display)
		}

		inner := cursorStr + permStr
		if selected {
			content.WriteString(permRowSelectedStyle.Render(inner))
		} else {
			content.WriteString(permRowStyle.Render(inner))
		}
		content.WriteString("\n")
	}

	remaining := len(m.globalPerms) - end
	if remaining > 0 {
		content.WriteString("  " + permStandaloneDimStyle.Render(fmt.Sprintf("  ↓ %d more", remaining)))
		content.WriteString("\n")
	}
}

func (m PermissionsModel) renderHints() string {
	var hints []struct{ key, label string }

	if m.tab == 1 {
		// Global tab: read-only
		hints = []struct{ key, label string }{
			{"j/k", "scroll"},
			{"tab", "project"},
			{"esc", "close"},
		}
	} else if m.inStandalone() {
		hints = []struct{ key, label string }{
			{"j/k", "navigate"},
			{"d", "delete"},
			{"c", "clean"},
			{"tab", "global"},
			{"s", "save"},
			{"esc", "cancel"},
		}
	} else {
		hints = []struct{ key, label string }{
			{"j/k", "navigate"},
			{"space", "toggle"},
			{"a", "all on"},
			{"c", "clean"},
			{"tab", "global"},
			{"s", "save"},
			{"esc", "cancel"},
		}
	}

	var hintParts []string
	for _, h := range hints {
		hintParts = append(hintParts, menuHintKeyStyle.Render(h.key)+" "+menuHintLabelStyle.Render(h.label))
	}
	return permHintBarStyle.Render(strings.Join(hintParts, "  "+menuHintDotStyle.Render("·")+"  "))
}

// shortenPath replaces the home directory with ~ for display.
func shortenPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}
