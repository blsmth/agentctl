package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/tmux"
)

// herdPollInterval is the hub's refresh cadence. The monitor writes
// state.json roughly every tick (500ms); polling faster than ~1s buys
// nothing (per the design doc).
const herdPollInterval = 1 * time.Second

// herdChromeRows is the number of non-list lines reserved above and below
// the scrollable line list: title(1) + blank(1) + subtitle(1) + blank(1)
// above; blank(1) + error-line(1) + hint-bar(1) below.
const herdChromeRows = 7

// herdMinSplitWidth is the terminal width below which the sidebar + main
// panel layout is unusable (a sidebar narrower than this truncates every
// agent name into mush). Below it the hub falls back to the original
// full-width flat list.
const herdMinSplitWidth = 80

// herdMinSidebarWidth / herdMinMainPanelWidth are the SplitPane minimums for
// the sidebar and main panel columns in split mode.
const (
	herdMinSidebarWidth   = 28
	herdMinMainPanelWidth = 40
)

// herdSidebarRatio is the fraction of content width given to the sidebar in
// split mode.
const herdSidebarRatio = 0.3

// herdPreviewLines is how many lines of an agent's terminal CapturePane
// requests. The main panel only ever displays as many as fit its height, but
// requesting a generous fixed amount keeps CapturePane's argument building
// simple and avoids re-shelling out if the panel is resized slightly taller.
const herdPreviewLines = 200

// herdMaxNameWidth caps how much of a row's width the instance name can
// claim, even in the wide flat-list fallback, so the activity line always
// keeps some room and a long instance name can't crowd it out entirely.
const herdMaxNameWidth = 24

// herdSidebarLeftInset is the left margin sidebar content lines start at —
// specifically the width of a group header's unselected cursor marker (see
// renderGroupHeader). herdLineDivider uses the same constant so a divider
// between two groups starts at the same column as the headers it separates,
// instead of running flush to the sidebar's left edge.
const herdSidebarLeftInset = 2

// herdSidebarRightGutter is the number of columns sidebar content lines
// reserve at their right edge so they never touch the detail panel's
// vertical border (see renderRow's "available" calculation). herdLineDivider
// reserves the same gutter so it can't collide with that border either.
const herdSidebarRightGutter = 2

// ── Styles ───────────────────────────────────────────────────────────────────

var (
	herdTitleStyle    lipgloss.Style
	herdSubtitleStyle lipgloss.Style
	herdDimStyle      lipgloss.Style
	herdErrStyle      lipgloss.Style
	herdSelectedStyle lipgloss.Style

	herdWorkingStyle lipgloss.Style
	herdBlockedStyle lipgloss.Style
	herdDoneStyle    lipgloss.Style
	herdIdleStyle    lipgloss.Style

	herdSessionStyle  lipgloss.Style
	herdProjectStyle  lipgloss.Style
	herdCountStyle    lipgloss.Style
	herdNameStyle     lipgloss.Style
	herdActivityStyle lipgloss.Style
)

// initHerdStyles initialises the hub view's styles from the active theme.
// Reuses the same state colours as the chrome bar's agent tabs (workingTab =
// Accent, blockedTab = Yellow, doneTab = Green, idleTab = Subtle) so the hub
// and the per-session chrome bar agree visually.
func initHerdStyles() {
	t := theme.Current()

	herdTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Align(lipgloss.Center)

	herdSubtitleStyle = tableStyles.Subtitle
	herdDimStyle = tableStyles.Dim
	herdErrStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Red))
	herdSelectedStyle = lipgloss.NewStyle().Background(lipgloss.Color(t.Surface))

	herdWorkingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent))
	herdBlockedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Yellow))
	herdDoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green))
	herdIdleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))

	herdSessionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Text))
	herdProjectStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Sky))
	herdCountStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	herdNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	herdActivityStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
}

// ── Messages ─────────────────────────────────────────────────────────────────

// herdTickMsg drives the hub's poll loop.
type herdTickMsg struct{}

// herdLoadedMsg carries the result of an async herd.Load() call.
type herdLoadedMsg struct {
	groups []herd.Group
	err    error
}

// herdCapturedMsg carries the result of an async tmux.CapturePane() call for
// whichever agent was selected at the time it was fired. session/windowID
// let the receiver discard a stale result that arrives after the user has
// already navigated to a different row.
type herdCapturedMsg struct {
	session  string
	windowID string
	content  string
	err      error
}

// herdLinkedMsg carries the result of an async tmux.ListHubWindowIDs() call
// — the current set of window IDs present in the hub session, used to mark
// which agent rows are open (linked in) in the sidebar.
type herdLinkedMsg struct {
	ids map[string]bool
}

// ── Flattened line model ─────────────────────────────────────────────────────

// herdLineKind distinguishes the three kinds of renderable lines in the
// hub's flattened, scrollable list.
type herdLineKind int

const (
	herdLineHeader      herdLineKind = iota // a group's session/project/rollup header
	herdLinePlaceholder                     // "no agents" / "snapshot unavailable"
	herdLineRow                             // a selectable agent row
	herdLineDivider                         // a non-selectable rule between two groups
)

// herdLine is one renderable line. Every kind except herdLineDivider is also
// selectable — see the "selection" section below.
type herdLine struct {
	kind      herdLineKind
	group     herd.Group // set for herdLineHeader / herdLinePlaceholder
	row       herd.Row   // set for herdLineRow
	groupHead bool       // true for the first row of a group (blank line above)
	stale     bool       // the owning group's Stale flag — dims rows/header without affecting selectability
}

