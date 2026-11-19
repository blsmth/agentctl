package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/prompt"
	"github.com/blsmth/agentctl/internal/theme"
)

// ── Context viewer styles ─────────────────────────────────────────────────────

var (
	ctxColHeaderStyle    lipgloss.Style
	ctxRuleStyle         lipgloss.Style
	ctxDimStyle          lipgloss.Style
	ctxSubtitleStyle     lipgloss.Style
	ctxNameStyle         lipgloss.Style
	ctxNameSelectedStyle lipgloss.Style
	ctxWarnStyle         lipgloss.Style
	ctxLabelStyle        lipgloss.Style
	ctxValueStyle        lipgloss.Style
)

// initContextStyles initialises context-viewer styles from the active theme.
func initContextStyles() {
	t := theme.Current()
	ctxColHeaderStyle = tableStyles.ColHeader
	ctxRuleStyle = tableStyles.Rule
	ctxDimStyle = tableStyles.Dim
	ctxSubtitleStyle = tableStyles.Subtitle
	ctxLabelStyle = tableStyles.DetailLabel
	ctxValueStyle = tableStyles.DetailValue
	ctxNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	ctxNameSelectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true)
	ctxWarnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Yellow)).Bold(true)
}

const (
	ctxColAgent   = 14
	ctxColTags    = 18
	ctxColSize    = 10
	ctxColUpdated = 9

	// ctxMaxPreviewBytes bounds how much of one entry is wrapped and shown, so a
	// pathological multi-megabyte entry cannot stall rendering.
	ctxMaxPreviewBytes = 256 * 1024

	ctxToastTTL = 4 * time.Second
)

// ContextModel is the full-screen Bubble Tea model for browsing the context
// store: a list of entries with a scrollable preview of the selected one.
type ContextModel struct {
	termWidth  int
	termHeight int

	all     []ctxstore.Entry
	entries []ctxstore.Entry // filtered view

	cursor    int
	splitPane SplitPane

	searchMode  bool
	searchInput textinput.Model
	searchQuery string

	confirmKey string // non-empty while a delete awaits y/n

	toast   string
	toastAt time.Time
	loadErr error

	quitting bool
}

// NewContextModel creates the viewer and loads the store.
func NewContextModel() ContextModel {
	si := textinput.New()
	si.Placeholder = "key, agent or tag..."
	siS := si.Styles()
	siS.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	si.SetStyles(siS)

	m := ContextModel{
		termWidth:   120,
		termHeight:  40,
		searchInput: si,
		splitPane:   SplitPane{ListRatio: 0.4, MinListRows: 3, MinDetailRows: 3},
	}
	m.resize()
	m.reload()
	return m
}

// Quitting reports whether the user asked to leave the viewer.
func (m ContextModel) Quitting() bool { return m.quitting }

// reload re-reads the store, keeping the selection on the same key if it
// still exists.
func (m *ContextModel) reload() {
	prev := m.selectedKey()
	all, err := ctxstore.List()
	m.loadErr = err
	m.all = all
	m.applyFilter()
	if prev != "" {
		for i, e := range m.entries {
			if e.Key == prev {
				m.cursor = i
				break
			}
		}
		m.splitPane.SetCursor(m.cursor)
	}
}

func (m ContextModel) selectedKey() string {
	if m.cursor >= 0 && m.cursor < len(m.entries) {
		return m.entries[m.cursor].Key
	}
	return ""
}

func (m *ContextModel) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.searchQuery))
	m.entries = m.entries[:0:0]
	for _, e := range m.all {
		if q == "" || contextMatches(e, q) {
			m.entries = append(m.entries, e)
		}
	}
	if m.cursor >= len(m.entries) {
		m.cursor = max(0, len(m.entries)-1)
	}
	m.splitPane.SetCursor(m.cursor)
}

func contextMatches(e ctxstore.Entry, q string) bool {
	if strings.Contains(strings.ToLower(e.Key), q) || strings.Contains(strings.ToLower(e.Agent), q) {
		return true
	}
	for _, tag := range e.Tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return true
		}
	}
	return false
}

