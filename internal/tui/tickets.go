package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/agentlabel"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/charmbracelet/x/ansi"
)

// tkStyles holds the pre-configured styles for the tickets browser popup (-w 90).
var tkStyles Styles

// Additional styles for the tickets browser.
var (
	tkColHeaderStyle     lipgloss.Style
	tkRuleStyle          lipgloss.Style
	tkNumStyle           lipgloss.Style
	tkNumSelectedStyle   lipgloss.Style
	tkTitleColStyle      lipgloss.Style
	tkTitleSelectedStyle lipgloss.Style
	tkAgeStyle           lipgloss.Style
	tkDimStyle           lipgloss.Style
	tkDetailLabelStyle   lipgloss.Style
	tkDetailValueStyle   lipgloss.Style
	tkDetailSectionStyle lipgloss.Style
	tkSubtitleStyle      lipgloss.Style
	tkCodeBlockStyle     lipgloss.Style
)

// Column widths for the tickets list view.
const (
	tkColNum      = 8
	tkColPR       = 8 // "PR#NNN  " badge or blank
	tkColLabels   = 24
	tkColAssignee = 12 // "@loginname  " badge or blank
	tkColAge      = 6
	// title fills the remaining space
)

// sortMode controls how the issue list is ordered.
type sortMode int

const (
	sortNewest sortMode = iota // newest first (default)
	sortOldest                 // oldest first
	sortNumber                 // ascending issue number
)

// createFocus is the focused section in the single-screen create form.
type createFocus int

const (
	focusTitle  createFocus = iota // title text input
	focusBody                      // body textarea
	focusLabels                    // label picker (only when labels available)
	focusSubmit                    // Submit button
	focusCancel                    // Cancel button
)

// autoPollTickMsg fires on each poll interval.
type autoPollTickMsg struct{}

// autoPollDispatch records a single auto-dispatch that occurred this cycle.
type autoPollDispatch struct {
	issueNumber int
	issueTitle  string
	agentName   string
}

// autoPollResultMsg is sent after a poll+dispatch cycle completes.
type autoPollResultMsg struct {
	dispatched []autoPollDispatch
	failures   []string // per-issue dispatch failures (not recorded as dispatched)
	err        error
}

// ghPR is a single GitHub pull request returned by gh pr list.
type ghPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
	URL         string `json:"url"`
}

// tkIssueClosedMsg is sent after an issue close command completes.
type tkIssueClosedMsg struct {
	number int
	err    error
}

// tkSelfAssignedMsg is sent after a self-assign command completes.
type tkSelfAssignedMsg struct {
	number int
	err    error
}

// tkDetailFetchedMsg is sent when we fetch full issue details for the detail view.
type tkDetailFetchedMsg struct {
	number    int
	title     string
	body      string
	labels    []ghLabel
	url       string
	assignees []ghUser
	err       error
}

// tkPreviewFetchedMsg is sent when a background preview fetch completes for the split pane.
type tkPreviewFetchedMsg tkDetailFetchedMsg

// TicketsResult is the output of the tickets browser popup.
type TicketsResult struct {
	Cancelled    bool
	LaunchAgent  bool
	TicketNumber int
	TicketTitle  string
	TicketBody   string
	AgentName    string // selected agent (empty = auto-detect from labels)
	PromptMode   string // "work" | "triage" | "none" | "custom"
	CustomPrompt string // populated when PromptMode == "custom"
}

// TicketsModel is the Bubble Tea model for the full-screen ticket viewer.
type TicketsModel struct {
	// Terminal dimensions — updated via WindowSizeMsg.
	termWidth  int
	termHeight int

	// List view state
	issues    []ghIssue
	allIssues []ghIssue
	cursor    int
	repoSlug  string
	loading   bool
	err       error

	// Detail view state
	viewMode     int // 0=list, 1=detail
	detail       *tkDetailFetchedMsg
	scrollOffset int

	// Label filter
	filterLabel  string   // "" = all, else filter to this label
	labelOptions []string // unique labels from fetched issues
	labelIdx     int      // current index in labelOptions cycle (-1 = all)

	// Search / filter
	searchMode  bool            // true when search input is active
	searchInput textinput.Model // text input for search query
	searchQuery string          // current active query (persists after closing input)

	// Create mode — the form is rendered inside a Modal overlay.
	createModal   Modal
	createConfirm string // brief "#N created" message shown after successful create

	// Agent list for the agent-selection overlay.
	agents []agent.Agent

	// confirmModal wraps ConfirmCloseModel for close confirmation.
	confirmModal Modal
	// agentModal wraps AgentSelectModel for agent selection.
	agentModal Modal
	// promptModal wraps PromptSelectModel for prompt selection.
	promptModal Modal

	result   TicketsResult
	quitting bool

	// Sort mode for the issue list.
	sortBy sortMode

	// Split-pane preview state
	previewDetail    *tkDetailFetchedMsg
	detailCache      map[int]*tkDetailFetchedMsg
	detailCacheOrder []int // tracks insertion order for LRU eviction (max 20)

	// splitPane manages list/detail pane geometry and focus.
	splitPane SplitPane

	// Keep the dir for fetching
	dir string

	// Linked PRs: map of issue number → open PR whose branch references that issue.
	linkedPRs map[int]ghPR

	// Autopilot state
	autoPoll         bool          // mirrors config.AutoPoll
	autoPollInterval time.Duration // effective interval
	autoPollTicker   bool          // true when tick loop is active
	lastPollTime     time.Time     // last successful poll
	lastPollResult   string        // status message from most recent cycle
	dispatching      bool          // true while a dispatch is in flight

}

// CreateOpts contains optional pre-filled values for the ticket create form.
// Non-zero fields are pre-populated when the TUI opens directly in create mode.
// If all three fields are non-empty, the body field receives initial focus so
// the user can review/edit the most likely thing they want to change.
type CreateOpts struct {
	Title  string
	Body   string
	Labels []string
}

// NewTicketsModelWithOpts creates the full-screen ticket viewer for the given
// directory and applies opts to pre-populate the create form.  When any field
// in opts is non-empty the model starts directly in create mode.
func NewTicketsModelWithOpts(dir string, agents []agent.Agent, opts CreateOpts) TicketsModel {
	m := NewTicketsModel(dir, agents)
	m = m.applyCreateOpts(opts)
	return m
}

// applyCreateOpts pre-fills the create form and (if any field is non-empty)
// opens it as a Modal overlay.  Label defaults are replaced when opts.Labels is
// non-empty; otherwise the defaults set by NewCreateTicketModel are preserved.
func (m TicketsModel) applyCreateOpts(opts CreateOpts) TicketsModel {
	if opts.Title == "" && opts.Body == "" && len(opts.Labels) == 0 {
		return m
	}

	content := NewCreateTicketModel(m.repoSlug, m.labelOptions)
	content.createInput.SetValue(opts.Title)
	content.bodyInput.SetValue(opts.Body)

	if len(opts.Labels) > 0 {
		content.selectedLabels = make(map[string]bool, len(opts.Labels))
		for _, l := range opts.Labels {
			content.selectedLabels[l] = true
		}
	}

	// Focus the body when all three fields are pre-filled so the user can
	// immediately review/edit the body without tabbing past title/labels.
	allPreFilled := opts.Title != "" && opts.Body != "" && len(opts.Labels) > 0
	if allPreFilled {
		content.createFocus = focusBody
		content.createInput.Blur()
		// bodyInput.Focus() sets the internal focused state; the blink cmd
		// will be fired via Init() once the program starts.
		_ = content.bodyInput.Focus()
	}

	m.createModal = NewModal("Create Ticket", content, ModalMedium)
	return m
}

// NewTicketsModel creates the full-screen ticket viewer for the given directory.
func NewTicketsModel(dir string, agents []agent.Agent) TicketsModel {
	slug := TLRepoSlug(dir)

	si := textinput.New()
	si.Placeholder = "filter issues..."
	siS := si.Styles()
	siS.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	si.SetStyles(siS)

	// Load config to initialise autopilot state.
	cfg := config.Load()

	m := TicketsModel{
		termWidth:        120,
		termHeight:       40,
		loading:          true,
		repoSlug:         slug,
		dir:              dir,
		result:           TicketsResult{Cancelled: true},
		searchInput:      si,
		labelIdx:         -1,
		agents:           agents,
		autoPoll:         cfg.AutoPoll,
		autoPollInterval: cfg.EffectiveAutoPollInterval(),
		detailCache:      make(map[int]*tkDetailFetchedMsg),
		splitPane: SplitPane{
			ListRatio:     0.30,
			MinListRows:   3,
			MinDetailRows: 4,
		},
	}
	return m
}