// ── HerdModel ────────────────────────────────────────────────────────────────

// HerdModel is the full-screen Bubble Tea model for the herd hub: the
// read-only, cross-session aggregator view that runs as the sole window of
// the agentctl-hub tmux session. Data is loaded from internal/herd, which
// does all the registry/tmux/state.json I/O; this model only renders it and
// handles navigation + jump.
//
// Layout: a sidebar (session/agent list) + main panel (selected agent's live
// preview + metadata, or a session summary when a group header is selected),
// built on SplitPane in OrientHorizontal mode. Below herdMinSplitWidth this
// degrades to the original full-width flat list — a sidebar that narrow is
// unusable, not a design worth defending.
//
// Both layouts share one selection model: every line in m.lines (group
// headers, placeholders, and agent rows alike) is selectable. Landing on a
// row shows that agent's detail; landing on a header or placeholder shows
// its session's summary. Selection is tracked by SplitPane's cursor, and
// resolved after each reload by (session, windowID) — or by session alone
// for a header — rather than by raw index, so a reorder from a state change
// never moves the cursor out from under the user.
type HerdModel struct {
	termWidth  int
	termHeight int

	groups []herd.Group
	lines  []herdLine

	// splitPane owns cursor position, scrolling, and (in split-layout mode)
	// the sidebar/main-panel rendering. HerdModel drives it via
	// SetListContent/SetDetailContent/SetSize/Cursor/SetCursor/View, but
	// does NOT delegate key handling to SplitPane.Update(): unlike the
	// other browsers' flat row lists, herd's sidebar mixes selectable
	// headers and rows, and Enter's jump behaviour only applies to rows —
	// both are simpler to express directly in handleKey/moveCursor below.
	splitPane SplitPane

	// scrollOffset is the flat-layout fallback's own vertical scroll
	// position (split mode uses splitPane's internal listScroll instead).
	scrollOffset int

	// selSession/selWindowID/selIsHeader identify the selection by
	// (session, windowID) — or by session alone when a header/placeholder
	// is selected — rather than by raw index, so a refresh that reorders
	// lines (a state change) doesn't move the cursor out from under the
	// user.
	selSession  string
	selWindowID string
	selIsHeader bool

	// preview/previewErr hold the last tmux.CapturePane() result for the
	// currently selected agent row. Populated asynchronously by
	// herdCaptureCmd; never fetched for anything but the selected row.
	preview    string
	previewErr string

	// linkedWindowIDs is the set of tmux window IDs currently present in the
	// hub session (herdLinkedCmd → tmux.ListHubWindowIDs), refreshed every
	// poll tick. Never stored/cached beyond that — tmux is the source of
	// truth for which agents are open in the hub, since a linked window can
	// be unlinked or vanish at any time. Drives the sidebar's link-state
	// glyph (see rowIsLinked).
	linkedWindowIDs map[string]bool

	loaded  bool
	loadErr error
	jumpErr string // a real failure: styled as an error, triggers a refresh

	// infoMsg is a benign, non-error inline message — e.g. tmux's expected
	// refusal to unlink a window that was never linked in. Rendered
	// alongside jumpErr but without the error styling; see renderFooter.
	infoMsg string

	quitting bool
}

// NewHerdModel creates the hub aggregator model. Data is loaded
// asynchronously via Init().
func NewHerdModel() HerdModel {
	return HerdModel{
		termWidth:  120,
		termHeight: 40,
		splitPane: SplitPane{
			Orientation:   OrientHorizontal,
			ListRatio:     herdSidebarRatio,
			MinListRows:   herdMinSidebarWidth,
			MinDetailRows: herdMinMainPanelWidth,
		},
	}
}

// Init kicks off the first load and starts the poll loop.
func (m HerdModel) Init() tea.Cmd {
	return tea.Batch(herdLoadCmd(), herdTickCmd(), herdLinkedCmd())
}

func herdTickCmd() tea.Cmd {
	return tea.Tick(herdPollInterval, func(time.Time) tea.Msg { return herdTickMsg{} })
}

func herdLoadCmd() tea.Cmd {
	return func() tea.Msg {
		groups, err := herd.Load()
		return herdLoadedMsg{groups: groups, err: err}
	}
}

// herdCaptureCmd asynchronously captures the given agent's pane. Only ever
// called for the currently-selected agent — never for every row — so the
// hub shells out to tmux at most once per poll tick, not once per row.
func herdCaptureCmd(session, windowID string) tea.Cmd {
	return func() tea.Msg {
		content, err := tmux.CapturePane(session, windowID, herdPreviewLines)
		return herdCapturedMsg{session: session, windowID: windowID, content: content, err: err}
	}
}

// herdLinkedCmd asynchronously fetches the current set of window IDs linked
// into the hub session. Cheap (a single `tmux list-windows`), so unlike
// herdCaptureCmd this runs every tick regardless of what's selected — it
// drives every row's link-state glyph, not just the selected one.
func herdLinkedCmd() tea.Cmd {
	return func() tea.Msg {
		return herdLinkedMsg{ids: tmux.ListHubWindowIDs()}
	}
}

