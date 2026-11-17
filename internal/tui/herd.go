package tui

import (
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/hubtarget"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/selfexec"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/tmux"
)

// herdPollInterval is the hub's refresh cadence; polling faster than ~1s
// buys nothing (per the design doc).
const herdPollInterval = 1 * time.Second

// herdChromeRows is the number of non-list lines reserved above and below
// the scrollable line list: title(1) + blank(1) + subtitle(1) + blank(1)
// above; blank(1) + error-line(1) + hint-bar(1) below.
const herdChromeRows = 7

// herdSidebarRatio is the fraction of content width the list column takes
// (see herdLiveLeftCols); herdMinSidebarWidth / herdMinMainPanelWidth are the
// SplitPane minimums, which here only feed its cursor bookkeeping.
const (
	herdSidebarRatio      = 0.3
	herdMinSidebarWidth   = 28
	herdMinMainPanelWidth = 40
)

// herdMaxNameWidth caps how much of a row's width the agent name can claim,
// even in the wide flat-list fallback, so the recap always keeps some room
// and a long name can't crowd it out entirely.
const herdMaxNameWidth = 24

// herdSidebarLeftInset is the left margin sidebar content lines start at —
// specifically the width of a group header's unselected cursor marker (see
// renderGroupHeader). herdLineDivider uses the same constant so a divider
// between two groups starts at the same column as the headers it separates,
// instead of running flush to the sidebar's left edge.
const herdSidebarLeftInset = 2

// herdSidebarRightGutter is the number of blank columns sidebar content lines
// reserve at their right edge so they never touch the hub pane's edge (see
// renderRow's "available" calculation). The close button, the "+ new"
// button and the divider all end this many columns short of the pane width.
const herdSidebarRightGutter = 1

// ── Styles ───────────────────────────────────────────────────────────────────

var (
	herdTitleStyle    lipgloss.Style
	herdSubtitleStyle lipgloss.Style
	herdDimStyle      lipgloss.Style
	herdErrStyle      lipgloss.Style
	herdSelectedStyle lipgloss.Style
	herdHoverStyle    lipgloss.Style // row tint, weaker than herdSelectedStyle

	herdCloseHoverStyle lipgloss.Style // ✕ under the pointer
	herdNewHoverStyle   lipgloss.Style // "+ new" under the pointer

	herdWorkingStyle lipgloss.Style
	herdBlockedStyle lipgloss.Style
	herdDoneStyle    lipgloss.Style
	herdIdleStyle    lipgloss.Style

	herdProjectStyle  lipgloss.Style
	herdCountStyle    lipgloss.Style
	herdActivityStyle lipgloss.Style
	herdRecapStyle    lipgloss.Style

	// Recap styles for a hovered row: the theme's primary text colour, with
	// the activity fallback still italic so its source stays distinguishable.
	herdRecapHoverStyle    lipgloss.Style
	herdActivityHoverStyle lipgloss.Style

	// herdPaletteColors is the stable accent palette (theme colours) that
	// project groups and agent types hash into.
	herdPaletteColors []string
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
	herdHoverStyle = lipgloss.NewStyle().Background(lipgloss.Color(midColor(t.Base, t.Surface)))
	herdCloseHoverStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Red))
	herdNewHoverStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Accent))

	herdWorkingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent))
	herdBlockedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Yellow))
	herdDoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green))
	herdIdleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))

	herdProjectStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Sky))
	herdCountStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	herdActivityStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	herdRecapStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))

	herdRecapHoverStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	herdActivityHoverStyle = herdRecapHoverStyle.Italic(true)

	herdPaletteColors = []string{t.Accent, t.Sky, t.Blue, t.Info, t.Orange, t.Green}
}

// midColor is the midpoint of two #rrggbb colours, so the hover tint sits
// between the base background and the selection highlight. Falls back to b
// for anything that is not a 6-digit hex colour.
func midColor(a, b string) string {
	parse := func(c string) (r, g, bl int64, ok bool) {
		if len(c) != 7 || c[0] != '#' {
			return 0, 0, 0, false
		}
		v, err := strconv.ParseInt(c[1:], 16, 32)
		if err != nil {
			return 0, 0, 0, false
		}
		return v >> 16 & 0xff, v >> 8 & 0xff, v & 0xff, true
	}
	ar, ag, ab, ok1 := parse(a)
	br, bg, bb, ok2 := parse(b)
	if !ok1 || !ok2 {
		return b
	}
	return fmt.Sprintf("#%02x%02x%02x", (ar+br)/2, (ag+bg)/2, (ab+bb)/2)
}

// herdPalette returns the accent palette, falling back to the active theme
// if styles have not been initialised.
func herdPalette() []string {
	if len(herdPaletteColors) == 0 {
		t := theme.Current()
		return []string{t.Accent, t.Sky, t.Blue, t.Info, t.Orange, t.Green}
	}
	return herdPaletteColors
}

// herdColorFor picks a stable palette colour for name: the same name always
// maps to the same colour.
func herdColorFor(name string) string {
	pal := herdPalette()
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return pal[int(h.Sum32()%uint32(len(pal)))]
}

// splitAgentName splits "go-engineer:2" into ("go-engineer", ":2"). Names
// without a numeric ":N" suffix return the whole name and "".
func splitAgentName(name string) (base, suffix string) {
	i := strings.LastIndex(name, ":")
	if i <= 0 || i == len(name)-1 {
		return name, ""
	}
	for _, c := range name[i+1:] {
		if c < '0' || c > '9' {
			return name, ""
		}
	}
	return name[:i], name[i:]
}

// renderAgentName colours the agent type by hash and keeps ":N" muted. The
// caller passes the already-truncated plain name. A blocked row gets a bold warning colour so it stands out.
func renderAgentName(name string, blocked bool) string {
	return renderAgentNameHover(name, blocked, false)
}

// renderAgentNameHover is renderAgentName; a hovered name is also bold.
func renderAgentNameHover(name string, blocked, hovered bool) string {
	base, suffix := splitAgentName(name)
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(herdColorFor(base)))
	if blocked {
		style = herdBlockedStyle.Bold(true)
	}
	if hovered {
		style = style.Bold(true)
	}
	out := style.Render(base)
	if suffix != "" {
		out += herdCountStyle.Render(suffix)
	}
	return out
}

// ── Messages ─────────────────────────────────────────────────────────────────

// herdTickMsg drives the hub's poll loop.
type herdTickMsg struct{}

// herdLoadedMsg carries the result of an async herd.Load() call.
type herdLoadedMsg struct {
	groups []herd.Group
	err    error
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
	herdLineSubheader                       // a non-selectable "dispatched (N)" label
)

// herdLine is one renderable line. Every kind except herdLineDivider is also
// selectable — see the "selection" section below.
type herdLine struct {
	kind       herdLineKind
	group      herd.Group // set for herdLineHeader / herdLinePlaceholder
	row        herd.Row   // set for herdLineRow
	groupHead  bool       // true for the first row of a group (blank line above)
	label      string     // set for herdLineSubheader, e.g. "dispatched (2)"
	treePrefix string     // connector prefix for herdLineRow ("" for roots)
	// nameCol is the group's uncapped name-column width (tree prefix plus
	// base name, max over its rows) for herdLineRow; renderTreeRow caps it
	// at render time so recaps line up within a group.
	nameCol int
}

