package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/render"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/textutil"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/tokens"
)

// Column widths for the sessions list view.
// sessColAgentMin and sessColProjectMin are minimum widths; actual widths are
// computed dynamically by sessElasticCols() to fill available terminal width.
const (
	sessColAge        = 10
	sessColAgentMin   = 15
	sessColProjectMin = 13
	sessColModel      = 8
	sessColTicket     = 6
	sessColDur        = 6
	sessColCost       = 8
	sessColIn         = 7
	sessColOut        = 7
	sessColTpm        = 7
	sessColCtx        = 11 // "▓▓▓░░ 62%" ~9 chars + pad
	sessColTurns      = 7
)

// sessElasticCols computes the elastic Agent and Project column widths.
// Surplus width (contentWidth minus all fixed columns minus cursor) is split
// evenly between them, with any remainder going to Project.
func sessElasticCols(contentWidth int) (agentW, projectW int) {
	fixedTotal := sessColAge + sessColAgentMin + sessColProjectMin + sessColModel +
		sessColTicket + sessColDur + sessColCost + sessColIn + sessColOut +
		sessColTpm + sessColCtx + sessColTurns + 3 // 3 = cursor prefix
	surplus := contentWidth - fixedTotal
	if surplus <= 0 {
		return sessColAgentMin, sessColProjectMin
	}
	half := surplus / 2
	return sessColAgentMin + half, sessColProjectMin + surplus - half
}

// Session list/detail popup styles.
var (
	sessTitleStyle         lipgloss.Style
	sessRuleStyle          lipgloss.Style
	sessSectionStyle       lipgloss.Style
	sessColHeaderStyle     lipgloss.Style
	sessRowStyle           lipgloss.Style
	sessRowSelectedStyle   lipgloss.Style
	sessAgeStyle           lipgloss.Style
	sessAgentStyle         lipgloss.Style
	sessAgentSelectedStyle lipgloss.Style
	sessDimStyle           lipgloss.Style
	sessCostStyle          lipgloss.Style
	sessHintBarStyle       lipgloss.Style
	sessDetailLabelStyle   lipgloss.Style
	sessDetailValueStyle   lipgloss.Style
	sessBulletStyle        lipgloss.Style
	sessSubtitleStyle      lipgloss.Style
	sessProjectStyle       lipgloss.Style
	sessModelStyle         lipgloss.Style
	sessTicketStyle        lipgloss.Style
	sessTpmStyle           lipgloss.Style
	sessTokenInStyle       lipgloss.Style
	sessTokenOutStyle      lipgloss.Style
	sessRowSeparatorStyle  lipgloss.Style
	sessDetailSectionStyle lipgloss.Style
)

// sessHighlightBg holds the background color string for selected rows.
var sessHighlightBg string

// initSessionStyles initialises sessions-popup styles from the active theme.
// Shared styles (ColHeader, Rule, Dim, etc.) are sourced from tableStyles,
// which must be initialised first by initTableStyles().
func initSessionStyles() {
	t := theme.Current()

	// ── Shared table styles ───────────────────────────────────────────────────
	sessRuleStyle = tableStyles.Rule
	sessColHeaderStyle = tableStyles.ColHeader
	sessDimStyle = tableStyles.Dim
	sessDetailLabelStyle = tableStyles.DetailLabel.Width(14)
	sessDetailValueStyle = tableStyles.DetailValue
	sessSubtitleStyle = tableStyles.Subtitle
	// Use the shared HintBar style but without the built-in MarginTop — the
	// viewList() layout writes an explicit blank line before the hint bar,
	// matching the tickets viewer pattern (one blank line, not two).
	sessHintBarStyle = tableStyles.HintBar.MarginTop(0)
	sessRowSeparatorStyle = tableStyles.RowSeparator

	// ── Session-specific styles ───────────────────────────────────────────────
	sessTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Align(lipgloss.Center)

	sessSectionStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Text))

	sessRowStyle = lipgloss.NewStyle()

	sessHighlightBg = t.Surface
	sessRowSelectedStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(sessHighlightBg))

	sessAgeStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Width(sessColAge)

	sessAgentStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Text))

	sessAgentSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)

	sessCostStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Green)).
		Width(sessColCost)

	sessBulletStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent))

	sessProjectStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Sky))

	sessModelStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Blue)).
		Width(sessColModel)

	sessTicketStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Yellow)).
		Width(sessColTicket)

	sessTpmStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Orange)).
		Width(sessColTpm)

	sessTokenInStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.TokenInput)).
		Width(sessColIn)

	sessTokenOutStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.TokenOutput)).
		Width(sessColOut)

	// Sky-colored section headers in the detail pane — sessions-specific.
	sessDetailSectionStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Sky))
}

// SessionEntry wraps a session with precomputed display fields.
type SessionEntry struct {
	Session           sessions.Session
	Summary           *sessions.Summary // full parsed summary (for detail pane)
	Age               string
	Agent             string
	Project           string // basename of Session.Project (e.g. "agentctl")
	Turns             int
	InputTokens       int // raw (uncached) input tokens only
	OutputTokens      int
	CacheReadTokens   int
	CacheCreateTokens int
	Tokens            int // raw input + output (excludes cache)
	Duration          time.Duration
	CostUSD           float64
	ModelShort        string
	GitBranch         string
	TicketNumber      int // from extractIssueFromBranch(GitBranch), 0 if none
	CtxPct            int // context window fill percentage (0-100)
	TokPerMin         int // (InputTokens+OutputTokens) / duration in minutes
}

// SessionsResult is the output of the sessions popup.
type SessionsResult struct {
	Cancelled bool
	Action    string // "resume", "log", or ""
	SessionID string
	Agent     string
	Project   string // working directory
	LogFile   string // path to the JSONL log file (populated for "log" action)
}

// sessLoadedMsg is sent when the async session fetch completes.
type sessLoadedMsg struct {
	entries []SessionEntry
	err     error
}