// contentWidth returns the usable content width based on terminal size.
func (m TicketsModel) contentWidth() int {
	w := m.termWidth - 4 // 2 padding each side
	if w < 40 {
		w = 40
	}
	return w
}

// cachePreview stores a fetched detail in the LRU cache (max 20 entries).
func (m *TicketsModel) cachePreview(d *tkDetailFetchedMsg) {
	const maxCache = 20
	if _, exists := m.detailCache[d.number]; !exists {
		if len(m.detailCacheOrder) >= maxCache {
			oldest := m.detailCacheOrder[0]
			m.detailCacheOrder = m.detailCacheOrder[1:]
			delete(m.detailCache, oldest)
		}
		m.detailCacheOrder = append(m.detailCacheOrder, d.number)
	}
	m.detailCache[d.number] = d
}

// detailVisibleRows returns how many content lines fit in the detail view.
func (m TicketsModel) detailVisibleRows() int {
	// title (1) + blank (1) + hint bar (1) + scroll indicator (1) = 4 chrome lines
	rows := m.termHeight - 4
	if rows < 3 {
		rows = 3
	}
	return rows
}

// Result returns the popup output after it quits.
func (m TicketsModel) Result() TicketsResult { return m.result }

func (m TicketsModel) Init() tea.Cmd {
	cmds := []tea.Cmd{tkFetchTicketsCmd(m.dir)}
	if m.autoPoll {
		cmds = append(cmds, autoPollTickCmd(m.autoPollInterval))
	}
	// When the model is created with pre-filled opts (via NewTicketsModelWithOpts)
	// the createModal may already be visible.  Fire the content's Init() so the
	// focused input animates correctly from the start.
	if m.createModal.Visible {
		if ctm, ok := m.createModal.Content.(CreateTicketModel); ok {
			cmds = append(cmds, ctm.Init())
		}
	}
	return tea.Batch(cmds...)
}

func (m TicketsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		// Reserve chrome lines: 7 header + 2 hint bar = 9.
		paneH := m.termHeight - 9
		if paneH < 10 {
			paneH = 10
		}
		m.splitPane.SetSize(msg.Width, paneH)
		return m, nil

	case ticketsFetchedMsg:
		m.loading = false
		if msg.err != nil || msg.repoSlug == "" {
			m.err = msg.err
			m.issues = nil
			m.allIssues = nil
		} else {
			m.repoSlug = msg.repoSlug
			m.allIssues = msg.issues
			m.labelOptions = extractLabels(msg.issues)
			m.labelIdx = -1
			m.filterLabel = ""
			m.searchQuery = ""
			m.applyFilters()
			// Kick off a preview fetch for the initially highlighted issue.
			if m.splitPane.DetailHeight() >= 4 && len(m.issues) > 0 && m.repoSlug != "" {
				first := m.issues[0]
				if _, ok := m.detailCache[first.Number]; !ok {
					return m, tkFetchPreviewCmd(m.repoSlug, first.Number)
				}
			}
		}
		if msg.linkedPRs != nil {
			m.linkedPRs = msg.linkedPRs
		} else {
			m.linkedPRs = make(map[int]ghPR)
		}
		return m, nil

	case tkIssueClosedMsg:
		if msg.err == nil {
			n := msg.number
			var newAll []ghIssue
			for _, iss := range m.allIssues {
				if iss.Number != n {
					newAll = append(newAll, iss)
				}
			}
			m.allIssues = newAll
			m.applyFilters()
			if m.cursor >= len(m.issues) {
				m.cursor = len(m.issues) - 1
			}
			if m.cursor < 0 {
				m.cursor = 0
			}
			m.createConfirm = fmt.Sprintf("closed #%d", n)
			// Return to list view if we were in detail view.
			if m.viewMode == 1 {
				m.viewMode = 0
				m.detail = nil
				m.scrollOffset = 0
			}
		}
		return m, nil

	case tkSelfAssignedMsg:
		if msg.err == nil && m.detail != nil && m.detail.number == msg.number {
			// Re-fetch the detail to get the updated assignees.
			return m, tkFetchDetailCmd(m.repoSlug, msg.number)
		}
		return m, nil

	case tkDetailFetchedMsg:
		if msg.err == nil {
			m.detail = &msg
			m.scrollOffset = 0
			m.viewMode = 1
			// Also populate the preview cache with this data.
			m.cachePreview(&msg)
			if m.previewDetail == nil || m.previewDetail.number == msg.number {
				m.previewDetail = &msg
			}
		}
		return m, nil

	case tkPreviewFetchedMsg:
		if msg.err == nil {
			d := tkDetailFetchedMsg(msg)
			m.cachePreview(&d)
			// Only update the visible preview if it's still the current cursor item.
			if len(m.issues) > 0 && m.cursor < len(m.issues) && m.issues[m.cursor].Number == msg.number {
				m.previewDetail = &d
			}
		}
		return m, nil

	case autoPollTickMsg:
		if !m.autoPoll || m.dispatching {
			// Reschedule but skip this cycle.
			return m, autoPollTickCmd(m.autoPollInterval)
		}
		m.dispatching = true
		return m, tea.Batch(
			autoPollCmd(m.repoSlug),
			autoPollTickCmd(m.autoPollInterval),
		)

	case autoPollResultMsg:
		m.dispatching = false
		m.lastPollTime = time.Now()
		if msg.err != nil {
			m.lastPollResult = fmt.Sprintf("poll error: %v", msg.err)
		} else if len(msg.dispatched) == 0 {
			m.lastPollResult = "no ready tickets"
		} else {
			var parts []string
			for _, d := range msg.dispatched {
				parts = append(parts, fmt.Sprintf("#%d → %s", d.issueNumber, d.agentName))
			}
			m.lastPollResult = strings.Join(parts, ", ")
		}
		// Refresh ticket list since labels may have changed.
		m.loading = true
		return m, tkFetchTicketsCmd(m.dir)

	case tea.KeyPressMsg:
		// ctrl+c / q always quit the application when any overlay is active.
		if (m.confirmModal.Visible || m.agentModal.Visible || m.promptModal.Visible) &&
			(msg.String() == "ctrl+c" || msg.String() == "q") {
			m.result = TicketsResult{Cancelled: true}
			m.quitting = true
			return m, tea.Quit
		}
		if m.confirmModal.Visible {
			var cmd tea.Cmd
			m.confirmModal, cmd = m.confirmModal.Update(msg)
			return m.checkConfirmModalDone(cmd)
		}
		if m.promptModal.Visible {
			var cmd tea.Cmd
			m.promptModal, cmd = m.promptModal.Update(msg)
			return m.checkPromptModalDone(cmd)
		}
		if m.agentModal.Visible {
			var cmd tea.Cmd
			m.agentModal, cmd = m.agentModal.Update(msg)
			return m.checkAgentModalDone(cmd)
		}
		if m.createModal.Visible {
			var cmd tea.Cmd
			m.createModal, cmd = m.createModal.Update(msg)
			return m.checkCreateModalDone(cmd)
		}
		if m.viewMode == 1 {
			return m.updateDetail(msg)
		}
		if m.searchMode {
			return m.updateSearch(msg)
		}
		return m.updateList(msg)

	default:
		// Forward non-key and background messages (e.g. cursor blink, issueCreatedMsg)
		// to the active modal.
		if m.createModal.Visible {
			var cmd tea.Cmd
			m.createModal, cmd = m.createModal.Update(msg)
			return m.checkCreateModalDone(cmd)
		}
		if m.promptModal.Visible {
			var cmd tea.Cmd
			m.promptModal, cmd = m.promptModal.Update(msg)
			return m.checkPromptModalDone(cmd)
		}
	}
	return m, nil
}