// selectable reports whether the cursor may rest on this line. Dividers and
// sub-section labels are decoration only.
func (l herdLine) selectable() bool {
	return l.kind != herdLineDivider && l.kind != herdLineSubheader
}

// ── HerdModel ────────────────────────────────────────────────────────────────

// HerdModel is the full-screen Bubble Tea model for the herd hub: the
// cross-session aggregator that runs as the sole window of the agentctl-hub
// tmux session. Data is loaded from internal/herd, which does all the
// registry/tmux/state.json I/O; this model renders the list and drives the
// live agent pane.
//
// Layout: the hub window's left column is this list (session/agent rows); the
// right column is a nested tmux client on a grouped viewer session showing the
// selected agent live (see internal/tmux/hubview.go). There is no other view.
//
// Every line in m.lines (group headers, placeholders, and agent rows alike) is
// selectable. Selection is tracked by SplitPane's cursor, and resolved after
// each reload by (session, windowID) — or by session alone for a header —
// rather than by raw index, so a reorder from a state change never moves the
// cursor out from under the user. Only agent rows drive the live pane: a
// header or placeholder leaves the last viewed agent on screen.
type HerdModel struct {
	termWidth  int
	termHeight int

	groups []herd.Group
	lines  []herdLine

	// splitPane owns cursor position and scrolling bookkeeping. HerdModel does
	// NOT delegate key handling to SplitPane.Update(): the list mixes
	// selectable headers and rows, which is simpler to express directly in
	// handleKey/moveCursor below.
	splitPane SplitPane

	// scrollOffset is the list's vertical scroll position.
	scrollOffset int

	// selSession/selWindowID/selIsHeader identify the selection by
	// (session, windowID) — or by session alone when a header/placeholder
	// is selected — rather than by raw index, so a refresh that reorders
	// lines (a state change) doesn't move the cursor out from under the
	// user.
	selSession  string
	selWindowID string
	selIsHeader bool

	loaded  bool
	loadErr error
	jumpErr string // a real failure: styled as an error, triggers a refresh

	// infoMsg is a benign, non-error inline message. Rendered alongside
	// jumpErr but without the error styling; see renderFooter.
	infoMsg string

	// confirmSession/confirmWindow identify the row awaiting inline close
	// confirmation ("close <name>? y/n"); empty when none. Cleared whenever
	// the selection moves off that row.
	confirmSession string
	confirmWindow  string

	// closePending remembers where a just-closed row sat so the next reload
	// can select its neighbour instead of jumping to the first row. Cleared
	// after one use.
	closePending *herdClosePending

	// menuOpen/menuLine: the right-click context menu (rendered in the footer)
	// and the line it was opened on.
	menuOpen bool
	menuLine int

	// liveShown is the "session|window" last handed to the viewer, so
	// selection changes (not every tick) drive it; a failed attempt is retried
	// only once the selection changes. liveLeft/liveRight are the column split
	// of the hub window (list column / live pane).
	liveShown string
	// viewerSeen is the "session|window" the viewer last reported on a poll;
	// a change relative to it that the hub did not cause drives the selection
	// (see viewerSyncTarget).
	viewerSeen string
	liveLeft   int
	liveRight  int

	// liveWinWidth is the hub window width the split was last applied for;
	// a list-pane resize at the same window width is a divider drag, not a
	// window resize. userLeft is the list width the user dragged to (0 = none),
	// preserved across window resizes.
	liveWinWidth int
	userLeft     int

	// pendingFocus is a just-launched window to select once it appears in a
	// reload. Cleared on success, give-up, manual selection, or replaced by a
	// newer launch.
	pendingFocus *herdPendingFocus

	// hover is the line/region under the pointer (see herd_mouse.go);
	// hoverX/hoverY/hasPointer remember the last pointer cell so the target
	// can be recomputed when the content moves under a still pointer.
	hover      herdHover
	hoverX     int
	hoverY     int
	hasPointer bool

	// card is the visible hover card (see herd_card.go); hoverGen invalidates
	// pending dwell ticks; cardCache holds parsed log stats by log path.
	card      *herdCard
	hoverGen  int
	cardCache map[string]cardCacheEntry

	quitting bool

	// exeChanged reports whether the executable on disk was replaced since
	// start (nil disables hot reload). reexec is set once it did; the model
	// then quits and the caller execs the new binary (see Reexec).
	exeChanged func() bool
	reexec     bool

	// hubChrome reports whether agent windows show their chrome bar in the
	// live pane. false (the hub_chrome default) zooms the viewed window's main
	// pane so its chrome bar is hidden (see tmux.SyncHubZoom); `c` toggles it.
	hubChrome bool
}

// HubSelectEnv carries the selected agent ("session|windowID") across a hot
// reload re-exec. It is consumed once at startup.
const HubSelectEnv = "AGENTCTL_HUB_SELECT"

// HubLeftEnv carries the user-dragged list width across a hot reload re-exec.
// It is consumed once at startup.
const HubLeftEnv = "AGENTCTL_HUB_LEFT"

// HubChromeEnv carries the hub's chrome toggle ("1" shown, "0" hidden) across
// a hot reload re-exec. It is consumed once at startup.
const HubChromeEnv = "AGENTCTL_HUB_CHROME"

// parseHubChrome parses a HubChromeEnv value; ok is false for anything but
// "1" or "0".
func parseHubChrome(v string) (shown, ok bool) {
	switch strings.TrimSpace(v) {
	case "1":
		return true, true
	case "0":
		return false, true
	}
	return false, false
}