func (m *ContextModel) setToast(s string) {
	m.toast = s
	m.toastAt = time.Now()
}

func (m ContextModel) contentWidth() int {
	return max(m.termWidth-4, 20)
}

func (m *ContextModel) resize() {
	paneH := max(m.termHeight-10, 7) // title, subtitle, header, rule, hints + detail box chrome
	m.splitPane.SetSize(m.contentWidth(), paneH)
}

// verifyCurrent re-reads the selected entry from the store. When another
// process deleted it, the viewer toasts and reloads, and reports false.
func (m *ContextModel) verifyCurrent() bool {
	key := m.selectedKey()
	if key == "" {
		return false
	}
	_, ok, err := ctxstore.Get(key)
	if err != nil {
		m.setToast(fmt.Sprintf("read %q: %v", key, err))
		return false
	}
	if !ok {
		m.setToast(fmt.Sprintf("%q no longer exists — reloaded", key))
		m.reload()
		return false
	}
	return true
}

// ── Bubble Tea ────────────────────────────────────────────────────────────────

// Init satisfies tea.Model.
func (m ContextModel) Init() tea.Cmd { return nil }

// Update satisfies tea.Model.
func (m ContextModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth, m.termHeight = msg.Width, msg.Height
		m.resize()
	case tea.KeyPressMsg:
		if m.toast != "" && time.Since(m.toastAt) > ctxToastTTL {
			m.toast = ""
		}
		switch {
		case m.confirmKey != "":
			return m.updateConfirm(msg)
		case m.searchMode:
			return m.updateSearch(msg)
		default:
			return m.updateNormal(msg)
		}
	}
	return m, nil
}

func (m ContextModel) updateConfirm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := m.confirmKey
	m.confirmKey = ""
	if msg.String() != "y" {
		m.setToast("delete cancelled")
		return m, nil
	}
	// Delete is a no-op for a missing key, so check first to tell the user
	// that someone else already removed it.
	_, ok, err := ctxstore.Get(key)
	switch {
	case err != nil:
		m.setToast(fmt.Sprintf("read %q: %v", key, err))
	case !ok:
		m.setToast(fmt.Sprintf("%q was already deleted — reloaded", key))
		m.reload()
	default:
		if err := ctxstore.Delete(key); err != nil {
			m.setToast(fmt.Sprintf("delete failed: %v", err))
		} else {
			m.setToast(fmt.Sprintf("deleted %q", key))
		}
		m.reload()
	}
	return m, nil
}

func (m ContextModel) updateNormal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "esc":
		m.quitting = true
		return m, tea.Quit
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "d":
		if len(m.entries) > 0 && m.verifyCurrent() {
			m.confirmKey = m.selectedKey()
		}
	case "r":
		m.reload()
		m.setToast("reloaded")
	case "/":
		m.searchMode = true
		m.searchInput.SetValue(m.searchQuery)
		m.searchInput.Focus()
		return m, textinput.Blink
	default:
		m.splitPane.Update(msg)
	}
	return m, nil
}

func (m *ContextModel) move(delta int) {
	n := m.cursor + delta
	if n < 0 || n >= len(m.entries) {
		return
	}
	m.cursor = n
	m.splitPane.SetCursor(n)
	m.splitPane.SetDetailContent("")
	m.splitPane.detailScroll = 0
	m.verifyCurrent()
}

func (m ContextModel) updateSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
	}
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	// Live filtering while typing.
	m.searchQuery = m.searchInput.Value()
	m.applyFilter()
	return m, cmd
}

// ── View ──────────────────────────────────────────────────────────────────────

// View satisfies tea.Model.
func (m ContextModel) View() tea.View {
	if m.quitting {
		v := tea.NewView("")
		v.AltScreen = true
		return v
	}
	v := tea.NewView(m.buildView())
	v.AltScreen = true
	return v
}

