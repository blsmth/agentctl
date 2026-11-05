package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/prompt"
	"github.com/blsmth/agentctl/internal/textutil"
	"github.com/blsmth/agentctl/internal/theme"
)

// ── AgentsModel messages ──────────────────────────────────────────────────────

// agentsPromptResolvedMsg is sent when a background prompt.Resolve completes.
type agentsPromptResolvedMsg struct {
	agentName string
	result    *prompt.ResolvedPrompt
	err       error
}

// agentsEditorDoneMsg is delivered by the tea.ExecProcess callback when the
// editor exits.  The model re-resolves the prompt on receipt.
type agentsEditorDoneMsg struct {
	agentName string
	filePath  string
	err       error
}

// ── Layout constants ──────────────────────────────────────────────────────────

// agColName / agColModel / agColScope are list column widths.
const (
	agColName  = 24
	agColModel = 14
	agColScope = 10
)

// Detail-pane focus constants.
const (
	agFocusList    = 0 // list pane has focus (default)
	agFocusRaw     = 1 // raw source sub-pane has focus
	agFocusPreview = 2 // preview sub-pane has focus
)

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	agColHeaderStyle    lipgloss.Style
	agRuleStyle         lipgloss.Style
	agDimStyle          lipgloss.Style
	agNameStyle         lipgloss.Style
	agNameSelectedStyle lipgloss.Style
	agModelStyle        lipgloss.Style
	agScopeStyle        lipgloss.Style
	agDescStyle         lipgloss.Style
	agSubtitleStyle     lipgloss.Style
)

// initAgentsStyles initialises AgentsModel styles from the active theme.
// Shared styles are sourced from tableStyles (initTableStyles must run first).
func initAgentsStyles() {
	t := theme.Current()

	agColHeaderStyle = tableStyles.ColHeader
	agRuleStyle = tableStyles.Rule
	agDimStyle = tableStyles.Dim
	agSubtitleStyle = tableStyles.Subtitle

	agNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	agNameSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)

	agModelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	agScopeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	agDescStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))
}

// ── AgentsModel ───────────────────────────────────────────────────────────────

// AgentsModel is the full-screen Bubble Tea model for the agent manager TUI.
// It presents a filterable agent list on top (via SplitPane) and a horizontally
// split detail pane below: left = raw .md source, right = resolved PromptPreview.
// Tab cycles focus through list → raw pane → preview pane → list.
type AgentsModel struct {
	termWidth  int
	termHeight int

	// Full agent list and the currently displayed (filtered) subset.
	allAgents []agent.Agent
	agents    []agent.Agent

	cursor int

	// SplitPane manages the list-on-top / detail-on-bottom layout.
	splitPane SplitPane

	// rawViewport shows raw .md source in the left detail sub-pane.
	rawViewport viewport.Model

	// detailFocus tracks which pane has focus: agFocusList, agFocusRaw, agFocusPreview.
	detailFocus int

	// Prompt preview component for the right detail sub-pane.
	preview PromptPreviewComponent

	// selected is the agent currently loaded in the preview (nil = none).
	selected *agent.Agent

	// resolving is true while a background prompt.Resolve is in-flight.
	resolving  bool
	resolveErr error

	// Search / filter
	searchMode  bool
	searchInput textinput.Model
	searchQuery string

	// statusMsg is a transient one-line notice (e.g. a clipboard failure)
	// shown in place of the hint bar until the next keypress.
	statusMsg string

	quitting bool
}

// NewAgentsModel constructs an AgentsModel populated from the discovered agents.
// The first agent is automatically selected and its prompt begins resolving.
func NewAgentsModel(agents []agent.Agent, _ *config.Config) AgentsModel {
	si := textinput.New()
	si.Placeholder = "filter agents..."
	siS := si.Styles()
	siS.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	si.SetStyles(siS)

	m := AgentsModel{
		termWidth:   120,
		termHeight:  40,
		allAgents:   agents,
		searchInput: si,
		splitPane: SplitPane{
			ListRatio:     0.4,
			MinListRows:   4,
			MinDetailRows: 6,
		},
	}
	m.applyFilter()

	// Auto-select the first agent.
	if len(m.agents) > 0 {
		m.selected = &m.agents[0]
		m.splitPane.SetCursor(0)
	}

	// Initialise pane sizes from default terminal dimensions.
	paneH := m.termHeight - 6
	if paneH < 10 {
		paneH = 10
	}
	m.splitPane.SetSize(m.termWidth, paneH)

	// Record totalRows for scroll math and tests.
	m.splitPane.SetListContent("", len(m.agents))

	halfW := (m.termWidth - 6) / 2
	m.preview.SetSize(halfW, m.splitPane.DetailHeight()-2)
	m.rawViewport.SetWidth(halfW)
	m.rawViewport.SetHeight(m.splitPane.DetailHeight() - 2)
	m.loadRawContent()

	return m
}