// parseHubLeft parses a HubLeftEnv value; ok is false for garbage or
// non-positive values.
func parseHubLeft(v string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// ReexecEnv returns the env entries to carry across a hot reload re-exec: the
// selection, the chrome toggle and, when the user dragged the divider, the
// list width.
func (m HerdModel) ReexecEnv() []string {
	var env []string
	if sel := m.SelectionEnv(); sel != "" {
		env = append(env, sel)
	}
	if m.userLeft > 0 {
		env = append(env, HubLeftEnv+"="+strconv.Itoa(m.userLeft))
	}
	chrome := "0"
	if m.hubChrome {
		chrome = "1"
	}
	env = append(env, HubChromeEnv+"="+chrome)
	return env
}

// parseHubSelect splits a HubSelectEnv value into session and window ID.
func parseHubSelect(v string) (session, windowID string, ok bool) {
	session, windowID, ok = strings.Cut(v, "|")
	if !ok || session == "" || windowID == "" {
		return "", "", false
	}
	return session, windowID, true
}

// Reexec reports whether the model quit because the binary on disk changed.
func (m HerdModel) Reexec() bool { return m.reexec }

// WithoutReload returns the model reset to keep running after a failed
// re-exec: hot reload is disabled and the current selection is re-applied once
// the list loads.
func (m HerdModel) WithoutReload() HerdModel {
	m.exeChanged, m.reexec = nil, false
	m.pendingFocus = nil
	if m.selSession != "" && m.selWindowID != "" && !m.selIsHeader {
		m.pendingFocus = &herdPendingFocus{session: m.selSession, windowID: m.selWindowID}
	}
	m.loaded = false
	return m
}

// SelectionEnv returns the HubSelectEnv entry ("KEY=session|windowID") for the
// current selection, or "" when no agent row is selected.
func (m HerdModel) SelectionEnv() string {
	if m.selIsHeader || m.selSession == "" || m.selWindowID == "" {
		return ""
	}
	return HubSelectEnv + "=" + m.selSession + "|" + m.selWindowID
}

// NewHerdModel creates the hub aggregator model. Data is loaded
// asynchronously via Init().
func NewHerdModel() HerdModel {
	m := newHerdModel()
	if id, err := selfexec.Capture(); err != nil {
		logging.Log("hub: hot reload disabled: %v", err)
	} else {
		m.exeChanged = func() bool { return selfexec.CheckChanged(id, time.Now()) }
	}
	if v, ok := os.LookupEnv(HubSelectEnv); ok {
		_ = os.Unsetenv(HubSelectEnv) // consume once; don't leak to child processes
		if session, win, ok := parseHubSelect(v); ok {
			m.pendingFocus = &herdPendingFocus{session: session, windowID: win}
		}
	}
	if v, ok := os.LookupEnv(HubLeftEnv); ok {
		_ = os.Unsetenv(HubLeftEnv)
		if n, ok := parseHubLeft(v); ok {
			m.userLeft = n
		}
	}
	m.hubChrome = hubChromeDefault()
	if v, ok := os.LookupEnv(HubChromeEnv); ok {
		_ = os.Unsetenv(HubChromeEnv)
		if shown, ok := parseHubChrome(v); ok {
			m.hubChrome = shown
		}
	}
	return m
}

func newHerdModel() HerdModel {
	const defaultWidth = 120
	left, right, _ := herdLiveSplit(defaultWidth)
	return HerdModel{
		termWidth:  defaultWidth,
		termHeight: 40,
		liveLeft:   left,
		liveRight:  right,
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
	return tea.Batch(herdLoadCmd(), herdTickCmd(), herdZoomSyncCmd(m.hubChrome))
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

// maybeShowCmd hands the selected agent to the live viewer, or returns nil if
// nothing selectable is an agent row (a header, placeholder, or empty hub is
// selected): the live pane then keeps showing the last viewed agent.
func (m *HerdModel) maybeShowCmd() tea.Cmd {
	if m.selIsHeader || m.selSession == "" || m.selWindowID == "" {
		return nil
	}
	return m.liveShowCmd()
}

// Update handles Bubble Tea messages.
func (m HerdModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.configureSplitPane()
		return m, herdProbeCmd(msg.Width)

	case herdProbeMsg:
		d := decideLiveResize(msg.win, m.liveWinWidth, m.liveLeft, msg.pane, m.userLeft)
		m.liveWinWidth, m.userLeft = d.winWidth, d.userLeft
		if d.err != nil {
			m.jumpErr = d.err.Error()
		}
		if d.left > 0 {
			m.liveLeft, m.liveRight = d.left, d.right
			m.configureSplitPane()
		}
		if !d.apply {
			return m, nil
		}
		left, right := d.left, d.right
		return m, func() tea.Msg {
			return herdLiveSizedMsg{left: left, right: right, err: tmux.ResizeLiveViewer(right)}
		}

	case herdTickMsg:
		if m.exeChanged != nil && m.exeChanged() {
			m.reexec = true
			return m, tea.Quit
		}
		show := m.maybeShowCmd()
		return m, tea.Batch(herdLoadCmd(), herdTickCmd(), show, herdViewerPollCmd(), herdZoomSyncCmd(m.hubChrome))

	case herdViewerMsg:
		return m.syncFromViewer(msg), nil

	case herdFocusErrMsg:
		m.jumpErr = msg.err.Error()
		return m, nil
	case herdLiveMsg:
		if msg.err != nil {
			// No fallback: surface the error; the next selection change retries.
			m.jumpErr = msg.err.Error()
			return m, func() tea.Msg {
				_ = tmux.HideLiveViewer() // drop any half-built viewer
				return nil
			}
		}
		m.jumpErr = ""
		m.liveLeft, m.liveRight = msg.left, msg.right
		if msg.win > 0 {
			m.liveWinWidth = msg.win
		}
		m.configureSplitPane()
		return m, nil

	case herdLiveSizedMsg:
		if msg.err != nil {
			m.jumpErr = msg.err.Error()
			return m, nil
		}
		m.liveLeft, m.liveRight = msg.left, msg.right
		m.configureSplitPane()
		return m, nil

	case herdLaunchedMsg:
		if msg.err != nil {
			m.infoMsg = ""
			m.jumpErr = msg.err.Error()
			return m, nil
		}
		m.jumpErr = ""
		m.infoMsg = "launched new agent in " + msg.session
		m.pendingFocus = nil
		if msg.windowID == "" {
			return m, herdLoadCmd()
		}
		m.pendingFocus = &herdPendingFocus{session: msg.session, windowID: msg.windowID}
		return m, tea.Batch(herdLoadCmd(), herdFocusRetryCmd(msg.session, msg.windowID))

	case herdFocusRetryMsg:
		return m, m.focusRetry(msg)

	case herdClosedMsg:
		if msg.err != nil {
			m.closePending = nil
			m.infoMsg = ""
			m.jumpErr = msg.err.Error()
			return m, herdLoadCmd()
		}
		m.jumpErr = ""
		if m.closePending != nil {
			m.closePending.killed = true
		}
		m.infoMsg = "closed " + msg.name + " (worktree kept; see agentctl sweep)"
		return m, herdLoadCmd()

	case herdLoadedMsg:
		m.loaded = true
		m.loadErr = msg.err
		if msg.err == nil {
			m.groups = msg.groups
			m.rebuildLines()
			m.refreshHover()
			m.applyClosePending()
			m.resolveSelection()
			m.applyPendingFocus()
		}
		show := m.maybeShowCmd()
		return m, show

	case tea.MouseClickMsg:
		m.hideCard()
		return m.handleClick(msg)

	case tea.MouseWheelMsg:
		m.hideCard()
		return m, nil

	case herdCardTickMsg:
		return m.handleCardTick(msg)

	case herdCardStatsMsg:
		m.storeCardStats(msg)
		return m, nil

	case tea.MouseMotionMsg:
		return m.handleMotion(msg)

	case tea.KeyPressMsg:
		m.hideCard()
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *HerdModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.menuOpen {
		key := msg.String()
		if key == "esc" || key == "q" {
			m.menuOpen = false
			return *m, nil
		}
		for _, it := range m.herdMenuItems(m.menuLine) {
			if it.key == key {
				return *m, m.runMenuKey(key)
			}
		}
		m.menuOpen = false
		return *m, nil
	}
	if m.closeArmed() {
		switch msg.String() {
		case "y", "Y":
			return *m, m.confirmClose()
		case "n", "N", "esc":
			m.cancelClose()
			return *m, nil
		}
	}
	switch msg.String() {
	case "n":
		return *m, m.launchForLine(m.splitPane.Cursor())

	case "x":
		m.startClose(m.splitPane.Cursor())
		return *m, nil

	case "p":
		return *m, m.openSelectedPR()

	case "ctrl+c":
		m.quitting = true
		return *m, tea.Quit

	case "q", "esc":
		home, _ := tmux.ViewerActiveWindow()
		hubtarget.LeaveHub(herdLeaver{m: m, home: home}, "")
		return *m, nil

	case "j", "down":
		m.moveCursor(1)
		show := m.maybeShowCmd()
		return *m, show

	case "k", "up":
		m.moveCursor(-1)
		show := m.maybeShowCmd()
		return *m, show

	case "enter":
		if m.selectedRow() == nil {
			return *m, nil
		}
		return m.focusLive()

	case "tab":
		return m.focusLive()

	case "J":
		return m.jumpToSelectedHome()

	case "b":
		return m.jumpToNextBlocked()

	case "r":
		m.jumpErr = ""
		m.infoMsg = ""
		return *m, herdLoadCmd()

	case "c":
		return *m, m.toggleChrome()
	}
	return *m, nil
}

// herdLiveMsg reports the outcome of handing a selection to the live viewer,
// along with the column split that was applied.
type herdLiveMsg struct {
	key         string
	win         int // hub window width the split was computed for
	left, right int
	err         error
}

// herdLiveSizedMsg reports the outcome of re-applying the live split sizes.
type herdLiveSizedMsg struct {
	left, right int
	err         error
}

// Live-mode list column bounds. The list column is the sidebar width the
// split layout would use (herdSidebarRatio of the content width) plus the
// content margin, floored so rows stay readable and capped so a very wide
// terminal does not hand the list most of the screen.
const (
	herdLiveMinLeft  = 44
	herdLiveMaxLeft  = 56
	herdLiveMinRight = 20
)

// herdLiveLeftCols is the width of the list column for a hub window
// totalWidth columns wide.
func herdLiveLeftCols(totalWidth int) int {
	left := int(float64(totalWidth-4)*herdSidebarRatio) + 4
	if left < herdLiveMinLeft {
		left = herdLiveMinLeft
	}
	if left > herdLiveMaxLeft {
		left = herdLiveMaxLeft
	}
	return left
}

// herdLiveUserMinLeft is the narrowest list column a mouse drag may leave.
const herdLiveUserMinLeft = 30

// clampUserLeft bounds a user-chosen list width to [herdLiveUserMinLeft,
// totalWidth-herdLiveMinRight-1] so the live pane always stays usable.
func clampUserLeft(left, totalWidth int) int {
	maxLeft := totalWidth - herdLiveMinRight - 1
	if left > maxLeft {
		left = maxLeft
	}
	if left < herdLiveUserMinLeft {
		left = herdLiveUserMinLeft
	}
	return left
}

// herdLiveSplit is herdLiveSplitFor with no user-chosen width.
func herdLiveSplit(totalWidth int) (left, right int, err error) {
	return herdLiveSplitFor(totalWidth, 0)
}

// herdLiveSplitFor splits a window totalWidth columns wide into the list
// column and the live pane (one column goes to the pane border). userLeft, when
// positive, is the list width the user dragged the divider to and replaces the
// computed default (clamped). err is set when the live pane would be unusably
// narrow.
func herdLiveSplitFor(totalWidth, userLeft int) (left, right int, err error) {
	left = herdLiveLeftCols(totalWidth)
	if userLeft > 0 {
		left = clampUserLeft(userLeft, totalWidth)
	}
	right = totalWidth - left - 1
	if right < herdLiveMinRight {
		return left, right, fmt.Errorf("terminal too narrow for live pane")
	}
	return left, right, nil
}

// herdResizeDecision is the outcome of decideLiveResize.
type herdResizeDecision struct {
	apply    bool // tmux must resize the live pane to right columns
	left     int  // list column width now in effect (0 = unchanged)
	right    int
	userLeft int // remembered user-chosen list width (0 = none)
	winWidth int // hub window width now considered applied
	err      error
}

// decideLiveResize decides what a list-pane resize means. winWidth is the hub
// window's current width (0 = unknown), lastWin the window width last applied,
// appliedLeft the list width last applied, paneWidth the list pane's width as
// the TUI just saw it, userLeft the remembered user-chosen list width.
//
//   - window width changed: re-apply the split, keeping userLeft if set;
//   - window width unchanged and the pane differs from what we applied: the
//     user dragged the divider; remember it (clamped) and leave tmux alone
//     unless clamping moved it;
//   - otherwise nothing.
func decideLiveResize(winWidth, lastWin, appliedLeft, paneWidth, userLeft int) herdResizeDecision {
	d := herdResizeDecision{userLeft: userLeft, winWidth: lastWin}
	if winWidth <= 0 {
		return d
	}
	if winWidth != lastWin {
		left, right, err := herdLiveSplitFor(winWidth, userLeft)
		d.winWidth, d.left, d.right, d.err = winWidth, left, right, err
		d.apply = err == nil
		return d
	}
	if paneWidth <= 0 || paneWidth == appliedLeft {
		return d
	}
	left := clampUserLeft(paneWidth, winWidth)
	d.userLeft, d.left, d.right = left, left, winWidth-left-1
	d.apply = left != paneWidth
	return d
}

// liveWindowWidth is the real width of the hub window: asked of tmux (the
// hub's own pane is only the list column once live), falling back to the
// width the TUI last saw.
func liveWindowWidth(fallback int) int {
	if w, err := tmux.HubWindowWidth(); err == nil && w > 0 {
		return w
	}
	return fallback
}

// herdProbeMsg carries the hub window's width (0 when tmux could not say)
// alongside the list pane width that triggered the probe.
type herdProbeMsg struct {
	pane, win int
}

// herdProbeCmd asks tmux for the hub window width after a pane resize.
func herdProbeCmd(paneWidth int) tea.Cmd {
	return func() tea.Msg {
		w, _ := tmux.HubWindowWidth()
		return herdProbeMsg{pane: paneWidth, win: w}
	}
}

// liveShowCmd hands the current selection to the live viewer, once per
// selection change (a failed attempt is not retried until the selection
// changes again). The split honours a user-dragged divider.
func (m *HerdModel) liveShowCmd() tea.Cmd {
	key := m.selSession + "|" + m.selWindowID
	if key == m.liveShown {
		return nil
	}
	m.liveShown = key
	session, window := m.selSession, m.selWindowID
	fallback, userLeft := m.termWidth, m.userLeft
	shown := m.hubChrome
	return func() tea.Msg {
		win := liveWindowWidth(fallback)
		left, right, err := herdLiveSplitFor(win, userLeft)
		if err == nil {
			err = tmux.ShowLiveViewer(session, window, right)
			if err == nil {
				hubZoomSync(!shown)
			}
		}
		return herdLiveMsg{key: key, left: left, right: right, win: win, err: err}
	}
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
	defer func() {
		if m.closeArmed() && (m.selSession != m.confirmSession || m.selWindowID != m.confirmWindow) {
			m.cancelClose()
		}
		if m.menuOpen && m.splitPane.Cursor() != m.menuLine {
			m.menuOpen = false
		}
	}()
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
	case herdLineDivider, herdLineSubheader:
		// Unreachable in practice — moveCursor/resolveSelection never park
		// the cursor on a divider or subheader — but defend against adopting a
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
	m.pendingFocus = nil
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
		if m.lines[idx].selectable() {
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
		if l.selectable() {
			m.splitPane.SetCursor(i)
			m.syncSelectionFromCursor()
			m.ensureCursorVisible()
			return
		}
	}

	m.selSession, m.selWindowID, m.selIsHeader = "", "", false
}

func (m *HerdModel) visibleLines() int {
	h := m.termHeight - herdChromeRows - m.liveHintRows()
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
	defer func(before int) {
		if m.scrollOffset != before {
			m.refreshHover() // the content under the pointer changed
		}
	}(m.scrollOffset)
	cursor := m.splitPane.Cursor()
	visible := m.visibleLines()
	if cursor < m.scrollOffset {
		m.scrollOffset = cursor
	}
	// Scroll until every row from the top through the cursor line, including
	// the selected row's detail lines, fits on screen.
	cw := m.contentWidth()
	for m.scrollOffset < cursor && m.rowsBetween(m.scrollOffset, cursor, cw) > visible {
		m.scrollOffset++
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
}

// rowsBetween is the number of screen rows lines from..to (inclusive) take.
func (m *HerdModel) rowsBetween(from, to, cw int) int {
	n := 0
	for i := from; i <= to && i < len(m.lines); i++ {
		n += m.lineHeight(i, cw)
	}
	return n
}

// ── Jump ─────────────────────────────────────────────────────────────────────

// tmux seams, overridden in tests.
var (
	switchToSessionWindow = tmux.SwitchToSessionWindow
	focusLivePane         = tmux.FocusLivePane
)

// focusLive is Enter's and tab's action: move tmux focus to the live pane so
// keystrokes go to the viewed agent. Prefix+o returns to the list.
func (m *HerdModel) focusLive() (tea.Model, tea.Cmd) {
	if err := focusLivePane(); err != nil {
		m.jumpErr = err.Error()
	}
	return *m, nil
}

// jumpToSelectedHome is J's action: switch the client to the selected row's
// window in its own home session, for workflows that want the project's own
// session context rather than the hub. If the switch fails (e.g. the session
// died between the last poll and this keypress), records an inline error
// and triggers a refresh instead of crashing. A no-op when the cursor is on
// a group header/placeholder rather than an agent row.
func (m *HerdModel) jumpToSelectedHome() (tea.Model, tea.Cmd) {
	r := m.selectedRow()
	if r == nil {
		return *m, nil
	}
	if err := switchToSessionWindow(r.Session, r.WindowID); err != nil {
		m.jumpErr = err.Error()
		return *m, herdLoadCmd()
	}
	m.jumpErr = ""
	m.infoMsg = ""
	return *m, nil
}

// jumpToNextBlocked selects the first blocked agent across all sessions,
// using the same internal/herd ordering the view itself renders with, shows
// it in the live pane, and moves tmux focus there so the user can answer the
// prompt immediately. It never switches the client out of the hub; J is the
// explicit way to leave.
func (m *HerdModel) jumpToNextBlocked() (tea.Model, tea.Cmd) {
	r := herd.FirstBlocked(m.groups)
	if r == nil {
		m.jumpErr = "no blocked agents"
		return *m, nil
	}
	found := false
	for i, l := range m.lines {
		if l.kind == herdLineRow && l.row.Session == r.Session && l.row.WindowID == r.WindowID {
			m.splitPane.SetCursor(i)
			found = true
			break
		}
	}
	if !found {
		m.jumpErr = "blocked agent not in the list"
		return *m, herdLoadCmd()
	}
	m.jumpErr = ""
	m.syncSelectionFromCursor()
	m.ensureCursorVisible()
	focus := func() tea.Msg {
		if err := focusLivePane(); err != nil {
			return herdFocusErrMsg{err: err}
		}
		return nil
	}
	if show := m.maybeShowCmd(); show != nil {
		return *m, tea.Sequence(show, focus)
	}
	return *m, focus
}

// herdFocusErrMsg reports a failure to move focus to the live pane.
type herdFocusErrMsg struct{ err error }

// ── Line model construction ──────────────────────────────────────────────────

// rebuildLines flattens m.groups into the scrollable line list. Called after
// every successful reload.
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
		lines = append(lines, herdLine{kind: herdLineHeader, group: g})
		if len(g.Rows) == 0 {
			lines = append(lines, herdLine{kind: herdLinePlaceholder, group: g})
			continue
		}
		first := true
		tree := treeRows(g.Rows)
		nameCol := 0
		for _, t := range tree {
			if t.Label == "" {
				nameCol = max(nameCol, lipgloss.Width(t.Prefix)+lipgloss.Width(herd.BaseName(t.Row)))
			}
		}
		for _, t := range tree {
			if t.Label != "" {
				lines = append(lines, herdLine{kind: herdLineSubheader, group: g, label: t.Label})
				continue
			}
			lines = append(lines, herdLine{kind: herdLineRow, row: t.Row, groupHead: first, treePrefix: t.Prefix, nameCol: nameCol})
			first = false
		}
	}
	m.lines = lines
}

// ── Rendering ────────────────────────────────────────────────────────────────

// contentWidth returns the usable content width based on terminal size.
func (m HerdModel) contentWidth() int {
	w := m.termWidth
	if m.liveLeft > 0 && w > m.liveLeft {
		w = m.liveLeft // the list column is all the hub pane has
	}
	if w < 40 {
		w = 40
	}
	return w
}

// View renders the hub as a full-screen (alt-screen) view.
func (m HerdModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	return v
}

func (m HerdModel) render() string {
	out := m.renderList()
	if m.card != nil {
		out = m.withCard(out)
	}
	return out
}

// renderHeader renders the title + subtitle chrome.
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
	case m.menuOpen:
		text, _ := menuItemSpans(m.herdMenuItems(m.menuLine))
		b.WriteString(herdBlockedStyle.Render(truncateToWidth(text+"   esc", cw)))
	case m.jumpErr != "":
		b.WriteString(herdErrStyle.Render("  " + truncateToWidth(m.jumpErr, cw-2)))
	case m.infoMsg != "":
		// A benign, non-error outcome (e.g. a close notice) — dim, not red.
		b.WriteString(herdDimStyle.Render("  " + truncateToWidth(m.infoMsg, cw-2)))
	}
	b.WriteString("\n")

	hintParts := []string{
		menuHintKeyStyle.Render("j/k") + " " + menuHintLabelStyle.Render("navigate"),
		menuHintKeyStyle.Render("enter/tab") + " " + menuHintLabelStyle.Render("focus live"),
		menuHintKeyStyle.Render("^b o") + " " + menuHintLabelStyle.Render("back"),
		menuHintKeyStyle.Render("J") + " " + menuHintLabelStyle.Render("home"),
		menuHintKeyStyle.Render("n") + " " + menuHintLabelStyle.Render("new"),
		menuHintKeyStyle.Render("x") + " " + menuHintLabelStyle.Render("close"),
		menuHintKeyStyle.Render("b") + " " + menuHintLabelStyle.Render("blocked"),
		menuHintKeyStyle.Render("r") + " " + menuHintLabelStyle.Render("refresh"),
		menuHintKeyStyle.Render("c") + " " + menuHintLabelStyle.Render("chrome"),
		menuHintKeyStyle.Render("q") + " " + menuHintLabelStyle.Render("leave"),
	}
	b.WriteString(strings.Join(m.wrapHints(hintParts, cw), "\n"))
	return b.String()
}