func (m ContextModel) buildView() string {
	var sb strings.Builder
	cw := m.contentWidth()
	t := theme.Current()

	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Width(cw).
		Align(lipgloss.Center).
		Render("Context Store")
	sb.WriteString(title + "\n")
	sb.WriteString(m.renderSubtitle(cw) + "\n")

	if len(m.entries) == 0 {
		sb.WriteString(m.renderEmpty(cw))
		sb.WriteString("\n" + m.renderHints(cw))
		return sb.String()
	}

	sb.WriteString(m.renderColumnHeaders(cw) + "\n")
	sb.WriteString(ctxRuleStyle.Render(strings.Repeat("─", cw)) + "\n")

	sp := m.splitPane
	sp.SetListContent(m.buildListContent(cw), len(m.entries))
	sp.SetDetailContent(m.buildDetailContent(cw - detailBoxHorizontalOverhead))
	sb.WriteString(sp.View())
	sb.WriteString("\n" + m.renderHints(cw))
	return sb.String()
}

func (m ContextModel) renderSubtitle(cw int) string {
	var s string
	switch {
	case m.confirmKey != "":
		return "  " + ctxWarnStyle.Render(fmt.Sprintf("Delete %q? y/n", m.confirmKey))
	case m.searchMode:
		return "  " + ctxSubtitleStyle.Render("filter: ") + m.searchInput.View()
	case m.toast != "" && time.Since(m.toastAt) <= ctxToastTTL:
		return "  " + ctxWarnStyle.Render(truncateToWidth(m.toast, cw-2))
	case m.loadErr != nil:
		s = fmt.Sprintf("  could not read store: %v", m.loadErr)
	case m.searchQuery != "":
		s = fmt.Sprintf("  filter: %q — %d of %d entries", m.searchQuery, len(m.entries), len(m.all))
	default:
		total := 0
		for _, e := range m.all {
			total += len(e.Content)
		}
		s = fmt.Sprintf("  %d entries · %s", len(m.all), formatContextBytes(total))
	}
	return ctxSubtitleStyle.Render(truncateToWidth(s, cw))
}

func (m ContextModel) renderEmpty(cw int) string {
	var lines []string
	switch {
	case m.loadErr != nil:
		lines = []string{"Could not read the context store.", m.loadErr.Error()}
	case len(m.all) > 0:
		lines = []string{fmt.Sprintf("No entries match %q.", m.searchQuery), "Press / to change the filter."}
	default:
		lines = []string{
			"The context store is empty.",
			"Add an entry with: agentctl context set <key> <value> --agent <name>",
		}
	}
	var sb strings.Builder
	sb.WriteString("\n")
	for _, l := range lines {
		sb.WriteString("  " + ctxDimStyle.Render(truncateToWidth(l, cw-2)) + "\n")
	}
	return sb.String()
}

// ctxColumns decides which optional columns fit in width cw.
func ctxColumns(cw int) (showAgent, showTags bool, keyW int) {
	fixed := ctxColSize + ctxColUpdated + 2
	showAgent = cw >= 50
	showTags = cw >= 72
	if showAgent {
		fixed += ctxColAgent
	}
	if showTags {
		fixed += ctxColTags
	}
	return showAgent, showTags, max(cw-fixed, 8)
}

func (m ContextModel) renderColumnHeaders(cw int) string {
	showAgent, showTags, keyW := ctxColumns(cw)
	var sb strings.Builder
	sb.WriteString("  " + ctxColHeaderStyle.Width(keyW).Render("KEY"))
	if showAgent {
		sb.WriteString(ctxColHeaderStyle.Width(ctxColAgent).Render("AGENT"))
	}
	if showTags {
		sb.WriteString(ctxColHeaderStyle.Width(ctxColTags).Render("TAGS"))
	}
	sb.WriteString(ctxColHeaderStyle.Width(ctxColSize).Render("SIZE"))
	sb.WriteString(ctxColHeaderStyle.Width(ctxColUpdated).Render("UPDATED"))
	return sb.String()
}