// applyFilters rebuilds m.issues from m.allIssues applying label, search, and sort.
func (m *TicketsModel) applyFilters() {
	filtered := filterByLabel(m.allIssues, m.filterLabel)
	if m.searchQuery != "" {
		q := strings.ToLower(m.searchQuery)
		var searched []ghIssue
		for _, iss := range filtered {
			if strings.Contains(strings.ToLower(iss.Title), q) {
				searched = append(searched, iss)
			}
		}
		filtered = searched
	}
	switch m.sortBy {
	case sortNewest:
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
		})
	case sortOldest:
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].CreatedAt.Before(filtered[j].CreatedAt)
		})
	case sortNumber:
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].Number < filtered[j].Number
		})
	}
	m.issues = filtered
	m.cursor = 0
	m.splitPane.SetCursor(0)
}

func (m TicketsModel) updateList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.result = TicketsResult{Cancelled: true}
		m.quitting = true
		return m, tea.Quit

	case "esc":
		if m.searchQuery != "" {
			// Clear the search filter rather than quitting
			m.searchQuery = ""
			m.searchInput.SetValue("")
			m.applyFilters()
			return m, nil
		}
		m.result = TicketsResult{Cancelled: true}
		m.quitting = true
		return m, tea.Quit

	case "up", "k":
		m.createConfirm = ""
		if m.cursor > 0 {
			m.cursor--
			m.splitPane.SetCursor(m.cursor)
		}
		if m.splitPane.DetailHeight() >= 4 && m.cursor < len(m.issues) {
			return m, m.previewFetchCmd(m.issues[m.cursor].Number)
		}

	case "down", "j":
		m.createConfirm = ""
		if m.cursor < len(m.issues)-1 {
			m.cursor++
			m.splitPane.SetCursor(m.cursor)
		}
		if m.splitPane.DetailHeight() >= 4 && m.cursor < len(m.issues) {
			return m, m.previewFetchCmd(m.issues[m.cursor].Number)
		}

	case "tab":
		m.splitPane.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		return m, nil

	case "enter":
		if len(m.issues) == 0 {
			break
		}
		issue := m.issues[m.cursor]
		if m.repoSlug == "" {
			break
		}
		return m, tkFetchDetailCmd(m.repoSlug, issue.Number)

	case "c":
		content := NewCreateTicketModel(m.repoSlug, m.labelOptions)
		m.createModal = NewModal("Create Ticket", content, ModalMedium)
		return m, content.Init()

	case "l":
		// Cycle label filter
		if len(m.labelOptions) == 0 {
			break
		}
		m.labelIdx++
		if m.labelIdx >= len(m.labelOptions) {
			m.labelIdx = -1
			m.filterLabel = ""
		} else {
			m.filterLabel = m.labelOptions[m.labelIdx]
		}
		m.applyFilters()

	case "/":
		m.searchMode = true
		m.searchInput.SetValue(m.searchQuery)
		m.searchInput.Focus()
		return m, textinput.Blink

	case "o":
		if len(m.issues) == 0 || m.repoSlug == "" {
			break
		}
		issue := m.issues[m.cursor]
		return m, openIssueInBrowserCmd(m.repoSlug, issue.Number)

	case "t":
		if len(m.issues) == 0 {
			break
		}
		issue := m.issues[m.cursor]
		m.result = TicketsResult{
			LaunchAgent:  true,
			TicketNumber: issue.Number,
			TicketTitle:  issue.Title,
			AgentName:    "",
			PromptMode:   "triage",
		}
		m.quitting = true
		return m, tea.Quit

	case "T":
		if len(m.issues) == 0 {
			break
		}
		issue := m.issues[m.cursor]
		if len(m.agents) > 0 {
			asm := newAgentSelectModel(m.agents, issue.Number, issue.Title, "")
			m.agentModal = NewModal(
				fmt.Sprintf("Select Agent  #%d", issue.Number),
				asm, ModalMedium,
			)
			return m, nil
		}
		m.result = TicketsResult{
			LaunchAgent:  true,
			TicketNumber: issue.Number,
			TicketTitle:  issue.Title,
		}
		m.quitting = true
		return m, tea.Quit

	case "d":
		// Show close-confirmation overlay before closing the highlighted issue.
		if len(m.issues) == 0 || m.repoSlug == "" {
			break
		}
		num := m.issues[m.cursor].Number
		m.confirmModal = NewModal("Close Ticket", newConfirmCloseModel(num), ModalSmall)
		return m, nil

	case "s":
		// Cycle sort modes: newest → oldest → number → newest
		m.sortBy = (m.sortBy + 1) % 3
		m.applyFilters()
		return m, nil

	case "e":
		// Open the highlighted issue for editing in the browser.
		if len(m.issues) == 0 || m.repoSlug == "" {
			break
		}
		issue := m.issues[m.cursor]
		return m, tkEditIssueCmd(m.repoSlug, issue.Number)

	case "P":
		// Toggle autopilot on/off.
		cfg := config.Load()
		newVal, err := cfg.ToggleAutoPoll()
		if err == nil {
			m.autoPoll = newVal
			m.autoPollInterval = cfg.EffectiveAutoPollInterval()
		}
		if m.autoPoll && !m.autoPollTicker {
			m.autoPollTicker = true
			return m, autoPollTickCmd(m.autoPollInterval)
		}
		return m, nil
	}
	return m, nil
}

func (m TicketsModel) updateSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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

func (m TicketsModel) updateDetail(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.result = TicketsResult{Cancelled: true}
		m.quitting = true
		return m, tea.Quit

	case "esc":
		m.viewMode = 0
		m.detail = nil
		m.scrollOffset = 0

	case "up", "k":
		if m.scrollOffset > 0 {
			m.scrollOffset--
		}

	case "down", "j":
		m.scrollOffset++

	case "o":
		if m.detail != nil && m.repoSlug != "" {
			return m, openIssueInBrowserCmd(m.repoSlug, m.detail.number)
		}

	case "t":
		if m.detail != nil {
			m.result = TicketsResult{
				LaunchAgent:  true,
				TicketNumber: m.detail.number,
				TicketTitle:  m.detail.title,
				TicketBody:   m.detail.body,
				AgentName:    "",
				PromptMode:   "triage",
			}
			m.quitting = true
			return m, tea.Quit
		}

	case "T":
		if m.detail != nil {
			if len(m.agents) > 0 {
				asm := newAgentSelectModel(m.agents, m.detail.number, m.detail.title, m.detail.body)
				m.agentModal = NewModal(
					fmt.Sprintf("Select Agent  #%d", m.detail.number),
					asm, ModalMedium,
				)
				return m, nil
			}
			m.result = TicketsResult{
				LaunchAgent:  true,
				TicketNumber: m.detail.number,
				TicketTitle:  m.detail.title,
				TicketBody:   m.detail.body,
			}
			m.quitting = true
			return m, tea.Quit
		}

	case "d":
		// Show close-confirmation overlay before closing the current issue.
		if m.detail != nil && m.repoSlug != "" {
			m.confirmModal = NewModal("Close Ticket", newConfirmCloseModel(m.detail.number), ModalSmall)
			return m, nil
		}

	case "a":
		// Self-assign the current issue.
		if m.detail != nil && m.repoSlug != "" {
			return m, tkAssignSelfCmd(m.repoSlug, m.detail.number)
		}

	case "e":
		// Open the current issue for editing in the browser.
		if m.detail != nil && m.repoSlug != "" {
			return m, tkEditIssueCmd(m.repoSlug, m.detail.number)
		}

	case "p":
		// Open the linked PR in browser.
		if m.detail != nil {
			if pr, ok := m.linkedPRs[m.detail.number]; ok {
				return m, tkOpenPRInBrowserCmd(pr.URL)
			}
		}
	}
	return m, nil
}