// sessFetchCmd fetches recent sessions asynchronously.
func sessFetchCmd(currentAgent string) tea.Cmd {
	return func() tea.Msg {
		recentSessions, err := sessions.FindRecentSessions(50, 30*24*time.Hour)
		if err != nil {
			return sessLoadedMsg{err: err}
		}
		var entries []SessionEntry
		for _, s := range recentSessions {
			sum, err := sessions.ParseSummary(s)
			if err != nil {
				continue
			}

			project := sessions.NormalizeProjectName(s.Project)
			modelShort := render.AbbreviateModel(sum.ModelName)
			ticketNum := extractIssueFromBranch(sum.GitBranch)

			lastTotal := sum.LastInputTokens + sum.LastCacheReadTokens + sum.LastCacheCreateTokens
			ctxPct := tokens.CtxPct(sum.ModelName, lastTotal)

			dur := s.Duration()
			tokPerMin := 0
			if mins := dur.Minutes(); mins > 0 {
				tokPerMin = int(float64(sum.TotalInputTokens+sum.TotalOutputTokens) / mins)
			}

			age := time.Since(s.StartTime)
			entries = append(entries, SessionEntry{
				Session:           s,
				Summary:           sum,
				Age:               fmtAge(age),
				Agent:             s.Agent,
				Project:           project,
				Turns:             sum.TurnCount,
				InputTokens:       sum.TotalInputTokens,
				OutputTokens:      sum.TotalOutputTokens,
				CacheReadTokens:   sum.CacheReadTokens,
				CacheCreateTokens: sum.CacheCreateTokens,
				Tokens:            sum.TotalInputTokens + sum.TotalOutputTokens,
				Duration:          dur,
				CostUSD:           sessions.EstimateCostFromSummary(sum),
				ModelShort:        modelShort,
				GitBranch:         sum.GitBranch,
				TicketNumber:      ticketNum,
				CtxPct:            ctxPct,
				TokPerMin:         tokPerMin,
			})
		}
		return sessLoadedMsg{entries: entries}
	}
}

// SessionsModel is the Bubble Tea model for the sessions browser popup.
type SessionsModel struct {
	// Terminal dimensions — updated via WindowSizeMsg.
	termWidth  int
	termHeight int

	entries    []SessionEntry
	allEntries []SessionEntry // unfiltered set
	cursor     int

	// viewMode controls which view is shown: 0 = list, 1 = detail.
	viewMode int

	// detailTab controls which detail sub-view is shown: 0 = summary, 1 = turn metrics.
	detailTab int

	filterAgent string // "" = all agents, else filter to this agent
	showAll     bool   // when false, filtered to filterAgent

	// Agent filter cycling
	agentOptions []string // unique agents from loaded entries
	agentIdx     int      // -1 = all, 0..N = index into agentOptions

	// Project filter cycling
	projectOptions []string // unique project basenames from loaded entries
	projectIdx     int      // -1 = all, 0..N = index into projectOptions
	filterProject  string   // "" = all projects

	// Search
	searchMode  bool
	searchInput textinput.Model
	searchQuery string

	// Loading state
	loading bool
	err     error

	result   SessionsResult
	quitting bool

	// splitPane manages list/detail layout, height allocation, and scroll.
	splitPane SplitPane
}

// sessChromRows is the number of lines reserved for chrome outside the SplitPane
// in viewList: title(1) + search-indicator(1) + blank(1) + subtitle(1) + blank(1)
// + col-headers(1) + rule(1) above the pane; blank(1) + hint-bar(1) below = 9 total.
// Mirrors the tickets viewer's "7 header + 2 hint bar = 9" formula.
const sessChromRows = 9

// NewSessionsModel creates the sessions browser with optional agent filter.
// Data is loaded asynchronously via Init().
func NewSessionsModel(currentAgent string) SessionsModel {
	si := textinput.New()
	si.Placeholder = "filter sessions..."
	siS := si.Styles()
	siS.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	si.SetStyles(siS)

	m := SessionsModel{
		termWidth:   120,
		termHeight:  40,
		loading:     true,
		filterAgent: currentAgent,
		showAll:     currentAgent == "",
		agentIdx:    -1,
		projectIdx:  -1,
		searchInput: si,
		splitPane: SplitPane{
			ListRatio:     0.45,
			MinListRows:   8,
			MinDetailRows: 5,
		},
	}
	m.splitPane.SetSize(m.termWidth, m.termHeight-sessChromRows)
	return m
}

// contentWidth returns the usable content width based on terminal size.
func (m SessionsModel) contentWidth() int {
	w := m.termWidth - 4
	if w < 40 {
		w = 40
	}
	return w
}

// Result returns the popup output after it quits.
func (m SessionsModel) Result() SessionsResult { return m.result }

func (m SessionsModel) Init() tea.Cmd {
	return sessFetchCmd(m.filterAgent)
}