// wrapHints packs hint parts into centered lines no wider than cw, breaking
// between parts so no "key label" pair is ever split.
func (m HerdModel) wrapHints(parts []string, cw int) []string {
	sep := "  " + menuHintDotStyle.Render("·") + "  "
	sepW := lipgloss.Width(sep)
	var lines []string
	var cur []string
	curW := 0
	flush := func() {
		if len(cur) > 0 {
			lines = append(lines, tableStyles.HintBar.MarginTop(0).Width(cw).Render(strings.Join(cur, sep)))
		}
		cur, curW = nil, 0
	}
	for _, p := range parts {
		w := lipgloss.Width(p)
		if len(cur) > 0 && curW+sepW+w > cw {
			flush()
		}
		if len(cur) > 0 {
			curW += sepW
		}
		cur = append(cur, p)
		curW += w
	}
	flush()
	return lines
}

// liveHintRows is how many rows the hint bar occupies beyond the single row
// the chrome budget (herdChromeRows) already reserves.
func (m HerdModel) liveHintRows() int {
	var b strings.Builder
	b.WriteString(m.renderFooter(m.contentWidth()))
	// footer = "\n" + message line + "\n" + hint rows
	extra := strings.Count(b.String(), "\n") - 2
	if extra < 0 {
		return 0
	}
	return extra
}