// checkCreateModalDone inspects the create modal's content after an update.
// If the content model has a result (submit or cancel), the modal is closed
// and — on successful creation — the ticket list is refreshed.
func (m TicketsModel) checkCreateModalDone(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	ctm, ok := m.createModal.Content.(CreateTicketModel)
	if !ok || ctm.result == nil {
		return m, cmd
	}
	m.createModal.Visible = false
	if ctm.result.Cancelled {
		return m, cmd
	}
	if ctm.result.Err != nil {
		return m, cmd
	}
	// Successful creation — show confirmation and refresh the list.
	if ctm.result.Number > 0 {
		m.createConfirm = fmt.Sprintf("created #%d", ctm.result.Number)
	} else {
		m.createConfirm = "issue created"
	}
	m.loading = true
	return m, tea.Batch(cmd, tkFetchTicketsCmd(m.dir))
}

// checkConfirmModalDone inspects the confirmModal content after an update.
// If the user confirmed, it fires the close command; if cancelled, it just
// hides the modal.
func (m TicketsModel) checkConfirmModalDone(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	ccm, ok := m.confirmModal.Content.(ConfirmCloseModel)
	if !ok || !ccm.Done() {
		return m, cmd
	}
	m.confirmModal.Visible = false
	if ccm.Confirmed() {
		return m, tea.Batch(cmd, tkCloseIssueCmd(m.repoSlug, ccm.issueNumber))
	}
	return m, cmd
}

// checkAgentModalDone inspects the agentModal content after an update.
// If an agent was selected it opens the promptModal; if cancelled it just
// hides the modal.
func (m TicketsModel) checkAgentModalDone(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	asm, ok := m.agentModal.Content.(AgentSelectModel)
	if !ok || !asm.Done() {
		return m, cmd
	}
	m.agentModal.Visible = false
	if asm.Cancelled() {
		return m, cmd
	}
	selectedAgent, _ := asm.Selected()
	psm := newPromptSelectModel(
		asm.pendingTicketNum,
		asm.pendingTicketTitle,
		asm.pendingTicketBody,
		selectedAgent.Name,
	)
	m.promptModal = NewModal(
		fmt.Sprintf("Prompt Mode  #%d", asm.pendingTicketNum),
		psm, ModalMedium,
	)
	return m, cmd
}

// checkPromptModalDone inspects the promptModal content after an update.
// If a mode was selected it quits with the result; if going back it re-shows
// the agentModal.
func (m TicketsModel) checkPromptModalDone(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	psm, ok := m.promptModal.Content.(PromptSelectModel)
	if !ok || !psm.Done() {
		return m, cmd
	}
	m.promptModal.Visible = false
	if psm.GoBack() {
		m.agentModal.Visible = true
		return m, cmd
	}
	mode, custom := psm.Selected()
	m.result = TicketsResult{
		LaunchAgent:  true,
		TicketNumber: psm.pendingTicketNum,
		TicketTitle:  psm.pendingTicketTitle,
		TicketBody:   psm.pendingTicketBody,
		AgentName:    psm.pendingAgentName,
		PromptMode:   mode,
		CustomPrompt: custom,
	}
	m.quitting = true
	return m, tea.Quit
}

func (m TicketsModel) View() tea.View {
	var s string
	switch {
	case m.confirmModal.Visible:
		// Render the confirmation overlay over whichever view is currently active.
		var bg string
		if m.viewMode == 1 {
			bg = m.viewDetail()
		} else {
			bg = m.viewList()
		}
		s = m.confirmModal.Overlay(bg, m.termWidth, m.termHeight)
	case m.promptModal.Visible:
		s = m.promptModal.Overlay(m.viewList(), m.termWidth, m.termHeight)
	case m.agentModal.Visible:
		s = m.agentModal.Overlay(m.viewList(), m.termWidth, m.termHeight)
	case m.createModal.Visible:
		// Render the create form as a Modal overlay over the current background view.
		var bg string
		if m.viewMode == 1 {
			bg = m.viewDetail()
		} else {
			bg = m.viewList()
		}
		s = m.createModal.Overlay(bg, m.termWidth, m.termHeight)
	case m.viewMode == 1:
		s = m.viewDetail()
	default:
		s = m.viewList()
	}
	v := tea.NewView(s)
	v.AltScreen = true
	return v
}

// autopilotOnStyle is a bright green dot for active autopilot.
var autopilotOnStyle lipgloss.Style

// initTicketStyles initialises tickets-browser styles from the active theme.
// Shared styles (ColHeader, Rule, Dim, etc.) are sourced from tableStyles,
// which must be initialised first by initTableStyles().
func initTicketStyles() {
	t := theme.Current()

	tkStyles = NewStyles(90)

	// ── Shared table styles ───────────────────────────────────────────────────
	tkColHeaderStyle = tableStyles.ColHeader
	tkRuleStyle = tableStyles.Rule
	tkDimStyle = tableStyles.Dim
	tkDetailLabelStyle = tableStyles.DetailLabel.Width(16)
	tkDetailValueStyle = tableStyles.DetailValue
	tkDetailSectionStyle = tableStyles.DetailSection
	tkSubtitleStyle = tableStyles.Subtitle

	// ── Ticket-specific styles ────────────────────────────────────────────────
	tkNumStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Width(8)

	tkNumSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true).
		Width(8)

	tkTitleColStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))

	tkTitleSelectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)

	tkAgeStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Width(8)

	tkCodeBlockStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(lipgloss.Color(t.Surface)).
		PaddingLeft(1)

	autopilotOnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green)).Bold(true)
}

// labelColor maps known label names to theme colors for visual differentiation.
func labelColor(name string) string {
	t := theme.Current()
	switch name {
	case "bug":
		return t.Red
	case "enhancement":
		return t.Blue
	case "quick-fix":
		return t.Green
	case "refactor":
		return t.Muted
	case "triage":
		return t.Yellow
	default:
		return t.Subtle
	}
}

