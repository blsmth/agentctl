package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/prompt"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/tokens"
)

// ── Memory TUI styles ─────────────────────────────────────────────────────────

var (
	memColHeaderStyle    lipgloss.Style
	memRuleStyle         lipgloss.Style
	memDimStyle          lipgloss.Style
	memNameStyle         lipgloss.Style
	memNameSelectedStyle lipgloss.Style
	memScopeGlobalStyle  lipgloss.Style
	memScopeProjectStyle lipgloss.Style
	memScopeSharedStyle  lipgloss.Style
	memSubtitleStyle     lipgloss.Style
)

// initMemoryStyles initialises memory-viewer styles from the active theme.
// Shared styles are sourced from tableStyles.
func initMemoryStyles() {
	t := theme.Current()

	memColHeaderStyle = tableStyles.ColHeader
	memRuleStyle = tableStyles.Rule
	memDimStyle = tableStyles.Dim
	memSubtitleStyle = tableStyles.Subtitle

	memNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	memNameSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)

	memScopeGlobalStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	memScopeProjectStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Blue))
	memScopeSharedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green))

}

// ── Column widths ─────────────────────────────────────────────────────────────

const (
	memColPath    = 45
	memColScope   = 9
	memColTokens  = 8
	memColModTime = 12
)

// ── MemoryEntry ───────────────────────────────────────────────────────────────

// MemoryEntry holds metadata for a single discovered memory file.
type MemoryEntry struct {
	Path     string    // absolute path
	Scope    string    // "global", "project", or "shared"
	Tokens   int       // estimated token count
	Modified time.Time // last modification time
	Order    int       // composition order (0 = first seen by Claude)
}

// DiscoverMemoryFiles finds all memory files Claude uses, in composition order
// (global → project), deduplicating by absolute path.
//
// Files discovered:
//   - ~/.claude/CLAUDE.md (global)
//   - ~/.claude/settings.json (user settings)
//   - $CWD/CLAUDE.md (project)
//   - $CWD/.claude/CLAUDE.md (project)
//   - $CWD/.claude/settings.json (project settings)
func DiscoverMemoryFiles() []MemoryEntry {
	var entries []MemoryEntry
	seen := make(map[string]bool)
	order := 0

	add := func(path, scope string) {
		abs, err := filepath.Abs(path)
		if err != nil || seen[abs] {
			return
		}
		info, err := os.Stat(abs)
		if err != nil {
			return
		}
		seen[abs] = true
		tok := estimateMemoryFileTokens(abs)
		entries = append(entries, MemoryEntry{
			Path:     abs,
			Scope:    scope,
			Tokens:   tok,
			Modified: info.ModTime(),
			Order:    order,
		})
		order++
	}

	home, _ := os.UserHomeDir()
	if home != "" {
		add(filepath.Join(home, ".claude", "CLAUDE.md"), "global")
		add(filepath.Join(home, ".claude", "settings.json"), "global")
	}

	cwd, _ := os.Getwd()
	if cwd != "" {
		add(filepath.Join(cwd, "CLAUDE.md"), "project")
		add(filepath.Join(cwd, ".claude", "CLAUDE.md"), "project")
		add(filepath.Join(cwd, ".claude", "settings.json"), "project")
	}

	return entries
}

// estimateMemoryFileTokens reads a file and estimates its token count.
func estimateMemoryFileTokens(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return prompt.EstimateTokens(string(data))
}

// resolveMemoryPrompt builds a *prompt.ResolvedPrompt for a memory file.
// For .md files it uses prompt.Resolve; for .json it formats the JSON as
// a fenced code block inside a synthetic ResolvedPrompt.
func resolveMemoryPrompt(entry MemoryEntry) *prompt.ResolvedPrompt {
	if strings.HasSuffix(entry.Path, ".json") {
		return resolveJSONPrompt(entry)
	}
	rp, err := prompt.Resolve(entry.Path)
	if err != nil {
		// On error, return a minimal ResolvedPrompt with the raw file content.
		data, _ := os.ReadFile(entry.Path)
		raw := string(data)
		return &prompt.ResolvedPrompt{
			FilePath:   entry.Path,
			RawContent: raw,
			Expanded:   raw,
			Sections: []prompt.Section{{
				Source:  entry.Path,
				Content: raw,
				Tokens:  prompt.EstimateTokens(raw),
			}},
			Tokens: prompt.TokenBreakdown{
				Total: prompt.EstimateTokens(raw),
			},
		}
	}
	return rp
}

