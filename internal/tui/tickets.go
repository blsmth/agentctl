package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/agentlabel"
	"github.com/blsmth/agentctl/internal/theme"
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
	tkColPR       = 8 // "#NNN↗" link or blank
	tkColLabels   = 24
	tkColAssignee = 12 // "@loginname  " badge or blank
	tkColAge      = 8  // "16h ago"
	tkRowPrefix   = 3  // selection gutter + cursor marker + space
	// title fills the remaining space
)

// Width breakpoints (terminal columns).
const (
	// tkDropAssigneeCols and the two below drop list columns, in this order,
	// as the terminal narrows: ASSIGNEE, then PR, then LABELS.
	tkDropAssigneeCols = 110
	tkDropPRCols       = 96
	tkDropLabelsCols   = 80
	// tkNarrowCols is where the preview/detail sidebar of action buttons
	// collapses into a one-line strip.
	tkNarrowCols = 90
)

// Views.
const (
	tkViewList   = 0
	tkViewDetail = 1
)

// tkDoubleClick is the longest gap between two clicks on the same zone that
// still counts as a double-click.
const tkDoubleClick = 400 * time.Millisecond

// tkNow is the browser's clock; tests replace it to drive double-click timing.
var tkNow = time.Now

// sortMode controls how the issue list is ordered.
type sortMode int

const (
	sortNewest     sortMode = iota // newest first (default)
	sortOldest                     // oldest first
	sortNumber                     // ascending issue number
	sortNumberDesc                 // descending issue number
	sortModeCount
)

// label is the chip text for the sort mode.
func (s sortMode) label() string {
	switch s {
	case sortOldest:
		return "oldest"
	case sortNumber:
		return "# ↑"
	case sortNumberDesc:
		return "# ↓"
	}
	return "newest"
}

// createFocus is the focused section in the single-screen create form.
type createFocus int

const (
	focusTitle  createFocus = iota // title text input
	focusBody                      // body textarea
	focusLabels                    // label picker (only when labels available)
	focusSubmit                    // Submit button
	focusCancel                    // Cancel button
)

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

// tkDetailFetchedMsg is sent when we fetch full issue details for the detail view.
type tkDetailFetchedMsg struct {
	number    int
	title     string
	body      string
	labels    []ghLabel
	url       string
	assignees []ghUser
	state     string    // OPEN / CLOSED
	updatedAt time.Time // GitHub's updatedAt when fetched (stale-edit check)
	err       error
}

// tkPreviewFetchedMsg is sent when a background preview fetch completes for the split pane.
type tkPreviewFetchedMsg tkDetailFetchedMsg