// maybeCaptureCmd fires herdCaptureCmd for whatever agent is currently
// selected, or returns nil if nothing selectable is an agent row (a header,
// placeholder, or empty hub is selected instead).
func (m *HerdModel) maybeCaptureCmd() tea.Cmd {
	if m.selIsHeader || m.selSession == "" || m.selWindowID == "" {
		return nil
	}
	return herdCaptureCmd(m.selSession, m.selWindowID)
}

// Update handles Bubble Tea messages.
func (m HerdModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.configureSplitPane()
		return m, nil

	case herdTickMsg:
		return m, tea.Batch(herdLoadCmd(), herdTickCmd(), herdLinkedCmd(), m.maybeCaptureCmd())

	case herdLinkedMsg:
		m.linkedWindowIDs = msg.ids
		return m, nil

	case herdLoadedMsg:
		m.loaded = true
		m.loadErr = msg.err
		if msg.err == nil {
			m.groups = msg.groups
			m.rebuildLines()
			m.resolveSelection()
		}
		return m, m.maybeCaptureCmd()

	case herdCapturedMsg:
		if msg.session == m.selSession && msg.windowID == m.selWindowID {
			m.preview = msg.content
			m.previewErr = ""
			if msg.err != nil {
				m.previewErr = msg.err.Error()
				m.preview = ""
			}
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *HerdModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return *m, tea.Quit

	case "q", "esc":
		if err := tmux.SwitchToLastSession(); err != nil {
			m.jumpErr = err.Error()
		}
		return *m, nil

	case "j", "down":
		m.moveCursor(1)
		return *m, m.maybeCaptureCmd()

	case "k", "up":
		m.moveCursor(-1)
		return *m, m.maybeCaptureCmd()

	case "enter":
		return m.linkSelectedIntoHub()

	case "J":
		return m.jumpToSelectedHome()

	case "u":
		return m.unlinkSelected()

	case "b":
		return m.jumpToNextBlocked()

	case "r":
		m.jumpErr = ""
		m.infoMsg = ""
		return *m, herdLoadCmd()
	}
	return *m, nil
}

// configureSplitPane sizes the SplitPane from the current terminal
// dimensions, reserving the same chrome rows as the flat fallback so both
// layouts occupy the same vertical footprint.
func (m *HerdModel) configureSplitPane() {
	h := m.termHeight - herdChromeRows
	if h < 3 {
		h = 3
	}
	m.splitPane.SetSize(m.contentWidth(), h)
}

// ── Selection / navigation ───────────────────────────────────────────────────

// selectedRow returns the currently selected agent row, or nil if the
// cursor is on a group header/placeholder or nothing is selected.
func (m *HerdModel) selectedRow() *herd.Row {
	idx := m.splitPane.Cursor()
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != herdLineRow {
		return nil
	}
	r := m.lines[idx].row
	return &r
}

// selectedGroup returns the currently selected group, when the cursor is on
// that group's header or placeholder line (not one of its agent rows).
func (m *HerdModel) selectedGroup() *herd.Group {
	idx := m.splitPane.Cursor()
	if idx < 0 || idx >= len(m.lines) {
		return nil
	}
	l := m.lines[idx]
	if l.kind == herdLineHeader || l.kind == herdLinePlaceholder {
		g := l.group
		return &g
	}
	return nil
}

// syncSelectionFromCursor refreshes selSession/selWindowID/selIsHeader from
// whatever line the SplitPane's cursor currently points at.
func (m *HerdModel) syncSelectionFromCursor() {
	idx := m.splitPane.Cursor()
	if idx < 0 || idx >= len(m.lines) {
		m.selSession, m.selWindowID, m.selIsHeader = "", "", false
		return
	}
	l := m.lines[idx]
	switch l.kind {
	case herdLineRow:
		m.selSession = l.row.Session
		m.selWindowID = l.row.WindowID
		m.selIsHeader = false
	case herdLineDivider:
		// Unreachable in practice — moveCursor/resolveSelection never park
		// the cursor on a divider — but defend against adopting a
		// divider's zero-value Group as the selection rather than leaving
		// whatever was previously selected in place.
	default: // herdLineHeader / herdLinePlaceholder
		m.selSession = l.group.Session
		m.selWindowID = ""
		m.selIsHeader = true
	}
}

// moveCursor moves the SplitPane cursor by delta lines, skipping over any
// herdLineDivider it would otherwise land on (headers, placeholders, and
// rows are all still selectable, as in the pre-divider design).
func (m *HerdModel) moveCursor(delta int) {
	if len(m.lines) == 0 {
		return
	}
	// Content itself is rebuilt fresh in render(); this call just keeps
	// SplitPane's totalRows (used for cursor clamping) in sync immediately,
	// rather than waiting for the next View().
	m.splitPane.SetListContent(m.splitPane.listContent, len(m.lines))
	m.splitPane.SetCursor(m.nextSelectable(m.splitPane.Cursor(), delta))
	m.syncSelectionFromCursor()
	m.ensureCursorVisible()
}

// nextSelectable walks from cursor in the direction of delta (one line at a
// time, repeating delta's sign) until it lands on a non-divider line,
// stopping at whichever end of m.lines it reaches first. rebuildLines never
// emits a divider as the first or last line, so the boundary itself is
// always guaranteed selectable — this only needs to skip dividers strictly
// between the boundary and the starting cursor.
func (m *HerdModel) nextSelectable(cursor, delta int) int {
	if delta == 0 || len(m.lines) == 0 {
		return cursor
	}
	idx := cursor
	for {
		next := idx + delta
		if next < 0 || next >= len(m.lines) {
			return idx // boundary reached; idx is the last selectable stop
		}
		idx = next
		if m.lines[idx].kind != herdLineDivider {
			return idx
		}
	}
}