// herdNoViewHint is shown in the list area until an agent has been viewed in
// the live pane (e.g. the cursor sits on a header and no agent was ever
// selected).
const herdNoViewHint = "select an agent to view it live"

// renderList renders the hub's list column: header, rows, footer. The list is
// padded to its full height so the footer (and click targets derived from it)
// never move.
func (m HerdModel) renderList() string {
	var b strings.Builder
	cw := m.contentWidth()

	b.WriteString(m.renderHeader(cw))

	if status, isStatus := m.renderStatusOrEmpty(); isStatus {
		b.WriteString(status)
	} else {
		visible := m.visibleLines()
		used := 0
		for i := m.scrollOffset; i < len(m.lines) && used < visible; i++ {
			for _, l := range strings.Split(m.renderLine(i, cw), "\n") {
				if used == visible {
					break
				}
				b.WriteString(l)
				b.WriteString("\n")
				used++
			}
		}
		if pad := visible - used; pad > 0 {
			if m.liveShown == "" {
				b.WriteString(herdDimStyle.Render("  " + truncateToWidth(herdNoViewHint, cw-2)))
				b.WriteString("\n")
				pad--
			}
			b.WriteString(strings.Repeat("\n", pad))
		}
	}

	b.WriteString(m.renderFooter(cw))
	return b.String()
}