// resolveJSONPrompt creates a synthetic ResolvedPrompt for a settings.json file.
func resolveJSONPrompt(entry MemoryEntry) *prompt.ResolvedPrompt {
	data, err := os.ReadFile(entry.Path)
	if err != nil {
		return nil
	}

	// Pretty-print the JSON.
	var v interface{}
	var pretty string
	if err := json.Unmarshal(data, &v); err == nil {
		if b, err := json.MarshalIndent(v, "", "  "); err == nil {
			pretty = string(b)
		}
	}
	if pretty == "" {
		pretty = string(data)
	}

	content := "```json\n" + pretty + "\n```\n"
	tok := prompt.EstimateTokens(content)
	return &prompt.ResolvedPrompt{
		FilePath:   entry.Path,
		RawContent: string(data),
		Expanded:   content,
		Sections: []prompt.Section{{
			Source:  entry.Path,
			Content: content,
			Tokens:  tok,
		}},
		Tokens: prompt.TokenBreakdown{
			Total: tok,
		},
	}
}

// buildCompositePrompt creates a ResolvedPrompt from all memory entries
// concatenated in composition order with section separators.
func buildCompositePrompt(entries []MemoryEntry) *prompt.ResolvedPrompt {
	if len(entries) == 0 {
		return nil
	}

	var sections []prompt.Section
	var expanded strings.Builder
	total := 0

	for _, e := range entries {
		rp := resolveMemoryPrompt(e)
		if rp == nil {
			continue
		}
		for _, s := range rp.Sections {
			sections = append(sections, s)
			expanded.WriteString(s.Content)
			expanded.WriteString("\n")
			total += s.Tokens
		}
	}

	return &prompt.ResolvedPrompt{
		FilePath:   "(composite)",
		RawContent: expanded.String(),
		Expanded:   expanded.String(),
		Sections:   sections,
		Tokens: prompt.TokenBreakdown{
			Total: total,
			// No model is in scope for a composite memory view, so we use the
			// default usable window (routed through tokens.UsableWindow so
			// the output-reserve logic stays in one place).
			ContextPct: float64(total) / float64(tokens.UsableWindow("")) * 100,
		},
	}
}

// ── MemoryModel ───────────────────────────────────────────────────────────────

// MemoryModel is the full-screen Bubble Tea model for browsing memory files.
type MemoryModel struct {
	termWidth  int
	termHeight int

	allEntries []MemoryEntry // discovered memory files in composition order
	entries    []MemoryEntry // filtered view

	cursor int

	// Composite view — toggle with 'c'.
	compositeMode bool

	// Preview component — renders the selected file or composite content.
	preview PromptPreviewComponent

	// splitPane manages list scroll, cursor, and layout.
	splitPane SplitPane

	// Search / filter.
	searchMode  bool
	searchInput textinput.Model
	searchQuery string

	quitting bool
}

// NewMemoryModel creates the memory browser, discovers files, and loads the
// initial preview.
func NewMemoryModel() MemoryModel {
	si := textinput.New()
	si.Placeholder = "filter files..."
	siS := si.Styles()
	siS.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	si.SetStyles(siS)

	m := MemoryModel{
		termWidth:   120,
		termHeight:  40,
		searchInput: si,
		splitPane: SplitPane{
			ListRatio:     0.4,
			MinListRows:   4,
			MinDetailRows: 6,
		},
	}
	m.allEntries = DiscoverMemoryFiles()
	m.applyFilter()
	return m
}

// applyFilter rebuilds the visible entry list from allEntries using searchQuery.
func (m *MemoryModel) applyFilter() {
	if m.searchQuery == "" {
		m.entries = make([]MemoryEntry, len(m.allEntries))
		copy(m.entries, m.allEntries)
	} else {
		q := strings.ToLower(m.searchQuery)
		m.entries = m.entries[:0]
		for _, e := range m.allEntries {
			if strings.Contains(strings.ToLower(e.Path), q) ||
				strings.Contains(strings.ToLower(e.Scope), q) {
				m.entries = append(m.entries, e)
			}
		}
	}
	if m.cursor >= len(m.entries) {
		m.cursor = max(0, len(m.entries)-1)
	}
	m.splitPane.SetCursor(m.cursor)
	m.refreshPreview()
}

