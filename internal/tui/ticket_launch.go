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
	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/theme"
)

// Internal width for the ticket launch popup content.
// tmux popup is -w 80; tmux border 2; lipgloss border 2; padding 4 = 72 usable.
const tlContentWidth = 72

// Ticket launch popup styles.
var (
	tlBoxStyle                 lipgloss.Style
	tlTitleStyle               lipgloss.Style
	tlSectionStyle             lipgloss.Style
	tlRuleStyle                lipgloss.Style
	tlRowStyle                 lipgloss.Style
	tlRowSelectedStyle         lipgloss.Style
	tlAgentNameStyle           lipgloss.Style
	tlAgentNameSelectedStyle   lipgloss.Style
	tlAgentDescStyle           lipgloss.Style
	tlTicketNumStyle           lipgloss.Style
	tlTicketNumSelectedStyle   lipgloss.Style
	tlTicketTitleStyle         lipgloss.Style
	tlTicketTitleSelectedStyle lipgloss.Style
	tlLoadingStyle             lipgloss.Style
	tlNoRepoStyle              lipgloss.Style
	tlSkipStyle                lipgloss.Style
	tlDirLabelStyle            lipgloss.Style
	tlDirValueStyle            lipgloss.Style
	tlDirEditingStyle          lipgloss.Style
	tlHintBarStyle             lipgloss.Style
	tlPromptLabelStyle         lipgloss.Style
	tlPromptLabelSelectedStyle lipgloss.Style
	tlPromptDescStyle          lipgloss.Style
)

// initTicketLaunchStyles initialises ticket-launch popup styles from the active theme.
func initTicketLaunchStyles() {
	t := theme.Current()

	tlBoxStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.Accent)).
		Padding(1, 2).
		Width(tlContentWidth + 6)

	tlTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Width(tlContentWidth).
		Align(lipgloss.Center)

	tlSectionStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Text))

	tlRuleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Surface))

	tlRowStyle = lipgloss.NewStyle().Width(tlContentWidth)

	tlRowSelectedStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.Surface)).
		Width(tlContentWidth)

	tlAgentNameStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Text)).
		Width(28)

	tlAgentNameSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true).
		Width(28)

	tlAgentDescStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))

	tlTicketNumStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Width(6)

	tlTicketNumSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true).
		Width(6)

	tlTicketTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))

	tlTicketTitleSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)

	tlLoadingStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Italic(true)

	tlNoRepoStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Overlay)).
		Italic(true)

	tlSkipStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Green)).
		Bold(true)

	tlDirLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	tlDirValueStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))

	tlDirEditingStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)

	tlHintBarStyle = lipgloss.NewStyle().
		Width(tlContentWidth).
		Align(lipgloss.Center).
		MarginTop(1).
		Foreground(lipgloss.Color(t.Muted))

	tlPromptLabelStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Text)).
		Width(22)

	tlPromptLabelSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true).
		Width(22)

	tlPromptDescStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
}

// tlFocus tracks which section is currently focused.
type tlFocus int

const (
	tlFocusAgents  tlFocus = iota
	tlFocusTickets tlFocus = iota
	tlFocusPrompt  tlFocus = iota
)

