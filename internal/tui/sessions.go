package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/render"
	"github.com/blsmth/agentctl/internal/sessions"
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

// Session list/detail popup styles.
var (
	sessTitleStyle         lipgloss.Style
	sessRuleStyle          lipgloss.Style
	sessSectionStyle       lipgloss.Style
	sessColHeaderStyle     lipgloss.Style
	sessAgeStyle           lipgloss.Style
	sessAgentStyle         lipgloss.Style
	sessAgentSelectedStyle lipgloss.Style
	sessDimStyle           lipgloss.Style
	sessCostStyle          lipgloss.Style
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

	sessHighlightBg = t.Surface

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
	CostUnknown       bool // model has no known price: show "?" and exclude from totals
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
		backends := config.Load().Backends
		var entries []SessionEntry
		for _, s := range recentSessions {
			sum, err := sessions.ParseSummary(s)
			if err != nil {
				continue
			}

			project := sessions.NormalizeProjectName(s.Project)
			modelShort := render.AbbreviateModel(sum.ModelName)
			sc := sessions.CostOfSummary(sum, sessions.BillingFor(s, sum.ModelName, backends))
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
				CostUSD:           sc.USD,
				CostUnknown:       !sc.Known,
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

// SessionsModel is the Bubble Tea model for the sessions browser.
//
// It lays out at whatever size it is given (the hub viewer pane, a full
// window, a popup): see sessions_view.go. Every clickable span is recorded as
// a zone when the frame renders (zones.go) and sessions_mouse.go maps a click
// back to the key it stands for.
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

	// Scrolling: first visible line of the list, and of the preview / detail
	// body.
	listTop       int
	previewScroll int

	// helpOpen shows the ? keys-and-gestures overlay.
	helpOpen bool

	// Mouse state. zones is shared with copies of the model so View (a value
	// receiver) can record the zones of the frame it drew.
	zones     *zoneList
	hover     string // innermost target under the pointer
	hoverRow  string // row zone under the pointer, for the row tint
	press     tkPress
	lastClick tkPress
}

// sessChromRows is the number of lines the list view spends outside the list
// body: title, chip row, column header, rule, totals line and hint bar.
const sessChromRows = 6

// NewSessionsModel creates the sessions browser with optional agent filter.
// Data is loaded asynchronously via Init().
func NewSessionsModel(currentAgent string) SessionsModel {
	si := textinput.New()
	si.Placeholder = "filter sessions..."
	siS := si.Styles()
	siS.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	si.SetStyles(siS)

	return SessionsModel{
		termWidth:   120,
		termHeight:  40,
		loading:     true,
		filterAgent: currentAgent,
		showAll:     currentAgent == "",
		agentIdx:    -1,
		projectIdx:  -1,
		searchInput: si,
		zones:       &zoneList{},
	}
}

// contentWidth returns the usable content width: the terminal minus the
// margins, never below a small floor so the layout math stays positive.
func (m SessionsModel) contentWidth() int {
	w := m.termWidth - 4
	if w < 20 {
		w = 20
	}
	return w
}

// Result returns the popup output after it quits.
func (m SessionsModel) Result() SessionsResult { return m.result }

func (m SessionsModel) Init() tea.Cmd {
	return sessFetchCmd(m.filterAgent)
}

func (m SessionsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if sm, ok := next.(SessionsModel); ok {
		sm.syncScroll()
		next = sm
	}
	return next, cmd
}

func (m SessionsModel) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
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
		return m.handleKey(msg)

	case tea.MouseClickMsg:
		return m.handleMousePress(msg)
	case tea.MouseReleaseMsg:
		return m.handleMouseRelease(msg)
	case tea.MouseMotionMsg:
		return m.handleMouseMotion(msg)
	case tea.MouseWheelMsg:
		return m.handleMouseWheel(msg)
	}
	return m, nil
}