// refreshPreview loads the currently selected file (or composite) into the
// preview component.
func (m *MemoryModel) refreshPreview() {
	if m.compositeMode {
		m.preview.SetContent(buildCompositePrompt(m.entries))
		return
	}
	if len(m.entries) == 0 {
		m.preview.SetContent(nil)
		return
	}
	entry := m.entries[m.cursor]
	m.preview.SetContent(resolveMemoryPrompt(entry))
}

// ── Layout helpers ────────────────────────────────────────────────────────────

func (m MemoryModel) contentWidth() int {
	w := m.termWidth - 4
	if w < 40 {
		w = 40
	}
	return w
}

// ── Bubble Tea interface ──────────────────────────────────────────────────────

// Init satisfies tea.Model.
func (m MemoryModel) Init() tea.Cmd { return nil }

// Update satisfies tea.Model.
func (m MemoryModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		paneH := m.termHeight - 6 // reserve title + subtitle + col-header + rule + hint + newline
		if paneH < 10 {
			paneH = 10
		}
		m.splitPane.SetSize(msg.Width, paneH)
		previewW := m.contentWidth()
		if previewW < 40 {
			previewW = 40
		}
		m.preview.SetSize(previewW, m.splitPane.DetailHeight()-2)
		return m, nil

	case memoryEditorDoneMsg:
		// Reload file list after editing so token counts are fresh.
		m.allEntries = DiscoverMemoryFiles()
		m.applyFilter()
		return m, nil

	case tea.KeyPressMsg:
		if m.searchMode {
			return m.updateSearch(msg)
		}
		return m.updateNormal(msg)
	}
	return m, nil
}

func (m MemoryModel) updateNormal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "esc":
		m.quitting = true
		return m, tea.Quit

	case "j", "down":
		if !m.compositeMode && m.cursor < len(m.entries)-1 {
			m.cursor++
			m.splitPane.SetCursor(m.cursor)
			m.refreshPreview()
		}

	case "k", "up":
		if !m.compositeMode && m.cursor > 0 {
			m.cursor--
			m.splitPane.SetCursor(m.cursor)
			m.refreshPreview()
		}

	case "enter":
		// Select currently highlighted entry (already shown in preview).
		// No-op — preview is shown on navigation.

	case "c":
		m.compositeMode = !m.compositeMode
		m.refreshPreview()

	case "e":
		if len(m.entries) > 0 && !m.compositeMode {
			return m, m.editCurrentFile()
		}

	case "/":
		m.searchMode = true
		m.searchInput.SetValue(m.searchQuery)
		m.searchInput.Focus()
		return m, textinput.Blink

	default:
		// Delegate remaining keys to the preview component.
		m.preview.Update(msg)
	}
	return m, nil
}

func (m MemoryModel) updateSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "esc":
		m.searchMode = false
		m.searchInput.Blur()
		m.searchQuery = m.searchInput.Value()
		m.applyFilter()
		return m, nil
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	default:
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		return m, cmd
	}
}

// editCurrentFile opens the selected file in $EDITOR in a subprocess.
// It returns a command that resumes the TUI after the editor exits.
func (m MemoryModel) editCurrentFile() tea.Cmd {
	if len(m.entries) == 0 {
		return nil
	}
	path := m.entries[m.cursor].Path
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return tea.ExecProcess(c, func(err error) tea.Msg {
		// After the editor exits, reload file tokens and refresh preview.
		return memoryEditorDoneMsg{path: path}
	})
}

// memoryEditorDoneMsg is sent when the editor process exits.
type memoryEditorDoneMsg struct{ path string }

// ── View ──────────────────────────────────────────────────────────────────────

// View satisfies tea.Model.
func (m MemoryModel) View() tea.View {
	if m.quitting {
		v := tea.NewView("")
		v.AltScreen = true
		return v
	}

	v := tea.NewView(m.buildView())
	v.AltScreen = true
	return v
}