func (m SessionsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.splitPane.SetSize(msg.Width, msg.Height-sessChromRows)
		return m, nil

	case sessLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.allEntries = msg.entries

		// Build sorted unique agent list.
		seenAgent := make(map[string]bool)
		for _, e := range msg.entries {
			if !seenAgent[e.Agent] {
				seenAgent[e.Agent] = true
				m.agentOptions = append(m.agentOptions, e.Agent)
			}
		}
		sort.Strings(m.agentOptions)

		// Build sorted unique project list.
		seenProject := make(map[string]bool)
		for _, e := range msg.entries {
			if e.Project != "" && !seenProject[e.Project] {
				seenProject[e.Project] = true
				m.projectOptions = append(m.projectOptions, e.Project)
			}
		}
		sort.Strings(m.projectOptions)

		// Find the initial agentIdx based on filterAgent.
		m.agentIdx = -1
		if m.filterAgent != "" && !m.showAll {
			for i, a := range m.agentOptions {
				if a == m.filterAgent {
					m.agentIdx = i
					break
				}
			}
		}

		m.applyFilters()
		return m, nil

	case tea.KeyPressMsg:
		if m.searchMode {
			return m.updateSearch(msg)
		}
		if m.viewMode == 1 {
			return m.updateDetail(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

// applyFilters rebuilds m.entries from m.allEntries applying agent, project and
// search filters.
func (m *SessionsModel) applyFilters() {
	result := m.allEntries
	if !m.showAll && m.filterAgent != "" {
		result = filterEntries(result, m.filterAgent)
	}
	if m.filterProject != "" {
		var filtered []SessionEntry
		for _, e := range result {
			if e.Project == m.filterProject {
				filtered = append(filtered, e)
			}
		}
		result = filtered
	}
	if m.searchQuery != "" {
		q := strings.ToLower(m.searchQuery)
		var filtered []SessionEntry
		for _, e := range result {
			if strings.Contains(strings.ToLower(e.Agent), q) ||
				strings.Contains(strings.ToLower(e.Project), q) {
				filtered = append(filtered, e)
			}
		}
		result = filtered
	}
	m.entries = result
	if m.cursor >= len(m.entries) {
		if len(m.entries) > 0 {
			m.cursor = len(m.entries) - 1
		} else {
			m.cursor = 0
		}
	}
}

func (m SessionsModel) updateList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.result.Cancelled = true
		m.quitting = true
		return m, tea.Quit

	case "esc":
		if m.searchQuery != "" {
			m.searchQuery = ""
			m.searchInput.SetValue("")
			m.applyFilters()
			return m, nil
		}
		if m.viewMode == 1 {
			m.viewMode = 0
			return m, nil
		}
		m.result.Cancelled = true
		m.quitting = true
		return m, tea.Quit

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}

	case "down", "j":
		if m.cursor < len(m.entries)-1 {
			m.cursor++
		}

	case "a":
		// Cycle through agents.
		if len(m.agentOptions) == 0 {
			break
		}
		m.agentIdx++
		if m.agentIdx >= len(m.agentOptions) {
			m.agentIdx = -1 // back to "all"
		}
		if m.agentIdx == -1 {
			m.filterAgent = ""
			m.showAll = true
		} else {
			m.filterAgent = m.agentOptions[m.agentIdx]
			m.showAll = false
		}
		m.cursor = 0
		m.applyFilters()

	case "p":
		// Cycle through projects.
		if len(m.projectOptions) == 0 {
			break
		}
		m.projectIdx++
		if m.projectIdx >= len(m.projectOptions) {
			m.projectIdx = -1 // back to "all"
		}
		if m.projectIdx == -1 {
			m.filterProject = ""
		} else {
			m.filterProject = m.projectOptions[m.projectIdx]
		}
		m.cursor = 0
		m.applyFilters()

	case "/":
		m.searchMode = true
		m.searchInput.SetValue(m.searchQuery)
		m.searchInput.Focus()
		return m, textinput.Blink

	case "t":
		// Toggle turn metrics view (opens detail pane if not already there).
		if len(m.entries) == 0 {
			break
		}
		if m.viewMode != 1 {
			m.viewMode = 1
			m.detailTab = 1
		} else {
			if m.detailTab == 1 {
				m.detailTab = 0
			} else {
				m.detailTab = 1
			}
		}
		return m, nil

	case "enter":
		// Open the full-screen detail view for the selected session.
		if len(m.entries) == 0 {
			break
		}
		m.viewMode = 1
		m.detailTab = 0
		return m, nil

	case "r":
		// Resume selected session.
		if len(m.entries) == 0 {
			break
		}
		entry := m.entries[m.cursor]
		m.result = SessionsResult{
			Action:    "resume",
			SessionID: entry.Session.ID,
			Agent:     entry.Session.Agent,
			Project:   entry.Session.Project,
		}
		m.quitting = true
		return m, tea.Quit

	case "l":
		if len(m.entries) == 0 {
			break
		}
		entry := m.entries[m.cursor]
		m.result = SessionsResult{
			Action:    "log",
			SessionID: entry.Session.ID,
			Agent:     entry.Session.Agent,
			Project:   entry.Session.Project,
			LogFile:   entry.Session.LogFile,
		}
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

func (m SessionsModel) updateSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.searchMode = false
		m.searchQuery = ""
		m.searchInput.SetValue("")
		m.searchInput.Blur()
		m.applyFilters()
		return m, nil

	case "enter":
		m.searchMode = false
		m.searchQuery = m.searchInput.Value()
		m.searchInput.Blur()
		m.applyFilters()
		return m, nil
	}
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	m.searchQuery = m.searchInput.Value()
	m.applyFilters()
	return m, cmd
}

func (m SessionsModel) View() tea.View {
	var s string
	if m.viewMode == 1 {
		s = m.viewDetail()
	} else {
		s = m.viewList()
	}
	v := tea.NewView(s)
	v.AltScreen = true
	return v
}

// dateGroup returns the period group label for a given time.
func dateGroup(t time.Time) string {
	now := time.Now()
	y1, mo1, d1 := now.Date()
	y2, mo2, d2 := t.In(time.Local).Date()
	if y1 == y2 && mo1 == mo2 && d1 == d2 {
		return "Today"
	}
	yesterday := now.AddDate(0, 0, -1)
	y3, mo3, d3 := yesterday.Date()
	if y2 == y3 && mo2 == mo3 && d2 == d3 {
		return "Yesterday"
	}
	weekAgo := now.AddDate(0, 0, -7)
	if t.After(weekAgo) {
		return "This Week"
	}
	return "Older"
}

// buildListContent renders all session entries into a pre-rendered string for
// the SplitPane list pane. It returns the rendered content and the line index
// (0-based) of the currently selected entry, so SplitPane.clampListScroll can
// keep the cursor visible.
//
// The aggregate stats footer (rule + summary line) is appended at the end so
// it scrolls with the content.
func (m SessionsModel) buildListContent() (content string, cursorLine int) {
	var b strings.Builder
	cw := m.contentWidth()

	if len(m.entries) == 0 {
		b.WriteString(sessRowStyle.Width(cw).Render(sessDimStyle.Render("   No sessions found")))
		b.WriteString("\n")
		// Append empty footer so layout stays consistent.
		b.WriteString(sessRuleStyle.Render("   " + strings.Repeat("╌", cw-6)))
		b.WriteString("\n")
		b.WriteString(sessDimStyle.Render("   0 sessions"))
		b.WriteString("\n")
		return b.String(), 0
	}

	t := theme.Current()
	lastGroup := ""
	lineNum := 0
	cursorLine = 0

	for i, entry := range m.entries {
		selected := i == m.cursor

		// Period group header — emitted once per new group.
		grp := dateGroup(entry.Session.StartTime)
		if grp != lastGroup {
			lastGroup = grp
			b.WriteString(sessSectionStyle.Render("  " + grp))
			b.WriteString("\n")
			lineNum++
		}

		// Record the line where the selected entry starts.
		if selected {
			cursorLine = lineNum
		}

		cursor := "   "
		if selected {
			cursor = " " + cursorActiveStyle.Render("▸") + " "
		}

		// Build each cell; when selected, add highlight background.
		bg := sessHighlightBg
		agentW, projectW := sessElasticCols(cw)
		ageCell := buildCell(sessAgeStyle, sessColAge, entry.Age, selected, bg)
		agentCell := buildCell(sessAgentStyle, agentW, textutil.Truncate(entry.Agent, agentW-1, "…"), selected, bg)
		if selected {
			agentCell = buildCell(sessAgentSelectedStyle, agentW, textutil.Truncate(entry.Agent, agentW-1, "…"), selected, bg)
		}
		projectCell := buildCell(sessProjectStyle, projectW, textutil.Truncate(entry.Project, projectW-1, "…"), selected, bg)
		modelCell := buildCell(sessModelStyle, sessColModel, textutil.Truncate(entry.ModelShort, sessColModel-1, "…"), selected, bg)

		ticketStr := "—"
		if entry.TicketNumber > 0 {
			ticketStr = fmt.Sprintf("#%d", entry.TicketNumber)
		}
		ticketCell := buildCell(sessTicketStyle, sessColTicket, ticketStr, selected, bg)

		durCell := buildCell(sessDimStyle.Width(sessColDur), sessColDur, fmtDuration(entry.Duration), selected, bg)
		costCell := buildCell(sessCostStyle, sessColCost, sessions.FormatCost(entry.CostUSD), selected, bg)
		inCell := buildCell(sessTokenInStyle, sessColIn, fmtTokensShort(entry.InputTokens), selected, bg)
		outCell := buildCell(sessTokenOutStyle, sessColOut, fmtTokensShort(entry.OutputTokens), selected, bg)
		tpmCell := buildCell(sessTpmStyle, sessColTpm, fmtTokensShort(entry.TokPerMin), selected, bg)
		ctxCell := renderCtxBarCell(entry.CtxPct, &t, sessColCtx, selected, bg)
		turnsCell := buildCell(lipgloss.NewStyle().Foreground(lipgloss.Color(turnsColor(entry.Turns, &t))).Width(sessColTurns), sessColTurns, fmt.Sprintf("%d", entry.Turns), selected, bg)

		inner := cursor + ageCell + agentCell + projectCell + modelCell +
			ticketCell + durCell + costCell + inCell + outCell + tpmCell + ctxCell + turnsCell

		if selected {
			b.WriteString(sessRowSelectedStyle.Width(cw).Render(inner))
		} else {
			b.WriteString(sessRowStyle.Width(cw).Render(inner))
		}
		b.WriteString("\n")
		lineNum++

		// Row separator between entries (not after the last one).
		if i < len(m.entries)-1 {
			sepWidth := cw - 6
			if sepWidth < 0 {
				sepWidth = 0
			}
			b.WriteString("   " + sessRowSeparatorStyle.Render(strings.Repeat("╌", sepWidth)))
			b.WriteString("\n")
			lineNum++
		}
	}

	// ── Aggregate footer ────────────────────────────────────────────────────
	var totalDuration time.Duration
	var totalCost float64
	var totalIn, totalOut, totalCacheRead, totalCacheCreate int
	for _, e := range m.entries {
		totalDuration += e.Duration
		totalCost += e.CostUSD
		totalIn += e.InputTokens
		totalOut += e.OutputTokens
		totalCacheRead += e.CacheReadTokens
		totalCacheCreate += e.CacheCreateTokens
	}
	totalCached := totalCacheRead + totalCacheCreate
	cachedAnnotation := ""
	if totalCached > 0 {
		cachedAnnotation = fmt.Sprintf(" (+%s cached)", fmtTokensShort(totalCached))
	}
	footer := fmt.Sprintf("%d sessions · %s · %s · %s in%s / %s out",
		len(m.entries), fmtDuration(totalDuration), sessions.FormatCost(totalCost),
		fmtTokensShort(totalIn), cachedAnnotation, fmtTokensShort(totalOut))

	b.WriteString(sessRuleStyle.Render("   " + strings.Repeat("╌", cw-6)))
	b.WriteString("\n")
	b.WriteString(sessDimStyle.Render("   " + footer))
	b.WriteString("\n")

	return b.String(), cursorLine
}