// ghIssue is a single GitHub issue returned by gh issue list.
type ghIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Labels    []ghLabel `json:"labels"`
	CreatedAt time.Time `json:"createdAt"`
	URL       string    `json:"url"`
	Assignees []ghUser  `json:"assignees"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghUser struct {
	Login string `json:"login"`
}

// ticketsFetchedMsg is sent when the gh issue list call completes.
type ticketsFetchedMsg struct {
	issues    []ghIssue
	repoSlug  string
	linkedPRs map[int]ghPR
	err       error
}

// issueBodyFetchedMsg is sent when gh issue view completes.
type issueBodyFetchedMsg struct {
	title string
	body  string
	err   error
}

// TicketLaunchResult is the output of the ticket launch popup.
type TicketLaunchResult struct {
	Cancelled    bool
	Agent        string
	Dir          string
	Ticket       int // 0 = no ticket
	TicketTitle  string
	TicketBody   string
	PromptMode   string // "work" | "triage" | "none" | "custom"
	CustomPrompt string // populated when PromptMode == "custom"
}

// tlPromptOption defines a prompt mode option shown in the picker.
type tlPromptOption struct {
	key         string
	label       string
	description string
}

// tlPromptOptions is the ordered list of prompt mode choices.
var tlPromptOptions = []tlPromptOption{
	{"w/enter", "Work on ticket", "Execute autonomously end-to-end"},
	{"r", "Code review", "Review implementation, flag issues"},
	{"t", "Triage ticket", "Analyze scope, suggest breakdown"},
	{"n", "No prompt", "Open conversational session"},
	{"c", "Custom", "Type your own prompt"},
}

// TicketLaunchModel is the Bubble Tea model for the quick ticket launcher popup.
type TicketLaunchModel struct {
	agents       []agent.Agent
	agentCursor  int
	tickets      []ghIssue
	ticketCursor int // 0 = "No ticket / fresh start", 1..N = tickets
	focus        tlFocus
	dir          string
	defaultDir   string
	editingDir   bool
	dirInput     textinput.Model
	loading      bool   // true while fetching tickets or issue body
	repoSlug     string // "owner/repo" for the selected directory
	noRepo       bool   // true if selected dir has no git remote
	result       TicketLaunchResult
	quitting     bool

	// Prompt mode picker state
	promptCursor        int
	editingCustomPrompt bool
	customPromptInput   textinput.Model
	pendingPromptMode   string // mode chosen in prompt picker, waiting for issue body fetch
	pendingCustomPrompt string // custom prompt text, waiting for issue body fetch
}

// NewTicketLaunchModel creates the ticket launch popup with the given agent list.
func NewTicketLaunchModel(agents []agent.Agent) TicketLaunchModel {
	defaultDir := tlCWD()

	ti := textinput.New()
	ti.CharLimit = 200
	ti.SetWidth(tlContentWidth - 10)
	ts := ti.Styles()
	ts.Focused.Prompt = tlDirEditingStyle
	ts.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	ti.SetStyles(ts)
	ti.Prompt = "dir: "
	ti.SetValue(defaultDir)

	ci := textinput.New()
	ci.CharLimit = 500
	ci.SetWidth(tlContentWidth - 10)
	cs := ci.Styles()
	cs.Focused.Prompt = tlDirEditingStyle
	cs.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	ci.SetStyles(cs)
	ci.Placeholder = "Describe what you want the agent to do..."

	return TicketLaunchModel{
		agents:            agents,
		focus:             tlFocusAgents,
		dir:               defaultDir,
		defaultDir:        defaultDir,
		dirInput:          ti,
		customPromptInput: ci,
	}
}

// PopupSize returns the tmux display-popup dimensions for this popup.
func (m TicketLaunchModel) PopupSize() PopupSize {
	return PopupSize{Width: 80, Height: 30}
}

// Result returns the popup output after it quits.
func (m TicketLaunchModel) Result() TicketLaunchResult { return m.result }

// Init kicks off an initial ticket fetch if the first agent has a directory.
func (m TicketLaunchModel) Init() tea.Cmd {
	if len(m.agents) > 0 {
		dir := m.tlAgentDir(0)
		if dir != "" {
			return tlFetchTicketsCmd(dir)
		}
	}
	return nil
}

// tlAgentDir returns the effective working directory for agent at index i.
func (m TicketLaunchModel) tlAgentDir(i int) string {
	if i < 0 || i >= len(m.agents) {
		return m.dir
	}
	if m.agents[i].Dir != "" {
		return m.agents[i].Dir
	}
	return m.dir
}

// ticketItemCount is "No ticket" row + all fetched tickets.
func (m TicketLaunchModel) ticketItemCount() int {
	return 1 + len(m.tickets)
}

// selectedAgentName returns the name of the currently highlighted agent.
func (m TicketLaunchModel) selectedAgentName() string {
	if len(m.agents) == 0 {
		return ""
	}
	if m.agentCursor >= len(m.agents) {
		return m.agents[len(m.agents)-1].Name
	}
	return m.agents[m.agentCursor].Name
}

func (m TicketLaunchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Dir edit mode — delegate almost everything to the text input.
	if m.editingDir {
		switch msg := msg.(type) {
		case tea.KeyPressMsg:
			switch msg.String() {
			case "enter":
				val := strings.TrimSpace(m.dirInput.Value())
				if val != "" {
					if strings.HasPrefix(val, "~/") {
						home, _ := os.UserHomeDir()
						val = filepath.Join(home, val[2:])
					}
					m.dir = val
				}
				m.editingDir = false
				m.dirInput.Blur()
				m.loading = true
				m.tickets = nil
				m.repoSlug = ""
				m.noRepo = false
				m.ticketCursor = 0
				return m, tlFetchTicketsCmd(m.dir)
			case "esc":
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
	case ticketsFetchedMsg:
		m.loading = false
		if msg.err != nil || msg.repoSlug == "" {
			m.noRepo = true
			m.tickets = nil
			m.repoSlug = ""
		} else {
			m.noRepo = false
			m.repoSlug = msg.repoSlug
			m.tickets = msg.issues
		}
		m.ticketCursor = 0
		return m, nil

	case issueBodyFetchedMsg:
		m.loading = false
		if msg.err != nil {
			// On error fall back to fresh launch
			m.result = TicketLaunchResult{
				Agent: m.selectedAgentName(),
				Dir:   m.dir,
			}
		} else {
			issue := m.tickets[m.ticketCursor-1]
			m.result = TicketLaunchResult{
				Agent:        m.selectedAgentName(),
				Dir:          m.dir,
				Ticket:       issue.Number,
				TicketTitle:  msg.title,
				TicketBody:   msg.body,
				PromptMode:   m.pendingPromptMode,
				CustomPrompt: m.pendingCustomPrompt,
			}
		}
		m.quitting = true
		return m, tea.Quit

	case tea.KeyPressMsg:
		switch m.focus {
		case tlFocusAgents:
			return m.updateAgents(msg)
		case tlFocusTickets:
			return m.updateTickets(msg)
		case tlFocusPrompt:
			return m.updatePrompt(msg)
		}
	}
	return m, nil
}

func (m TicketLaunchModel) updateAgents(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc", "q":
		m.result.Cancelled = true
		m.quitting = true
		return m, tea.Quit

	case "up", "k":
		if m.agentCursor > 0 {
			m.agentCursor--
			return m.onAgentChange()
		}

	case "down", "j":
		if m.agentCursor < len(m.agents)-1 {
			m.agentCursor++
			return m.onAgentChange()
		}

	case "d":
		m.editingDir = true
		return m, m.dirInput.Focus()

	case "tab", "enter":
		if !m.loading {
			m.focus = tlFocusTickets
		}

	case "f":
		m.result = TicketLaunchResult{
			Agent: m.selectedAgentName(),
			Dir:   m.dir,
		}
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

func (m TicketLaunchModel) updateTickets(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.result.Cancelled = true
		m.quitting = true
		return m, tea.Quit

	case "esc", "tab":
		m.focus = tlFocusAgents

	case "up", "k":
		if m.ticketCursor > 0 {
			m.ticketCursor--
		}

	case "down", "j":
		if m.ticketCursor < m.ticketItemCount()-1 {
			m.ticketCursor++
		}

	case "d":
		m.editingDir = true
		return m, m.dirInput.Focus()

	case "f":
		m.result = TicketLaunchResult{
			Agent: m.selectedAgentName(),
			Dir:   m.dir,
		}
		m.quitting = true
		return m, tea.Quit

	case "v", "o":
		// Open ticket in browser for viewing
		if m.ticketCursor > 0 && m.repoSlug != "" {
			issue := m.tickets[m.ticketCursor-1]
			return m, openIssueInBrowserCmd(m.repoSlug, issue.Number)
		}

	case "enter":
		if m.ticketCursor == 0 {
			// "No ticket" — fresh launch
			m.result = TicketLaunchResult{
				Agent: m.selectedAgentName(),
				Dir:   m.dir,
			}
			m.quitting = true
			return m, tea.Quit
		}
		// Transition to prompt mode picker
		m.focus = tlFocusPrompt
		m.promptCursor = 0
		m.editingCustomPrompt = false
		return m, nil

	case "D":
		// Fast-path: autonomous dispatch (skip prompt picker)
		if m.ticketCursor > 0 {
			issue := m.tickets[m.ticketCursor-1]
			m.pendingPromptMode = "work"
			m.pendingCustomPrompt = ""
			m.loading = true
			return m, tlFetchIssueBodyCmd(m.repoSlug, issue.Number)
		}
	}
	return m, nil
}

func (m TicketLaunchModel) updatePrompt(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Custom prompt text input is active — delegate to it.
	if m.editingCustomPrompt {
		switch msg.String() {
		case "enter":
			customPrompt := strings.TrimSpace(m.customPromptInput.Value())
			m.editingCustomPrompt = false
			m.customPromptInput.Blur()
			return m.fetchForPromptMode("custom", customPrompt)
		case "esc":
			m.editingCustomPrompt = false
			m.customPromptInput.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.customPromptInput, cmd = m.customPromptInput.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "ctrl+c", "q":
		m.result.Cancelled = true
		m.quitting = true
		return m, tea.Quit

	case "esc":
		// Go back to ticket picker
		m.focus = tlFocusTickets
		return m, nil

	case "up", "k":
		if m.promptCursor > 0 {
			m.promptCursor--
		}

	case "down", "j":
		if m.promptCursor < len(tlPromptOptions)-1 {
			m.promptCursor++
		}

	case "w":
		m.promptCursor = 0
		return m.fetchForPromptMode("work", "")

	case "r":
		m.promptCursor = 1
		return m.fetchForPromptMode("review", "")

	case "t":
		m.promptCursor = 2
		return m.fetchForPromptMode("triage", "")

	case "n":
		m.promptCursor = 3
		return m.fetchForPromptMode("none", "")

	case "c":
		m.promptCursor = 4
		m.editingCustomPrompt = true
		m.customPromptInput.SetValue("")
		m.customPromptInput.Focus()
		return m, textinput.Blink

	case "enter":
		switch m.promptCursor {
		case 0:
			return m.fetchForPromptMode("work", "")
		case 1:
			return m.fetchForPromptMode("review", "")
		case 2:
			return m.fetchForPromptMode("triage", "")
		case 3:
			return m.fetchForPromptMode("none", "")
		case 4:
			m.editingCustomPrompt = true
			m.customPromptInput.SetValue("")
			m.customPromptInput.Focus()
			return m, textinput.Blink
		}
	}
	return m, nil
}

// fetchForPromptMode stores the chosen mode and triggers the issue body fetch.
func (m TicketLaunchModel) fetchForPromptMode(mode, customPrompt string) (tea.Model, tea.Cmd) {
	m.pendingPromptMode = mode
	m.pendingCustomPrompt = customPrompt
	if m.ticketCursor > 0 && m.repoSlug != "" {
		issue := m.tickets[m.ticketCursor-1]
		m.loading = true
		return m, tlFetchIssueBodyCmd(m.repoSlug, issue.Number)
	}
	// No ticket body to fetch — quit directly
	m.result = TicketLaunchResult{
		Agent:        m.selectedAgentName(),
		Dir:          m.dir,
		PromptMode:   mode,
		CustomPrompt: customPrompt,
	}
	m.quitting = true
	return m, tea.Quit
}

// onAgentChange updates dir and kicks off ticket fetching when agent cursor moves.
func (m TicketLaunchModel) onAgentChange() (TicketLaunchModel, tea.Cmd) {
	newDir := m.tlAgentDir(m.agentCursor)
	m.dir = newDir
	m.dirInput.SetValue(newDir)
	m.loading = true
	m.tickets = nil
	m.repoSlug = ""
	m.noRepo = false
	m.ticketCursor = 0
	return m, tlFetchTicketsCmd(newDir)
}

// tlFetchTicketsCmd shells out to gh issue list and returns a ticketsFetchedMsg.
func tlFetchTicketsCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		slug := TLRepoSlug(dir)
		if slug == "" {
			return ticketsFetchedMsg{repoSlug: ""}
		}
		raw, err := listOpenIssues(slug, 15)
		if err != nil {
			return ticketsFetchedMsg{repoSlug: slug, err: err}
		}
		// Filter out issues with the "triage" label — they aren't ready for work
		var issues []ghIssue
		for _, iss := range raw {
			hasTriage := false
			for _, l := range iss.Labels {
				if l.Name == "triage" {
					hasTriage = true
					break
				}
			}
			if !hasTriage {
				issues = append(issues, iss)
			}
		}
		return ticketsFetchedMsg{issues: issues, repoSlug: slug}
	}
}

// tlFetchIssueBodyCmd fetches the full body of a single GitHub issue.
func tlFetchIssueBodyCmd(slug string, number int) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("gh", "issue", "view",
			fmt.Sprintf("%d", number),
			"--repo", slug,
			"--json", "title,body",
		)
		out, err := cmd.Output()
		if err != nil {
			return issueBodyFetchedMsg{err: err}
		}
		var v struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := json.Unmarshal(out, &v); err != nil {
			return issueBodyFetchedMsg{err: err}
		}
		return issueBodyFetchedMsg{title: v.Title, body: v.Body}
	}
}

// TLRepoSlug resolves "owner/repo" from the git remote in the given directory.
func TLRepoSlug(dir string) string {
	if dir == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return tlParseRepoSlug(strings.TrimSpace(string(out)))
}

// tlParseRepoSlug extracts "owner/repo" from an HTTPS or SSH git remote URL.
func tlParseRepoSlug(rawURL string) string {
	// SSH: git@github.com:owner/repo.git
	if strings.HasPrefix(rawURL, "git@") {
		parts := strings.SplitN(rawURL, ":", 2)
		if len(parts) == 2 {
			return strings.TrimSuffix(parts[1], ".git")
		}
		return ""
	}
	// HTTPS: https://github.com/owner/repo.git
	rawURL = strings.TrimSuffix(rawURL, ".git")
	parts := strings.Split(rawURL, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return ""
}

// tlCWD returns the current working directory (falls back to $HOME).
func tlCWD() string {
	dir, err := os.Getwd()
	if err != nil {
		home, _ := os.UserHomeDir()
		return home
	}
	return dir
}

func (m TicketLaunchModel) View() tea.View {
	// Prompt mode picker screen — replaces the normal ticket/agent view.
	if m.focus == tlFocusPrompt {
		return tea.NewView(m.viewPromptPicker())
	}

	var b strings.Builder

	// Title banner
	b.WriteString(tlTitleStyle.Render("Quick Ticket Launcher"))
	b.WriteString("\n")

	// Working directory row
	b.WriteString("\n")
	if m.editingDir {
		b.WriteString("  " + m.dirInput.View())
	} else {
		dirDisplay := shortenDir(m.dir)
		modified := ""
		if m.dir != m.defaultDir {
			modified = tlDirEditingStyle.Render(" (custom)")
		}
		b.WriteString("  " + tlDirLabelStyle.Render("dir: ") + tlDirValueStyle.Render(dirDisplay) + modified)
	}
	b.WriteString("\n")

	// --- Agent section ---
	b.WriteString("\n")
	agentHeader := "  " + tlSectionStyle.Render("Agent")
	if m.focus == tlFocusAgents {
		agentHeader += "  " + menuHintKeyStyle.Render("← focused")
	}
	b.WriteString(agentHeader)
	b.WriteString("\n")
	b.WriteString("  " + tlRuleStyle.Render(strings.Repeat("╌", tlContentWidth-2)))
	b.WriteString("\n")

	if len(m.agents) == 0 {
		b.WriteString(tlRowStyle.Render("   " + tlNoRepoStyle.Render("No agents found")))
		b.WriteString("\n")
	} else {
		const maxAgentsVisible = 5
		start := 0
		if m.agentCursor >= maxAgentsVisible {
			start = m.agentCursor - maxAgentsVisible + 1
		}
		end := start + maxAgentsVisible
		if end > len(m.agents) {
			end = len(m.agents)
		}
		for i := start; i < end; i++ {
			a := m.agents[i]
			selected := i == m.agentCursor && m.focus == tlFocusAgents
			cursor := "   "
			if selected {
				cursor = " " + cursorActiveStyle.Render("") + " "
			}
			nameS := tlAgentNameStyle
			if selected {
				nameS = tlAgentNameSelectedStyle
			}
			nameStr := nameS.Render(a.Name)
			desc := ""
			if a.Description != "" {
				maxDesc := tlContentWidth - 32
				d := a.Description
				if len(d) > maxDesc {
					d = d[:maxDesc-1] + "…"
				}
				desc = tlAgentDescStyle.Render(d)
			}
			inner := cursor + nameStr + desc
			if selected {
				b.WriteString(tlRowSelectedStyle.Render(inner))
			} else {
				b.WriteString(tlRowStyle.Render(inner))
			}
			b.WriteString("\n")
		}
		// Pad to consistent height
		rendered := end - start
		for i := rendered; i < maxAgentsVisible; i++ {
			b.WriteString(tlRowStyle.Render(""))
			b.WriteString("\n")
		}
	}

	// --- Ticket section ---
	b.WriteString("\n")
	ticketHeader := "  " + tlSectionStyle.Render("Ticket")
	if m.repoSlug != "" {
		ticketHeader += "  " + tlDirLabelStyle.Render(m.repoSlug)
	}
	if m.focus == tlFocusTickets {
		ticketHeader += "  " + menuHintKeyStyle.Render("← focused")
	}
	b.WriteString(ticketHeader)
	b.WriteString("\n")
	b.WriteString("  " + tlRuleStyle.Render(strings.Repeat("╌", tlContentWidth-2)))
	b.WriteString("\n")

	const maxTicketsVisible = 7

	switch {
	case m.loading:
		b.WriteString(tlRowStyle.Render("   " + tlLoadingStyle.Render("Loading tickets...")))
		b.WriteString("\n")
		for i := 1; i < maxTicketsVisible; i++ {
			b.WriteString(tlRowStyle.Render(""))
			b.WriteString("\n")
		}

	case m.noRepo:
		b.WriteString(tlRowStyle.Render("   " + tlNoRepoStyle.Render("No git remote found in this directory")))
		b.WriteString("\n")
		b.WriteString(tlRowStyle.Render(""))
		b.WriteString("\n")
		noTicketSelected := m.focus == tlFocusTickets && m.ticketCursor == 0
		b.WriteString(m.renderNoTicketRow(noTicketSelected))
		b.WriteString("\n")
		for i := 3; i < maxTicketsVisible; i++ {
			b.WriteString(tlRowStyle.Render(""))
			b.WriteString("\n")
		}

	default:
		// "No ticket" row (ticketCursor == 0)
		noTicketSelected := m.focus == tlFocusTickets && m.ticketCursor == 0
		b.WriteString(m.renderNoTicketRow(noTicketSelected))
		b.WriteString("\n")

		if len(m.tickets) == 0 {
			b.WriteString(tlRowStyle.Render("   " + tlNoRepoStyle.Render("No open issues found")))
			b.WriteString("\n")
			for i := 2; i < maxTicketsVisible; i++ {
				b.WriteString(tlRowStyle.Render(""))
				b.WriteString("\n")
			}
		} else {
			start := 0
			effective := m.ticketCursor - 1
			if effective < 0 {
				effective = 0
			}
			if effective >= maxTicketsVisible-1 {
				start = effective - (maxTicketsVisible - 2)
			}
			end := start + (maxTicketsVisible - 1)
			if end > len(m.tickets) {
				end = len(m.tickets)
			}
			for i := start; i < end; i++ {
				issue := m.tickets[i]
				selected := m.focus == tlFocusTickets && m.ticketCursor == i+1
				cursor := "   "
				if selected {
					cursor = " " + cursorActiveStyle.Render("") + " "
				}
				numS := tlTicketNumStyle
				titleS := tlTicketTitleStyle
				if selected {
					numS = tlTicketNumSelectedStyle
					titleS = tlTicketTitleSelectedStyle
				}
				numStr := numS.Render(fmt.Sprintf("#%-4d", issue.Number))
				maxTitle := tlContentWidth - 14
				title := issue.Title
				if len(title) > maxTitle {
					title = title[:maxTitle-1] + "…"
				}
				titleStr := titleS.Render(title)
				rowInner := cursor + numStr + titleStr
				if selected {
					b.WriteString(tlRowSelectedStyle.Render(rowInner))
				} else {
					b.WriteString(tlRowStyle.Render(rowInner))
				}
				b.WriteString("\n")
			}
			// Pad remaining rows
			rendered := end - start
			for i := rendered + 1; i < maxTicketsVisible; i++ {
				b.WriteString(tlRowStyle.Render(""))
				b.WriteString("\n")
			}
		}
	}

	// Hint bar
	var hints []struct{ key, label string }
	if m.focus == tlFocusAgents {
		hints = []struct{ key, label string }{
			{"j/k", "navigate"},
			{"enter/tab", "tickets"},
			{"d", "change dir"},
			{"f", "fresh"},
			{"esc", "cancel"},
		}
	} else {
		hints = []struct{ key, label string }{
			{"j/k", "navigate"},
			{"enter", "pick mode"},
			{"D", "dispatch"},
			{"v", "view"},
			{"tab/esc", "agents"},
			{"f", "fresh"},
		}
	}
	var hintParts []string
	for _, h := range hints {
		hintParts = append(hintParts, menuHintKeyStyle.Render(h.key)+" "+menuHintLabelStyle.Render(h.label))
	}
	b.WriteString(tlHintBarStyle.Render(strings.Join(hintParts, "  "+menuHintDotStyle.Render("·")+"  ")))

	return tea.NewView("\n" + tlBoxStyle.Render(b.String()) + "\n")
}

// viewPromptPicker renders the prompt mode selection screen.
func (m TicketLaunchModel) viewPromptPicker() string {
	var b strings.Builder

	b.WriteString(tlTitleStyle.Render("Quick Ticket Launcher"))
	b.WriteString("\n\n")

	// Show the selected ticket context
	if m.ticketCursor > 0 && m.ticketCursor <= len(m.tickets) {
		issue := m.tickets[m.ticketCursor-1]
		ticketRef := fmt.Sprintf("#%d", issue.Number)
		title := issue.Title
		maxTitle := tlContentWidth - len(ticketRef) - 4
		if len(title) > maxTitle {
			title = title[:maxTitle-1] + "…"
		}
		b.WriteString("  " + tlDirLabelStyle.Render(ticketRef+"  ") + tlDirValueStyle.Render(title))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString("  " + tlSectionStyle.Render("Prompt Mode"))
	b.WriteString("\n")
	b.WriteString("  " + tlRuleStyle.Render(strings.Repeat("╌", tlContentWidth-2)))
	b.WriteString("\n")

	if m.loading {
		b.WriteString(tlRowStyle.Render("   " + tlLoadingStyle.Render("Launching...")))
		b.WriteString("\n")
		for i := 1; i < len(tlPromptOptions); i++ {
			b.WriteString(tlRowStyle.Render(""))
			b.WriteString("\n")
		}
	} else {
		for i, opt := range tlPromptOptions {
			selected := i == m.promptCursor
			cursor := "   "
			if selected {
				cursor = " " + cursorActiveStyle.Render("▸") + " "
			}
			labelS := tlPromptLabelStyle
			if selected {
				labelS = tlPromptLabelSelectedStyle
			}
			inner := cursor + labelS.Render(opt.label) + tlPromptDescStyle.Render(opt.description)
			if selected {
				b.WriteString(tlRowSelectedStyle.Render(inner))
			} else {
				b.WriteString(tlRowStyle.Render(inner))
			}
			b.WriteString("\n")

			// Show custom prompt input below the "Custom" option when active
			if i == 4 && m.editingCustomPrompt {
				b.WriteString("   " + m.customPromptInput.View())
				b.WriteString("\n")
			}
		}
	}

	// Hint bar
	var hints []struct{ key, label string }
	if m.editingCustomPrompt {
		hints = []struct{ key, label string }{
			{"enter", "confirm"},
			{"esc", "cancel"},
		}
	} else {
		hints = []struct{ key, label string }{
			{"j/k", "navigate"},
			{"enter", "select"},
			{"esc", "back"},
		}
	}
	var hintParts []string
	for _, h := range hints {
		hintParts = append(hintParts, menuHintKeyStyle.Render(h.key)+" "+menuHintLabelStyle.Render(h.label))
	}
	b.WriteString(tlHintBarStyle.Render(strings.Join(hintParts, "  "+menuHintDotStyle.Render("·")+"  ")))

	return "\n" + tlBoxStyle.Render(b.String()) + "\n"
}

// renderNoTicketRow renders the "Launch fresh (no ticket)" selection row.
func (m TicketLaunchModel) renderNoTicketRow(selected bool) string {
	cursor := "   "
	if selected {
		cursor = " " + cursorActiveStyle.Render("") + " "
	}
	inner := cursor + tlSkipStyle.Render("Launch fresh (no ticket)")
	if selected {
		return tlRowSelectedStyle.Render(inner)
	}
	return tlRowStyle.Render(inner)
}