func (m MemoryModel) buildView() string {
	var sb strings.Builder
	cw := m.contentWidth()

	// ── Title bar ──────────────────────────────────────────────────────────
	title := "Memory Files"
	if m.compositeMode {
		title += " — Composite View"
	}
	t := theme.Current()
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Width(cw).
		Align(lipgloss.Center)
	sb.WriteString(titleStyle.Render(title))
	sb.WriteString("\n")

	// ── Subtitle / filter indicator ────────────────────────────────────────
	if m.searchMode {
		sb.WriteString("  " + memSubtitleStyle.Render("filter: ") + m.searchInput.View())
	} else if m.searchQuery != "" {
		sb.WriteString(memSubtitleStyle.Width(cw).Render(fmt.Sprintf("  filter: %q — %d of %d files", m.searchQuery, len(m.entries), len(m.allEntries))))
	} else {
		totalToks := 0
		for _, e := range m.allEntries {
			totalToks += e.Tokens
		}
		sb.WriteString(memSubtitleStyle.Width(cw).Render(fmt.Sprintf("  %d files · ~%s tokens total", len(m.allEntries), formatTokenCount(totalToks))))
	}
	sb.WriteString("\n")

	// ── Column headers (list mode only) ────────────────────────────────────
	if !m.compositeMode {
		sb.WriteString(m.renderColumnHeaders())
		sb.WriteString("\n")
		sb.WriteString(memRuleStyle.Render(strings.Repeat("─", cw)))
		sb.WriteString("\n")
	}

	// ── SplitPane (list + separator + preview) ─────────────────────────────
	listContent := m.buildListContent()
	detailContent := m.buildDetailContent()

	// Clone splitPane to avoid mutating the value receiver.
	sp := m.splitPane
	sp.SetListContent(listContent, len(m.entries))
	sp.SetDetailContent(detailContent)
	sb.WriteString(sp.View())
	sb.WriteString("\n")

	// ── Hint bar ──────────────────────────────────────────────────────────
	sb.WriteString(m.renderHints())

	return sb.String()
}

// buildListContent returns all list rows as a single string.
// SplitPane handles the windowing/scrolling.
func (m MemoryModel) buildListContent() string {
	if len(m.entries) == 0 {
		return memDimStyle.Render("  (no memory files found)")
	}

	cw := m.contentWidth()
	pathW := cw - memColScope - memColTokens - memColModTime - 3
	if pathW < 20 {
		pathW = 20
	}

	var sb strings.Builder
	for i, e := range m.entries {
		selected := i == m.cursor && !m.compositeMode

		short := shortenMemoryPath(e.Path)
		if len(short) > pathW-1 {
			short = short[:pathW-2] + "…"
		}

		var nameS lipgloss.Style
		if selected {
			nameS = memNameSelectedStyle.Width(pathW)
		} else {
			nameS = memNameStyle.Width(pathW)
		}

		var scopeS lipgloss.Style
		switch e.Scope {
		case "global":
			scopeS = memScopeGlobalStyle.Width(memColScope)
		case "project":
			scopeS = memScopeProjectStyle.Width(memColScope)
		default:
			scopeS = memScopeSharedStyle.Width(memColScope)
		}

		tokStr := formatTokenCount(e.Tokens)
		modStr := ""
		if !e.Modified.IsZero() {
			modStr = e.Modified.Format("Jan 02 15:04")
		}

		prefix := "  "
		if selected {
			prefix = "> "
		}

		line := fmt.Sprintf("%s%s%s%s%s",
			prefix,
			nameS.Render(short),
			scopeS.Render(e.Scope),
			memDimStyle.Width(memColTokens).Render(tokStr),
			memDimStyle.Width(memColModTime).Render(modStr),
		)
		sb.WriteString(line)
		if i < len(m.entries)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// buildDetailContent returns the rendered preview pane content.
func (m MemoryModel) buildDetailContent() string {
	return m.preview.View()
}

func (m MemoryModel) renderColumnHeaders() string {
	cw := m.contentWidth()
	pathW := cw - memColScope - memColTokens - memColModTime - 3
	if pathW < 20 {
		pathW = 20
	}

	path := memColHeaderStyle.Width(pathW).Render("PATH")
	scope := memColHeaderStyle.Width(memColScope).Render("SCOPE")
	toks := memColHeaderStyle.Width(memColTokens).Render("TOKENS")
	modtime := memColHeaderStyle.Width(memColModTime).Render("MODIFIED")
	return fmt.Sprintf("  %s%s%s%s", path, scope, toks, modtime)
}

func (m MemoryModel) renderHints() string {
	hints := []string{
		"j/k", "navigate",
		"enter", "select",
		"c", "composite",
		"e", "edit",
		"/", "filter",
		"q", "quit",
	}
	hintBarCentered := lipgloss.NewStyle().Width(m.contentWidth()).Align(lipgloss.Center)
	return hintBarCentered.Render(renderHintLine(hints...))
}

// shortenMemoryPath replaces the home directory prefix with ~ and returns a
// short path suitable for display in narrow columns.
func shortenMemoryPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	rel, err := filepath.Rel(home, p)
	if err != nil {
		return p
	}
	if !strings.HasPrefix(rel, "..") {
		return "~/" + rel
	}
	return p
}