// buildDetailContent renders the detail pane content suitable for passing to
// SplitPane.SetDetailContent. SplitPane.View() wraps this in tableStyles.DetailBox.
func (m SessionsModel) buildDetailContent() string {
	// Inner width: subtract box border (1 per side) + padding (2 per side) = 6 total.
	innerWidth := m.contentWidth() - 6
	if innerWidth < 20 {
		innerWidth = 20
	}
	height := m.splitPane.DetailHeight()
	content := m.renderDetailPane(innerWidth, height)
	return strings.TrimRight(content, "\n")
}

func (m SessionsModel) viewList() string {
	var b strings.Builder
	cw := m.contentWidth()

	// ── 1. Title bar ────────────────────────────────────────────────────────
	// Build pagination indicator right-aligned.
	pageInfo := ""
	if !m.loading && m.err == nil && len(m.entries) > 0 {
		end := m.cursor + 1
		if end > len(m.entries) {
			end = len(m.entries)
		}
		pageInfo = fmt.Sprintf("%d of %d", end, len(m.entries))
	}
	titleText := "Sessions"
	titleStyle := sessTitleStyle.Width(cw)
	if pageInfo != "" {
		// Right-align pageInfo within the title bar.
		inner := titleText + strings.Repeat(" ", max(0, cw-len(titleText)-len(pageInfo)-4)) + pageInfo
		b.WriteString(titleStyle.Render(inner))
	} else {
		b.WriteString(titleStyle.Render(titleText))
	}
	b.WriteString("\n")

	// ── 2. Search query indicator — always exactly 1 line ───────────────────
	// Keeps chrome height stable regardless of search state. Mirrors the
	// autopilot status line in tickets.go: one line always written here.
	if m.searchQuery != "" {
		b.WriteString(sessDimStyle.Render(fmt.Sprintf("  🔍 %s", m.searchQuery)))
	}
	b.WriteString("\n")

	// ── 3. Blank line ────────────────────────────────────────────────────────
	b.WriteString("\n")

	// ── 4. Subtitle / filter indicator ──────────────────────────────────────
	var subtitleParts []string
	if m.agentIdx >= 0 && m.filterAgent != "" {
		subtitleParts = append(subtitleParts, fmt.Sprintf("agent: %s", m.filterAgent))
	} else {
		subtitleParts = append(subtitleParts, "All agents")
	}
	if m.filterProject != "" {
		subtitleParts = append(subtitleParts, fmt.Sprintf("project: %s", m.filterProject))
	}
	subtitle := strings.Join(subtitleParts, "  ·  ")
	subtitle += "  (a agent · p project)"

	if m.loading {
		b.WriteString(sessSubtitleStyle.Render("  Loading sessions…"))
	} else if m.err != nil {
		b.WriteString(sessSubtitleStyle.Render(fmt.Sprintf("  Error: %v", m.err)))
	} else {
		b.WriteString(sessSubtitleStyle.Render("  " + subtitle))
	}
	b.WriteString("\n\n")

	if !m.loading && m.err == nil {
		// ── 5. Column headers + rule ─────────────────────────────────────────
		hAgentW, hProjectW := sessElasticCols(cw)
		b.WriteString(fmt.Sprintf("   %s%s%s%s%s%s%s%s%s%s%s%s",
			sessColHeaderStyle.Render(fmt.Sprintf("%-*s", sessColAge, "AGE")),
			sessColHeaderStyle.Render(fmt.Sprintf("%-*s", hAgentW, "AGENT")),
			sessColHeaderStyle.Render(fmt.Sprintf("%-*s", hProjectW, "PROJECT")),
			sessColHeaderStyle.Render(fmt.Sprintf("%-*s", sessColModel, "MODEL")),
			sessColHeaderStyle.Render(fmt.Sprintf("%-*s", sessColTicket, "#")),
			sessColHeaderStyle.Render(fmt.Sprintf("%-*s", sessColDur, "DUR")),
			sessColHeaderStyle.Render(fmt.Sprintf("%-*s", sessColCost, "COST")),
			sessColHeaderStyle.Render(fmt.Sprintf("%-*s", sessColIn, "IN")),
			sessColHeaderStyle.Render(fmt.Sprintf("%-*s", sessColOut, "OUT")),
			sessColHeaderStyle.Render(fmt.Sprintf("%-*s", sessColTpm, "T/M")),
			sessColHeaderStyle.Render(fmt.Sprintf("%-*s", sessColCtx, "CTX")),
			sessColHeaderStyle.Render(fmt.Sprintf("%-*s", sessColTurns, "TURNS")),
		))
		b.WriteString("\n")
		b.WriteString("   " + sessRuleStyle.Render(strings.Repeat("╌", cw-3)))
		b.WriteString("\n")

		// ── 6. SplitPane: list rows + separator + detail box ─────────────────
		sp := m.splitPane
		listContent, cursorLine := m.buildListContent()
		lineCount := strings.Count(listContent, "\n")
		sp.SetCursor(cursorLine)
		sp.SetListContent(listContent, lineCount)
		sp.SetDetailContent(m.buildDetailContent())
		b.WriteString(sp.View())
	} else if !m.loading {
		// Error state: emit blank lines to keep chrome height stable.
		for i := 0; i < 3; i++ {
			b.WriteString("\n")
		}
	}

	// ── 7. Hint bar ─────────────────────────────────────────────────────────
	// Rendered below the split pane. When search mode is active, the search
	// input replaces the hint bar (same pattern as tickets.go).
	b.WriteString("\n")
	if m.searchMode {
		dot := "  " + menuHintDotStyle.Render("·") + "  "
		searchLine := menuHintKeyStyle.Render("/") + " " + m.searchInput.View()
		b.WriteString(searchLine + dot +
			menuHintKeyStyle.Render("enter") + " " + menuHintLabelStyle.Render("confirm") + dot +
			menuHintKeyStyle.Render("esc") + " " + menuHintLabelStyle.Render("clear"))
	} else {
		hintParts := []string{
			menuHintKeyStyle.Render("j/k") + " " + menuHintLabelStyle.Render("navigate"),
			menuHintKeyStyle.Render("enter") + " " + menuHintLabelStyle.Render("detail"),
			menuHintKeyStyle.Render("t") + " " + menuHintLabelStyle.Render("turns"),
			menuHintKeyStyle.Render("r") + " " + menuHintLabelStyle.Render("resume"),
			menuHintKeyStyle.Render("l") + " " + menuHintLabelStyle.Render("log"),
			menuHintKeyStyle.Render("a") + " " + menuHintLabelStyle.Render("agent"),
			menuHintKeyStyle.Render("p") + " " + menuHintLabelStyle.Render("project"),
			menuHintKeyStyle.Render("/") + " " + menuHintLabelStyle.Render("search"),
			menuHintKeyStyle.Render("q") + " " + menuHintLabelStyle.Render("quit"),
		}
		sep := "  " + menuHintDotStyle.Render("·") + "  "
		b.WriteString(sessHintBarStyle.Width(cw).Render(strings.Join(hintParts, sep)))
	}

	return b.String()
}