func (m TicketsModel) viewList() string {
	var b strings.Builder
	contentWidth := m.contentWidth()

	// Title banner
	titleText := "Tickets"
	if m.repoSlug != "" {
		titleText = fmt.Sprintf("Tickets  %s", m.repoSlug)
	}
	b.WriteString(tkStyles.Title.Width(contentWidth).Render(titleText))
	b.WriteString("\n")

	// Autopilot status line — always occupies exactly one line so that total
	// chrome height is stable regardless of whether autopilot is enabled.
	// Leaving this conditional caused viewList() to produce a variable number
	// of lines, which made Bubble Tea leave stale content in the preview pane
	// when the rendered frame shrank by one line.
	if m.autoPoll {
		intervalStr := m.autoPollInterval.String()
		dot := autopilotOnStyle.Render("●")
		b.WriteString(autopilotOnStyle.Render("  Autopilot ") + dot + " " + tkDimStyle.Render(intervalStr))
		if m.lastPollResult != "" {
			b.WriteString("  " + tkDimStyle.Render(m.lastPollResult))
		}
	}
	b.WriteString("\n")

	// Subtitle / filter indicator
	b.WriteString("\n")
	if m.createConfirm != "" && !m.loading {
		checkmark := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Green)).Bold(true).Render("✓")
		b.WriteString("  " + checkmark + " " + tkDimStyle.Render(m.createConfirm))
	} else if m.loading {
		b.WriteString(tkSubtitleStyle.Render("  Loading..."))
	} else if m.err != nil {
		b.WriteString(tkSubtitleStyle.Render("  No git remote found in this directory"))
	} else {
		sortLabel := [3]string{"newest", "oldest", "number"}[m.sortBy]
		var subtitle string
		if m.filterLabel != "" && m.searchQuery != "" {
			subtitle = fmt.Sprintf("  label:%s  ·  filtering: %q  (%d)  ·  sort:%s", m.filterLabel, m.searchQuery, len(m.issues), sortLabel)
		} else if m.filterLabel != "" {
			subtitle = fmt.Sprintf("  label:%s  (%d)  ·  sort:%s", m.filterLabel, len(m.issues), sortLabel)
		} else if m.searchQuery != "" {
			subtitle = fmt.Sprintf("  filtering: %q  (%d of %d issues)  ·  sort:%s", m.searchQuery, len(m.issues), len(m.allIssues), sortLabel)
		} else {
			subtitle = fmt.Sprintf("  open (%d)  ·  sort:%s", len(m.issues), sortLabel)
		}
		b.WriteString(tkSubtitleStyle.Render(subtitle))
	}
	b.WriteString("\n\n")

	if !m.loading && m.err == nil {
		// Column headers — title column fills remaining space.
		titleColWidth := contentWidth - tkColNum - tkColPR - tkColLabels - tkColAssignee - tkColAge - 3
		if titleColWidth < 10 {
			titleColWidth = 10
		}

		b.WriteString(fmt.Sprintf("  %s%s%s%s%s%s",
			tkColHeaderStyle.Render(fmt.Sprintf("%-*s", tkColNum, "#")),
			tkColHeaderStyle.Render(fmt.Sprintf("%-*s", tkColPR, "PR")),
			tkColHeaderStyle.Render(fmt.Sprintf("%-*s", titleColWidth, "TITLE")),
			tkColHeaderStyle.Render(fmt.Sprintf("%-*s", tkColLabels, "LABELS")),
			tkColHeaderStyle.Render(fmt.Sprintf("%-*s", tkColAssignee, "ASSIGNEE")),
			tkColHeaderStyle.Render(fmt.Sprintf("%-*s", tkColAge, "AGE")),
		))
		b.WriteString("\n")
		b.WriteString("  " + tkRuleStyle.Render(strings.Repeat("╌", contentWidth-2)))
		b.WriteString("\n")

		// SplitPane: list rows + separator + detail preview.
		sp := m.splitPane
		sp.SetListContent(m.buildListContent(), len(m.issues))
		// SetListContent establishes totalRows; now sync the cursor so that
		// scroll clamping uses the actual cursor position.
		sp.SetCursor(m.cursor)
		sp.SetDetailContent(m.buildPreviewContent())
		b.WriteString(sp.View())
	} else if !m.loading {
		// Error state already shown in subtitle
		for i := 0; i < 3; i++ {
			b.WriteString("\n")
		}
	}

	// Pin hint bar to terminal bottom: pad content to termHeight-1, then render hint bar.
	var hintBar string
	if m.searchMode {
		dot := "  " + menuHintDotStyle.Render("·") + "  "
		searchLine := menuHintKeyStyle.Render("/") + " " + m.searchInput.View()
		hintBar = searchLine + dot +
			menuHintKeyStyle.Render("enter") + " " + menuHintLabelStyle.Render("confirm") + dot +
			menuHintKeyStyle.Render("esc") + " " + menuHintLabelStyle.Render("clear")
	} else {
		type hint struct{ key, label string }
		hints := []hint{
			{"j/k", "navigate"},
			{"enter", "detail"},
			{"e", "edit"},
			{"d", "close"},
			{"c", "create"},
			{"l", "filter"},
			{"s", "sort"},
			{"o", "open"},
			{"t", "triage"},
			{"T", "launch"},
			{"P", "autopilot"},
		}
		if m.searchQuery != "" {
			hints = append(hints, hint{"/", "edit filter"}, hint{"esc", "clear"})
		} else {
			hints = append(hints, hint{"/", "search"}, hint{"esc/q", "quit"})
		}
		sep := "  " + menuHintDotStyle.Render("·") + "  "
		var hintParts []string
		for _, h := range hints {
			hintParts = append(hintParts, menuHintKeyStyle.Render(h.key)+" "+menuHintLabelStyle.Render(h.label))
		}
		hintBarCentered := lipgloss.NewStyle().Width(m.contentWidth()).Align(lipgloss.Center)
		hintBar = hintBarCentered.Render(strings.Join(hintParts, sep))
	}

	out := b.String()

	// Pad content so the hint bar lands on the last terminal line (termHeight-1).
	if m.termHeight > 0 {
		lineCount := strings.Count(out, "\n")
		targetLines := m.termHeight - 1
		if lineCount < targetLines {
			out += strings.Repeat("\n", targetLines-lineCount)
		}
	}
	out += hintBar

	// Safety net: ensure the output is exactly termHeight lines so Bubble Tea
	// always overwrites the full frame.  Without this, any short-by-N render
	// leaves N stale lines from the previous frame visible on screen.
	if m.termHeight > 0 {
		lineCount := strings.Count(out, "\n")
		if lineCount < m.termHeight {
			out += strings.Repeat("\n", m.termHeight-lineCount)
		}
	}

	return out
}

// renderBodyLines renders a markdown body string into display lines, applying
// per-line styling and visually differentiating fenced code blocks (``` ... ```)
// from prose. Lines inside a code block are styled with tkCodeBlockStyle; prose
// lines use tkDetailValueStyle. Long lines are hard-wrapped at maxWidth.
func renderBodyLines(body string, maxWidth int) []string {
	var out []string
	inCodeBlock := false

	appendLine := func(text string, inBlock bool) {
		if inBlock {
			out = append(out, tkCodeBlockStyle.Render(text))
		} else {
			out = append(out, "  "+tkDetailValueStyle.Render(text))
		}
	}

	wrapAndAppend := func(rawLine string, inBlock bool) {
		if len(rawLine) <= maxWidth {
			appendLine(rawLine, inBlock)
			return
		}
		for len(rawLine) > maxWidth {
			appendLine(rawLine[:maxWidth], inBlock)
			rawLine = rawLine[maxWidth:]
		}
		if rawLine != "" {
			appendLine(rawLine, inBlock)
		}
	}

	for _, rawLine := range strings.Split(body, "\n") {
		if strings.HasPrefix(rawLine, "```") {
			inCodeBlock = !inCodeBlock
			// Render the fence marker itself in code-block style so there's
			// no gap in the left border gutter.
			appendLine(rawLine, true)
			continue
		}
		wrapAndAppend(rawLine, inCodeBlock)
	}
	return out
}