func (m HerdModel) renderLine(i int, cw int) string {
	l := m.lines[i]
	selected := i == m.splitPane.Cursor()
	switch l.kind {
	case herdLineHeader:
		return m.renderGroupHeaderHover(l.group, selected, m.hover.region(i) == herdHoverNew, cw)
	case herdLinePlaceholder:
		marker := "    "
		if selected {
			marker = "  " + cursorActiveStyle.Render("▸") + " "
		}
		return marker + herdDimStyle.Render("no agents")
	case herdLineDivider:
		return renderHerdDivider(cw)
	case herdLineSubheader:
		// Align under agent names: bar + cursor slot (3 cols) + glyph + space.
		return strings.Repeat(" ", 3+2) + herdDimStyle.Render(l.label)
	default: // herdLineRow
		return m.renderTreeRowHover(l.row, l.treePrefix, l.nameCol, selected, m.hover.region(i), cw)
	}
}

// renderHerdDivider renders a non-selectable rule between two adjacent
// groups, inset to match the sidebar's content block: herdSidebarLeftInset
// blank columns on the left (the same column a group header's marker
// starts at) and herdSidebarRightGutter blank columns on the right (the
// same gutter content rows leave before the pane edge). Below that combined inset+gutter width
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
// window-rename convention in internal/winname: ● working,
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

// renderGroupHeader renders a session's group header line, now selectable
// (a group header/placeholder can be the cursor's target, showing that
// session's summary in the main panel) — hence the cursor marker, matching
// renderRow's convention.
func (m HerdModel) renderGroupHeader(g herd.Group, selected bool, cw int) string {
	return m.renderGroupHeaderHover(g, selected, false, cw)
}

// renderGroupHeaderHover is renderGroupHeader with the "+ new" button hovered.
func (m HerdModel) renderGroupHeaderHover(g herd.Group, selected, newHover bool, cw int) string {
	glyph, style := rollupGlyph(g.Rollup)
	count := fmt.Sprintf("(%d agent%s)", len(g.Rows), plural(len(g.Rows)))
	marker := strings.Repeat(" ", herdSidebarLeftInset)
	if selected {
		marker = " " + cursorActiveStyle.Render("▸") + " "
	}
	projStyle := herdProjectStyle.Bold(true).Foreground(lipgloss.Color(herdColorFor(g.Project)))
	line := fmt.Sprintf("%s%s  %s  %s  %s",
		marker,
		style.Render(glyph),
		projStyle.Render(g.Project),
		herdCountStyle.Render(g.Session),
		herdCountStyle.Render(count),
	)
	if groupCanLaunch(g) {
		btnW := lipgloss.Width(herdNewLabel)
		line = truncateToWidth(line, cw-herdSidebarRightGutter-btnW-1)
		pad := cw - herdSidebarRightGutter - btnW - lipgloss.Width(line)
		if pad < 1 {
			pad = 1
		}
		newStyle := herdDimStyle
		if newHover {
			newStyle = herdNewHoverStyle
		}
		line += strings.Repeat(" ", pad) + newStyle.Render(herdNewLabel)
	}
	return herdFitLine(line, selected, cw)
}

// renderRow renders one agent row.
func (m HerdModel) renderRow(r herd.Row, selected bool, cw int) string {
	return m.renderTreeRow(r, "", 0, selected, cw)
}

// herdNow is the clock row ages are measured against; tests replace it.
var herdNow = time.Now

// herdFitLine clamps a sidebar line to the columns left of the right gutter.
// A selected line is highlighted up to the gutter (never into it) and then
// padded with plain gutter columns, so it measures exactly cw.
func herdFitLine(line string, selected bool, cw int) string {
	if selected {
		return herdFitLineStyled(line, &herdSelectedStyle, cw)
	}
	return herdFitLineStyled(line, nil, cw)
}

// herdFitLineStyled is herdFitLine with an arbitrary highlight (nil = none).
func herdFitLineStyled(line string, hl *lipgloss.Style, cw int) string {
	inner := cw - herdSidebarRightGutter
	if inner < 1 {
		return ansi.Truncate(line, max(cw, 0), "")
	}
	line = ansi.Truncate(line, inner, "")
	if hl == nil {
		return line
	}
	line = reapplyBackground(line, hl)
	return hl.Width(inner).Render(line) + strings.Repeat(" ", herdSidebarRightGutter)
}

// reapplyBackground re-emits hl's background after every SGR reset in line.
// Segments are pre-rendered with their own styles and each ends in a reset,
// which would otherwise drop the outer background and leave the highlight
// patchy after the first segment.
func reapplyBackground(line string, hl *lipgloss.Style) string {
	bg := hl.GetBackground()
	if bg == nil {
		return line
	}
	seq, _, ok := strings.Cut(lipgloss.NewStyle().Background(bg).Render("x"), "x")
	if !ok || seq == "" {
		return line
	}
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+seq)
	return strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+seq)
}

// herdRowCursor is a row's left bar in the group colour plus the cursor slot.
func herdRowCursor(r herd.Row, selected bool) string {
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color(herdColorFor(r.Project))).Render("▎")
	if selected {
		return bar + cursorActiveStyle.Render("▸") + " "
	}
	return bar + "  "
}