// updateDetail handles key presses when viewMode == 1 (full-screen detail view).
func (m SessionsModel) updateDetail(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.result.Cancelled = true
		m.quitting = true
		return m, tea.Quit

	case "esc":
		m.viewMode = 0
		m.detailTab = 0
		return m, nil

	case "t":
		// Toggle between summary and turn metrics.
		if m.detailTab == 1 {
			m.detailTab = 0
		} else {
			m.detailTab = 1
		}
		return m, nil

	case "r":
		if len(m.entries) == 0 {
			break
		}
		entry := m.entries[m.cursor]
		m.result = SessionsResult{
			Action:    "resume",
			SessionID: entry.Session.ID,
			Agent:     entry.Session.Agent,
			Project:   entry.Session.Project,
		}
		m.quitting = true
		return m, tea.Quit

	case "l":
		if len(m.entries) == 0 {
			break
		}
		entry := m.entries[m.cursor]
		m.result = SessionsResult{
			Action:    "log",
			SessionID: entry.Session.ID,
			Agent:     entry.Session.Agent,
			Project:   entry.Session.Project,
			LogFile:   entry.Session.LogFile,
		}
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

// viewDetail renders a full-screen detail view for the selected session.
// Esc returns to the list; r resumes; l opens the log; t toggles turn metrics.
func (m SessionsModel) viewDetail() string {
	if m.detailTab == 1 {
		return m.viewTurnMetrics()
	}

	var b strings.Builder
	cw := m.contentWidth()

	if len(m.entries) == 0 || m.cursor >= len(m.entries) {
		b.WriteString(sessDimStyle.Render("  No session selected"))
		return b.String()
	}

	entry := m.entries[m.cursor]
	sum := entry.Summary
	s := entry.Session
	loc := time.Now().Location()
	t := theme.Current()

	// ── Title ────────────────────────────────────────────────────────────────
	titleText := fmt.Sprintf("Session Detail  ·  %s  ·  %s", entry.Agent, entry.Project)
	b.WriteString(sessTitleStyle.Width(cw).Render(titleText))
	b.WriteString("\n")

	// ── Subtitle ─────────────────────────────────────────────────────────────
	b.WriteString(sessSubtitleStyle.Render(fmt.Sprintf("  %s  ·  %s",
		s.StartTime.In(loc).Format("Mon Jan 2, 2006 3:04 PM"),
		fmtDuration(entry.Duration),
	)))
	b.WriteString("\n")
	b.WriteString("   " + sessRuleStyle.Render(strings.Repeat("╌", cw-3)))
	b.WriteString("\n\n")

	if sum != nil {
		labelStyle := sessDetailLabelStyle
		valueStyle := sessDetailValueStyle
		dimStyle := sessDimStyle

		// ── Session Details ───────────────────────────────────────────────────
		b.WriteString(sessDetailSectionStyle.Render("── Session Details ──"))
		b.WriteString("\n\n")

		b.WriteString(labelStyle.Render("Agent:") + valueStyle.Render(s.Agent) + "\n")
		b.WriteString(labelStyle.Render("Project:") + valueStyle.Render(shortenDir(s.Project)) + "\n")
		if sum.GitBranch != "" {
			b.WriteString(labelStyle.Render("Branch:") + dimStyle.Render(sum.GitBranch) + "\n")
		}
		if entry.TicketNumber > 0 {
			b.WriteString(labelStyle.Render("Ticket:") + lipgloss.NewStyle().Foreground(lipgloss.Color(t.Yellow)).Render(fmt.Sprintf("#%d", entry.TicketNumber)) + "\n")
		}
		b.WriteString(labelStyle.Render("Started:") + valueStyle.Render(s.StartTime.In(loc).Format("Mon Jan 2 3:04 PM")) + "\n")
		b.WriteString(labelStyle.Render("Duration:") + valueStyle.Render(fmtDuration(entry.Duration)) + "\n")
		if sum.ModelName != "" {
			b.WriteString(labelStyle.Render("Model:") + dimStyle.Render(sum.ModelName) + "\n")
		}

		// ── Token Usage ───────────────────────────────────────────────────────
		b.WriteString("\n")
		b.WriteString(sessDetailSectionStyle.Render("── Token Usage ──"))
		b.WriteString("\n\n")

		b.WriteString(labelStyle.Render("Input:") + lipgloss.NewStyle().Foreground(lipgloss.Color(t.TokenInput)).Render(fmtTokens(sum.TotalInputTokens)+" tokens") + "\n")
		b.WriteString(labelStyle.Render("Output:") + lipgloss.NewStyle().Foreground(lipgloss.Color(t.TokenOutput)).Render(fmtTokens(sum.TotalOutputTokens)+" tokens") + "\n")
		b.WriteString(labelStyle.Render("Cache write:") + dimStyle.Render(fmtTokens(sum.CacheCreateTokens)+" tokens") + "\n")
		b.WriteString(labelStyle.Render("Cache read:") + dimStyle.Render(fmtTokens(sum.CacheReadTokens)+" tokens") + "\n")
		b.WriteString(labelStyle.Render("Turns:") + valueStyle.Render(fmt.Sprintf("%d", sum.TurnCount)) + "\n")
		b.WriteString(labelStyle.Render("Cost:") + lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green)).Render(sessions.FormatCost(entry.CostUSD)) + "\n")
		if entry.TokPerMin > 0 {
			b.WriteString(labelStyle.Render("Tok/min:") + lipgloss.NewStyle().Foreground(lipgloss.Color(t.Orange)).Render(fmtTokensShort(entry.TokPerMin)) + "\n")
		}
		if entry.CtxPct > 0 {
			b.WriteString(labelStyle.Render("Context:") + renderCtxBar(entry.CtxPct, &t) + "\n")
		}

		// ── User Messages ─────────────────────────────────────────────────────
		if len(sum.UserMsgs) > 0 {
			b.WriteString("\n")
			b.WriteString(sessDetailSectionStyle.Render("── User Messages ──"))
			b.WriteString("\n\n")
			maxMsgWidth := cw - 6
			if maxMsgWidth < 10 {
				maxMsgWidth = 10
			}
			for _, msg := range sum.UserMsgs {
				if len([]rune(msg)) > maxMsgWidth {
					msg = string([]rune(msg)[:maxMsgWidth-1]) + "…"
				}
				b.WriteString("  " + sessBulletStyle.Render("›") + " " + valueStyle.Render(msg) + "\n")
			}
		}

		// ── Tool Calls ────────────────────────────────────────────────────────
		if len(sum.ToolCalls) > 0 {
			b.WriteString("\n")
			b.WriteString(sessDetailSectionStyle.Render("── Tool Calls ──"))
			b.WriteString("\n\n")

			breakdown := sum.ToolBreakdown()
			type toolCount struct {
				name  string
				count int
			}
			var sorted []toolCount
			for name, count := range breakdown {
				sorted = append(sorted, toolCount{name, count})
			}
			// Sort by count descending, name ascending as tiebreak.
			for i := 1; i < len(sorted); i++ {
				for j := i; j > 0; j-- {
					a, bb := sorted[j-1], sorted[j]
					if a.count < bb.count || (a.count == bb.count && a.name > bb.name) {
						sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
					} else {
						break
					}
				}
			}
			for _, tc := range sorted {
				b.WriteString("  " +
					lipgloss.NewStyle().Foreground(lipgloss.Color(t.Blue)).Render(fmt.Sprintf("%-14s", tc.name)) +
					sessDimStyle.Render(fmt.Sprintf("%4d", tc.count)) + "\n")
			}
		}
	}

	// ── Hint bar ─────────────────────────────────────────────────────────────
	hintParts := []string{
		menuHintKeyStyle.Render("esc") + " " + menuHintLabelStyle.Render("back"),
		menuHintKeyStyle.Render("t") + " " + menuHintLabelStyle.Render("turns"),
		menuHintKeyStyle.Render("r") + " " + menuHintLabelStyle.Render("resume"),
		menuHintKeyStyle.Render("l") + " " + menuHintLabelStyle.Render("log"),
		menuHintKeyStyle.Render("q") + " " + menuHintLabelStyle.Render("quit"),
	}
	sep := "  " + menuHintDotStyle.Render("·") + "  "
	b.WriteString("\n")
	b.WriteString(sessHintBarStyle.Width(cw).Render(strings.Join(hintParts, sep)))

	return b.String()
}