func (m TicketsModel) viewDetail() string {
	if m.detail == nil {
		return m.viewList()
	}

	d := m.detail
	contentWidth := m.contentWidth()

	// Column widths: left 60% (body), right 40% (metadata+actions).
	leftWidth := contentWidth * 60 / 100
	rightWidth := contentWidth - leftWidth - 2 // -2 for separator gap
	if leftWidth < 20 {
		leftWidth = 20
	}
	if rightWidth < 15 {
		rightWidth = 15
	}

	// ── Right column: metadata + actions (static) ────────────────────────────
	var rightLines []string
	rightLines = append(rightLines, tkDetailSectionStyle.Render("── Details ──"))
	rightLines = append(rightLines, "")
	rightLines = append(rightLines, tkDetailLabelStyle.Render("Title:"))
	rightLines = append(rightLines, tkDetailValueStyle.Render(d.title))
	rightLines = append(rightLines, "")
	rightLines = append(rightLines, tkDetailLabelStyle.Render("Number:")+tkDetailValueStyle.Render(fmt.Sprintf("#%d", d.number)))
	rightLines = append(rightLines, tkDimStyle.Render(d.url))

	// Labels
	if len(d.labels) > 0 {
		rightLines = append(rightLines, "")
		rightLines = append(rightLines, tkDetailLabelStyle.Render("Labels:"))
		for _, l := range d.labels {
			rightLines = append(rightLines, "  "+lipgloss.NewStyle().Foreground(lipgloss.Color(labelColor(l.Name))).Render(l.Name))
		}
	}

	// Assignees
	rightLines = append(rightLines, "")
	if len(d.assignees) > 0 {
		var logins []string
		for _, u := range d.assignees {
			logins = append(logins, u.Login)
		}
		rightLines = append(rightLines, tkDetailLabelStyle.Render("Assignees:"))
		rightLines = append(rightLines, tkDetailValueStyle.Render(strings.Join(logins, ", ")))
	} else {
		rightLines = append(rightLines, tkDimStyle.Render("(unassigned)"))
		rightLines = append(rightLines, tkDimStyle.Render("  press a to assign"))
	}

	// Linked PR
	if pr, ok := m.linkedPRs[d.number]; ok {
		rightLines = append(rightLines, "")
		rightLines = append(rightLines, tkDetailSectionStyle.Render("── Linked PR ──"))
		rightLines = append(rightLines, "")
		prNumStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Green)).Bold(true)
		rightLines = append(rightLines, tkDetailLabelStyle.Render("PR:")+prNumStyle.Render(fmt.Sprintf("#%d", pr.Number)))
		rightLines = append(rightLines, tkDetailValueStyle.Render(pr.Title))
		rightLines = append(rightLines, tkDimStyle.Render(pr.HeadRefName))
	}

	// ── Left column: body (scrollable) ────────────────────────────────────────
	var bodyLines []string
	if d.body != "" {
		maxBodyWidth := leftWidth - 2
		if maxBodyWidth < 20 {
			maxBodyWidth = 20
		}
		rendered := glamourRender(d.body, maxBodyWidth)
		bodyLines = append(bodyLines, strings.Split(strings.TrimRight(rendered, "\n"), "\n")...)
	} else {
		bodyLines = append(bodyLines, tkDimStyle.Render("  (no body)"))
	}

	// Apply scroll to the body lines.
	detailVisible := m.detailVisibleRows()
	totalLines := len(bodyLines)
	maxScroll := totalLines - detailVisible
	if maxScroll < 0 {
		maxScroll = 0
	}
	scrollOffset := m.scrollOffset
	if scrollOffset > maxScroll {
		scrollOffset = maxScroll
	}
	end := scrollOffset + detailVisible
	if end > totalLines {
		end = totalLines
	}
	visibleBody := bodyLines[scrollOffset:end]
	for len(visibleBody) < detailVisible {
		visibleBody = append(visibleBody, "")
	}

	// Pad right column to detailVisible rows.
	for len(rightLines) < detailVisible {
		rightLines = append(rightLines, "")
	}
	if len(rightLines) > detailVisible {
		rightLines = rightLines[:detailVisible]
	}

	leftCol := lipgloss.NewStyle().Width(leftWidth).Render(strings.Join(visibleBody, "\n"))
	rightCol := lipgloss.NewStyle().Width(rightWidth).Render(strings.Join(rightLines, "\n"))
	twoCol := lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "  ", rightCol)

	var b strings.Builder
	b.WriteString(tkStyles.Title.Width(contentWidth).Render(fmt.Sprintf("Issue #%d  %s", d.number, d.title)))
	b.WriteString("\n")

	b.WriteString(twoCol)
	b.WriteString("\n")

	// Scroll indicator
	if totalLines > detailVisible {
		scrollPct := 0
		if maxScroll > 0 {
			scrollPct = scrollOffset * 100 / maxScroll
		}
		b.WriteString(tkDimStyle.Render(fmt.Sprintf("  ── %d%% ──", scrollPct)))
		b.WriteString("\n")
	}

	// Build hint bar for detail view.
	detailHints := []struct{ key, label string }{
		{"j/k", "scroll"},
		{"e", "edit"},
		{"d", "close"},
		{"a", "assign"},
		{"o", "open"},
		{"t", "triage"},
		{"T", "launch"},
		{"esc", "back"},
	}
	if _, ok := m.linkedPRs[d.number]; ok {
		detailHints = append(detailHints, struct{ key, label string }{"p", "open PR"})
	}
	sep := "  " + menuHintDotStyle.Render("·") + "  "
	var hintParts []string
	for _, h := range detailHints {
		hintParts = append(hintParts, menuHintKeyStyle.Render(h.key)+" "+menuHintLabelStyle.Render(h.label))
	}
	hintBarCentered := lipgloss.NewStyle().Width(contentWidth).Align(lipgloss.Center)
	hintBar := hintBarCentered.Render(strings.Join(hintParts, sep))

	out := b.String()

	// Pad content so the hint bar lands on the last terminal line (termHeight-1).
	if m.termHeight > 0 {
		lineCount := strings.Count(out, "\n")
		targetLines := m.termHeight - 1
		if lineCount < targetLines {
			out += strings.Repeat("\n", targetLines-lineCount)
		}
	}
	out += hintBar

	// Safety net: ensure the output is exactly termHeight lines.
	if m.termHeight > 0 {
		lineCount := strings.Count(out, "\n")
		if lineCount < m.termHeight {
			out += strings.Repeat("\n", m.termHeight-lineCount)
		}
	}

	return out
}

// previewFetchCmd returns a command to fetch a preview for the given issue, using the cache
// to avoid redundant API calls.
func (m TicketsModel) previewFetchCmd(number int) tea.Cmd {
	if cached, ok := m.detailCache[number]; ok {
		// Serve from cache immediately via a synthetic message.
		d := *cached
		return func() tea.Msg { return tkPreviewFetchedMsg(d) }
	}
	return tkFetchPreviewCmd(m.repoSlug, number)
}

// buildListContent returns all issue rows rendered as a single string (one row
// per line).  SplitPane handles viewport scrolling based on the cursor.
func (m TicketsModel) buildListContent() string {
	if len(m.issues) == 0 {
		return "   " + tkDimStyle.Render("No open issues found")
	}

	contentWidth := m.contentWidth()
	titleColWidth := contentWidth - tkColNum - tkColPR - tkColLabels - tkColAssignee - tkColAge - 3
	if titleColWidth < 10 {
		titleColWidth = 10
	}

	prBadgeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Green))
	assigneeBadgeStyle := tkDimStyle

	var rows []string
	for i, issue := range m.issues {
		selected := i == m.cursor

		cur := "   "
		if selected {
			cur = " " + cursorActiveStyle.Render("▸") + " "
		}

		numStr := fmt.Sprintf("#%-*d", tkColNum-2, issue.Number)
		if selected {
			numStr = tkNumSelectedStyle.Render(numStr)
		} else {
			numStr = tkNumStyle.Render(numStr)
		}

		// PR badge column
		var prBadge string
		if pr, ok := m.linkedPRs[issue.Number]; ok {
			raw := fmt.Sprintf("PR#%d", pr.Number)
			prBadge = prBadgeStyle.Width(tkColPR).Render(raw)
		} else {
			prBadge = lipgloss.NewStyle().Width(tkColPR).Render("")
		}

		// Labels column — each label gets its own color.
		// Pre-truncate to tkColLabels-1 visible chars to prevent overflow.
		const labelsMax = tkColLabels - 1
		var labelParts []string
		for _, l := range issue.Labels {
			labelParts = append(labelParts, lipgloss.NewStyle().Foreground(lipgloss.Color(labelColor(l.Name))).Render(l.Name))
		}
		labelsRaw := strings.Join(labelParts, ",")
		if len(ansi.Strip(labelsRaw)) > labelsMax {
			var kept []string
			for _, part := range labelParts {
				candidate := strings.Join(append(kept, part), ",")
				if len(ansi.Strip(candidate))+1 > labelsMax { // +1 for "…"
					break
				}
				kept = append(kept, part)
			}
			if len(kept) == 0 {
				plain := ansi.Strip(labelParts[0])
				if len(plain) > labelsMax-1 {
					plain = plain[:labelsMax-1]
				}
				labelsRaw = lipgloss.NewStyle().Foreground(lipgloss.Color(labelColor(issue.Labels[0].Name))).Render(plain) + "…"
			} else {
				labelsRaw = strings.Join(kept, ",") + "…"
			}
		}
		labelsCol := lipgloss.NewStyle().Width(tkColLabels).Render(labelsRaw)

		// Assignee badge column
		var assigneeCol string
		if len(issue.Assignees) > 0 {
			login := issue.Assignees[0].Login
			if len(login) > tkColAssignee-2 {
				login = login[:tkColAssignee-3] + "…"
			}
			assigneeCol = assigneeBadgeStyle.Width(tkColAssignee).Render("@" + login)
		} else {
			assigneeCol = lipgloss.NewStyle().Width(tkColAssignee).Render("")
		}

		// Age column
		age := fmtAge(time.Since(issue.CreatedAt))
		ageCol := tkAgeStyle.Render(fmt.Sprintf("%-*s", tkColAge, age))

		// Title column fills remaining space
		title := issue.Title
		if len(title) > titleColWidth-1 {
			title = title[:titleColWidth-2] + "…"
		}
		var titleCol string
		if selected {
			titleCol = tkTitleSelectedStyle.Width(titleColWidth).Render(fmt.Sprintf("%-*s", titleColWidth, title))
		} else {
			titleCol = tkTitleColStyle.Width(titleColWidth).Render(fmt.Sprintf("%-*s", titleColWidth, title))
		}

		rowContent := cur + numStr + prBadge + titleCol + labelsCol + assigneeCol + ageCol
		if selected {
			selectedRowStyle := lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(lipgloss.Color(theme.Current().Accent)).
				PaddingLeft(0)
			rowContent = selectedRowStyle.Render(rowContent)
		}
		rows = append(rows, rowContent)
	}
	return strings.Join(rows, "\n")
}