// ── Bubble Tea interface ──────────────────────────────────────────────────────

// Init fires the initial background prompt resolve if an agent is pre-selected.
func (m AgentsModel) Init() tea.Cmd {
	if m.selected != nil && m.selected.FilePath != "" {
		return agentsResolveCmd(m.selected.Name, m.selected.FilePath)
	}
	return nil
}

// Update handles all incoming Bubble Tea messages.
func (m AgentsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		paneH := m.termHeight - 6 // reserve title + subtitle + col-header + rule + hint rows
		if paneH < 10 {
			paneH = 10
		}
		m.splitPane.SetSize(msg.Width, paneH)
		halfW := (msg.Width - 6) / 2
		m.preview.SetSize(halfW, m.splitPane.DetailHeight()-2)
		m.rawViewport.SetWidth(halfW)
		m.rawViewport.SetHeight(m.splitPane.DetailHeight() - 2)
		return m, nil

	case agentsPromptResolvedMsg:
		m.resolving = false
		if msg.err != nil {
			m.resolveErr = msg.err
			m.preview.SetContent(nil)
		} else {
			m.resolveErr = nil
			m.preview.SetContent(msg.result)
		}
		return m, nil

	case agentsEditorDoneMsg:
		// Re-resolve the prompt after the editor exits; also reload raw content.
		if msg.filePath != "" {
			m.resolving = true
			m.resolveErr = nil
			m.loadRawContent()
			return m, agentsResolveCmd(msg.agentName, msg.filePath)
		}
		return m, nil

	case tea.KeyPressMsg:
		m.statusMsg = ""
		if m.searchMode {
			return m.updateSearch(msg)
		}
		return m.updateList(msg)
	}

	return m, nil
}

// ── List update ───────────────────────────────────────────────────────────────

func (m AgentsModel) updateList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.quitting = true
		return m, tea.Quit

	case "esc":
		if m.searchQuery != "" {
			m.searchQuery = ""
			m.searchInput.SetValue("")
			m.applyFilter()
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit

	case "tab":
		// Cycle focus: list → raw pane → preview pane → list.
		m.detailFocus = (m.detailFocus + 1) % 3
		return m, nil

	case "up", "k":
		switch m.detailFocus {
		case agFocusList:
			if m.cursor > 0 {
				m.cursor--
				m.splitPane.SetCursor(m.cursor)
			}
		case agFocusRaw:
			m.rawViewport.ScrollUp(1)
		case agFocusPreview:
			m.preview.Update(msg)
		}
		return m, nil

	case "down", "j":
		switch m.detailFocus {
		case agFocusList:
			if m.cursor < len(m.agents)-1 {
				m.cursor++
				m.splitPane.SetCursor(m.cursor)
			}
		case agFocusRaw:
			m.rawViewport.ScrollDown(1)
		case agFocusPreview:
			m.preview.Update(msg)
		}
		return m, nil

	case "enter":
		if len(m.agents) == 0 {
			break
		}
		ag := m.agents[m.cursor]
		m.selected = &m.agents[m.cursor]
		m.loadRawContent()
		if ag.FilePath != "" {
			m.resolving = true
			m.resolveErr = nil
			m.preview.SetContent(nil)
			return m, agentsResolveCmd(ag.Name, ag.FilePath)
		}

	case "e":
		// Open the selected agent file in $EDITOR via tea.ExecProcess,
		// then re-resolve the prompt on return.
		if m.selected == nil || m.selected.FilePath == "" {
			return m, nil
		}
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		fp := m.selected.FilePath
		name := m.selected.Name
		cmd := exec.Command(editor, fp)
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
			return agentsEditorDoneMsg{agentName: name, filePath: fp, err: err}
		})

	case "o":
		// Copy the agent file path to the system clipboard.
		if m.selected != nil && m.selected.FilePath != "" {
			if err := agentsCopyToClipboard(m.selected.FilePath); err != nil {
				m.statusMsg = fmt.Sprintf("copy failed: %v", err)
			} else {
				m.statusMsg = "copied path to clipboard"
			}
		}

	case "/":
		m.searchMode = true
		m.searchInput.SetValue(m.searchQuery)
		m.searchInput.Focus()
		return m, textinput.Blink

	// Delegate page/home/end scroll keys to the focused sub-pane.
	case "pgup", "pgdown", "home", "end":
		switch m.detailFocus {
		case agFocusRaw:
			switch msg.String() {
			case "pgup":
				m.rawViewport.PageUp()
			case "pgdown":
				m.rawViewport.PageDown()
			case "home":
				m.rawViewport.GotoTop()
			case "end":
				m.rawViewport.GotoBottom()
			}
		default:
			m.preview.Update(msg)
		}
	}

	return m, nil
}

