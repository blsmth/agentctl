package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/prompt"
	"github.com/blsmth/agentctl/internal/theme"
)

// ── Skill scope ───────────────────────────────────────────────────────────────

// SkillScope indicates where a skill command was discovered.
type SkillScope int

const (
	SkillScopeGlobal  SkillScope = iota // ~/.claude/commands/
	SkillScopeProject                   // $CWD/.claude/commands/
)

func (s SkillScope) String() string {
	switch s {
	case SkillScopeGlobal:
		return "global"
	case SkillScopeProject:
		return "project"
	default:
		return "unknown"
	}
}

// ── SkillEntry ────────────────────────────────────────────────────────────────

// SkillEntry represents a single discovered slash-command skill file.
type SkillEntry struct {
	Name     string     // command name (filename sans .md)
	FilePath string     // absolute path to the .md file
	Scope    SkillScope // global or project
	Tokens   int        // estimated token count
}

// DiscoverSkills finds all slash-command .md files from global and
// project-local command directories. Project-local commands are discovered
// when cwd is non-empty and .claude/commands/ exists there.
func DiscoverSkills(cwd string) []SkillEntry {
	var entries []SkillEntry
	seen := make(map[string]bool)

	addDir := func(dir string, scope SkillScope) {
		fis, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, fi := range fis {
			if fi.IsDir() || !strings.HasSuffix(fi.Name(), ".md") {
				continue
			}
			absPath := filepath.Join(dir, fi.Name())
			if seen[absPath] {
				continue
			}
			seen[absPath] = true
			name := strings.TrimSuffix(fi.Name(), ".md")
			tok := estimateSkillTokens(absPath)
			entries = append(entries, SkillEntry{
				Name:     name,
				FilePath: absPath,
				Scope:    scope,
				Tokens:   tok,
			})
		}
	}

	// Global: ~/.claude/commands/
	home, _ := os.UserHomeDir()
	if home != "" {
		addDir(filepath.Join(home, ".claude", "commands"), SkillScopeGlobal)
	}

	// Project-local: $CWD/.claude/commands/
	if cwd != "" {
		addDir(filepath.Join(cwd, ".claude", "commands"), SkillScopeProject)
	}

	return entries
}

// estimateSkillTokens reads a file and estimates token count.
func estimateSkillTokens(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return prompt.EstimateTokens(string(data))
}

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	skColHeaderStyle    lipgloss.Style
	skRuleStyle         lipgloss.Style
	skDimStyle          lipgloss.Style
	skSubtitleStyle     lipgloss.Style
	skNameStyle         lipgloss.Style
	skNameSelectedStyle lipgloss.Style
	skScopeGlobalStyle  lipgloss.Style
	skScopeProjectStyle lipgloss.Style
)

// initSkillsStyles initialises skills-viewer styles from the active theme.
// Must be called after initTableStyles().
func initSkillsStyles() {
	t := theme.Current()

	skColHeaderStyle = tableStyles.ColHeader
	skRuleStyle = tableStyles.Rule
	skDimStyle = tableStyles.Dim
	skSubtitleStyle = tableStyles.Subtitle

	skNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	skNameSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)

	skScopeGlobalStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	skScopeProjectStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Blue))
}

// ── Column widths ─────────────────────────────────────────────────────────────

const (
	skColName   = 28
	skColScope  = 10
	skColTokens = 8
)

// ── SkillsModel ───────────────────────────────────────────────────────────────

// SkillsModel is the full-screen Bubble Tea model for browsing Claude Code
// slash-command skills (~/.claude/commands/ and .claude/commands/).
type SkillsModel struct {
	termWidth  int
	termHeight int

	allEntries []SkillEntry // all discovered skills
	entries    []SkillEntry // filtered view

	// Navigation
	cursor int

	// Search / filter
	searchMode  bool
	searchInput textinput.Model
	searchQuery string

	// Preview — embedded PromptPreviewComponent
	preview   PromptPreviewComponent
	splitPane SplitPane

	// New command prompt
	newMode   bool
	newInput  textinput.Model
	newErrMsg string

	// After quit: file to open in $EDITOR (empty if none).
	editTarget string

	quitting bool
}