// handleKey routes a key press (typed, or synthesized by a click) to the
// overlay, the search box or the current view.
func (m SessionsModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.helpOpen {
		switch msg.String() {
		case "esc", "?", "q", "enter":
			m.helpOpen = false
		case "ctrl+c":
			m.result.Cancelled = true
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	}
	if m.searchMode {
		return m.updateSearch(msg)
	}
	if m.viewMode == 1 {
		return m.updateDetail(msg)
	}
	return m.updateList(msg)
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
	m.previewScroll = 0
}

// clearAgentFilter shows every agent again.
func (m *SessionsModel) clearAgentFilter() {
	m.agentIdx = -1
	m.filterAgent = ""
	m.showAll = true
	m.cursor = 0
	m.applyFilters()
}

// clearProjectFilter shows every project again.
func (m *SessionsModel) clearProjectFilter() {
	m.projectIdx = -1
	m.filterProject = ""
	m.cursor = 0
	m.applyFilters()
}

// clearSearch drops the search query.
func (m *SessionsModel) clearSearch() {
	m.searchQuery = ""
	m.searchInput.SetValue("")
	m.applyFilters()
}

// moveCursor moves the selection to i (clamped), resetting the preview scroll.
func (m *SessionsModel) moveCursor(i int) {
	if i >= len(m.entries) {
		i = len(m.entries) - 1
	}
	if i < 0 {
		i = 0
	}
	if i != m.cursor {
		m.previewScroll = 0
	}
	m.cursor = i
}

// resumeResult and logResult build the result of the r and l actions.
func (m SessionsModel) quitWith(action string) (tea.Model, tea.Cmd) {
	if len(m.entries) == 0 || m.cursor >= len(m.entries) {
		return m, nil
	}
	s := m.entries[m.cursor].Session
	m.result = SessionsResult{
		Action:    action,
		SessionID: s.ID,
		Agent:     s.Agent,
		Project:   s.Project,
	}
	if action == "log" {
		m.result.LogFile = s.LogFile
	}
	m.quitting = true
	return m, tea.Quit
}

func (m SessionsModel) quit() (tea.Model, tea.Cmd) {
	m.result.Cancelled = true
	m.quitting = true
	return m, tea.Quit
}

func (m SessionsModel) updateList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m.quit()

	case "?":
		m.helpOpen = true

	case "esc":
		if m.searchQuery != "" {
			m.clearSearch()
			return m, nil
		}
		return m.quit()

	case "up", "k":
		m.moveCursor(m.cursor - 1)

	case "down", "j":
		m.moveCursor(m.cursor + 1)

	case "J":
		m.previewScroll++
	case "K":
		if m.previewScroll > 0 {
			m.previewScroll--
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
		m.viewMode = 1
		m.detailTab = 1
		m.previewScroll = 0

	case "enter":
		// Open the full detail view for the selected session.
		if len(m.entries) == 0 {
			break
		}
		m.viewMode = 1
		m.detailTab = 0
		m.previewScroll = 0

	case "r":
		return m.quitWith("resume")

	case "l":
		return m.quitWith("log")
	}
	return m, nil
}

func (m SessionsModel) updateSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.searchMode = false
		m.searchInput.Blur()
		m.clearSearch()
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

// updateDetail handles key presses in the full-size detail view.
func (m SessionsModel) updateDetail(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m.quit()

	case "?":
		m.helpOpen = true

	case "esc":
		m.viewMode = 0
		m.detailTab = 0
		m.previewScroll = 0

	case "t":
		// Toggle between summary and turn metrics.
		m.detailTab = 1 - m.detailTab
		m.previewScroll = 0

	case "j", "down", "J", "pgdown":
		m.previewScroll++
	case "k", "up", "K", "pgup":
		if m.previewScroll > 0 {
			m.previewScroll--
		}

	case "r":
		return m.quitWith("resume")

	case "l":
		return m.quitWith("log")
	}
	return m, nil
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