// resolveSelection re-finds the cursor after a reload by matching
// (selSession, selWindowID) — or selSession alone for a header/placeholder
// selection — rather than a raw index, so a state change that reorders
// lines doesn't move the cursor under the user. Falls back to the first
// agent row if the previous selection is gone (matching the pre-sidebar
// default of always landing somewhere jumpable), then to the very first
// line, then to no selection if there are no lines at all.
func (m *HerdModel) resolveSelection() {
	// Content itself is rebuilt fresh in render(); this call just keeps
	// SplitPane's totalRows (used for cursor clamping) in sync immediately,
	// rather than waiting for the next View().
	m.splitPane.SetListContent(m.splitPane.listContent, len(m.lines))

	if m.selSession != "" {
		for i, l := range m.lines {
			if m.selIsHeader {
				if (l.kind == herdLineHeader || l.kind == herdLinePlaceholder) && l.group.Session == m.selSession {
					m.splitPane.SetCursor(i)
					m.syncSelectionFromCursor()
					m.ensureCursorVisible()
					return
				}
				continue
			}
			if l.kind == herdLineRow && l.row.Session == m.selSession && l.row.WindowID == m.selWindowID {
				m.splitPane.SetCursor(i)
				m.syncSelectionFromCursor()
				m.ensureCursorVisible()
				return
			}
		}
	}

	for i, l := range m.lines {
		if l.kind == herdLineRow {
			m.splitPane.SetCursor(i)
			m.syncSelectionFromCursor()
			m.ensureCursorVisible()
			return
		}
	}

	// No row anywhere (e.g. every group is an empty placeholder) — fall back
	// to the first selectable line. rebuildLines never emits a divider as
	// the first line, but this skips one anyway rather than assuming it.
	for i, l := range m.lines {
		if l.kind != herdLineDivider {
			m.splitPane.SetCursor(i)
			m.syncSelectionFromCursor()
			m.ensureCursorVisible()
			return
		}
	}

	m.selSession, m.selWindowID, m.selIsHeader = "", "", false
}

func (m *HerdModel) visibleLines() int {
	h := m.termHeight - herdChromeRows
	if h < 3 {
		h = 3
	}
	return h
}