// NewSkillsModel creates the full-screen skills browser for the given working
// directory. Pass an empty string to skip project-local discovery.
func NewSkillsModel(cwd string) SkillsModel {
	entries := DiscoverSkills(cwd)

	si := textinput.New()
	si.Placeholder = "filter commands..."
	siS := si.Styles()
	siS.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	si.SetStyles(siS)

	ni := textinput.New()
	ni.Placeholder = "command-name (creates ~/.claude/commands/<name>.md)"
	niS := ni.Styles()
	niS.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	ni.SetStyles(niS)

	m := SkillsModel{
		termWidth:   120,
		termHeight:  40,
		allEntries:  entries,
		searchInput: si,
		newInput:    ni,
		splitPane: SplitPane{
			ListRatio:     0.4,
			MinListRows:   4,
			MinDetailRows: 6,
		},
	}
	m.applyFilters()
	m.loadPreview()
	return m
}

// EditTarget returns the file that should be opened in $EDITOR after the
// model quits, or empty string if no edit was requested.
func (m SkillsModel) EditTarget() string { return m.editTarget }

// ── Filter ────────────────────────────────────────────────────────────────────

func (m *SkillsModel) applyFilters() {
	var filtered []SkillEntry
	for _, e := range m.allEntries {
		if m.searchQuery != "" {
			q := strings.ToLower(m.searchQuery)
			if !strings.Contains(strings.ToLower(e.Name), q) {
				continue
			}
		}
		filtered = append(filtered, e)
	}
	m.entries = filtered
	if len(m.entries) == 0 {
		m.cursor = 0
	} else if m.cursor >= len(m.entries) {
		m.cursor = len(m.entries) - 1
	}
	m.splitPane.SetCursor(m.cursor)
}

// ── Preview ───────────────────────────────────────────────────────────────────

func (m *SkillsModel) loadPreview() {
	if len(m.entries) == 0 {
		m.preview.SetContent(nil)
		return
	}
	e := m.entries[m.cursor]
	rp, err := prompt.Resolve(e.FilePath)
	if err != nil {
		// Fallback: build a minimal ResolvedPrompt from raw content.
		data, _ := os.ReadFile(e.FilePath)
		content := string(data)
		rp = &prompt.ResolvedPrompt{
			Agent:      e.Name,
			FilePath:   e.FilePath,
			RawContent: content,
			Expanded:   content,
			Sections: []prompt.Section{
				{
					Source:  e.FilePath,
					Content: content,
					Tokens:  prompt.EstimateTokens(content),
				},
			},
		}
		rp.Tokens = prompt.ComputeBreakdown(rp)
	}
	m.preview.SetContent(rp)
}

// ── Layout helpers ────────────────────────────────────────────────────────────

func (m SkillsModel) contentWidth() int {
	w := m.termWidth - 4
	if w < 40 {
		w = 40
	}
	return w
}

// ── Bubble Tea interface ──────────────────────────────────────────────────────

func (m SkillsModel) Init() tea.Cmd { return nil }

func (m SkillsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		paneH := m.termHeight - 6 // reserve title + subtitle + header + hint rows
		if paneH < 10 {
			paneH = 10
		}
		m.splitPane.SetSize(msg.Width, paneH)
		previewW := msg.Width - 6
		if previewW < 40 {
			previewW = 40
		}
		m.preview.SetSize(previewW, m.splitPane.DetailHeight()-2)
		return m, nil

	case tea.KeyPressMsg:
		if m.newMode {
			return m.updateNewMode(msg)
		}
		if m.searchMode {
			return m.updateSearch(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

// ── List update ───────────────────────────────────────────────────────────────

func (m SkillsModel) updateList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.quitting = true
		return m, tea.Quit

	case "esc":
		if m.searchQuery != "" {
			m.searchQuery = ""
			m.searchInput.SetValue("")
			m.applyFilters()
			m.loadPreview()
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit

	case "j", "down":
		if m.cursor < len(m.entries)-1 {
			m.cursor++
			m.splitPane.SetCursor(m.cursor)
			m.loadPreview()
		}
		return m, nil

	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
			m.splitPane.SetCursor(m.cursor)
			m.loadPreview()
		}
		return m, nil

	case "enter":
		// Confirm selection and refresh preview.
		if len(m.entries) > 0 {
			m.loadPreview()
		}
		return m, nil

	case "e":
		if len(m.entries) > 0 {
			return m.openEditor(m.entries[m.cursor].FilePath)
		}
		return m, nil

	case "n":
		m.newMode = true
		m.newInput.SetValue("")
		m.newErrMsg = ""
		m.newInput.Focus()
		return m, textinput.Blink

	case "/":
		m.searchMode = true
		m.searchInput.SetValue(m.searchQuery)
		m.searchInput.Focus()
		return m, textinput.Blink

	default:
		// Delegate scroll keys to the preview component.
		if consumed := m.preview.Update(msg); consumed {
			return m, nil
		}
	}
	return m, nil
}

// ── Search update ─────────────────────────────────────────────────────────────

func (m SkillsModel) updateSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.searchMode = false
		m.searchQuery = ""
		m.searchInput.SetValue("")
		m.searchInput.Blur()
		m.applyFilters()
		m.loadPreview()
		return m, nil

	case "enter":
		m.searchMode = false
		m.searchQuery = m.searchInput.Value()
		m.searchInput.Blur()
		m.applyFilters()
		m.loadPreview()
		return m, nil
	}

	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	m.searchQuery = m.searchInput.Value()
	m.applyFilters()
	m.loadPreview()
	return m, cmd
}