func (m ContextModel) buildListContent(cw int) string {
	showAgent, showTags, keyW := ctxColumns(cw)
	rows := make([]string, 0, len(m.entries))
	for i, e := range m.entries {
		sel := i == m.cursor
		nameS, prefix := ctxNameStyle, "  "
		if sel {
			nameS, prefix = ctxNameSelectedStyle, "> "
		}
		var sb strings.Builder
		sb.WriteString(prefix + nameS.Width(keyW).MaxWidth(keyW).Render(truncateToWidth(sanitizeContextLine(e.Key), keyW-1)))
		if showAgent {
			sb.WriteString(ctxDimStyle.Width(ctxColAgent).MaxWidth(ctxColAgent).Render(truncateToWidth(contextAgentLabel(e.Agent), ctxColAgent-1)))
		}
		if showTags {
			sb.WriteString(ctxDimStyle.Width(ctxColTags).MaxWidth(ctxColTags).Render(truncateToWidth(strings.Join(e.Tags, ","), ctxColTags-1)))
		}
		sb.WriteString(ctxDimStyle.Width(ctxColSize).Render(formatContextSize(e.Content)))
		sb.WriteString(ctxDimStyle.Width(ctxColUpdated).Render(FormatContextAge(e.Timestamp)))
		rows = append(rows, sb.String())
	}
	return strings.Join(rows, "\n")
}

func contextAgentLabel(agent string) string {
	if agent == "" {
		return "—"
	}
	return sanitizeContextLine(agent)
}

// buildDetailContent renders the header and wrapped content of the selected
// entry. width is the interior width of the detail box.
func (m ContextModel) buildDetailContent(width int) string {
	if m.cursor < 0 || m.cursor >= len(m.entries) {
		return ""
	}
	width = max(width, 10)
	e := m.entries[m.cursor]

	label := func(k, v string) string {
		return ctxLabelStyle.Render(fmt.Sprintf("%-8s", k)) + ctxValueStyle.Render(v)
	}
	agent := "— (stored, not auto-injected)"
	if e.Agent != "" {
		agent = sanitizeContextLine(e.Agent)
	}
	tags := "—"
	if len(e.Tags) > 0 {
		tags = sanitizeContextLine(strings.Join(e.Tags, ", "))
	}
	header := []string{
		label("key", sanitizeContextLine(e.Key)),
		label("agent", agent),
		label("tags", tags),
		label("updated", e.Timestamp.Format("2006-01-02 15:04:05")+" ("+FormatContextAge(e.Timestamp)+")"),
		label("size", formatContextSize(e.Content)+" · "+formatContextBytes(len(e.Content))),
		ctxRuleStyle.Render(strings.Repeat("─", width)),
	}
	hdr := lipgloss.NewStyle().Width(width).MaxWidth(width).Render(strings.Join(header, "\n"))

	body := e.Content
	note := ""
	if len(body) > ctxMaxPreviewBytes {
		body = strings.ToValidUTF8(body[:ctxMaxPreviewBytes], "")
		note = "\n" + ctxDimStyle.Render("… truncated for display; use `agentctl context get` for the full entry")
	}
	if strings.TrimSpace(body) == "" {
		body = "(empty)"
	}
	body = lipgloss.NewStyle().Width(width).MaxWidth(width).Render(sanitizeContextBody(body))
	return hdr + "\n" + body + note
}

func (m ContextModel) renderHints(cw int) string {
	hints := []string{"j/k", "navigate", "J/K", "scroll", "/", "filter", "d", "delete", "r", "reload", "q", "quit"}
	if m.confirmKey != "" {
		hints = []string{"y", "confirm delete", "n", "cancel"}
	}
	return lipgloss.NewStyle().Width(cw).Align(lipgloss.Center).Render(renderHintLine(hints...))
}

// ── Formatting helpers ────────────────────────────────────────────────────────

// formatContextSize renders approximate tokens, the unit the memory viewer uses.
func formatContextSize(content string) string {
	return "~" + formatTokenCount(prompt.EstimateTokens(content)) + " tok"
}

func formatContextBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// sanitizeContextLine flattens a single-line value: control characters become
// spaces so they cannot move the cursor or break the row layout.
func sanitizeContextLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// sanitizeContextBody keeps newlines, expands tabs and drops other control
// characters (including ESC, so stored escape sequences cannot reach the
// terminal).
func sanitizeContextBody(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
}