// buildPreviewContent returns the rendered detail preview content for the
// currently highlighted issue.  Uses a two-column layout: body on the left
// (60%), metadata/actions on the right (40%).  SplitPane handles clipping.
func (m TicketsModel) buildPreviewContent() string {
	maxH := m.splitPane.DetailHeight()
	if maxH < 1 {
		maxH = 1
	}
	contentWidth := m.contentWidth()

	if m.previewDetail == nil {
		// Nothing fetched yet — show a loading indicator.
		var loadingLines []string
		loadingLines = append(loadingLines, tkDimStyle.Render("  Loading preview..."))
		for len(loadingLines) < maxH {
			loadingLines = append(loadingLines, "")
		}
		return strings.Join(loadingLines, "\n")
	}

	d := m.previewDetail

	// Column widths: left 60%, right 40%.
	leftWidth := contentWidth * 60 / 100
	rightWidth := contentWidth - leftWidth - 2 // -2 for separator gap
	if leftWidth < 20 {
		leftWidth = 20
	}
	if rightWidth < 10 {
		rightWidth = 10
	}

	// ── Left column: title + body ─────────────────────────────────────────────
	var leftLines []string

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.Current().Text))
	titleLine := titleStyle.Render(fmt.Sprintf("#%d  %s", d.number, d.title))
	leftLines = append(leftLines, titleLine)

	// Labels badges
	if len(d.labels) > 0 {
		var badges []string
		for _, l := range d.labels {
			badges = append(badges, lipgloss.NewStyle().Foreground(lipgloss.Color(labelColor(l.Name))).Render("["+l.Name+"]"))
		}
		leftLines = append(leftLines, strings.Join(badges, " "))
	}

	leftLines = append(leftLines, "")

	maxBodyWidth := leftWidth - 2
	if maxBodyWidth < 20 {
		maxBodyWidth = 20
	}
	if d.body != "" {
		rendered := glamourRender(d.body, maxBodyWidth)
		leftLines = append(leftLines, strings.Split(strings.TrimRight(rendered, "\n"), "\n")...)
	} else {
		leftLines = append(leftLines, tkDimStyle.Render("(no body)"))
	}

	// ── Right column: actions panel ───────────────────────────────────────────
	var rightLines []string

	rightLines = append(rightLines, tkDetailSectionStyle.Render("Actions"))
	rightLines = append(rightLines, "")

	// Linked PR
	if pr, ok := m.linkedPRs[d.number]; ok {
		prNumStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Green)).Bold(true)
		rightLines = append(rightLines, tkDetailLabelStyle.Render("PR:")+prNumStyle.Render(fmt.Sprintf("#%d", pr.Number)))
		rightLines = append(rightLines, tkDimStyle.Render(pr.HeadRefName))
		rightLines = append(rightLines, "")
	}

	// Assignees
	if len(d.assignees) > 0 {
		var logins []string
		for _, u := range d.assignees {
			logins = append(logins, u.Login)
		}
		rightLines = append(rightLines, tkDetailLabelStyle.Render("Assignees:"))
		rightLines = append(rightLines, tkDetailValueStyle.Render(strings.Join(logins, ", ")))
		rightLines = append(rightLines, "")
	} else {
		rightLines = append(rightLines, tkDimStyle.Render("unassigned"))
		rightLines = append(rightLines, "")
	}

	// Labels
	if len(d.labels) > 0 {
		rightLines = append(rightLines, tkDetailLabelStyle.Render("Labels:"))
		for _, l := range d.labels {
			rightLines = append(rightLines, lipgloss.NewStyle().Foreground(lipgloss.Color(labelColor(l.Name))).Render("  "+l.Name))
		}
		rightLines = append(rightLines, "")
	}

	// Hint at bottom
	rightLines = append(rightLines, tkDimStyle.Render("enter detail · o open · e edit"))

	// ── Compose two-column layout ─────────────────────────────────────────────
	// Reserve 1 line at the bottom for the hint row (handled inside rightLines).
	maxBodyLines := maxH - 1
	if maxBodyLines < 1 {
		maxBodyLines = 1
	}
	if len(leftLines) > maxBodyLines {
		leftLines = leftLines[:maxBodyLines]
	}
	for len(leftLines) < maxBodyLines {
		leftLines = append(leftLines, "")
	}
	if len(rightLines) > maxH {
		rightLines = rightLines[:maxH]
	}
	for len(rightLines) < maxH {
		rightLines = append(rightLines, "")
	}

	leftCol := lipgloss.NewStyle().Width(leftWidth).Render(strings.Join(leftLines, "\n"))
	rightCol := lipgloss.NewStyle().Width(rightWidth).Render(strings.Join(rightLines, "\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "  ", rightCol)
}

// tkFetchPreviewCmd fetches full details for a single issue and returns tkPreviewFetchedMsg.
func tkFetchPreviewCmd(slug string, number int) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("gh", "issue", "view",
			fmt.Sprintf("%d", number),
			"--repo", slug,
			"--json", "title,body,labels,assignees,url",
		)
		out, err := cmd.Output()
		if err != nil {
			return tkPreviewFetchedMsg{number: number, err: err}
		}
		var v struct {
			Title     string    `json:"title"`
			Body      string    `json:"body"`
			Labels    []ghLabel `json:"labels"`
			Assignees []ghUser  `json:"assignees"`
			URL       string    `json:"url"`
		}
		if err := json.Unmarshal(out, &v); err != nil {
			return tkPreviewFetchedMsg{number: number, err: err}
		}
		return tkPreviewFetchedMsg{
			number:    number,
			title:     v.Title,
			body:      v.Body,
			labels:    v.Labels,
			assignees: v.Assignees,
			url:       v.URL,
		}
	}
}

// tkFetchTicketsCmd fetches open issues for the repo in the given directory.
// It also fetches open PRs and builds a map of issue number → linked PR.
func tkFetchTicketsCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		slug := TLRepoSlug(dir)
		if slug == "" {
			return ticketsFetchedMsg{repoSlug: ""}
		}
		issues, err := listOpenIssues(slug, 20)
		if err != nil {
			return ticketsFetchedMsg{repoSlug: slug, err: err}
		}

		// Fetch open PRs and build issue number → PR map from branch names.
		linkedPRs := make(map[int]ghPR)
		prCmd := exec.Command("gh", "pr", "list",
			"--repo", slug,
			"--state", "open",
			"--limit", "50",
			"--json", "number,title,headRefName,url",
		)
		if prOut, prErr := prCmd.Output(); prErr == nil {
			var prs []ghPR
			if json.Unmarshal(prOut, &prs) == nil {
				for _, pr := range prs {
					if issNum := extractIssueFromBranch(pr.HeadRefName); issNum > 0 {
						linkedPRs[issNum] = pr
					}
				}
			}
		}

		return ticketsFetchedMsg{issues: issues, repoSlug: slug, linkedPRs: linkedPRs}
	}
}

// tkFetchDetailCmd fetches full details for a single issue.
func tkFetchDetailCmd(slug string, number int) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("gh", "issue", "view",
			fmt.Sprintf("%d", number),
			"--repo", slug,
			"--json", "title,body,labels,assignees,url",
		)
		out, err := cmd.Output()
		if err != nil {
			return tkDetailFetchedMsg{number: number, err: err}
		}
		var v struct {
			Title     string    `json:"title"`
			Body      string    `json:"body"`
			Labels    []ghLabel `json:"labels"`
			Assignees []ghUser  `json:"assignees"`
			URL       string    `json:"url"`
		}
		if err := json.Unmarshal(out, &v); err != nil {
			return tkDetailFetchedMsg{number: number, err: err}
		}
		return tkDetailFetchedMsg{
			number:    number,
			title:     v.Title,
			body:      v.Body,
			labels:    v.Labels,
			assignees: v.Assignees,
			url:       v.URL,
		}
	}
}