// ── New command update ────────────────────────────────────────────────────────

func (m SkillsModel) updateNewMode(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.newMode = false
		m.newInput.Blur()
		return m, nil

	case "enter":
		name := strings.TrimSpace(m.newInput.Value())
		if name == "" {
			m.newErrMsg = "name cannot be empty"
			return m, nil
		}
		for _, r := range name {
			if !isValidSkillNameChar(r) {
				m.newErrMsg = "name may only contain letters, digits, hyphens, and underscores"
				return m, nil
			}
		}
		filePath, err := createSkillFile(name)
		if err != nil {
			m.newErrMsg = err.Error()
			return m, nil
		}
		m.newMode = false
		m.newInput.Blur()
		// Reload entries.
		cwd, _ := os.Getwd()
		m.allEntries = DiscoverSkills(cwd)
		m.applyFilters()
		// Move cursor to the newly created skill.
		for i, e := range m.entries {
			if e.FilePath == filePath {
				m.cursor = i
				m.splitPane.SetCursor(i)
				break
			}
		}
		m.loadPreview()
		return m.openEditor(filePath)
	}

	var cmd tea.Cmd
	m.newInput, cmd = m.newInput.Update(msg)
	return m, cmd
}

// ── Editor helper ─────────────────────────────────────────────────────────────

// openEditor stores the target path and quits so the caller can invoke $EDITOR.
func (m SkillsModel) openEditor(filePath string) (tea.Model, tea.Cmd) {
	m.editTarget = filePath
	m.quitting = true
	return m, tea.Quit
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m SkillsModel) View() tea.View {
	v := tea.NewView(m.buildView())
	v.AltScreen = true
	return v
}