// ── Search update ─────────────────────────────────────────────────────────────

func (m AgentsModel) updateSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.searchMode = false
		m.searchQuery = ""
		m.searchInput.SetValue("")
		m.searchInput.Blur()
		m.applyFilter()
		return m, nil

	case "enter":
		m.searchMode = false
		m.searchQuery = m.searchInput.Value()
		m.searchInput.Blur()
		m.applyFilter()
		return m, nil
	}

	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	m.searchQuery = m.searchInput.Value()
	m.applyFilter()
	return m, cmd
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m AgentsModel) View() tea.View {
	v := tea.NewView(m.buildView())
	v.AltScreen = true
	return v
}

// buildView assembles the full-screen layout:
//
//	Title
//	Subtitle
//	Column headers + rule
//	SplitPane (list on top, horizontally-split detail below)
//	Hint bar
func (m AgentsModel) buildView() string {
	cw := m.termWidth - 4
	if cw < 40 {
		cw = 40
	}

	var b strings.Builder

	// ── Title ──────────────────────────────────────────────────────────────
	b.WriteString(m.renderTitle(cw))
	b.WriteString("\n")

	// ── Subtitle ────────────────────────────────────────────────────────────
	b.WriteString(m.renderSubtitle())
	b.WriteString("\n\n")

	// ── Column headers + rule ───────────────────────────────────────────────
	descW := cw - agColName - agColModel - agColScope - 3
	if descW < 0 {
		descW = 0
	}
	b.WriteString(fmt.Sprintf("  %s%s%s%s",
		agColHeaderStyle.Width(agColName).Render("NAME"),
		agColHeaderStyle.Width(agColModel).Render("MODEL"),
		agColHeaderStyle.Width(agColScope).Render("SCOPE"),
		agColHeaderStyle.Width(descW).Render("DESCRIPTION"),
	))
	b.WriteString("\n")
	b.WriteString("  " + agRuleStyle.Render(strings.Repeat("╌", cw-2)))
	b.WriteString("\n")

	// ── SplitPane (list + horizontally-split detail) ────────────────────────
	listContent := m.buildListContent(descW)
	detailContent := m.buildDetailContent()

	// Clone splitPane to avoid mutating the value receiver.
	sp := m.splitPane
	sp.SetListContent(listContent, len(m.agents))
	sp.SetDetailContent(detailContent)
	b.WriteString(sp.View())
	b.WriteString("\n")

	// ── Hint bar ────────────────────────────────────────────────────────────
	b.WriteString(m.renderHintBar())

	return b.String()
}

// buildListContent returns the rendered list rows for the SplitPane.
func (m AgentsModel) buildListContent(descW int) string {
	if len(m.agents) == 0 {
		return "   " + agDimStyle.Render("No agents found")
	}

	var b strings.Builder
	for i, ag := range m.agents {
		selected := i == m.cursor

		cursor := "   "
		if selected {
			cursor = " " + cursorActiveStyle.Render("▸") + " "
		}

		name := textutil.Truncate(ag.Name, agColName-1, "…")
		var nameStr string
		if selected {
			nameStr = agNameSelectedStyle.Width(agColName).Render(name)
		} else {
			nameStr = agNameStyle.Width(agColName).Render(name)
		}

		model := ag.Model
		if model == "" {
			model = "inherit"
		}
		modelStr := agModelStyle.Width(agColModel).Render(textutil.Truncate(model, agColModel-1, "…"))

		scope := ag.Scope.String()
		if ag.ProjectName != "" {
			scope = ag.ProjectName
		}
		scopeStr := agScopeStyle.Width(agColScope).Render(textutil.Truncate(scope, agColScope-1, "…"))

		desc := ag.Description
		if desc == "" {
			desc = "—"
		}
		var descStr string
		if descW > 0 {
			descStr = agDescStyle.Width(descW).Render(textutil.Truncate(desc, descW-1, "…"))
		}

		b.WriteString(cursor + nameStr + modelStr + scopeStr + descStr)
		if i < len(m.agents)-1 {
			b.WriteString("\n")
		}
	}

	// Append search input when filter is active.
	if m.searchMode {
		b.WriteString("\n")
		b.WriteString("  " + menuHintKeyStyle.Render("/") + " " + m.searchInput.View())
	}

	return b.String()
}