// viewTurnMetrics renders the full-screen turn metrics view for the selected session.
func (m SessionsModel) viewTurnMetrics() string {
	var b strings.Builder
	cw := m.contentWidth()
	t := theme.Current()

	if len(m.entries) == 0 || m.cursor >= len(m.entries) {
		b.WriteString(sessDimStyle.Render("  No session selected"))
		return b.String()
	}

	entry := m.entries[m.cursor]
	sum := entry.Summary

	// ── Title ────────────────────────────────────────────────────────────────
	titleText := fmt.Sprintf("Turn Metrics  ·  %s  ·  %s", entry.Agent, entry.Project)
	b.WriteString(sessTitleStyle.Width(cw).Render(titleText))
	b.WriteString("\n")

	// ── Subtitle ─────────────────────────────────────────────────────────────
	b.WriteString(sessSubtitleStyle.Render(fmt.Sprintf("  %d turns  ·  %s total",
		entry.Turns, sessions.FormatCost(entry.CostUSD),
	)))
	b.WriteString("\n")
	b.WriteString("   " + sessRuleStyle.Render(strings.Repeat("╌", cw-3)))
	b.WriteString("\n\n")

	if sum == nil || len(sum.TurnMetrics) == 0 {
		b.WriteString(sessDimStyle.Render("  No turn metrics available"))
	} else {
		// Sample up to 15 rows.
		sampled := sessions.SampleTurnMetrics(sum.TurnMetrics, 15)

		// Compute session average cost/turn for the "sweet spot" detection.
		totalCost := sessions.EstimateCostFromSummary(sum)
		avgCostPerTurn := 0.0
		if sum.TurnCount > 0 {
			avgCostPerTurn = totalCost / float64(sum.TurnCount)
		}

		// Find sweet spot: last turn before cost/turn exceeded 2x average.
		sweetSpotTurn := -1
		for _, tm := range sum.TurnMetrics {
			c := sessions.TurnCost(tm, sum.ModelName)
			if c > 2*avgCostPerTurn {
				break
			}
			sweetSpotTurn = tm.Turn
		}

		// Column headers.
		hdrTurn := sessColHeaderStyle.Render(fmt.Sprintf("%-6s", "TURN"))
		hdrInput := sessColHeaderStyle.Render(fmt.Sprintf("%-9s", "INPUT"))
		hdrOutput := sessColHeaderStyle.Render(fmt.Sprintf("%-9s", "OUTPUT"))
		hdrCost := sessColHeaderStyle.Render(fmt.Sprintf("%-9s", "COST"))
		hdrCtx := sessColHeaderStyle.Render(fmt.Sprintf("%-8s", "CTX%"))
		b.WriteString("   " + hdrTurn + hdrInput + hdrOutput + hdrCost + hdrCtx)
		b.WriteString("\n")
		b.WriteString("   " + sessRuleStyle.Render(strings.Repeat("╌", cw-3)))
		b.WriteString("\n")

		for _, tm := range sampled {
			cost := sessions.TurnCost(tm, sum.ModelName)
			ctxPct := tokens.CtxPct(sum.ModelName, tm.InputTokens+tm.CacheRead+tm.CacheCreate)

			// Annotate turns where cost/turn > 2x session average.
			annotation := ""
			if avgCostPerTurn > 0 && cost > 2*avgCostPerTurn {
				annotation = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Red)).Render("  ← cost/turn 2x avg")
			}

			turnStr := fmt.Sprintf("%-6d", tm.Turn)
			inputStr := fmt.Sprintf("%-9s", fmtTokensShort(tm.InputTokens))
			outputStr := fmt.Sprintf("%-9s", fmtTokensShort(tm.OutputTokens))
			costStr := fmt.Sprintf("%-9s", sessions.FormatCost(cost))
			ctxStr := fmt.Sprintf("%-8s", fmt.Sprintf("%d%%", ctxPct))

			turnCell := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)).Render(turnStr)
			inputCell := lipgloss.NewStyle().Foreground(lipgloss.Color(t.TokenInput)).Render(inputStr)
			outputCell := lipgloss.NewStyle().Foreground(lipgloss.Color(t.TokenOutput)).Render(outputStr)
			costCell := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green)).Render(costStr)
			ctxColor := t.Green
			if ctxPct >= 80 {
				ctxColor = t.Red
			} else if ctxPct >= 60 {
				ctxColor = t.Yellow
			}
			ctxCell := lipgloss.NewStyle().Foreground(lipgloss.Color(ctxColor)).Render(ctxStr)

			b.WriteString("   " + turnCell + inputCell + outputCell + costCell + ctxCell + annotation)
			b.WriteString("\n")
		}

		b.WriteString("   " + sessRuleStyle.Render(strings.Repeat("╌", cw-3)))
		b.WriteString("\n")

		// Sweet spot line.
		if sweetSpotTurn > 0 {
			b.WriteString(sessDimStyle.Render(fmt.Sprintf("  Sweet spot: ~turn %d (cost/turn doubled after)", sweetSpotTurn)))
			b.WriteString("\n")
		}
	}

	// ── Hint bar ─────────────────────────────────────────────────────────────
	hintParts := []string{
		menuHintKeyStyle.Render("esc") + " " + menuHintLabelStyle.Render("back"),
		menuHintKeyStyle.Render("t") + " " + menuHintLabelStyle.Render("summary"),
		menuHintKeyStyle.Render("r") + " " + menuHintLabelStyle.Render("resume"),
		menuHintKeyStyle.Render("l") + " " + menuHintLabelStyle.Render("log"),
		menuHintKeyStyle.Render("q") + " " + menuHintLabelStyle.Render("quit"),
	}
	sep := "  " + menuHintDotStyle.Render("·") + "  "
	b.WriteString("\n")
	b.WriteString(sessHintBarStyle.Width(cw).Render(strings.Join(hintParts, sep)))

	return b.String()
}