// herdNameBudget is the most columns a row's name (plus tree prefix) may take
// at width cw: herdMaxNameWidth, or a third of a narrow sidebar so the recap
// keeps most of the row.
func herdNameBudget(cw int) int {
	return max(min(herdMaxNameWidth, cw/3), 0)
}

// rowRight renders the right-aligned status before the close button: the PR
// badge ("#60", or "#60 ✓" on a done row, which replaces the age) and the
// dim time in the current state (blocked style on a blocked row).
func rowRight(r herd.Row) string {
	var parts []string
	if r.PRNumber > 0 {
		badge := fmt.Sprintf("#%d", r.PRNumber)
		if r.Done {
			return herdDoneStyle.Render(badge + " ✓")
		}
		parts = append(parts, herdProjectStyle.Render(badge))
	}
	if !r.StateSince.IsZero() {
		style := herdDimStyle
		if r.State == "blocked" {
			style = herdBlockedStyle
		}
		parts = append(parts, style.Render(herd.CompactAge(herdNow().Sub(r.StateSince))))
	}
	return strings.Join(parts, " ")
}

// recapStyle is the style for a row's recap text: the activity fallback is
// the weakest signal and renders dimmer than a real recap, task or title.
func recapStyle(src herd.RecapSource) lipgloss.Style {
	if src == herd.RecapActivity {
		return herdActivityStyle.Italic(true)
	}
	return herdRecapStyle
}

// renderTreeRow renders an agent row: cursor, glyph, tree prefix (dim), base
// agent name, recap, then the right-aligned age/PR badge and close button.
// The prefix is clamped so deep nesting cannot starve the name. A selected
// row also renders its detail lines (see rowDetail) beneath it, joined by
// newlines; every line measures at most cw (exactly cw when selected).
func (m HerdModel) renderTreeRow(r herd.Row, treePrefix string, nameCol int, selected bool, cw int) string {
	return m.renderTreeRowHover(r, treePrefix, nameCol, selected, herdHoverNone, cw)
}

// renderTreeRowHover is renderTreeRow with the hover region of this row: a
// body hover tints an unselected row, a close hover recolours the ✕.
func (m HerdModel) renderTreeRowHover(r herd.Row, treePrefix string, nameCol int, selected bool, hover herdHoverRegion, cw int) string {
	glyph, style := rowGlyph(r)
	cursor := herdRowCursor(r, selected)
	glyphStr := style.Render(glyph)

	hovered := hover == herdHoverBody || hover == herdHoverClose
	closeW := 1 + lipgloss.Width(herdCloseLabel)
	inner := cw - herdSidebarRightGutter
	// Columns before the name: bar, cursor slot, glyph and a space. The
	// name column may not push the recap below herdMinRecapWidth.
	maxCol := inner - 5 - 2 - closeW - herdMinRecapWidth
	nameBudget := min(herdNameBudget(cw), max(maxCol, 0))
	col := min(nameCol, nameBudget)
	if tw := lipgloss.Width(treePrefix); tw > 0 {
		keep := min(tw, nameBudget-6)
		treePrefix = ansi.Truncate(treePrefix, max(keep, 0), "")
		nameBudget -= lipgloss.Width(treePrefix)
	}
	name := truncateToWidth(herd.BaseName(r), nameBudget)

	padW := max(col-lipgloss.Width(treePrefix)-lipgloss.Width(name), 0)
	prefix := cursor + glyphStr + " " + herdDimStyle.Render(treePrefix) + renderAgentNameHover(name, r.State == "blocked", hovered) + strings.Repeat(" ", padW) + "  "
	available := inner - lipgloss.Width(prefix) - closeW

	closeStyle := herdDimStyle
	var text, right string
	if m.confirmingRow(r) {
		// The full instance name: this is the identifier being closed.
		text = herdBlockedStyle.Render(truncateToWidth("close "+r.Name+"? y/n", available))
		closeStyle = herdBlockedStyle
	} else {
		if hover == herdHoverClose {
			closeStyle = herdCloseHoverStyle
		}
		right = rowRight(r)
		if rw := lipgloss.Width(right); rw > 0 && available-rw-1 >= herdMinRecapWidth {
			available -= rw + 1
		} else {
			right = ""
		}
		recap := rowRecap(r)
		rs := recapStyle(recap.Source)
		if hovered {
			rs = herdRecapHoverStyle
			if recap.Source == herd.RecapActivity {
				rs = herdActivityHoverStyle
			}
		}
		text = rs.Render(truncateToWidth(recap.Text, available))
	}

	line := prefix + text
	pad := inner - closeW - lipgloss.Width(line) - lipgloss.Width(right)
	line += strings.Repeat(" ", max(pad, 0)) + right + " " + closeStyle.Render(herdCloseLabel)
	var hl *lipgloss.Style
	switch {
	case selected:
		hl = &herdSelectedStyle
	case hovered:
		hl = &herdHoverStyle
	}
	out := herdFitLineStyled(line, hl, cw)
	if !selected {
		return out
	}
	for _, d := range rowDetailStyled(r, cw) {
		out += "\n" + herdFitLine(d, true, cw)
	}
	return out
}

// rowRecap is the row's recap as loaded (herd.TagLogs), or, for a row that
// never went through it, the log-free part of the chain (task, activity).
func rowRecap(r herd.Row) herd.RecapLine {
	if r.Recap.Source != herd.RecapNone {
		return r.Recap
	}
	return herd.Recap(r, herd.LogInfo{})
}

// herdMinRecapWidth is the narrowest recap the right-aligned age/badge may
// leave; below it the age/badge is dropped.
const herdMinRecapWidth = 8

// herdDetailIndent is how far a selected row's detail lines are indented:
// the bar, the cursor slot and the glyph column, so they align with the name.
const herdDetailIndent = 5

// herdDetailMaxLines caps a selected row's extra lines.
const herdDetailMaxLines = 3

// herdDetailLine is one plain-text detail line and whether it is the
// activity line (dimmer) rather than recap text.
type herdDetailLine struct {
	text     string
	activity bool
}

// rowDetail returns the plain detail lines a selected row renders beneath
// itself at width cw: the full recap wrapped, then the last activity line
// when it differs from the recap, at most herdDetailMaxLines in total. Hit
// testing and scrolling count these lines, so rendering must use exactly
// this.
func rowDetail(r herd.Row, cw int) []herdDetailLine {
	w := cw - herdSidebarRightGutter - herdDetailIndent
	if w < herdMinRecapWidth {
		return nil
	}
	activity := herd.ActivityLine(r)
	rc := rowRecap(r)
	recap := rc.Text
	isActivity := rc.Source == herd.RecapActivity
	showActivity := activity != "" && !isActivity && activity != recap

	recapMax := herdDetailMaxLines
	if showActivity {
		recapMax--
	}
	var out []herdDetailLine
	for _, l := range wrapLines(recap, w, recapMax) {
		out = append(out, herdDetailLine{text: l, activity: isActivity})
	}
	if showActivity {
		out = append(out, herdDetailLine{text: truncateToWidth(activity, w), activity: true})
	}
	return out
}