// buildDetailContent renders the horizontally split detail pane.
// Left sub-pane: raw .md source (rawViewport).
// Right sub-pane: resolved PromptPreview (preview component).
// Both are joined line-by-line with a │ separator.
func (m AgentsModel) buildDetailContent() string {
	t := theme.Current()
	halfW := (m.termWidth - 6) / 2
	if halfW < 20 {
		halfW = 20
	}

	// ── Left sub-pane: raw source ─────────────────────────────────────────
	var leftContent string
	if m.selected == nil {
		leftContent = agDimStyle.Render("  Select an agent to view source")
	} else if m.selected.FilePath == "" {
		leftContent = agDimStyle.Render("  (template — no file path)")
	} else {
		leftContent = m.rawViewport.View()
	}

	// ── Right sub-pane: resolved preview ─────────────────────────────────
	rightContent := m.renderPreviewPane(halfW)

	// ── Pane header labels with focus highlight ───────────────────────────
	focusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true)
	dimLabelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))

	var rawLabel, previewLabel string
	if m.detailFocus == agFocusRaw {
		rawLabel = focusStyle.Render("raw source")
	} else {
		rawLabel = dimLabelStyle.Render("raw source")
	}
	if m.detailFocus == agFocusPreview {
		previewLabel = focusStyle.Render("preview")
	} else {
		previewLabel = dimLabelStyle.Render("preview")
	}

	sep := sepStyle.Render("│")

	// ── Assemble: header row + content rows ───────────────────────────────
	var b strings.Builder

	// Header row: "raw source  │  preview"
	headerLeft := "  " + rawLabel
	visLeft := len([]rune(agentsStripAnsi("  raw source")))
	pad := halfW - visLeft
	if pad < 0 {
		pad = 0
	}
	b.WriteString(headerLeft + strings.Repeat(" ", pad))
	b.WriteString("  " + sep + "  ")
	b.WriteString(previewLabel)
	b.WriteString("\n")

	// Content rows: join leftContent and rightContent line-by-line.
	leftLines := strings.Split(leftContent, "\n")
	rightLines := strings.Split(rightContent, "\n")

	maxLines := len(leftLines)
	if len(rightLines) > maxLines {
		maxLines = len(rightLines)
	}

	for i := 0; i < maxLines; i++ {
		left := ""
		if i < len(leftLines) {
			left = leftLines[i]
		}
		right := ""
		if i < len(rightLines) {
			right = rightLines[i]
		}
		vis := len([]rune(agentsStripAnsi(left)))
		if vis < halfW {
			left += strings.Repeat(" ", halfW-vis)
		}
		b.WriteString(left)
		b.WriteString("  " + sep + "  ")
		b.WriteString(right)
		if i < maxLines-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}

// renderPreviewPane renders the right detail sub-pane content (preview / error).
// The width parameter is accepted for API compatibility but size is managed via SetSize.
func (m AgentsModel) renderPreviewPane(_ int) string {
	switch {
	case m.resolving:
		return "  " + agDimStyle.Render("Resolving prompt…")
	case m.resolveErr != nil:
		return "  " + agDimStyle.Render(fmt.Sprintf("Error: %v", m.resolveErr))
	case m.selected == nil:
		return "  " + agDimStyle.Render("Select an agent with enter to preview its resolved prompt")
	case m.selected.FilePath == "":
		return "  " + agDimStyle.Render("(template — no file path)")
	}
	return m.preview.View()
}

// ── Sub-renderers ─────────────────────────────────────────────────────────────

func (m AgentsModel) renderTitle(w int) string {
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(theme.Current().Bright)).
		Background(lipgloss.Color(theme.Current().Accent)).
		Padding(0, 2).
		Width(w).
		Align(lipgloss.Center).
		Render("Agents")
}