func (m SkillsModel) buildView() string {
	cw := m.contentWidth()
	t := theme.Current()

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Width(cw).
		Align(lipgloss.Center)

	var b strings.Builder

	// ── Title ──────────────────────────────────────────────────────────────
	b.WriteString(titleStyle.Render("Skills — Slash Commands"))
	b.WriteString("\n")

	// ── New command input overlay ───────────────────────────────────────────
	if m.newMode {
		b.WriteString("\n")
		b.WriteString(skDimStyle.Render("  New command name: ") + m.newInput.View())
		b.WriteString("\n")
		if m.newErrMsg != "" {
			errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Red))
			b.WriteString("  " + errStyle.Render(m.newErrMsg))
			b.WriteString("\n")
		}
		b.WriteString("\n")
		hintBarCentered := lipgloss.NewStyle().Width(cw).Align(lipgloss.Center)
		b.WriteString(hintBarCentered.Render(renderHintLine("enter", "create", "esc", "cancel")))
		return b.String()
	}

	// ── Subtitle ────────────────────────────────────────────────────────────
	b.WriteString("\n")
	if m.searchQuery != "" {
		b.WriteString(skSubtitleStyle.Render(fmt.Sprintf(
			"  filtering: %q  (%d of %d)",
			m.searchQuery, len(m.entries), len(m.allEntries),
		)))
	} else {
		b.WriteString(skSubtitleStyle.Render(fmt.Sprintf("  %d commands", len(m.entries))))
	}
	b.WriteString("\n\n")

	// ── Column headers ──────────────────────────────────────────────────────
	b.WriteString(fmt.Sprintf("  %s%s%s",
		skColHeaderStyle.Width(skColName).Render("COMMAND"),
		skColHeaderStyle.Width(skColScope).Render("SCOPE"),
		skColHeaderStyle.Width(skColTokens).Render("TOKENS"),
	))
	b.WriteString("\n")
	b.WriteString("  " + skRuleStyle.Render(strings.Repeat("╌", cw-2)))
	b.WriteString("\n")

	// ── SplitPane (list + preview) ──────────────────────────────────────────
	listContent := m.buildListContent()
	previewContent := m.buildPreviewContent()

	// Clone splitPane to avoid mutating value receiver.
	sp := m.splitPane
	sp.SetListContent(listContent, len(m.entries))
	sp.SetDetailContent(previewContent)
	b.WriteString(sp.View())
	b.WriteString("\n")

	// ── Hint bar ────────────────────────────────────────────────────────────
	if m.searchMode {
		dot := "  " + menuHintDotStyle.Render("·") + "  "
		b.WriteString(
			menuHintKeyStyle.Render("/") + " " + m.searchInput.View() +
				dot +
				menuHintKeyStyle.Render("enter") + " " + menuHintLabelStyle.Render("confirm") +
				dot +
				menuHintKeyStyle.Render("esc") + " " + menuHintLabelStyle.Render("clear"),
		)
	} else {
		hintBarCentered := lipgloss.NewStyle().Width(cw).Align(lipgloss.Center)
		b.WriteString(hintBarCentered.Render(renderHintLine(
			"j/k", "navigate",
			"enter", "select",
			"e", "edit",
			"n", "new",
			"/", "filter",
			"q", "quit",
		)))
	}

	return b.String()
}

// buildListContent returns the rendered list rows as a single string.
func (m SkillsModel) buildListContent() string {
	if len(m.entries) == 0 {
		return skDimStyle.Render("   No commands found. Press n to create one.")
	}

	var b strings.Builder
	for i, e := range m.entries {
		selected := i == m.cursor

		cursor := "   "
		if selected {
			cursor = " " + cursorActiveStyle.Render("▸") + " "
		}

		// Name
		name := e.Name
		if len(name) > skColName-1 {
			name = name[:skColName-2] + "…"
		}
		var nameStr string
		if selected {
			nameStr = skNameSelectedStyle.Width(skColName).Render(name)
		} else {
			nameStr = skNameStyle.Width(skColName).Render(name)
		}

		// Scope
		var scopeStr string
		switch e.Scope {
		case SkillScopeProject:
			scopeStr = skScopeProjectStyle.Width(skColScope).Render("project")
		default:
			scopeStr = skScopeGlobalStyle.Width(skColScope).Render("global")
		}

		// Tokens
		tokStr := "-"
		if e.Tokens > 0 {
			if e.Tokens >= 1000 {
				tokStr = fmt.Sprintf("%dk", e.Tokens/1000)
			} else {
				tokStr = fmt.Sprintf("%d", e.Tokens)
			}
		}
		tokRendered := skDimStyle.Width(skColTokens).Render(tokStr)

		b.WriteString(cursor + nameStr + scopeStr + tokRendered)
		if i < len(m.entries)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// buildPreviewContent returns the rendered preview pane content.
func (m SkillsModel) buildPreviewContent() string {
	if len(m.entries) == 0 {
		return skDimStyle.Render("  Select a command to preview it here.")
	}
	e := m.entries[m.cursor]
	t := theme.Current()

	heading := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Text)).
		Render(fmt.Sprintf("  /%s", e.Name)) +
		"  " +
		skDimStyle.Render(fmt.Sprintf("(%s)", e.Scope.String()))

	return heading + "\n\n" + m.preview.View()
}

// ── File creation ─────────────────────────────────────────────────────────────

// createSkillFile creates a new skill file in ~/.claude/commands/<name>.md
// and returns its absolute path.
func createSkillFile(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	dir := filepath.Join(home, ".claude", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating commands directory: %w", err)
	}
	path := filepath.Join(dir, name+".md")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("command %q already exists", name)
	}
	content := fmt.Sprintf("# %s\n\n<!-- Describe what this command does. -->\n", name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
	return path, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// isValidSkillNameChar reports whether r is a valid character in a skill name:
// letters, digits, hyphens, and underscores are allowed.
func isValidSkillNameChar(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '-' || r == '_'
}