// rowDetailStyled renders rowDetail's lines with the bar and indent.
func rowDetailStyled(r herd.Row, cw int) []string {
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color(herdColorFor(r.Project))).Render("▎")
	indent := bar + strings.Repeat(" ", herdDetailIndent-1)
	var out []string
	for _, d := range rowDetail(r, cw) {
		style := herdActivityStyle
		if d.activity {
			style = herdDimStyle.Italic(true)
		}
		out = append(out, indent+style.Render(d.text))
	}
	return out
}

// wrapLines word-wraps s to width w, keeping at most n lines; when it does
// not fit, the last kept line ends in "…".
func wrapLines(s string, w, n int) []string {
	if s == "" || w <= 0 || n <= 0 {
		return nil
	}
	lines := strings.Split(ansi.Wrap(s, w, ""), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	if len(lines) <= n {
		return lines
	}
	lines = lines[:n]
	last := lines[n-1]
	if lipgloss.Width(last)+1 > w {
		last = ansi.Truncate(last, w-1, "")
	}
	lines[n-1] = last + "…"
	return lines
}

// lineHeight is how many screen rows line i renders as at width cw: a
// selected agent row adds its detail lines, everything else is one row.
func (m HerdModel) lineHeight(i, cw int) int {
	if i < 0 || i >= len(m.lines) {
		return 0
	}
	if l := m.lines[i]; l.kind == herdLineRow && i == m.splitPane.Cursor() {
		return 1 + len(rowDetail(l.row, cw))
	}
	return 1
}

// truncateToWidth shortens s to at most width display columns (as measured
// by ansi.Truncate, which is ANSI- and wide-rune-aware and preserves escapes), appending "…" when
// truncated. Returns s unchanged if it already fits or width <= 0.
func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// herdClosePending records a closed row's position within its group.
type herdClosePending struct {
	session  string
	windowID string
	nextID   string // window id of the row directly after it, if any
	prevID   string // window id of the row directly before it, if any
	killed   bool   // the close succeeded
	tries    int    // reloads that still showed the window
}

// herdClosePendingMaxTries bounds how many reloads wait for the monitor's
// snapshot to drop a closed window before giving up.
const herdClosePendingMaxTries = 6

// recordClose captures the neighbours of the row being closed.
func (m *HerdModel) recordClose(r herd.Row) {
	p := &herdClosePending{session: r.Session, windowID: r.WindowID}
	if g := m.groupForSession(r.Session); g != nil {
		ordered := treeOrder(treeRows(g.Rows)) // display order
		for i, gr := range ordered {
			if gr.WindowID != r.WindowID {
				continue
			}
			if i+1 < len(ordered) {
				p.nextID = ordered[i+1].WindowID
			}
			if i > 0 {
				p.prevID = ordered[i-1].WindowID
			}
			break
		}
	}
	m.closePending = p
}

// applyClosePending, run after a reload rebuilt m.lines, moves the selection
// to the closed row's neighbour: next, else previous, else the project header.
// Waits (a few reloads) while the closed window is still listed. One-shot.
func (m *HerdModel) applyClosePending() {
	p := m.closePending
	if p == nil || !p.killed {
		return
	}
	has := func(id string) bool {
		for _, l := range m.lines {
			if l.kind == herdLineRow && l.row.Session == p.session && l.row.WindowID == id {
				return true
			}
		}
		return false
	}
	if has(p.windowID) {
		if p.tries++; p.tries >= herdClosePendingMaxTries {
			m.closePending = nil
		}
		return
	}
	m.closePending = nil
	switch {
	case p.nextID != "" && has(p.nextID):
		m.selSession, m.selWindowID, m.selIsHeader = p.session, p.nextID, false
	case p.prevID != "" && has(p.prevID):
		m.selSession, m.selWindowID, m.selIsHeader = p.session, p.prevID, false
	case m.groupForSession(p.session) != nil:
		m.selSession, m.selWindowID, m.selIsHeader = p.session, "", true
	}
}

// herdViewerMsg reports the window the live viewer currently shows.
type herdViewerMsg struct{ home, window string }

// herdViewerPollCmd asks tmux which window the viewer shows.
func herdViewerPollCmd() tea.Cmd {
	return func() tea.Msg {
		home, win := tmux.ViewerActiveWindow()
		return herdViewerMsg{home: home, window: win}
	}
}

// viewerSyncTarget decides whether the hub selection must follow the viewer.
// The viewer shows home|win; seen is what the previous poll saw and liveShown
// what the hub itself last handed to the viewer. Only a change the hub did not
// cause (observed differs from both) is followed, and only when the window is
// a row of the list. It returns the key to remember as seen.
func viewerSyncTarget(lines []herdLine, liveShown, seen, home, win string) (key string, follow bool) {
	if home == "" || win == "" {
		return seen, false
	}
	key = home + "|" + win
	if key == seen || key == liveShown {
		return key, false
	}
	for _, l := range lines {
		if l.kind == herdLineRow && l.row.Session == home && l.row.WindowID == win {
			return key, true
		}
	}
	return key, false
}

// syncFromViewer moves the list selection to the window the viewer switched
// to by other means (a chrome-bar tab click inside the live pane).
func (m HerdModel) syncFromViewer(msg herdViewerMsg) HerdModel {
	key, follow := viewerSyncTarget(m.lines, m.liveShown, m.viewerSeen, msg.home, msg.window)
	m.viewerSeen = key
	if !follow {
		return m
	}
	m.selSession, m.selWindowID, m.selIsHeader = msg.home, msg.window, false
	m.liveShown = key
	m.resolveSelection()
	return m
}

// herdLeaver adapts the hub list to hubtarget.Leaver. The herd process runs in
// the hub pane (no popup), so the current client is right and no -c is needed;
// failures surface through jumpErr rather than display-message.
type herdLeaver struct {
	m    *HerdModel
	home string
}

func (herdLeaver) SwitchLast() error { return tmux.SwitchClientToLast("") }
func (herdLeaver) SwitchSession(n string) error {
	return tmux.SwitchClientToSession("", n)
}
func (l herdLeaver) ViewedHome() string             { return l.home }
func (herdLeaver) Sessions() []tmux.SessionActivity { return tmux.ListSessionActivity() }
func (l herdLeaver) Message(msg string)             { l.m.jumpErr = msg }

// Chrome-zoom seams, overridden in tests so they never touch tmux.
var (
	hubZoomSync      = tmux.SyncHubZoom
	hubChromeDefault = func() bool { return config.Load().HubChrome }
)

// herdZoomSyncCmd brings the viewed window's zoom in line with the chrome
// toggle (shown = chrome bar visible in the live pane). Run on start (which
// also sweeps zooms leaked by a previous hub), every poll tick (the viewer
// can change window without the hub: chrome-bar tab clicks, leaving and
// returning to the hub) and on toggle.
func herdZoomSyncCmd(shown bool) tea.Cmd {
	return func() tea.Msg {
		hubZoomSync(!shown)
		return nil
	}
}

// toggleChrome flips whether the live pane shows the agent's chrome bar and
// applies it to the viewed window at once.
func (m *HerdModel) toggleChrome() tea.Cmd {
	m.hubChrome = !m.hubChrome
	if m.hubChrome {
		m.infoMsg = "chrome bar shown in the hub"
	} else {
		m.infoMsg = "chrome bar hidden in the hub"
	}
	m.jumpErr = ""
	return herdZoomSyncCmd(m.hubChrome)
}

// HubChrome reports whether the live pane shows the agent's chrome bar.
func (m HerdModel) HubChrome() bool { return m.hubChrome }