func (m AgentsModel) renderSubtitle() string {
	if m.searchQuery != "" {
		return agSubtitleStyle.Render(fmt.Sprintf(
			"  filtering: %q  (%d of %d)", m.searchQuery, len(m.agents), len(m.allAgents),
		))
	}
	return agSubtitleStyle.Render(fmt.Sprintf("  %d agents", len(m.allAgents)))
}

// renderHintBar renders the bottom key-binding hint bar.
func (m AgentsModel) renderHintBar() string {
	if m.statusMsg != "" {
		return "  " + agDimStyle.Render(m.statusMsg)
	}
	if m.searchMode {
		dot := "  " + menuHintDotStyle.Render("·") + "  "
		return menuHintKeyStyle.Render("/") + " " + m.searchInput.View() + dot +
			menuHintKeyStyle.Render("enter") + " " + menuHintLabelStyle.Render("confirm") + dot +
			menuHintKeyStyle.Render("esc") + " " + menuHintLabelStyle.Render("clear")
	}

	type hint struct{ key, label string }
	hints := []hint{
		{"j/k", "navigate"},
		{"enter", "preview"},
		{"tab", "focus"},
		{"e", "edit"},
		{"o", "copy path"},
		{"/", "search"},
		{"q", "quit"},
	}
	var parts []string
	for _, h := range hints {
		parts = append(parts, menuHintKeyStyle.Render(h.key)+" "+menuHintLabelStyle.Render(h.label))
	}
	return strings.Join(parts, "  "+menuHintDotStyle.Render("·")+"  ")
}

// ── Filter ────────────────────────────────────────────────────────────────────

func (m *AgentsModel) applyFilter() {
	var filtered []agent.Agent
	for _, ag := range m.allAgents {
		if m.searchQuery != "" {
			q := strings.ToLower(m.searchQuery)
			nameMatch := strings.Contains(strings.ToLower(ag.Name), q)
			descMatch := strings.Contains(strings.ToLower(ag.Description), q)
			if !nameMatch && !descMatch {
				continue
			}
		}
		filtered = append(filtered, ag)
	}
	m.agents = filtered
	m.cursor = 0
	m.splitPane.SetCursor(0)
}

// loadRawContent reads the selected agent's file into the rawViewport.
// This is synchronous — agent .md files are small.
func (m *AgentsModel) loadRawContent() {
	if m.selected == nil || m.selected.FilePath == "" {
		m.rawViewport.SetContent("")
		return
	}
	data, err := os.ReadFile(m.selected.FilePath)
	if err != nil {
		m.rawViewport.SetContent(fmt.Sprintf("(error reading file: %v)", err))
		return
	}
	m.rawViewport.SetContent(string(data))
}

// ── Commands ──────────────────────────────────────────────────────────────────

// agentsResolveCmd resolves the agent prompt in a background goroutine.
func agentsResolveCmd(agentName, filePath string) tea.Cmd {
	return func() tea.Msg {
		rp, err := prompt.Resolve(filePath)
		return agentsPromptResolvedMsg{agentName: agentName, result: rp, err: err}
	}
}

// ── Utilities ─────────────────────────────────────────────────────────────────

// agentsCopyToClipboard writes text to the system clipboard.
// It uses github.com/atotto/clipboard, which shells out to pbcopy, xclip/xsel,
// wl-copy, or clip.exe as appropriate and returns an error when none exists.
func agentsCopyToClipboard(text string) error {
	return clipboard.WriteAll(text)
}

// agentsStripAnsi removes ANSI escape sequences from s.
// Used for measuring visible character width when padding list rows.
// It handles both two-character escape sequences (ESC + final byte) and
// CSI sequences (ESC [ ... final byte).
func agentsStripAnsi(s string) string {
	var out strings.Builder
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		r := runes[i]
		if r == '\x1b' && i+1 < len(runes) {
			i++
			next := runes[i]
			if next == '[' {
				// CSI sequence: ESC [ <params> <final-byte 0x40-0x7E>
				i++
				for i < len(runes) {
					c := runes[i]
					i++
					if c >= 0x40 && c <= 0x7E {
						break
					}
				}
			} else if next >= 0x40 && next <= 0x5F {
				// Two-character escape sequence (e.g. ESC M, ESC c)
				i++
			}
			// Unknown: skip the ESC; the next character will be re-evaluated.
			continue
		}
		out.WriteRune(r)
		i++
	}
	return out.String()
}