// tkFlashExpireMsg clears the flash line if it is still showing flash id.
type tkFlashExpireMsg struct{ id int }

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
	issues     []ghIssue
	allIssues  []ghIssue
	cursor     int
	repoSlug   string
	loading    bool
	err        error
	issueState string // "open" (default), "closed" or "all"

	// Detail view state
	viewMode     int // tkViewList or tkViewDetail
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
	createModal Modal

	// flash is the transient one-line message under the title ("✓ created
	// #12", "✗ close failed: ..."); flashErr renders it red. flashID lets a
	// stale expiry tick leave a newer message alone.
	flash    string
	flashErr bool
	flashID  int

	// Agent list for the agent-selection overlay.
	agents []agent.Agent

	// confirmModal wraps ConfirmCloseModel for close confirmation.
	confirmModal Modal
	// agentModal wraps AgentSelectModel for agent selection.
	agentModal Modal
	// promptModal wraps PromptSelectModel for prompt selection.
	promptModal Modal
	// helpModal lists keys and mouse gestures (?).
	helpModal Modal

	result   TicketsResult
	quitting bool

	// Sort mode for the issue list.
	sortBy sortMode

	// Split-pane preview state
	previewDetail    *tkDetailFetchedMsg
	previewScroll    int // preview body scroll (mouse wheel over the preview)
	detailCache      map[int]*tkDetailFetchedMsg
	detailCacheOrder []int // tracks insertion order for LRU eviction (max 20)

	// splitPane manages list/detail pane geometry and focus.
	splitPane SplitPane

	// Keep the dir for fetching
	dir string

	// Linked PRs: map of issue number → open PR whose branch references that issue.
	linkedPRs map[int]ghPR

	// Mouse state (tickets_mouse.go). zones is shared by every copy of the
	// model: View records the frame's click zones into it, so hit-testing
	// always runs against exactly what is on screen.
	zones         *zoneList
	hover         string // innermost zone under the pointer
	hoverRow      string // row zone under the pointer (for the row tint)
	press         tkPress
	lastClick     tkPress            // last activated zone, for double-click
	deferredFetch *ticketsFetchedMsg // list refresh held back during a press

	// autopilot is the auto_poll state (tickets_autopilot.go).
	autopilot tkAutopilot
	// choiceModal is the small button dialog (autopilot confirmation,
	// discard and stale-edit prompts).
	choiceModal Modal
	// pickerModal is the label / assignee / label-filter picker.
	pickerModal Modal

	// edit is the in-place title/body draft (tickets_edit.go).
	edit tkEdit
	// pendingEdit opens the editor on pendingEditNum once its detail loads
	// (e from the list).
	pendingEdit    tkEditField
	pendingEditNum int
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

	// Resolve the autopilot setting the way the toggle will.
	on, interval := tkLoadAutoPoll()

	m := TicketsModel{
		termWidth:   120,
		termHeight:  40,
		loading:     true,
		repoSlug:    slug,
		dir:         dir,
		issueState:  tkStateOpen,
		result:      TicketsResult{Cancelled: true},
		searchInput: si,
		labelIdx:    -1,
		agents:      agents,
		autopilot:   tkAutopilot{on: on, interval: interval},
		detailCache: make(map[int]*tkDetailFetchedMsg),
		zones:       &zoneList{},
		splitPane: SplitPane{
			ListRatio:     0.30,
			MinListRows:   3,
			MinDetailRows: 4,
		},
	}
	m.splitPane.SetSize(m.termWidth, m.paneHeight())
	// Init schedules this loop's ticks; marking it running here keeps a
	// later P from starting a second loop.
	if on {
		m.autopilot.gen = 1
		m.autopilot.ticker = true
		m.autopilot.nextPoll = tkNow().Add(interval)
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

// narrow reports whether the sidebar actions collapse to a one-line strip.
func (m TicketsModel) narrow() bool { return m.termWidth < tkNarrowCols }

// List-view chrome around the split pane: title, flash line, chips, blank,
// column header and rule above it; a blank line and the hint bar below.
const (
	tkListChromeTop    = 6
	tkListChromeBottom = 2
)

// paneHeight is the height handed to the split pane, chosen so the list,
// its separator and the bordered preview box fill the screen exactly
// between the list chrome and the hint bar.
func (m TicketsModel) paneHeight() int {
	h := m.termHeight - tkListChromeTop - tkListChromeBottom - detailBoxVerticalOverhead
	if h < 8 {
		h = 8
	}
	return h
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

// forgetPreview drops an issue from the preview cache so the next preview
// fetch goes to GitHub (after an edit, assign or close).
func (m *TicketsModel) forgetPreview(number int) {
	if m.detailCache == nil {
		return
	}
	delete(m.detailCache, number)
	for i, n := range m.detailCacheOrder {
		if n == number {
			m.detailCacheOrder = append(m.detailCacheOrder[:i], m.detailCacheOrder[i+1:]...)
			break
		}
	}
}

// Detail-view chrome: header and a blank line above the body; the scroll
// indicator, a blank line and the hint bar below.
const (
	tkDetailChromeTop    = 2
	tkDetailChromeBottom = 3
)

// detailVisibleRows returns how many content lines fit in the detail view.
func (m TicketsModel) detailVisibleRows() int {
	rows := m.termHeight - tkDetailChromeTop - tkDetailChromeBottom
	if rows < 3 {
		rows = 3
	}
	return rows
}

// Result returns the popup output after it quits.
func (m TicketsModel) Result() TicketsResult { return m.result }

func (m TicketsModel) Init() tea.Cmd {
	cmds := []tea.Cmd{tkFetchTicketsCmd(m.dir, m.issueState)}
	if m.autopilot.on && m.autopilot.ticker {
		cmds = append(cmds, autoPollTickCmd(m.autopilot.interval, m.autopilot.gen), autopilotClockCmd(m.autopilot.gen))
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

// setFlash shows a transient message under the title and returns the tick
// that clears it.
func (m *TicketsModel) setFlash(text string, isErr bool) tea.Cmd {
	m.flashID++
	m.flash, m.flashErr = text, isErr
	id := m.flashID
	d := 4 * time.Second
	if isErr {
		d = 8 * time.Second
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return tkFlashExpireMsg{id: id} })
}

// selectedIssue returns the highlighted list issue.
func (m TicketsModel) selectedIssue() (ghIssue, bool) {
	if m.cursor < 0 || m.cursor >= len(m.issues) {
		return ghIssue{}, false
	}
	return m.issues[m.cursor], true
}

// selectedNumber is the highlighted issue's number, 0 when none.
func (m TicketsModel) selectedNumber() int {
	if iss, ok := m.selectedIssue(); ok {
		return iss.Number
	}
	return 0
}

// currentNumber is the issue the keys act on: the detail's issue in the
// detail view, the highlighted row in the list.
func (m TicketsModel) currentNumber() int {
	if m.viewMode == tkViewDetail && m.detail != nil {
		return m.detail.number
	}
	return m.selectedNumber()
}

// currentURL is the web URL of the issue the keys act on.
func (m TicketsModel) currentURL() string {
	if m.viewMode == tkViewDetail && m.detail != nil && m.detail.url != "" {
		return m.detail.url
	}
	if iss, ok := m.selectedIssue(); ok {
		if iss.URL != "" {
			return iss.URL
		}
		if d, ok := m.detailCache[iss.Number]; ok && d.url != "" {
			return d.url
		}
	}
	if n := m.currentNumber(); n > 0 && m.repoSlug != "" {
		return fmt.Sprintf("https://github.com/%s/issues/%d", m.repoSlug, n)
	}
	return ""
}

// selectIssue moves the list cursor to issue number n and fetches its
// preview. It is a no-op returning nil when n is not in the list.
func (m *TicketsModel) selectIssue(n int) tea.Cmd {
	for i, iss := range m.issues {
		if iss.Number == n {
			m.cursor = i
			m.splitPane.SetCursor(i)
			return m.previewCmdForCursor()
		}
	}
	return nil
}

// previewCmdForCursor fetches the preview of the highlighted issue when the
// preview pane is showing.
func (m TicketsModel) previewCmdForCursor() tea.Cmd {
	if m.splitPane.DetailHeight() < 4 || m.repoSlug == "" {
		return nil
	}
	if iss, ok := m.selectedIssue(); ok {
		return m.previewFetchCmd(iss.Number)
	}
	return nil
}

func (m TicketsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.splitPane.SetSize(msg.Width, m.paneHeight())
		m.hover, m.hoverRow = "", "" // zones move on resize; the next motion re-resolves
		if m.edit.active {
			m.sizeEditor()
		}
		return m, nil

	case ticketsFetchedMsg:
		if m.press.active {
			// Never move rows out from under a click in progress: apply the
			// refresh once the button is released.
			m.deferredFetch = &msg
			return m, nil
		}
		return m.applyFetched(msg)

	case tkIssueClosedMsg:
		if msg.err != nil {
			return m, m.setFlash(fmt.Sprintf("✗ close #%d failed: %v", msg.number, msg.err), true)
		}
		n := msg.number
		m.forgetPreview(n)
		if m.edit.active && m.edit.number == n {
			// The close finished after an edit started: stay on the draft.
			m.edit.err = fmt.Sprintf("✗ #%d was closed · your draft is kept", n)
			if m.detail != nil {
				d := *m.detail
				d.state = "CLOSED"
				m.detail = &d
			}
			return m, tea.Batch(m.setFlash(fmt.Sprintf("closed #%d", n), false), tkFetchTicketsCmd(m.dir, m.issueState))
		}
		flash := m.setFlash(fmt.Sprintf("closed #%d", n), false)
		if m.viewMode == tkViewDetail {
			m.backToList()
		}
		if m.issueState != tkStateOpen {
			// The issue stays listed (closed/all): refetch to show its state.
			m.loading = true
			return m, tea.Batch(flash, tkFetchTicketsCmd(m.dir, m.issueState))
		}
		var newAll []ghIssue
		for _, iss := range m.allIssues {
			if iss.Number != n {
				newAll = append(newAll, iss)
			}
		}
		m.allIssues = newAll
		m.applyFilters()
		return m, tea.Batch(flash, m.previewCmdForCursor())

	case tkDetailFetchedMsg:
		pending := m.pendingEdit
		if m.pendingEditNum != msg.number {
			pending = 0
		}
		m.pendingEdit, m.pendingEditNum = 0, 0
		if msg.err != nil {
			return m, m.setFlash(fmt.Sprintf("✗ #%d: %v", msg.number, msg.err), true)
		}
		if m.edit.active && m.viewMode == tkViewDetail {
			return m, nil // never swap the issue out from under a draft
		}
		m.endEdit()
		m.detail = &msg
		m.scrollOffset = 0
		m.viewMode = tkViewDetail
		// Also populate the preview cache with this data.
		m.cachePreview(&msg)
		if m.previewDetail == nil || m.previewDetail.number == msg.number {
			m.previewDetail = &msg
		}
		if pending != 0 {
			return m.startEdit(pending)
		}
		return m, nil

	case tkPreviewFetchedMsg:
		if msg.err == nil {
			d := tkDetailFetchedMsg(msg)
			m.cachePreview(&d)
			// Only update the visible preview if it's still the current cursor item.
			if m.selectedNumber() == msg.number {
				if m.previewDetail == nil || m.previewDetail.number != msg.number {
					m.previewScroll = 0
				}
				m.previewDetail = &d
			}
			if m.detail != nil && m.detail.number == msg.number && m.viewMode == tkViewDetail && !m.edit.active {
				m.detail = &d
			}
		}
		return m, nil

	case tkBrowserOpenedMsg:
		if msg.err != nil {
			return m, m.setFlash("✗ open in browser failed: "+msg.err.Error(), true)
		}
		return m, nil

	case tkCopiedMsg:
		if msg.err != nil {
			return m, m.setFlash("✗ copy failed: "+msg.err.Error(), true)
		}
		return m, m.setFlash("copied "+msg.url, false)

	case tkFlashExpireMsg:
		if msg.id == m.flashID {
			m.flash, m.flashErr = "", false
		}
		return m, nil

	case autoPollTickMsg, autopilotClockMsg, autoPollListedMsg, autoPollDispatchedMsg, autopilotPreviewMsg:
		return m.updateAutopilot(msg)

	case tea.MouseClickMsg:
		return m.handleMousePress(msg)
	case tea.MouseReleaseMsg:
		return m.handleMouseRelease(msg)
	case tea.MouseMotionMsg:
		return m.handleMouseMotion(msg)
	case tea.MouseWheelMsg:
		return m.handleMouseWheel(msg)

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tkEditRemoteMsg:
		return m.onEditRemote(msg)
	case tkEditSavedMsg:
		return m.onEditSaved(msg)
	case tkEditorDoneMsg:
		return m.onEditorDone(msg)
	case tkDetailRefreshedMsg:
		return m.onDetailRefreshed(msg)
	case tkPickerOptionsMsg:
		return m.onPickerOptions(msg)
	case tkPickerAppliedMsg:
		return m.onPickerApplied(msg)

	case tea.PasteMsg:
		switch {
		case m.pickerModal.Visible:
			var cmd tea.Cmd
			m.pickerModal, cmd = m.pickerModal.Update(msg)
			return m, cmd
		case m.createModal.Visible:
			var cmd tea.Cmd
			m.createModal, cmd = m.createModal.Update(msg)
			return m.checkCreateModalDone(cmd)
		case m.modalOpen():
			return m, nil
		case m.edit.active:
			return m.forwardToEditor(msg)
		case m.searchMode:
			var cmd tea.Cmd
			m.searchInput, cmd = m.searchInput.Update(msg)
			m.searchQuery = m.searchInput.Value()
			m.applyFilters()
			return m, cmd
		}
		return m, nil

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
		if m.edit.active {
			// Cursor blink and other component messages.
			var c1, c2 tea.Cmd
			m.edit.title, c1 = m.edit.title.Update(msg)
			m.edit.body, c2 = m.edit.body.Update(msg)
			return m, tea.Batch(c1, c2)
		}
		if m.searchMode {
			var cmd tea.Cmd
			m.searchInput, cmd = m.searchInput.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// applyFetched installs a fetched issue list, keeping the selection (by
// issue number), filters and search across the refresh.
func (m TicketsModel) applyFetched(msg ticketsFetchedMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	if msg.linkedPRs != nil {
		m.linkedPRs = msg.linkedPRs
	} else {
		m.linkedPRs = make(map[int]ghPR)
	}
	if msg.err != nil || msg.repoSlug == "" {
		m.err = msg.err
		if msg.repoSlug == "" && msg.err == nil {
			m.err = errNoRepo
		}
		m.issues = nil
		m.allIssues = nil
		return m, nil
	}
	m.err = nil
	m.repoSlug = msg.repoSlug
	m.allIssues = msg.issues
	m.labelOptions = extractLabels(msg.issues)
	if m.filterLabel != "" && !containsString(m.labelOptions, m.filterLabel) {
		m.filterLabel = ""
		m.labelIdx = -1
	}
	m.applyFilters()
	return m, m.previewCmdForCursor()
}

// errNoRepo is shown when the directory has no GitHub remote.
var errNoRepo = fmt.Errorf("no GitHub remote found in this directory")

// handleKey routes a key press to the open overlay or the active view.
func (m TicketsModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// ctrl+c / q always quit the application when any overlay is active.
	if (m.confirmModal.Visible || m.agentModal.Visible || m.promptModal.Visible) &&
		(msg.String() == "ctrl+c" || msg.String() == "q") {
		m.result = TicketsResult{Cancelled: true}
		m.quitting = true
		return m, tea.Quit
	}
	if m.choiceModal.Visible {
		if msg.String() == "ctrl+c" {
			if m.edit.dirty() {
				msg = tkKey("esc") // cancel the dialog; never drop a draft on ctrl+c
			} else {
				return m.quit()
			}
		}
		var cmd tea.Cmd
		m.choiceModal, cmd = m.choiceModal.Update(msg)
		return m.checkChoiceModalDone(cmd)
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
	if m.pickerModal.Visible {
		if msg.String() == "ctrl+c" {
			return m.quit()
		}
		var cmd tea.Cmd
		m.pickerModal, cmd = m.pickerModal.Update(msg)
		next, applyCmd := m.checkPickerDone()
		return next, tea.Batch(cmd, applyCmd)
	}
	if m.helpModal.Visible {
		if msg.String() == "ctrl+c" && !m.edit.dirty() {
			return m.quit()
		}
		var cmd tea.Cmd
		m.helpModal, cmd = m.helpModal.Update(msg)
		switch c := m.helpModal.Content.(type) {
		case tkHelpModel:
			if c.done {
				m.helpModal.Visible = false
			}
		case tkTheirsModel:
			if c.done {
				// Back to the conflict prompt: the user still has to choose.
				m.helpModal.Visible = false
				prompt := c.prompt
				prompt.chosen = -1
				m.choiceModal = NewModal("Edit conflict", prompt, ModalMedium)
			}
		}
		return m, cmd
	}
	if m.edit.active && m.viewMode == tkViewDetail {
		return m.updateEdit(msg)
	}
	if m.viewMode == tkViewDetail {
		return m.updateDetail(msg)
	}
	if m.searchMode {
		return m.updateSearch(msg)
	}
	return m.updateList(msg)
}

// applyFilters rebuilds m.issues from m.allIssues applying label, search, and
// sort. The highlighted issue stays highlighted when it survives the filter.
func (m *TicketsModel) applyFilters() {
	sel := m.selectedNumber()
	filtered := filterByLabel(m.allIssues, m.filterLabel)
	if m.searchQuery != "" {
		q := strings.ToLower(m.searchQuery)
		var searched []ghIssue
		for _, iss := range filtered {
			if strings.Contains(strings.ToLower(iss.Title), q) ||
				strings.Contains("#"+strconv.Itoa(iss.Number), q) {
				searched = append(searched, iss)
			}
		}
		filtered = searched
	}
	// filterByLabel returns its input unfiltered; never sort the master list in place.
	filtered = append([]ghIssue(nil), filtered...)
	switch m.sortBy {
	case sortNewest:
		sort.SliceStable(filtered, func(i, j int) bool {
			return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
		})
	case sortOldest:
		sort.SliceStable(filtered, func(i, j int) bool {
			return filtered[i].CreatedAt.Before(filtered[j].CreatedAt)
		})
	case sortNumber:
		sort.SliceStable(filtered, func(i, j int) bool {
			return filtered[i].Number < filtered[j].Number
		})
	case sortNumberDesc:
		sort.SliceStable(filtered, func(i, j int) bool {
			return filtered[i].Number > filtered[j].Number
		})
	}
	m.issues = filtered
	m.cursor = 0
	for i, iss := range filtered {
		if iss.Number == sel {
			m.cursor = i
			break
		}
	}
	m.splitPane.SetListContent(m.splitPane.listContent, len(m.issues))
	m.splitPane.SetCursor(m.cursor)
}

// setLabelFilter filters the list to one label ("" clears the filter).
func (m *TicketsModel) setLabelFilter(label string) {
	m.filterLabel = label
	m.labelIdx = -1
	for i, l := range m.labelOptions {
		if l == label {
			m.labelIdx = i
		}
	}
	m.applyFilters()
}

// toggleSortColumn handles a click on a sortable column header: "num"
// toggles ascending/descending issue number, "age" newest/oldest.
func (m *TicketsModel) toggleSortColumn(col string) {
	switch col {
	case "num":
		if m.sortBy == sortNumber {
			m.sortBy = sortNumberDesc
		} else {
			m.sortBy = sortNumber
		}
	case "age":
		if m.sortBy == sortNewest {
			m.sortBy = sortOldest
		} else {
			m.sortBy = sortNewest
		}
	}
	m.applyFilters()
}

// quit ends the browser without launching anything.
func (m TicketsModel) quit() (tea.Model, tea.Cmd) {
	m.result = TicketsResult{Cancelled: true}
	m.quitting = true
	return m, tea.Quit
}

// openHelp shows the keys-and-gestures overlay.
func (m TicketsModel) openHelp() (tea.Model, tea.Cmd) {
	m.helpModal = NewModal("Tickets — keys & mouse", tkHelpModel{}, ModalMedium)
	return m, nil
}

// launchTriage quits with a triage launch of issue n.
func (m TicketsModel) launchTriage(n int, title, body string) (tea.Model, tea.Cmd) {
	m.result = TicketsResult{
		LaunchAgent:  true,
		TicketNumber: n,
		TicketTitle:  title,
		TicketBody:   body,
		PromptMode:   "triage",
	}
	m.quitting = true
	return m, tea.Quit
}

// launchDispatch opens the agent picker for issue n, or quits with a launch
// on the label-inferred agent when no agents are known.
func (m TicketsModel) launchDispatch(n int, title, body string) (tea.Model, tea.Cmd) {
	if len(m.agents) > 0 {
		asm := newAgentSelectModel(m.agents, n, title, body)
		m.agentModal = NewModal(fmt.Sprintf("Select Agent  #%d", n), asm, ModalMedium)
		return m, nil
	}
	m.result = TicketsResult{LaunchAgent: true, TicketNumber: n, TicketTitle: title, TicketBody: body}
	m.quitting = true
	return m, tea.Quit
}

// confirmClose opens the close-confirmation modal for issue n.
func (m TicketsModel) confirmClose(n int, title, state string) (tea.Model, tea.Cmd) {
	if strings.EqualFold(state, "closed") {
		return m, m.setFlash(fmt.Sprintf("#%d is already closed", n), false)
	}
	c := newConfirmCloseModel(n)
	c.issueTitle = title
	m.confirmModal = NewModal("Close issue", c, ModalSmall)
	return m, nil
}

// openLinkedPR opens issue n's linked PR, if it has one.
func (m TicketsModel) openLinkedPR(n int) (tea.Model, tea.Cmd) {
	if pr, ok := m.linkedPRs[n]; ok {
		return m, tkOpenPRInBrowserCmd(pr.URL)
	}
	return m, m.setFlash(fmt.Sprintf("#%d has no linked PR", n), false)
}

// copyURL copies the current issue's URL to the tmux buffer.
func (m TicketsModel) copyURL() (tea.Model, tea.Cmd) {
	url := m.currentURL()
	if url == "" {
		return m, nil
	}
	return m, tkCopyURLCmd(url)
}

func (m TicketsModel) updateList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	iss, hasIssue := m.selectedIssue()
	switch msg.String() {
	case "ctrl+c", "q":
		return m.quit()

	case "esc":
		if m.searchQuery != "" {
			// Clear the search filter rather than quitting
			m.searchQuery = ""
			m.searchInput.SetValue("")
			m.applyFilters()
			return m, m.previewCmdForCursor()
		}
		return m.quit()

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.splitPane.SetCursor(m.cursor)
		}
		return m, m.previewCmdForCursor()

	case "down", "j":
		if m.cursor < len(m.issues)-1 {
			m.cursor++
			m.splitPane.SetCursor(m.cursor)
		}
		return m, m.previewCmdForCursor()

	case "tab":
		m.splitPane.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		return m, nil

	case "J":
		m.scrollPreview(tkWheelStep)
		return m, nil

	case "K":
		m.scrollPreview(-tkWheelStep)
		return m, nil

	case "enter":
		if !hasIssue || m.repoSlug == "" {
			break
		}
		return m, tkFetchDetailCmd(m.repoSlug, iss.Number)

	case "c":
		content := NewCreateTicketModel(m.repoSlug, m.labelOptions)
		m.createModal = NewModal("Create Ticket", content, ModalMedium)
		return m, content.Init()

	case "l":
		return m.openLabelPicker()

	case "f":
		return m.openFilterPicker()

	case "/":
		m.searchMode = true
		m.searchInput.SetValue(m.searchQuery)
		m.searchInput.Focus()
		return m, textinput.Blink

	case "S":
		// Cycle the issue state shown: open → closed → all.
		m.issueState = nextIssueState(m.issueState)
		m.loading = true
		return m, tkFetchTicketsCmd(m.dir, m.issueState)

	case "o":
		if !hasIssue || m.repoSlug == "" {
			break
		}
		return m, openIssueInBrowserCmd(m.repoSlug, iss.Number)

	case "p":
		if hasIssue {
			return m.openLinkedPR(iss.Number)
		}

	case "y":
		return m.copyURL()

	case "a":
		return m.openAssignPicker()

	case "t":
		if hasIssue {
			return m.launchTriage(iss.Number, iss.Title, "")
		}

	case "T":
		if hasIssue {
			return m.launchDispatch(iss.Number, iss.Title, "")
		}

	case "d":
		// Show close-confirmation overlay before closing the highlighted issue.
		if hasIssue && m.repoSlug != "" {
			return m.confirmClose(iss.Number, iss.Title, iss.State)
		}

	case "s":
		// Cycle sort modes: newest → oldest → # ascending → # descending.
		m.sortBy = (m.sortBy + 1) % sortModeCount
		m.applyFilters()
		return m, m.previewCmdForCursor()

	case "e":
		// Edit in place: open the detail with the title focused.
		if !hasIssue {
			break
		}
		if m.repoSlug == "" {
			return m, m.setFlash("✗ editing needs a GitHub remote for this directory", true)
		}
		m.pendingEdit, m.pendingEditNum = editTitle, iss.Number
		return m, tkFetchDetailCmd(m.repoSlug, iss.Number)

	case "E":
		// Edit in the browser.
		if hasIssue && m.repoSlug != "" {
			return m, tkEditIssueCmd(m.repoSlug, iss.Number)
		}

	case "?":
		return m.openHelp()

	case "P":
		return m.toggleAutopilot()
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
		return m, m.previewCmdForCursor()

	case "enter":
		m.commitSearch()
		return m, m.previewCmdForCursor()
	}

	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	m.searchQuery = m.searchInput.Value()
	m.applyFilters()
	return m, cmd
}

// commitSearch closes the search input, keeping its query as the filter.
func (m *TicketsModel) commitSearch() {
	m.searchMode = false
	m.searchQuery = m.searchInput.Value()
	m.searchInput.Blur()
	m.applyFilters()
}

// backToList leaves the detail view.
func (m *TicketsModel) backToList() {
	m.endEdit() // an edit never outlives its detail view
	m.viewMode = tkViewList
	m.detail = nil
	m.scrollOffset = 0
}

func (m TicketsModel) updateDetail(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	d := m.detail
	if d == nil {
		m.viewMode = tkViewList
		return m.updateList(msg)
	}
	switch msg.String() {
	case "ctrl+c", "q":
		return m.quit()

	case "esc":
		m.backToList()

	case "up", "k":
		m.scrollDetail(-1)

	case "down", "j":
		m.scrollDetail(1)

	case "o":
		if m.repoSlug != "" {
			return m, openIssueInBrowserCmd(m.repoSlug, d.number)
		}

	case "y":
		return m.copyURL()

	case "t":
		return m.launchTriage(d.number, d.title, d.body)

	case "T":
		return m.launchDispatch(d.number, d.title, d.body)

	case "d":
		// Show close-confirmation overlay before closing the current issue.
		if m.repoSlug != "" {
			return m.confirmClose(d.number, d.title, d.state)
		}

	case "a":
		return m.openAssignPicker()

	case "l":
		return m.openLabelPicker()

	case "f":
		return m.openFilterPicker()

	case "e":
		// Edit in place, body focused.
		return m.startEdit(editBody)

	case "E":
		// Edit in the browser.
		if m.repoSlug != "" {
			return m, tkEditIssueCmd(m.repoSlug, d.number)
		}

	case "p":
		return m.openLinkedPR(d.number)

	case "P":
		return m.toggleAutopilot()

	case "?":
		return m.openHelp()
	}
	return m, nil
}

// scrollDetail moves the detail body by delta lines, clamped to its length.
func (m *TicketsModel) scrollDetail(delta int) {
	m.scrollOffset += delta
	if maxScroll := m.detailMaxScroll(); m.scrollOffset > maxScroll {
		m.scrollOffset = maxScroll
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
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
		return m, tea.Batch(cmd, m.setFlash("✗ create failed: "+ctm.result.Err.Error(), true))
	}
	// Successful creation — show confirmation and refresh the list.
	text := "issue created"
	if ctm.result.Number > 0 {
		text = fmt.Sprintf("created #%d", ctm.result.Number)
	}
	m.loading = true
	return m, tea.Batch(cmd, m.setFlash(text, false), tkFetchTicketsCmd(m.dir, m.issueState))
}

// checkChoiceModalDone acts on a finished choice dialog.
func (m TicketsModel) checkChoiceModalDone(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	c, ok := m.choiceModal.Content.(tkChoiceModel)
	if !ok || !c.done() {
		return m, cmd
	}
	m.choiceModal.Visible = false
	switch c.kind {
	case "autopilot":
		if c.picked() == "y" {
			next, onCmd := m.setAutopilot(true)
			return next, tea.Batch(cmd, onCmd)
		}
	case "discard":
		if c.picked() == "d" {
			m.endEdit()
		}
	case "quit":
		if c.picked() == "d" {
			return m.quit()
		}
	case "stale":
		next, staleCmd := m.onStaleChoice(c)
		return next, tea.Batch(cmd, staleCmd)
	}
	return m, cmd
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
		out, err := tkExec("gh", args...)
		if err != nil {
			return issueCreatedMsg{Err: err}
		}
		url := strings.TrimSpace(string(out))
		num := extractIssueNumber(url)
		return issueCreatedMsg{URL: url, Num: num}
	}
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