// renderDetailPane renders the two-column detail pane for the selected entry.
// It fits within at most `height` lines.
func (m SessionsModel) renderDetailPane(cw, height int) string {
	if len(m.entries) == 0 || m.cursor >= len(m.entries) || m.entries[m.cursor].Summary == nil {
		return sessDimStyle.Render("  No session selected") + "\n"
	}

	entry := m.entries[m.cursor]
	sum := entry.Summary
	s := entry.Session
	loc := time.Now().Location()
	t := theme.Current()

	leftW := cw * 45 / 100
	if leftW < 20 {
		leftW = 20
	}

	labelStyle := sessDetailLabelStyle
	valueStyle := sessDetailValueStyle
	sectionStyle := sessDetailSectionStyle
	dimStyle := sessDimStyle

	// ── Left column: metadata ──
	var left []string
	left = append(left, sectionStyle.Render("── Session Details ──"))
	left = append(left, "")
	left = append(left, labelStyle.Render("Agent:")+valueStyle.Render(s.Agent))
	left = append(left, labelStyle.Render("Project:")+valueStyle.Render(shortenDir(s.Project)))
	if sum.GitBranch != "" {
		left = append(left, labelStyle.Render("Branch:")+dimStyle.Render(sum.GitBranch))
	}
	if entry.TicketNumber > 0 {
		left = append(left, labelStyle.Render("Ticket:")+lipgloss.NewStyle().Foreground(lipgloss.Color(t.Yellow)).Render(fmt.Sprintf("#%d", entry.TicketNumber)))
	}
	left = append(left, labelStyle.Render("Started:")+valueStyle.Render(s.StartTime.In(loc).Format("Mon Jan 2 3:04 PM")))
	left = append(left, labelStyle.Render("Duration:")+valueStyle.Render(fmtDuration(entry.Duration)))
	if sum.ModelName != "" {
		left = append(left, labelStyle.Render("Model:")+dimStyle.Render(sum.ModelName))
	}

	// ── Right column: metrics ──
	var right []string
	right = append(right, sectionStyle.Render("── Token Usage ──"))
	right = append(right, "")
	right = append(right, labelStyle.Render("Input:")+lipgloss.NewStyle().Foreground(lipgloss.Color(t.TokenInput)).Render(fmtTokens(sum.TotalInputTokens)+" tokens"))
	right = append(right, labelStyle.Render("Output:")+lipgloss.NewStyle().Foreground(lipgloss.Color(t.TokenOutput)).Render(fmtTokens(sum.TotalOutputTokens)+" tokens"))
	right = append(right, labelStyle.Render("Cache write:")+dimStyle.Render(fmtTokens(sum.CacheCreateTokens)+" tokens"))
	right = append(right, labelStyle.Render("Cache read:")+dimStyle.Render(fmtTokens(sum.CacheReadTokens)+" tokens"))
	right = append(right, labelStyle.Render("Turns:")+valueStyle.Render(fmt.Sprintf("%d", sum.TurnCount)))
	right = append(right, labelStyle.Render("Cost:")+lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green)).Render(sessions.FormatCost(entry.CostUSD)))
	if entry.TokPerMin > 0 {
		right = append(right, labelStyle.Render("Tok/min:")+lipgloss.NewStyle().Foreground(lipgloss.Color(t.Orange)).Render(fmtTokensShort(entry.TokPerMin)))
	}
	if entry.CtxPct > 0 {
		right = append(right, labelStyle.Render("Context:")+renderCtxBar(entry.CtxPct, &t))
	}

	// Merge left and right into two-column lines.
	maxMeta := max(len(left), len(right))
	var lines []string
	for i := 0; i < maxMeta; i++ {
		lLine := ""
		if i < len(left) {
			lLine = left[i]
		}
		rLine := ""
		if i < len(right) {
			rLine = right[i]
		}
		// Pad left column to leftW using a fixed-width container.
		lPadded := lipgloss.NewStyle().Width(leftW).MaxWidth(leftW).Render(lLine)
		lines = append(lines, lPadded+rLine)
	}

	// Below metadata: User Messages + Tool Calls.
	remainingLines := height - maxMeta - 1 // -1 for the blank separator line
	if remainingLines > 2 {
		lines = append(lines, "")
		remainingLines--

		// User Messages (left) + Tool Calls (right).
		var userLeft []string
		var toolRight []string

		if len(sum.UserMsgs) > 0 {
			userLeft = append(userLeft, sectionStyle.Render("── User Messages ──"))
			maxMsg := leftW - 4
			if maxMsg < 10 {
				maxMsg = 10
			}
			for _, msg := range sum.UserMsgs {
				if len(msg) > maxMsg {
					msg = msg[:maxMsg-1] + "…"
				}
				userLeft = append(userLeft, "  "+sessBulletStyle.Render("›")+" "+valueStyle.Render(msg))
			}
		}

		if len(sum.ToolCalls) > 0 {
			toolRight = append(toolRight, sectionStyle.Render("── Tool Calls ──"))
			breakdown := sum.ToolBreakdown()
			type toolCount struct {
				name  string
				count int
			}
			var sorted []toolCount
			for name, count := range breakdown {
				sorted = append(sorted, toolCount{name, count})
			}
			for i := 1; i < len(sorted); i++ {
				for j := i; j > 0; j-- {
					a, bb := sorted[j-1], sorted[j]
					if a.count < bb.count || (a.count == bb.count && a.name > bb.name) {
						sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
					} else {
						break
					}
				}
			}
			for i := 0; i < len(sorted); i += 2 {
				left := sorted[i]
				leftStr := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Blue)).Render(fmt.Sprintf("%-10s", left.name)) +
					dimStyle.Render(fmt.Sprintf("%4d", left.count))
				if i+1 < len(sorted) {
					right := sorted[i+1]
					rightStr := "    " + lipgloss.NewStyle().Foreground(lipgloss.Color(t.Blue)).Render(fmt.Sprintf("%-10s", right.name)) +
						dimStyle.Render(fmt.Sprintf("%4d", right.count))
					toolRight = append(toolRight, leftStr+rightStr)
				} else {
					toolRight = append(toolRight, leftStr)
				}
			}
		}

		maxBottom := max(len(userLeft), len(toolRight))
		cap := remainingLines
		if maxBottom < cap {
			cap = maxBottom
		}
		for i := 0; i < cap; i++ {
			lLine := ""
			if i < len(userLeft) {
				lLine = userLeft[i]
			}
			rLine := ""
			if i < len(toolRight) {
				rLine = toolRight[i]
			}
			lPadded := lipgloss.NewStyle().Width(leftW).MaxWidth(leftW).Render(lLine)
			lines = append(lines, lPadded+rLine)
		}
	}

	// Truncate to height.
	if len(lines) > height {
		lines = lines[:height]
	}

	// Pad to height.
	for len(lines) < height {
		lines = append(lines, "")
	}

	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// buildCell renders a single list cell with optional selected-row background.