// ensureCursorVisible keeps the flat fallback's own scroll position
// (scrollOffset) following the cursor. It's a no-op in split mode (which
// uses SplitPane's own listScroll instead) but harmless to call
// unconditionally.
func (m *HerdModel) ensureCursorVisible() {
	if len(m.lines) == 0 {
		return
	}
	cursor := m.splitPane.Cursor()
	visible := m.visibleLines()
	if cursor < m.scrollOffset {
		m.scrollOffset = cursor
	}
	if cursor >= m.scrollOffset+visible {
		m.scrollOffset = cursor - visible + 1
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
}

// ── Jump / link / unlink ─────────────────────────────────────────────────────

// linkSelectedIntoHub is Enter's action: link the selected row's window into
// the hub session (idempotently — see tmux.LinkWindowIntoHub) and select it,
// putting the caller in the real, interactive agent terminal without leaving
// the hub session. A no-op when the cursor is on a group header/placeholder
// rather than an agent row. If the target window vanished between the last
// poll and this keypress (or any other tmux failure), records an inline
// error and triggers a refresh instead of crashing.
func (m *HerdModel) linkSelectedIntoHub() (tea.Model, tea.Cmd) {
	r := m.selectedRow()
	if r == nil {
		return *m, nil
	}
	if err := tmux.LinkWindowIntoHub(r.Session, r.WindowID); err != nil {
		m.jumpErr = err.Error()
		return *m, herdLoadCmd()
	}
	m.jumpErr = ""
	m.infoMsg = ""
	return *m, herdLinkedCmd()
}

// jumpToSelectedHome is J's action: switch the client to the selected row's
// window in its own home session — the pre-link-window jump behaviour,
// preserved for workflows that want the project's own session context
// rather than staying inside the hub. If the switch fails (e.g. the session
// died between the last poll and this keypress), records an inline error
// and triggers a refresh instead of crashing. A no-op when the cursor is on
// a group header/placeholder rather than an agent row.
func (m *HerdModel) jumpToSelectedHome() (tea.Model, tea.Cmd) {
	r := m.selectedRow()
	if r == nil {
		return *m, nil
	}
	if err := tmux.SwitchToSessionWindow(r.Session, r.WindowID); err != nil {
		m.jumpErr = err.Error()
		return *m, herdLoadCmd()
	}
	m.jumpErr = ""
	m.infoMsg = ""
	return *m, nil
}

// unlinkSelected is u's action: unlink the selected row's window from the
// hub session, leaving it exactly as-is in its home session (see
// tmux.UnlinkWindowFromHub — never passes -k, so this can never destroy a
// live agent). A no-op when the cursor is on a group header/placeholder.
//
// tmux itself refuses to unlink a window that was never linked into the hub
// to begin with ("window only linked to one session") — that refusal is
// expected and non-fatal, so it's surfaced via the dim infoMsg rather than
// the red jumpErr styling; it is not a real failure and does not trigger a
// reload.
func (m *HerdModel) unlinkSelected() (tea.Model, tea.Cmd) {
	r := m.selectedRow()
	if r == nil {
		return *m, nil
	}
	if err := tmux.UnlinkWindowFromHub(r.WindowID); err != nil {
		m.jumpErr = ""
		m.infoMsg = err.Error()
		return *m, herdLinkedCmd()
	}
	m.jumpErr = ""
	m.infoMsg = ""
	return *m, herdLinkedCmd()
}

// jumpToNextBlocked jumps to the first blocked agent across all sessions,
// using the same internal/herd ordering the view itself renders with, and
// moves the sidebar cursor to match so the highlighted row agrees with
// wherever the user just jumped.
func (m *HerdModel) jumpToNextBlocked() (tea.Model, tea.Cmd) {
	r := herd.FirstBlocked(m.groups)
	if r == nil {
		m.jumpErr = "no blocked agents"
		return *m, nil
	}
	if err := tmux.SwitchToSessionWindow(r.Session, r.WindowID); err != nil {
		m.jumpErr = err.Error()
		return *m, herdLoadCmd()
	}
	m.jumpErr = ""
	for i, l := range m.lines {
		if l.kind == herdLineRow && l.row.Session == r.Session && l.row.WindowID == r.WindowID {
			m.splitPane.SetCursor(i)
			break
		}
	}
	m.syncSelectionFromCursor()
	m.ensureCursorVisible()
	return *m, m.maybeCaptureCmd()
}

// ── Line model construction ──────────────────────────────────────────────────

// rebuildLines flattens m.groups into the scrollable line list. Called after
// every successful reload.
//
// A stale group whose snapshot still has last-known rows renders those rows
// (dimmed, via herdLine.stale) rather than a placeholder — a briefly-wedged
// monitor must not make a whole project's agents vanish from the hub. The
// "snapshot unavailable" placeholder is reserved for groups with no rows at
// all (snapshot missing/unreadable, or genuinely zero agents).
//
// A non-selectable herdLineDivider is inserted between adjacent groups —
// never before the first group or after the last — so the groups read as
// distinct blocks in the sidebar. Cursor movement (moveCursor) and selection
// re-resolution (resolveSelection) both skip herdLineDivider explicitly;
// see those functions for why a divider can never become the selection.
func (m *HerdModel) rebuildLines() {
	var lines []herdLine
	for i, g := range m.groups {
		if i > 0 {
			lines = append(lines, herdLine{kind: herdLineDivider})
		}
		lines = append(lines, herdLine{kind: herdLineHeader, group: g, stale: g.Stale})
		if len(g.Rows) == 0 {
			lines = append(lines, herdLine{kind: herdLinePlaceholder, group: g, stale: g.Stale})
			continue
		}
		for j, r := range g.Rows {
			lines = append(lines, herdLine{kind: herdLineRow, row: r, groupHead: j == 0, stale: g.Stale})
		}
	}
	m.lines = lines
}

// ── Rendering ────────────────────────────────────────────────────────────────

// contentWidth returns the usable content width based on terminal size.
func (m HerdModel) contentWidth() int {
	w := m.termWidth - 4
	if w < 40 {
		w = 40
	}
	return w
}

// View renders the hub as a full-screen (alt-screen) view.
func (m HerdModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m HerdModel) render() string {
	if !useSplitLayout(m.termWidth) {
		return m.renderFlat()
	}
	return m.renderSplit()
}

// useSplitLayout reports whether the sidebar + main panel layout is usable
// at the given terminal width. Below herdMinSplitWidth a sidebar would
// truncate every agent name and activity line into mush, so the original
// full-width flat list is strictly more usable than a split layout would
// be.
func useSplitLayout(width int) bool {
	return width >= herdMinSplitWidth
}

// renderHeader renders the title + subtitle chrome shared by both layouts.
func (m HerdModel) renderHeader(cw int) string {
	var b strings.Builder
	b.WriteString(herdTitleStyle.Width(cw).Render("agentctl hub"))
	b.WriteString("\n")

	total := 0
	for _, g := range m.groups {
		total += len(g.Rows)
	}
	b.WriteString(herdSubtitleStyle.Render(fmt.Sprintf("  %d session%s · %d agent%s", len(m.groups), plural(len(m.groups)), total, plural(total))))
	b.WriteString("\n\n")
	return b.String()
}

// renderStatusOrEmpty renders the loading/error/empty states shared by both
// layouts. Returns ("", false) when there's real data to render (the caller
// should proceed to render its list/split content instead).
func (m HerdModel) renderStatusOrEmpty() (string, bool) {
	switch {
	case !m.loaded:
		return herdDimStyle.Render("  loading…") + "\n", true
	case m.loadErr != nil:
		return herdErrStyle.Render(fmt.Sprintf("  error loading sessions: %v", m.loadErr)) + "\n", true
	case len(m.groups) == 0:
		return herdDimStyle.Render("  no agentctl sessions running") + "\n", true
	}
	return "", false
}

// renderFooter renders the jump-error line + hint bar shared by both layouts.
func (m HerdModel) renderFooter(cw int) string {
	var b strings.Builder
	b.WriteString("\n")
	switch {
	case m.jumpErr != "":
		b.WriteString(herdErrStyle.Render("  " + truncateToWidth(m.jumpErr, cw-2)))
	case m.infoMsg != "":
		// A benign, non-error outcome (e.g. tmux's expected refusal to
		// unlink a window that was never linked in) — dim, not red.
		b.WriteString(herdDimStyle.Render("  " + truncateToWidth(m.infoMsg, cw-2)))
	}
	b.WriteString("\n")

	hintParts := []string{
		menuHintKeyStyle.Render("j/k") + " " + menuHintLabelStyle.Render("navigate"),
		menuHintKeyStyle.Render("enter") + " " + menuHintLabelStyle.Render("open"),
		menuHintKeyStyle.Render("J") + " " + menuHintLabelStyle.Render("home"),
		menuHintKeyStyle.Render("u") + " " + menuHintLabelStyle.Render("unlink"),
		menuHintKeyStyle.Render("b") + " " + menuHintLabelStyle.Render("blocked"),
		menuHintKeyStyle.Render("r") + " " + menuHintLabelStyle.Render("refresh"),
		menuHintKeyStyle.Render("q") + " " + menuHintLabelStyle.Render("back"),
	}
	sep := "  " + menuHintDotStyle.Render("·") + "  "
	b.WriteString(tableStyles.HintBar.MarginTop(0).Width(cw).Render(strings.Join(hintParts, sep)))
	return b.String()
}

// renderFlat renders the original full-width flat list — the narrow-terminal
// fallback, and unchanged in spirit from the hub's pre-sidebar layout.
func (m HerdModel) renderFlat() string {
	var b strings.Builder
	cw := m.contentWidth()

	b.WriteString(m.renderHeader(cw))

	if status, isStatus := m.renderStatusOrEmpty(); isStatus {
		b.WriteString(status)
	} else {
		visible := m.visibleLines()
		start := m.scrollOffset
		end := start + visible
		if end > len(m.lines) {
			end = len(m.lines)
		}
		for i := start; i < end; i++ {
			b.WriteString(m.renderLine(i, cw))
			b.WriteString("\n")
		}
	}

	b.WriteString(m.renderFooter(cw))
	return b.String()
}

// renderSplit renders the sidebar + main panel layout via SplitPane in
// OrientHorizontal mode.
func (m HerdModel) renderSplit() string {
	var b strings.Builder
	cw := m.contentWidth()

	b.WriteString(m.renderHeader(cw))

	if status, isStatus := m.renderStatusOrEmpty(); isStatus {
		b.WriteString(status)
	} else {
		sp := m.splitPane // clone — View() must not mutate the receiver
		listContent := m.buildSidebarContent(sp.ListWidth())
		detailContent := m.buildMainPanelContent(sp.DetailWidth())
		sp.SetListContent(listContent, len(m.lines))
		sp.SetDetailContent(detailContent)
		b.WriteString(sp.View())
		b.WriteString("\n")
	}

	b.WriteString(m.renderFooter(cw))
	return b.String()
}

// buildSidebarContent renders every line in m.lines at the given sidebar
// width, for SplitPane's list content in split mode. Reuses the very same
// renderLine used by the flat fallback — the sidebar isn't a different
// rendering, just a narrower one — so truncation behaviour at a narrow
// width only needs to be right in one place.
func (m HerdModel) buildSidebarContent(width int) string {
	if len(m.lines) == 0 {
		return ""
	}
	lines := make([]string, len(m.lines))
	for i := range m.lines {
		lines[i] = m.renderLine(i, width)
	}
	return strings.Join(lines, "\n")
}

// buildMainPanelContent builds the main panel's content for whichever line
// is selected.
func (m HerdModel) buildMainPanelContent(width int) string {
	return mainPanelContent(m.lines, m.splitPane.Cursor(), m.preview, m.previewErr, width)
}

func (m HerdModel) renderLine(i int, cw int) string {
	l := m.lines[i]
	selected := i == m.splitPane.Cursor()
	switch l.kind {
	case herdLineHeader:
		return m.renderGroupHeader(l.group, selected, cw)
	case herdLinePlaceholder:
		marker := "    "
		if selected {
			marker = "  " + cursorActiveStyle.Render("▸") + " "
		}
		if l.group.Stale {
			return marker + herdDimStyle.Render("snapshot unavailable")
		}
		return marker + herdDimStyle.Render("no agents")
	case herdLineDivider:
		return renderHerdDivider(cw)
	default: // herdLineRow
		return m.renderRow(l.row, selected, l.stale, cw)
	}
}

// renderHerdDivider renders a non-selectable rule between two adjacent
// groups, inset to match the sidebar's content block: herdSidebarLeftInset
// blank columns on the left (the same column a group header's marker
// starts at) and herdSidebarRightGutter blank columns on the right (the
// same gutter content rows leave before the detail panel's vertical
// border), so the rule reads as part of the sidebar rather than colliding
// with the border in a T-junction. Below that combined inset+gutter width
// there's no room to reserve either margin, so it falls back to a plain
// full-width rule (still exactly cw wide) rather than disappearing.
//
// The returned string always measures exactly cw columns wide (measured
// with lipgloss.Width, never len, so it stays exact under the dim style's
// SGR codes). Never rendered before the first group or after the last —
// see rebuildLines.
func renderHerdDivider(cw int) string {
	if cw <= 0 {
		return ""
	}
	if cw < herdSidebarLeftInset+herdSidebarRightGutter {
		return herdDimStyle.Render(strings.Repeat("─", cw))
	}
	dashW := cw - herdSidebarLeftInset - herdSidebarRightGutter
	return strings.Repeat(" ", herdSidebarLeftInset) +
		herdDimStyle.Render(strings.Repeat("─", dashW)) +
		strings.Repeat(" ", herdSidebarRightGutter)
}

// rollupGlyph returns the glyph and style for a group's rolled-up status,
// reusing the same state colours as the per-session chrome bar's agent tabs.
func rollupGlyph(r herd.RollupStatus) (string, lipgloss.Style) {
	switch r {
	case herd.RollupBlocked:
		return "⏸", herdBlockedStyle
	case herd.RollupWorking:
		return "●", herdWorkingStyle
	case herd.RollupDone:
		return "✓", herdDoneStyle
	default:
		return "○", herdIdleStyle
	}
}

// rowGlyph returns the glyph and style for a single agent row, matching the
// window-rename convention in monitor.go's applyVisuals: ● working,
// ⏸ blocked, ✓ done (unseen), bare/○ otherwise.
func rowGlyph(r herd.Row) (string, lipgloss.Style) {
	switch {
	case r.State == "blocked":
		return "⏸", herdBlockedStyle
	case r.State == "working":
		return "●", herdWorkingStyle
	case r.Done:
		return "✓", herdDoneStyle
	default:
		return "○", herdIdleStyle
	}
}

// herdStaleMarker is the dim "stale" tag appended to a stale group's header.
const herdStaleMarker = "stale"

// renderGroupHeader renders a session's group header line, now selectable
// (a group header/placeholder can be the cursor's target, showing that
// session's summary in the main panel) — hence the cursor marker, matching
// renderRow's convention.
func (m HerdModel) renderGroupHeader(g herd.Group, selected bool, cw int) string {
	glyph, style := rollupGlyph(g.Rollup)
	count := fmt.Sprintf("(%d agent%s)", len(g.Rows), plural(len(g.Rows)))
	marker := strings.Repeat(" ", herdSidebarLeftInset)
	if selected {
		marker = " " + cursorActiveStyle.Render("▸") + " "
	}
	line := fmt.Sprintf("%s%s  %s  %s  %s",
		marker,
		style.Render(glyph),
		herdSessionStyle.Render(g.Session),
		herdProjectStyle.Render(g.Project),
		herdCountStyle.Render(count),
	)
	if g.Stale {
		line += "  " + herdDimStyle.Render(herdStaleMarker)
	}
	line = truncateToWidth(line, cw)
	if selected {
		return herdSelectedStyle.Width(cw).Render(line)
	}
	return line
}

// herdLinkedGlyph marks an agent row whose window is currently linked into
// the hub session — open, and reachable with a plain window switch, not
// just a jump into another session. Rendered dim so it doesn't compete with
// the state glyph for attention.
const herdLinkedGlyph = "↗"

// rowIsLinked reports whether an agent row's window is currently linked
// into the hub session, per linkedWindowIDs — populated every poll tick
// from tmux.ListHubWindowIDs. tmux is always the source of truth for link
// state; nothing about it is ever stored or cached beyond that one map. A
// nil/empty map (e.g. before the hub's first tick, or a momentarily
// unreadable hub session) means "nothing known to be linked", never a
// crash or a false positive.
func rowIsLinked(r herd.Row, linkedWindowIDs map[string]bool) bool {
	if len(linkedWindowIDs) == 0 {
		return false
	}
	return linkedWindowIDs[r.WindowID]
}

// renderRow renders one agent row. When stale is true (the owning group's
// snapshot has gone stale but retained its last-known rows) the glyph, name,
// and activity are all dimmed — but the row stays selectable and jumpable:
// the tmux window is very likely still there even though the monitor's
// snapshot is momentarily out of date.
func (m HerdModel) renderRow(r herd.Row, selected bool, stale bool, cw int) string {
	glyph, style := rowGlyph(r)
	if stale {
		style = herdDimStyle
	}
	cursor := "   "
	if selected {
		cursor = " " + cursorActiveStyle.Render("▸") + " "
	}

	nameStyle := herdNameStyle
	activityStyle := herdActivityStyle
	if stale {
		nameStyle = herdDimStyle
		activityStyle = herdDimStyle
	}

	linkedMarker := ""
	if rowIsLinked(r, m.linkedWindowIDs) {
		linkedMarker = " " + herdDimStyle.Render(herdLinkedGlyph)
	}

	// Reserve room for the cursor marker, glyph, linked marker, and
	// separators; whatever's left is split between the name and the
	// activity line. The name is capped at herdMaxNameWidth even when
	// there's more room, so a long instance name can't crowd out the
	// activity line in a wide flat view, and is truncated to whatever's
	// actually available in a narrow sidebar. All measured with
	// lipgloss.Width, never len, since ANSI styling and any multi-byte
	// runes would otherwise miscount and overflow the terminal width.
	glyphStr := style.Render(glyph)
	overhead := lipgloss.Width(cursor) + lipgloss.Width(glyphStr) + 1 /* space */ + lipgloss.Width(linkedMarker) + 2 /* gap before activity */
	nameBudget := cw - overhead
	if nameBudget > herdMaxNameWidth {
		nameBudget = herdMaxNameWidth
	}
	if nameBudget < 0 {
		nameBudget = 0
	}
	name := truncateToWidth(r.Name, nameBudget)

	prefix := cursor + glyphStr + " " + nameStyle.Render(name) + linkedMarker + "  "
	prefixW := lipgloss.Width(prefix)
	available := cw - prefixW - herdSidebarRightGutter
	if available < 0 {
		available = 0
	}
	activity := truncateToWidth(herd.ActivityLine(r), available)

	line := prefix + activityStyle.Render(activity)
	if selected {
		return herdSelectedStyle.Width(cw).Render(line)
	}
	return line
}

// truncateToWidth shortens s to at most width display columns (as measured
// by lipgloss.Width, which is ANSI- and wide-rune-aware), appending "…" when
// truncated. Returns s unchanged if it already fits or width <= 0.
func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)) > width-1 {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ── Main panel content mapping ───────────────────────────────────────────────

// mainPanelContent builds the main panel's content for whichever line is
// currently selected in the sidebar:
//   - an agent row → its metadata (state, model, context %, tokens, ticket,
//     activity) plus the already-captured live pane preview (this function
//     does no I/O itself — preview/previewErr are supplied by the caller)
//   - a group header or placeholder → a session-level summary (project,
//     agent count, rolled-up state breakdown)
//   - neither (no lines, or the cursor doesn't land on anything) → a
//     "nothing selected" placeholder
//
// Kept free of any Bubble Tea / tmux dependency so it's directly
// table-driven testable with no TTY.
func mainPanelContent(lines []herdLine, cursor int, preview, previewErr string, width int) string {
	if cursor < 0 || cursor >= len(lines) {
		return herdDimStyle.Render("  nothing selected")
	}
	l := lines[cursor]
	if l.kind == herdLineRow {
		return renderAgentDetail(l.row, preview, previewErr, width)
	}
	return renderGroupSummary(l.group)
}

// detailLabel renders one "Label: value" metadata line for the main panel,
// with a fixed label column width so values line up.
func detailLabel(labelWidth int, k, v string) string {
	return fmt.Sprintf("%s %s\n", tableStyles.DetailLabel.Width(labelWidth).Render(k+":"), tableStyles.DetailValue.Render(v))
}

// stateDisplay returns a human-friendly label for an agent row's state.
func stateDisplay(r herd.Row) string {
	switch {
	case r.State == "blocked":
		return "blocked"
	case r.State == "working":
		return "working"
	case r.Done:
		return "done (unseen)"
	default:
		return "idle"
	}
}

// renderAgentDetail renders the selected agent's metadata plus its live
// terminal preview.
func renderAgentDetail(r herd.Row, preview, previewErr string, width int) string {
	var b strings.Builder

	b.WriteString(tableStyles.DetailSection.Render(r.Name))
	b.WriteString("\n\n")

	const labelW = 12
	b.WriteString(detailLabel(labelW, "State", stateDisplay(r)))
	if r.ModelName != "" {
		b.WriteString(detailLabel(labelW, "Model", r.ModelName))
	}
	b.WriteString(detailLabel(labelW, "Context", fmt.Sprintf("%d%%", r.ContextPct)))
	b.WriteString(detailLabel(labelW, "Tokens", fmt.Sprintf("%s in / %s out", formatTokenCount(r.InputTokens), formatTokenCount(r.OutputTokens))))
	if r.Ticket > 0 {
		b.WriteString(detailLabel(labelW, "Ticket", fmt.Sprintf("#%d", r.Ticket)))
	}
	if activity := herd.ActivityLine(r); activity != "" {
		b.WriteString(detailLabel(labelW, "Activity", truncateToWidth(activity, width-labelW-2)))
	}

	b.WriteString("\n")
	b.WriteString(tableStyles.DetailSection.Render("Terminal"))
	b.WriteString("\n")
	switch {
	case previewErr != "":
		b.WriteString(herdErrStyle.Render("  preview unavailable: " + previewErr))
	case strings.TrimSpace(preview) == "":
		b.WriteString(herdDimStyle.Render("  (no output yet)"))
	default:
		b.WriteString(preview)
	}

	return b.String()
}

// stateBreakdown counts rows in each state category, using the same
// classification as rowGlyph/renderRow: blocked, working, done (unseen), or
// idle.
func stateBreakdown(rows []herd.Row) (blocked, working, done, idle int) {
	for _, r := range rows {
		switch {
		case r.State == "blocked":
			blocked++
		case r.State == "working":
			working++
		case r.Done:
			done++
		default:
			idle++
		}
	}
	return blocked, working, done, idle
}

// renderGroupSummary renders a session-level summary for the main panel:
// project path, agent count, and the rolled-up state breakdown.
func renderGroupSummary(g herd.Group) string {
	var b strings.Builder

	b.WriteString(tableStyles.DetailSection.Render(g.Session))
	b.WriteString("\n\n")

	const labelW = 14
	b.WriteString(detailLabel(labelW, "Project", g.Project))
	b.WriteString(detailLabel(labelW, "Agents", fmt.Sprintf("%d", len(g.Rows))))

	blocked, working, done, idle := stateBreakdown(g.Rows)
	b.WriteString(detailLabel(labelW, "Blocked", fmt.Sprintf("%d", blocked)))
	b.WriteString(detailLabel(labelW, "Working", fmt.Sprintf("%d", working)))
	b.WriteString(detailLabel(labelW, "Done", fmt.Sprintf("%d", done)))
	b.WriteString(detailLabel(labelW, "Idle", fmt.Sprintf("%d", idle)))

	if g.Stale {
		b.WriteString("\n")
		b.WriteString(herdDimStyle.Render("  snapshot stale"))
	}

	return b.String()
}