// listOpenIssues returns up to limit open issues for the repo slug via
// `gh issue list`. Shared by the tickets browser and the ticket launcher.
func listOpenIssues(slug string, limit int) ([]ghIssue, error) {
	out, err := exec.Command("gh", "issue", "list",
		"--repo", slug,
		"--limit", strconv.Itoa(limit),
		"--state", "open",
		"--json", "number,title,labels,createdAt,url,assignees",
	).Output()
	if err != nil {
		return nil, err
	}
	var issues []ghIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, err
	}
	return issues, nil
}

// openIssueInBrowserCmd opens the given issue in the browser.
func openIssueInBrowserCmd(slug string, number int) tea.Cmd {
	return func() tea.Msg {
		_ = exec.Command("gh", "issue", "view",
			fmt.Sprintf("%d", number),
			"--repo", slug,
			"--web",
		).Run()
		return nil
	}
}

// tkCloseIssueCmd closes a GitHub issue and returns tkIssueClosedMsg.
func tkCloseIssueCmd(slug string, number int) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("gh", "issue", "close",
			fmt.Sprintf("%d", number),
			"--repo", slug,
		)
		_, err := cmd.CombinedOutput()
		return tkIssueClosedMsg{number: number, err: err}
	}
}

// tkAssignSelfCmd assigns the current user to a GitHub issue.
func tkAssignSelfCmd(slug string, number int) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("gh", "issue", "edit",
			fmt.Sprintf("%d", number),
			"--repo", slug,
			"--add-assignee", "@me",
		)
		_, err := cmd.CombinedOutput()
		return tkSelfAssignedMsg{number: number, err: err}
	}
}

// tkEditIssueCmd opens the given issue for editing in the browser via `gh issue edit --web`.
func tkEditIssueCmd(slug string, number int) tea.Cmd {
	return func() tea.Msg {
		_ = exec.Command("gh", "issue", "edit",
			fmt.Sprintf("%d", number),
			"--repo", slug,
			"--web",
		).Run()
		return nil
	}
}

// tkOpenPRInBrowserCmd opens a PR URL in the browser.
func tkOpenPRInBrowserCmd(url string) tea.Cmd {
	return func() tea.Msg {
		_ = exec.Command("gh", "pr", "view", url, "--web").Run()
		return nil
	}
}

// extractIssueFromBranch parses an issue number from branch names like
// "agent/go-engineer/issue-42" or "fix/issue-123".
func extractIssueFromBranch(branch string) int {
	// Try patterns: "issue-N" or "issue/N"
	for _, sep := range []string{"issue-", "issue/"} {
		if idx := strings.LastIndex(branch, sep); idx >= 0 {
			numStr := branch[idx+len(sep):]
			// Take only leading digits
			end := 0
			for end < len(numStr) && numStr[end] >= '0' && numStr[end] <= '9' {
				end++
			}
			if end > 0 {
				if n, err := strconv.Atoi(numStr[:end]); err == nil && n > 0 {
					return n
				}
			}
		}
	}
	return 0
}

// tkCreateIssueCmd creates a new GitHub issue against the given repo.
// selectedLabels is a set of label names to apply; if empty, no --label flag is passed.
func tkCreateIssueCmd(slug, title, body string, selectedLabels map[string]bool) tea.Cmd {
	return func() tea.Msg {
		if body == "" {
			body = title
		}
		args := []string{
			"issue", "create",
			"--repo", slug,
			"--title", title,
			"--body", body,
		}
		var labelNames []string
		for name, on := range selectedLabels {
			if on {
				labelNames = append(labelNames, name)
			}
		}
		sort.Strings(labelNames)
		if len(labelNames) > 0 {
			args = append(args, "--label", strings.Join(labelNames, ","))
		}
		cmd := exec.Command("gh", args...)
		out, err := cmd.Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return issueCreatedMsg{Err: fmt.Errorf("%s", strings.TrimSpace(string(exitErr.Stderr)))}
			}
			return issueCreatedMsg{Err: err}
		}
		url := strings.TrimSpace(string(out))
		num := extractIssueNumber(url)
		return issueCreatedMsg{URL: url, Num: num}
	}
}

// autoPollTickCmd schedules the next autopilot poll tick.
func autoPollTickCmd(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return autoPollTickMsg{}
	})
}

// autoPollRunner abstracts the external effects of an autopoll cycle so tests
// can inject them.
type autoPollRunner struct {
	// list returns the JSON of ready issues for repoSlug.
	list func(repoSlug string) ([]byte, error)
	// dispatch launches an agent on an issue.
	dispatch func(issue int, agent string) error
}

// execAutoPollRunner is the production runner. It dispatches via the running
// binary (os.Executable) rather than whatever "agentctl" happens to be first on
// PATH, which may be a different version or absent.
var execAutoPollRunner = autoPollRunner{
	list: func(repoSlug string) ([]byte, error) {
		return exec.Command("gh", "issue", "list",
			"--repo", repoSlug,
			"--label", "ready",
			"--state", "open",
			"--json", "number,title,labels",
		).Output()
	},
	dispatch: func(issue int, agent string) error {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locating agentctl executable: %w", err)
		}
		out, err := exec.Command(exe, "dispatch",
			"--issue", fmt.Sprintf("%d", issue),
			"--agent", agent,
		).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	},
}

// autoPollCmd polls GitHub for ready issues and dispatches them via `agentctl dispatch`.
func autoPollCmd(repoSlug string) tea.Cmd {
	return func() tea.Msg { return runAutoPoll(repoSlug, execAutoPollRunner) }
}

// runAutoPoll performs one poll cycle. An issue is recorded as dispatched only
// when the dispatch actually succeeded; failures are collected in the result's
// failures so the UI can surface them.
func runAutoPoll(repoSlug string, r autoPollRunner) autoPollResultMsg {
	if repoSlug == "" {
		return autoPollResultMsg{}
	}
	out, err := r.list(repoSlug)
	if err != nil {
		return autoPollResultMsg{err: err}
	}

	var issues []struct {
		Number int       `json:"number"`
		Title  string    `json:"title"`
		Labels []ghLabel `json:"labels"`
	}
	if err := json.Unmarshal(out, &issues); err != nil {
		return autoPollResultMsg{err: err}
	}

	var res autoPollResultMsg
	for _, iss := range issues {
		// Skip issues that are already in-progress.
		if hasLabel(iss.Labels, "in-progress") {
			continue
		}

		agent := inferAgentFromLabels(iss.Labels)
		if agent == "" {
			agent = agentlabel.Default
		}

		if err := r.dispatch(iss.Number, agent); err != nil {
			logging.Warn("autopoll: dispatch #%d to %s failed: %v", iss.Number, agent, err)
			res.failures = append(res.failures, fmt.Sprintf("#%d: %v", iss.Number, err))
			continue
		}
		res.dispatched = append(res.dispatched, autoPollDispatch{
			issueNumber: iss.Number,
			issueTitle:  iss.Title,
			agentName:   agent,
		})
	}
	return res
}

// inferAgentFromLabels determines the target agent from issue labels.
// Priority: exact agent name match > alias map > empty string (caller uses default).
func inferAgentFromLabels(labels []ghLabel) string {
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return agentlabel.Infer(names)
}

// hasLabel returns true if the issue has a label with the given name.
func hasLabel(labels []ghLabel, name string) bool {
	for _, l := range labels {
		if l.Name == name {
			return true
		}
	}
	return false
}

// extractLabels returns a sorted list of unique label names from issues.
func extractLabels(issues []ghIssue) []string {
	seen := make(map[string]bool)
	for _, iss := range issues {
		for _, l := range iss.Labels {
			if l.Name != "" {
				seen[l.Name] = true
			}
		}
	}
	var labels []string
	for name := range seen {
		labels = append(labels, name)
	}
	sort.Strings(labels)
	return labels
}

// filterByLabel returns only issues that have the given label.
func filterByLabel(issues []ghIssue, label string) []ghIssue {
	if label == "" {
		return issues
	}
	var filtered []ghIssue
	for _, iss := range issues {
		for _, l := range iss.Labels {
			if l.Name == label {
				filtered = append(filtered, iss)
				break
			}
		}
	}
	return filtered
}