func buildCell(style lipgloss.Style, width int, content string, selected bool, bgColor string) string {
	s := style.Width(width)
	if selected {
		s = s.Background(lipgloss.Color(bgColor))
	}
	return s.Render(content)
}

// renderCtxBarCell renders the context bar cell with optional row highlight background.
func renderCtxBarCell(pct int, t *theme.Theme, width int, selected bool, bgColor string) string {
	bar := renderCtxBar(pct, t)
	s := lipgloss.NewStyle().Width(width)
	if selected {
		s = s.Background(lipgloss.Color(bgColor))
	}
	return s.Render(bar)
}

// filterEntries returns only entries matching the given agent name.
func filterEntries(entries []SessionEntry, agent string) []SessionEntry {
	var filtered []SessionEntry
	for _, e := range entries {
		if e.Agent == agent {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// fmtTokensShort formats a token count with compact k/M suffix.
// e.g. 0→"0", 999→"999", 1500→"1.5k", 42381→"42.4k", 142381→"142k", 1409000→"1.4M"
func fmtTokensShort(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 100_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// renderCtxBar renders a 5-block bar + percentage string.
// e.g. 62 → "▓▓▓░░ 62%", colors based on fill level.
func renderCtxBar(pct int, t *theme.Theme) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := pct / 20
	empty := 5 - filled

	fillColor := t.Green
	switch {
	case pct >= 80:
		fillColor = t.Red
	case pct >= 60:
		fillColor = t.Yellow
	}
	if fillColor == "" {
		fillColor = t.Accent
	}
	emptyColor := t.Muted
	if emptyColor == "" {
		emptyColor = t.Subtle
	}

	bar := lipgloss.NewStyle().Foreground(lipgloss.Color(fillColor)).Render(strings.Repeat("▓", filled)) +
		lipgloss.NewStyle().Foreground(lipgloss.Color(emptyColor)).Render(strings.Repeat("░", empty))
	pctStr := lipgloss.NewStyle().Foreground(lipgloss.Color(fillColor)).Render(fmt.Sprintf("%3d%%", pct))
	return bar + pctStr
}

// turnsColor returns the theme color appropriate for a given turn count.
func turnsColor(turns int, t *theme.Theme) string {
	switch {
	case turns > 50:
		return t.TurnHot
	case turns > 20:
		return t.TurnWarn
	default:
		return t.TurnNormal
	}
}

// fmtTokens formats an integer with comma separators.
// e.g. 142381 → "142,381".
func fmtTokens(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	result := make([]byte, 0, len(s)+len(s)/3)
	rem := len(s) % 3
	if rem == 0 {
		rem = 3
	}
	result = append(result, s[:rem]...)
	for i := rem; i < len(s); i += 3 {
		result = append(result, ',')
		result = append(result, s[i:i+3]...)
	}
	return string(result)
}

// fmtAge formats a duration as a relative age string.
func fmtAge(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// fmtDuration formats a duration concisely.
func fmtDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	mn := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", h, mn)
}

// max returns the larger of two ints.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
