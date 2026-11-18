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
// the scrollable line list: title(1) + the "↑ N more" row(1), blank when
// nothing is scrolled off, above; the "↓ N more" row(1) + message line(1) +
// hint bar(1) below, plus one spare row.
const herdChromeRows = 6

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
	herdDimStyle      lipgloss.Style
	herdErrStyle      lipgloss.Style
	herdSuccessStyle  lipgloss.Style // transient success notices in the footer
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

	// Row weight by state (see rowWeight): the glyph carries the state
	// colour, the name and recap only get brighter or dimmer.
	herdNameWorkingStyle  lipgloss.Style
	herdNameDoneStyle     lipgloss.Style
	herdNameIdleStyle     lipgloss.Style
	herdRecapWorkingStyle lipgloss.Style
	herdRecapIdleStyle    lipgloss.Style

	// Recap styles for a hovered row: the theme's primary text colour, with
	// the activity fallback still italic so its source stays distinguishable.
	herdRecapHoverStyle    lipgloss.Style
	herdActivityHoverStyle lipgloss.Style

	// herdEmptyStyle is the muted italic fallback sentence of a row with no
	// recap text (see rowRecapText).
	herdEmptyStyle lipgloss.Style

	// herdPaletteColors is the stable accent palette (theme colours) that
	// project groups hash into: the left bar and the group header only.
	herdPaletteColors []string
)

// initHerdStyles initialises the hub view's styles from the active theme.
// Reuses the same state colours as the chrome bar's agent tabs (workingTab =
// Accent, blockedTab = Yellow, doneTab = Green, idleTab = Subtle) so the hub
// and the per-session chrome bar agree visually. Those colours belong to the
// state glyphs; agent names use plain text weights so a palette colour can
// never be mistaken for a state.
func initHerdStyles() {
	t := theme.Current()

	herdTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Accent))

	herdDimStyle = tableStyles.Dim
	herdErrStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Red))
	herdSuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green))
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

	herdNameWorkingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Bright))
	herdNameDoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	herdNameIdleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))
	herdRecapWorkingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	herdRecapIdleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))

	herdRecapHoverStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	herdActivityHoverStyle = herdRecapHoverStyle.Italic(true)
	herdEmptyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)).Italic(true)

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

// herdWeight is a row's visual weight, derived from its state the same way
// rowGlyph picks the glyph.
type herdWeight int

const (
	herdWeightIdle herdWeight = iota
	herdWeightDone
	herdWeightWorking
	herdWeightBlocked
)

// rowWeight classifies r for name and recap styling.
func rowWeight(r herd.Row) herdWeight {
	switch {
	case r.State == "blocked":
		return herdWeightBlocked
	case r.State == "working":
		return herdWeightWorking
	case r.Done:
		return herdWeightDone
	default:
		return herdWeightIdle
	}
}

// herdNameStyle is the agent name's style for a row of weight w: working is
// bold bright, done normal text, idle subtle and blocked bold warning. The
// state colour lives on the glyph; names never use the project palette.
func herdNameStyle(w herdWeight) lipgloss.Style {
	switch w {
	case herdWeightBlocked:
		return herdBlockedStyle.Bold(true)
	case herdWeightWorking:
		return herdNameWorkingStyle
	case herdWeightDone:
		return herdNameDoneStyle
	default:
		return herdNameIdleStyle
	}
}

// renderAgentName renders the agent name in its row-weight style and keeps
// ":N" muted. The caller passes the already-truncated plain name.
func renderAgentName(name string, w herdWeight) string {
	return renderAgentNameHover(name, w, false)
}

// renderAgentNameHover is renderAgentName; a hovered name is also bold.
func renderAgentNameHover(name string, w herdWeight, hovered bool) string {
	base, suffix := splitAgentName(name)
	style := herdNameStyle(w)
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
	// dimmed marks a herdLineRow the attention filter keeps only as the
	// ancestor of a matching row; it renders muted.
	dimmed bool
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
	// infoOK marks infoMsg as a finished success (✓, success colour) that the
	// poll tick clears once infoUntil has passed; a progress notice
	// ("launching…") does not expire, its outcome replaces it.
	infoOK    bool
	infoUntil time.Time

	// confirmSession/confirmWindow identify the row awaiting inline close
	// confirmation ("close <name>? y/n"); empty when none. Cleared whenever
	// the selection moves off that row.
	confirmSession string
	confirmWindow  string

	// newSession/newLine identify the group header whose "+ new" button is
	// armed as the shell | agent selector (newLine is the cursor line it was
	// armed from); empty when none. Cleared when the cursor moves off newLine.
	newSession string
	newLine    int

	// closePending remembers where a just-closed row sat so the next reload
	// can select its neighbour instead of jumping to the first row. Cleared
	// after one use.
	closePending *herdClosePending

	// menuOpen/menuLine: the right-click context menu (rendered in the footer)
	// and the line it was opened on.
	menuOpen bool
	menuLine int
	// menuHelp marks the open menu as the "?" key reference (every binding,
	// see herdHelpItems) rather than a line's context menu.
	menuHelp bool

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

	// folded is the set of folded groups by session name (see herd_fold.go):
	// a folded group renders as its header line only. focusMode records that
	// `Z` folded the quiet groups, so the next `Z` unfolds everything. filter
	// is the attention filter `f` cycles. All three survive reloads, the hot
	// reload re-exec (HubViewEnv) and hub restarts (viewPath, "" = not
	// persisted).
	folded    map[string]bool
	focusMode bool
	filter    herdFilter
	viewPath  string
	// viewDirty marks a fold/filter change made on the way to a jump
	// (revealRow) that flushView has not persisted yet.
	viewDirty bool
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
	if v := m.viewEnv(); v != "" {
		env = append(env, v)
	}
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
	m.loadViewState()
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
		groups, err := herd.LoadWithShells()
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
		m.expireInfo(herdNow())
		if m.exeChanged != nil && m.exeChanged() {
			m.reexec = true
			return m, tea.Quit
		}
		show := m.maybeShowCmd()
		return m, tea.Batch(herdLoadCmd(), herdTickCmd(), show, herdViewerPollCmd(), herdZoomSyncCmd(m.hubChrome))

	case herdViewerMsg:
		m = m.syncFromViewer(msg)
		return m, m.flushView()

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
			m.clearInfo()
			m.jumpErr = msg.err.Error()
			return m, nil
		}
		m.jumpErr = ""
		m.setSuccess("launched new agent in " + m.projectLabel(msg.session))
		m.pendingFocus = nil
		if msg.windowID == "" {
			return m, herdLoadCmd()
		}
		m.pendingFocus = &herdPendingFocus{session: msg.session, windowID: msg.windowID}
		return m, tea.Batch(herdLoadCmd(), herdFocusRetryCmd(msg.session, msg.windowID))

	case herdShellOpenedMsg:
		if msg.err != nil {
			m.clearInfo()
			m.jumpErr = msg.err.Error()
			return m, nil
		}
		m.jumpErr = ""
		m.setSuccess("opened shell in " + m.projectLabel(msg.session))
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
			m.clearInfo()
			m.jumpErr = msg.err.Error()
			return m, herdLoadCmd()
		}
		m.jumpErr = ""
		if m.closePending != nil {
			m.closePending.killed = true
		}
		m.setSuccess("closed " + msg.name + " (worktree kept; see agentctl sweep)")
		return m, herdLoadCmd()

	case herdLoadedMsg:
		m.loaded = true
		m.loadErr = msg.err
		if msg.err == nil {
			m.groups = msg.groups
			m.rebuildLines()
			m.validateNew()
			m.refreshHover()
			m.applyClosePending()
			m.resolveSelection()
			m.applyPendingFocus()
		}
		show := m.maybeShowCmd()
		return m, tea.Batch(show, m.flushView())

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
	key := msg.String()
	// An error stays on screen until the next key, which dismisses it.
	m.jumpErr = ""
	if m.menuOpen && m.menuHelp {
		// The key reference closes on any key; a binding it lists then runs
		// as usual, so the menu never swallows a keystroke.
		m.closeMenu()
		m.ensureCursorVisible()
		if key == "?" || key == "esc" || key == "q" {
			return *m, nil
		}
		return m.runKey(key)
	}
	if m.menuOpen {
		if key == "esc" || key == "q" {
			m.closeMenu()
			return *m, nil
		}
		for _, it := range m.herdMenuItems(m.menuLine) {
			if it.key == key {
				return *m, m.runMenuKey(key)
			}
		}
		m.closeMenu()
		return *m, nil
	}
	if m.closeArmed() {
		switch key {
		case "y", "Y":
			return *m, m.confirmClose()
		case "n", "N", "esc":
			m.cancelClose()
			return *m, nil
		}
	}
	if m.newArmed() {
		if cmd, handled := m.handleNewKey(key); handled {
			return *m, cmd
		}
	}
	return m.runKey(key)
}

// runKey runs the hub binding for key (as tea.KeyPressMsg.String() spells
// it). Shared by the keyboard and the clickable "?" key reference.
func (m *HerdModel) runKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "?":
		m.openHelp()
		return *m, nil

	case "n":
		m.armNew(m.splitPane.Cursor())
		return *m, nil

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
		m.clearInfo()
		return *m, herdLoadCmd()

	case "c":
		return *m, m.toggleChrome()

	case "z":
		return *m, m.toggleFoldAt(m.splitPane.Cursor())

	case "Z":
		return *m, m.toggleFocusMode()

	case "f":
		return *m, m.cycleFilter()
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
		if m.newArmed() && m.splitPane.Cursor() != m.newLine {
			m.cancelNew()
		}
		m.validateNew()
		if m.menuOpen && m.splitPane.Cursor() != m.menuLine {
			m.closeMenu()
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
		// The row still exists but is hidden (folded or filtered out): stay
		// on its group's header.
		if !m.selIsHeader && m.rowExists(m.selSession, m.selWindowID) {
			for i, l := range m.lines {
				if l.kind == herdLineHeader && l.group.Session == m.selSession {
					m.splitPane.SetCursor(i)
					m.syncSelectionFromCursor()
					m.ensureCursorVisible()
					return
				}
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
	m.clearInfo()
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
	m.revealRow(r.Session, r.WindowID) // unfold its group first
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
		return *m, tea.Batch(tea.Sequence(show, focus), m.flushView())
	}
	return *m, tea.Batch(focus, m.flushView())
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
//
// Folding and the attention filter are applied here, so the line list,
// selection, overflow counts and mouse hit-testing all see exactly what is
// drawn: a folded group contributes its header only; under a filter a group
// keeps its matching rows plus their ancestors (marked dimmed), and a group
// with nothing left shows its header only. Two consecutive header-only groups
// get no divider between them, so a folded hub reads as a compact list.
func (m *HerdModel) rebuildLines() {
	defer func() {
		// Keep SplitPane's row count (cursor clamping) in step at once.
		m.splitPane.SetListContent(m.splitPane.listContent, len(m.lines))
	}()
	var lines []herdLine
	prevBody := false
	for i, g := range m.groups {
		rows, context := g.Rows, map[string]bool(nil)
		if !m.isFolded(g.Session) {
			rows, context = filterRows(g.Rows, m.filter)
		}
		body := !m.isFolded(g.Session) && (len(rows) > 0 || (len(g.Rows) == 0 && m.filter == herdFilterAll))
		if i > 0 && (body || prevBody) {
			lines = append(lines, herdLine{kind: herdLineDivider})
		}
		prevBody = body
		lines = append(lines, herdLine{kind: herdLineHeader, group: g})
		if !body {
			continue
		}
		if len(g.Rows) == 0 {
			lines = append(lines, herdLine{kind: herdLinePlaceholder, group: g})
			continue
		}
		first := true
		tree := treeRows(rows)
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
			lines = append(lines, herdLine{kind: herdLineRow, row: t.Row, groupHead: first, treePrefix: t.Prefix, nameCol: nameCol, dimmed: context[t.Row.WindowID]})
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

// renderHeader renders the title line and the row below it, which is blank
// unless the list is scrolled and it carries the "↑ N more" indicator. It
// always ends in two line breaks, so the list (and every click target) starts
// at the same row (herdListTop).
func (m HerdModel) renderHeader(cw int) string {
	return m.renderTitle(cw) + "\n" + m.renderOverflowAbove(cw) + "\n"
}

// fleetCounts tallies every row by its glyph.
func (m HerdModel) fleetCounts() (blocked, working, done, idle int) {
	for _, g := range m.groups {
		for _, r := range g.Rows {
			switch rowWeight(r) {
			case herdWeightBlocked:
				blocked++
			case herdWeightWorking:
				working++
			case herdWeightDone:
				done++
			default:
				idle++
			}
		}
	}
	return blocked, working, done, idle
}

// renderTitle renders "agentctl hub" in bold accent on the left and the fleet
// totals right-aligned: a yellow inverse ⏸ cell (only when something is
// blocked, and the only filled cell), then ● ✓ ○ counts and a dim agent and
// session total. An active attention filter adds a muted tag after the title
// ("· needs you"). Narrow widths drop the totals, then tighten the counts and
// shorten the tag, then drop the counts, then truncate.
func (m HerdModel) renderTitle(cw int) string {
	title := strings.Repeat(" ", herdSidebarLeftInset) + herdTitleStyle.Render("agentctl hub")
	left, shortLeft := title, title
	if m.filter != herdFilterAll {
		left += herdCountStyle.Render(" · " + m.filter.label())
		short, _, _ := strings.Cut(m.filter.label(), " ")
		shortLeft += herdCountStyle.Render(" · " + short)
	}
	if !m.loaded || m.loadErr != nil {
		return herdFitLine(left, false, cw)
	}
	blocked, working, done, idle := m.fleetCounts()
	count := func(glyph string, st lipgloss.Style, n int) string {
		return st.Render(glyph) + " " + herdRecapWorkingStyle.Render(fmt.Sprint(n))
	}
	var cells []string
	if blocked > 0 {
		cells = append(cells, herdBlockedStyle.Bold(true).Reverse(true).Render(fmt.Sprintf(" ⏸ %d ", blocked)))
	}
	cells = append(cells, count("●", herdWorkingStyle, working), count("✓", herdDoneStyle, done), count("○", herdIdleStyle, idle))
	counts := strings.Join(cells, "  ")
	total := working + blocked + done + idle
	suffix := herdDimStyle.Render(fmt.Sprintf(" · %d agent%s, %d session%s", total, plural(total), len(m.groups), plural(len(m.groups))))

	candidates := [][2]string{{left, counts + suffix}, {left, counts}}
	if m.filter != herdFilterAll {
		tight := strings.Join(cells, " ")
		candidates = append(candidates, [2]string{left, tight}, [2]string{shortLeft, tight})
	}
	for _, c := range candidates {
		room := cw - herdSidebarRightGutter - lipgloss.Width(c[0]) - 1
		if lipgloss.Width(c[1]) <= room {
			pad := cw - herdSidebarRightGutter - lipgloss.Width(c[0]) - lipgloss.Width(c[1])
			return herdFitLine(c[0]+strings.Repeat(" ", pad)+c[1], false, cw)
		}
	}
	return herdFitLine(left, false, cw)
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

// shownEnd is one past the last line renderList draws (even partly) at the
// current scroll offset.
func (m HerdModel) shownEnd(cw int) int {
	visible := m.visibleLines()
	used := 0
	i := m.scrollOffset
	for ; i < len(m.lines) && used < visible; i++ {
		used += m.lineHeight(i, cw)
	}
	return i
}

// hiddenRows counts the agent rows among lines[from:to].
func (m HerdModel) hiddenRows(from, to int) (rows, other int) {
	for i := max(from, 0); i < min(to, len(m.lines)); i++ {
		switch m.lines[i].kind {
		case herdLineRow:
			rows++
		case herdLineHeader, herdLinePlaceholder:
			other++
		}
	}
	return rows, other
}

// renderOverflow is the muted "↑ N more" / "↓ N more" indicator for the
// agent rows scrolled out of view on one side ("" when nothing is hidden).
func renderOverflow(arrow string, rows, other, cw int) string {
	var text string
	switch {
	case rows > 0:
		text = fmt.Sprintf("%s %d more", arrow, rows)
	case other > 0:
		text = arrow + " more"
	default:
		return ""
	}
	return herdCountStyle.Render(strings.Repeat(" ", herdSidebarLeftInset+1) + truncateToWidth(text, cw-herdSidebarLeftInset-1-herdSidebarRightGutter))
}

// renderOverflowAbove is the indicator for lines scrolled off the top, drawn
// on the header row just above the list.
func (m HerdModel) renderOverflowAbove(cw int) string {
	if !m.clickable() {
		return ""
	}
	rows, other := m.hiddenRows(0, m.scrollOffset)
	return renderOverflow("↑", rows, other, cw)
}

// renderOverflowBelow is the indicator for lines past the bottom, drawn on the
// footer's blank row just below the list so the footer itself never moves.
func (m HerdModel) renderOverflowBelow(cw int) string {
	if !m.clickable() {
		return ""
	}
	rows, other := m.hiddenRows(m.shownEnd(cw), len(m.lines))
	return renderOverflow("↓", rows, other, cw)
}

func (m HerdModel) renderLine(i int, cw int) string {
	l := m.lines[i]
	selected := i == m.splitPane.Cursor()
	switch l.kind {
	case herdLineHeader:
		return m.renderGroupHeaderHover(l.group, selected, m.hover.region(i), cw)
	case herdLinePlaceholder:
		marker := "    "
		if selected {
			marker = "  " + cursorActiveStyle.Render("▸") + " "
		}
		return marker + herdDimStyle.Render("no agents")
	case herdLineDivider:
		return renderHerdDivider(cw)
	case herdLineSubheader:
		// The group's bar keeps the spine unbroken; the label aligns under
		// agent names: bar + cursor slot + glyph + space.
		return herdProjectBar(l.group.Project) + strings.Repeat(" ", herdDetailIndent-1) + herdDimStyle.Render(l.label)
	default: // herdLineRow
		out := m.renderTreeRowHover(l.row, l.treePrefix, l.nameCol, selected, m.hover.region(i), cw)
		if l.dimmed && !selected && m.hover.region(i) == herdHoverNone {
			out = dimContextRow(l.row, out)
		}
		return out
	}
}

// renderHerdDivider renders the non-selectable gap between two adjacent
// groups: an empty row. The line kind stays so hit-testing and navigation
// treat it as before. Never rendered before the first group or after the
// last; see rebuildLines.
func renderHerdDivider(int) string {
	return ""
}

// herdProjectBar is the project-coloured left bar shared by a group's rows,
// detail lines and sub-section labels.
func herdProjectBar(project string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(herdColorFor(project))).Render("▎")
}

// rowGlyph returns the glyph and style for a single agent row, matching the
// window-rename convention in internal/winname: ● working,
// ⏸ blocked, ✓ done (unseen), bare/○ otherwise.
func rowGlyph(r herd.Row) (string, lipgloss.Style) {
	if r.Shell {
		return "$", herdDimStyle
	}
	switch rowWeight(r) {
	case herdWeightBlocked:
		return "⏸", herdBlockedStyle
	case herdWeightWorking:
		return "●", herdWorkingStyle
	case herdWeightDone:
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
	return m.renderGroupHeaderHover(g, selected, herdHoverNone, cw)
}

// groupCustomSession reports whether g's tmux session name is not the one
// agentctl derives from its project, and so is worth showing. Session names
// are sanitized ("energy-solutions.ops" runs as
// "agentctl-energy-solutions-ops"), so compare against the derived name.
func groupCustomSession(g herd.Group) bool {
	if g.Session == "" || g.Session == tmux.SessionNameForDirName(g.Project) {
		return false
	}
	return g.ProjectDir == "" || g.Session != tmux.SessionNameForProjectRoot(g.ProjectDir)
}

// herdMinSessionLabel is the narrowest custom-session label worth drawing.
const herdMinSessionLabel = 6

// renderGroupHeaderHover is renderGroupHeader with a hovered region: the "+ new"
// button, or one side of the shell | agent selector when it is armed.
//
// Layout: marker, a ⏸ flag slot (yellow when the group has a blocked agent),
// the project name, a custom session name when there is one, then the
// per-state counts right-aligned in fixed slots (see groupStateCounts) and the
// button. The counts never truncate; the session name gives way first, then
// the project name.
func (m HerdModel) renderGroupHeaderHover(g herd.Group, selected bool, hover herdHoverRegion, cw int) string {
	line, _ := m.groupHeaderLayout(g, selected, hover, cw)
	return line
}

// Fold markers drawn before a group's project name.
const (
	herdFoldedGlyph   = "▸"
	herdUnfoldedGlyph = "▾"
)

// herdHeaderNameCol is the column a group header's fold marker starts at:
// the cursor marker, the ⏸ flag slot and a space.
const herdHeaderNameCol = herdSidebarLeftInset + 2

// herdNewGap is the blank columns between a header's counts and its "+ new"
// button, so the button never reads as part of the counts.
const herdNewGap = 2

// headerNameSpan is the [start, end) columns of group g's fold marker and
// project name on its header: the click target that toggles the fold.
func (m HerdModel) headerNameSpan(g herd.Group, cw int) [2]int {
	_, span := m.groupHeaderLayout(g, false, herdHoverNone, cw)
	return span
}

// groupHeaderLayout renders a group header (see renderGroupHeaderHover) and
// reports the columns its fold marker and project name occupy.
func (m HerdModel) groupHeaderLayout(g herd.Group, selected bool, hover herdHoverRegion, cw int) (string, [2]int) {
	marker := strings.Repeat(" ", herdSidebarLeftInset)
	if selected {
		marker = " " + cursorActiveStyle.Render("▸") + " "
	}
	counts := groupStateCounts(g, m.countDigits(), herdCountGap(cw))
	flag := " "
	if counts.blocked > 0 {
		flag = herdBlockedStyle.Render("⏸")
	}

	var btn string
	btnW := lipgloss.Width(herdNewLabel)
	if groupCanLaunch(g) {
		if m.newArmedFor(g) {
			shell, agent := herdDimStyle, herdDimStyle
			switch hover {
			case herdHoverNewShell:
				shell = herdNewHoverStyle
			case herdHoverNewAgent:
				agent = herdNewHoverStyle
			}
			btn = shell.Render(herdShellLabel) + herdDimStyle.Render(herdSelSep) + agent.Render(herdAgentLabel)
		} else {
			style := herdDimStyle
			if hover == herdHoverNew {
				style = herdNewHoverStyle
			}
			btn = style.Render(herdNewLabel)
		}
		btnW = lipgloss.Width(btn)
	}
	right := counts.text + strings.Repeat(" ", herdNewGap) + btn + strings.Repeat(" ", btnW-lipgloss.Width(btn))

	leftW := cw - herdSidebarRightGutter - lipgloss.Width(right) - 1
	projStyle := herdProjectStyle.Bold(true).Foreground(lipgloss.Color(herdColorFor(g.Project)))
	if hover == herdHoverFold {
		projStyle = projStyle.Underline(true)
	}
	fold := herdDimStyle.Render(herdUnfoldedGlyph)
	if m.isFolded(g.Session) {
		fold = herdCountStyle.Render(herdFoldedGlyph)
	}
	head := marker + flag + " " + fold + " "
	name := truncateToWidth(g.Project, leftW-lipgloss.Width(head))
	span := [2]int{herdHeaderNameCol, lipgloss.Width(head) + lipgloss.Width(name)}
	line := head + projStyle.Render(name)
	if groupCustomSession(g) {
		if room := leftW - lipgloss.Width(line) - 2; room >= herdMinSessionLabel {
			line += "  " + herdCountStyle.Render(truncateToWidth(g.Session, room))
		}
	}
	if leftW < lipgloss.Width(head) {
		// Too narrow for any name: keep the right side intact.
		line = truncateToWidth(line, max(leftW, 0))
		span[1] = min(span[1], max(leftW, 0))
	}
	pad := max(cw-herdSidebarRightGutter-lipgloss.Width(right)-lipgloss.Width(line), 1)
	return herdFitLine(line+strings.Repeat(" ", pad)+right, selected, cw), span
}

// countDigits is the widest per-state count across all groups, so the count
// slots line up down the whole sidebar.
func (m HerdModel) countDigits() int {
	d := 1
	for _, g := range m.groups {
		c := groupStateCounts(g, 1, 1)
		for _, n := range []int{c.blocked, c.working, c.done, c.idle} {
			d = max(d, len(fmt.Sprint(n)))
		}
	}
	return d
}

// herdCountGap is the space between header count slots: two columns when the
// sidebar has room, one at the narrow end.
func herdCountGap(cw int) int {
	if cw >= 56 {
		return 2
	}
	return 1
}

// herdGroupCounts is a group's per-state tally and its rendered slots.
type herdGroupCounts struct {
	blocked, working, done, idle int
	text                         string
}

// groupStateCounts tallies a group's rows by state and renders them in fixed
// slots, in the title's order ⏸ ● ✓ ○: each slot is the glyph, a space and
// the count right-aligned to digits columns; an empty bucket is a blank slot
// of the same width, so the counts form columns across groups.
func groupStateCounts(g herd.Group, digits, gap int) herdGroupCounts {
	var c herdGroupCounts
	for _, r := range g.Rows {
		if r.Shell {
			continue
		}
		switch rowWeight(r) {
		case herdWeightBlocked:
			c.blocked++
		case herdWeightWorking:
			c.working++
		case herdWeightDone:
			c.done++
		default:
			c.idle++
		}
	}
	slots := []struct {
		glyph string
		st    lipgloss.Style
		n     int
	}{
		{"⏸", herdBlockedStyle, c.blocked},
		{"●", herdWorkingStyle, c.working},
		{"✓", herdDoneStyle, c.done},
		{"○", herdIdleStyle, c.idle},
	}
	parts := make([]string, len(slots))
	for i, s := range slots {
		if s.n == 0 {
			parts[i] = strings.Repeat(" ", 2+digits)
			continue
		}
		parts[i] = s.st.Render(s.glyph) + " " + herdCountStyle.Render(fmt.Sprintf("%*d", digits, s.n))
	}
	c.text = strings.Join(parts, strings.Repeat(" ", gap))
	return c
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
	bar := herdProjectBar(r.Project)
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

// Fixed right-hand columns of a row: the PR badge slot ("#1025" is the widest
// seen) and the age slot, both right-aligned and blank when absent.
const (
	herdPRSlotW  = 5
	herdAgeSlotW = 3
)

// herdRightSlots is how many of the right-hand columns a row draws; narrow
// rows drop the age first, then the PR.
type herdRightSlots int

const (
	herdRightNone herdRightSlots = iota // neither column
	herdRightPR                         // PR column only
	herdRightFull                       // PR and age columns
)

// width is the columns the slots take, excluding the gap before them.
func (s herdRightSlots) width() int {
	switch s {
	case herdRightFull:
		return herdPRSlotW + 1 + herdAgeSlotW
	case herdRightPR:
		return herdPRSlotW
	}
	return 0
}

// fitRightSlots picks the most right-hand columns that still leave the recap
// herdMinRecapWidth of the available columns (one more goes to the gap).
func fitRightSlots(available int) herdRightSlots {
	for _, s := range []herdRightSlots{herdRightFull, herdRightPR} {
		if available-s.width()-1 >= herdMinRecapWidth {
			return s
		}
	}
	return herdRightNone
}

// padLeft right-aligns a rendered s in w columns.
func padLeft(s string, w int) string {
	return strings.Repeat(" ", max(w-lipgloss.Width(s), 0)) + s
}

// rowRight renders the right-aligned status columns before the close button:
// the PR badge ("#60", in the done style on a done row) in a herdPRSlotW slot
// and the dim time in the current state (blocked style on a blocked row) in a
// herdAgeSlotW slot, one space apart. An absent value leaves its slot blank so
// the columns line up down the sidebar.
func rowRight(r herd.Row, slots herdRightSlots) string {
	if slots == herdRightNone {
		return ""
	}
	var pr string
	if r.PRNumber > 0 {
		bs := herdProjectStyle
		if r.Done {
			bs = herdDoneStyle
		}
		pr = bs.Render(fmt.Sprintf("#%d", r.PRNumber))
	}
	out := padLeft(pr, herdPRSlotW)
	if slots == herdRightPR {
		return out
	}
	var age string
	if !r.StateSince.IsZero() {
		style := herdDimStyle
		if r.State == "blocked" {
			style = herdBlockedStyle
		}
		age = style.Render(herd.CompactAge(herdNow().Sub(r.StateSince)))
	}
	return out + " " + padLeft(age, herdAgeSlotW)
}

// recapStyle is the style for a row's recap text, by row weight: working in
// normal text, done subtle, idle muted, blocked subtle as before. The
// activity fallback is the weakest signal and stays italic; a row with no
// recap at all (src RecapNone, see rowRecap) gets the muted italic fallback.
func recapStyle(w herdWeight, src herd.RecapSource) lipgloss.Style {
	if src == herd.RecapNone {
		return herdEmptyStyle
	}
	var st lipgloss.Style
	switch w {
	case herdWeightWorking:
		st = herdRecapWorkingStyle
	case herdWeightIdle:
		st = herdRecapIdleStyle
	default: // done, blocked
		st = herdRecapStyle
	}
	if src == herd.RecapActivity {
		if w == herdWeightBlocked {
			st = herdActivityStyle
		}
		return st.Italic(true)
	}
	return st
}

// renderTreeRow renders an agent row: cursor, glyph, tree prefix (dim), base
// agent name, recap, then the right-aligned PR and age columns and the close
// button. The prefix is clamped so deep nesting cannot starve the name. A
// selected row also renders its detail lines (see rowDetail) beneath it,
// joined by newlines; every line measures at most cw (exactly cw when
// selected).
func (m HerdModel) renderTreeRow(r herd.Row, treePrefix string, nameCol int, selected bool, cw int) string {
	return m.renderTreeRowHover(r, treePrefix, nameCol, selected, herdHoverNone, cw)
}

// renderTreeRowHover is renderTreeRow with the hover region of this row: a
// body hover tints an unselected row, a close hover recolours the ✕. The ✕
// is drawn only on the selected and the hovered row; every other row keeps
// its one-cell slot blank so alignment and hit regions never change.
func (m HerdModel) renderTreeRowHover(r herd.Row, treePrefix string, nameCol int, selected bool, hover herdHoverRegion, cw int) string {
	glyph, style := rowGlyph(r)
	weight := rowWeight(r)
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
	treePrefix = herdKeptPrefix(treePrefix, cw)
	nameBudget -= lipgloss.Width(treePrefix)
	name := truncateToWidth(herd.BaseName(r), nameBudget)

	padW := max(col-lipgloss.Width(treePrefix)-lipgloss.Width(name), 0)
	prefix := cursor + glyphStr + " " + herdDimStyle.Render(treePrefix) + renderAgentNameHover(name, weight, hovered) + strings.Repeat(" ", padW) + "  "
	available := inner - lipgloss.Width(prefix) - closeW

	closeStyle := herdDimStyle
	showClose := selected || hovered
	var text, right string
	if m.confirmingRow(r) {
		// The full instance name: this is the identifier being closed.
		text = herdBlockedStyle.Render(truncateToWidth("close "+r.Name+"? y/n", available))
		closeStyle = herdBlockedStyle
		showClose = true
	} else {
		if hover == herdHoverClose {
			closeStyle = herdCloseHoverStyle
		}
		// The columns sit at fixed positions, but the recap may run into a
		// slot this row leaves blank: only what is drawn is reserved.
		right = strings.TrimLeft(rowRight(r, fitRightSlots(available)), " ")
		if rw := lipgloss.Width(right); rw > 0 {
			available -= rw + 1
		}
		recap := rowRecap(r)
		rs := recapStyle(weight, recap.Source)
		if hovered && recap.Source != herd.RecapNone {
			rs = herdRecapHoverStyle
			if recap.Source == herd.RecapActivity {
				rs = herdActivityHoverStyle
			}
		}
		text = rs.Render(truncateToWidth(recap.Text, available))
	}

	closeBtn := strings.Repeat(" ", lipgloss.Width(herdCloseLabel))
	if showClose {
		closeBtn = closeStyle.Render(herdCloseLabel)
	}
	line := prefix + text
	pad := inner - closeW - lipgloss.Width(line) - lipgloss.Width(right)
	line += strings.Repeat(" ", max(pad, 0)) + right + " " + closeBtn
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
	for _, d := range rowDetailStyled(r, treePrefix, cw) {
		out += "\n" + herdFitLine(d, true, cw)
	}
	return out
}

// rowRecap is the row's recap as loaded (herd.TagLogs), or, for a row that
// never went through it, the log-free part of the chain (task, activity).
// A row with nothing at all gets a fallback sentence built from what the row
// already knows (see emptyRecapText), still with Source RecapNone so it
// renders as a fallback; herd.Recap itself stays pure.
func rowRecap(r herd.Row) herd.RecapLine {
	if r.Shell {
		return herd.RecapLine{Text: "shell"}
	}
	if r.Recap.Source != herd.RecapNone {
		return r.Recap
	}
	if rc := herd.Recap(r, herd.LogInfo{}); rc.Source != herd.RecapNone {
		return rc
	}
	return herd.RecapLine{Text: emptyRecapText(r, herdNow())}
}

// emptyRecapText is the sentence a row with no recap shows: "waiting for
// first prompt · 4m" before the first turn, otherwise "no activity yet ·
// started 2d ago". The age is omitted when the start time is unknown.
func emptyRecapText(r herd.Row, now time.Time) string {
	var since time.Time
	if r.StartTime > 0 {
		since = time.Unix(r.StartTime, 0)
	} else if !r.StateSince.IsZero() {
		since = r.StateSince
	}
	if r.TurnCount == 0 {
		if since.IsZero() {
			return "waiting for first prompt"
		}
		return "waiting for first prompt · " + herd.CompactAge(now.Sub(since))
	}
	if since.IsZero() {
		return "no activity yet"
	}
	return "no activity yet · started " + herd.CompactAge(now.Sub(since)) + " ago"
}

// herdKeptPrefix clamps a row's tree prefix the way renderTreeRowHover does,
// so deep nesting cannot starve the name. Rendering and detail-line layout
// both go through it.
func herdKeptPrefix(treePrefix string, cw int) string {
	tw := lipgloss.Width(treePrefix)
	if tw == 0 {
		return ""
	}
	inner := cw - herdSidebarRightGutter
	maxCol := inner - 5 - 2 - (1 + lipgloss.Width(herdCloseLabel)) - herdMinRecapWidth
	nameBudget := min(herdNameBudget(cw), max(maxCol, 0))
	keep := min(tw, nameBudget-6)
	return ansi.Truncate(treePrefix, max(keep, 0), "")
}

// herdContinuation turns a (kept) tree prefix into the continuation drawn
// beside detail lines: "│  " where siblings follow at that depth, spaces
// otherwise. The result has the same display width as the prefix.
func herdContinuation(prefix string) string {
	rs := []rune(prefix)
	var b strings.Builder
	for i := 0; i < len(rs); i += 3 {
		chunk := string(rs[i:min(i+3, len(rs))])
		switch {
		case chunk == treeBar || chunk == treeBranch:
			b.WriteString(treeBar)
		case chunk == treeLast || chunk == treeGap:
			b.WriteString(treeGap)
		default:
			b.WriteString(strings.Repeat(" ", len(rs[i:min(i+3, len(rs))])))
		}
	}
	return b.String()
}

// herdMinRecapWidth is the narrowest recap the right-aligned age/badge may
// leave; below it the age/badge is dropped.
const herdMinRecapWidth = 8

// herdDetailIndent is how far a selected row's detail lines are indented
// before the tree prefix: the bar, the cursor slot and the glyph column, so
// they align with the name.
const herdDetailIndent = 5

// herdDetailMaxLines caps a selected row's extra lines.
const herdDetailMaxLines = 3

// herdDetailLine is one plain-text detail line and its marker ("" for recap
// text, herdActivityMarker or herdCommandMarker for an activity line).
type herdDetailLine struct {
	text   string
	marker string
}

// rowDetail returns the plain detail lines a selected row renders beneath
// itself at width cw: the full recap wrapped, then the last activity line
// when it differs from the recap, at most herdDetailMaxLines in total. A
// recap that is itself the activity (herd.RecapActivity: the last tool
// command or text) is one marked, truncated line and never wraps. Hit
// testing and scrolling count these lines, so rendering must use exactly
// this. prefixW is the width of the row's kept tree prefix (herdKeptPrefix).
func rowDetail(r herd.Row, prefixW, cw int) []herdDetailLine {
	w := cw - herdSidebarRightGutter - herdDetailIndent - prefixW
	if w < herdMinRecapWidth {
		return nil
	}
	activity := herd.ActivityLine(r)
	rc := rowRecap(r)
	if rc.Source == herd.RecapNone {
		return nil // the fallback sentence is already the whole story
	}
	if rc.Source == herd.RecapActivity {
		marker, text := activityMarker(r, rc.Text)
		return []herdDetailLine{{text: truncateToWidth(text, w-lipgloss.Width(marker)), marker: marker}}
	}
	recap := rc.Text
	showActivity := activity != "" && activity != recap

	recapMax := herdDetailMaxLines
	if showActivity {
		recapMax--
	}
	var out []herdDetailLine
	for _, l := range wrapLines(recap, w, recapMax) {
		out = append(out, herdDetailLine{text: l})
	}
	if showActivity {
		marker, text := activityMarker(r, activity)
		out = append(out, herdDetailLine{text: truncateToWidth(text, w-lipgloss.Width(marker)), marker: marker})
	}
	return out
}

// Markers leading an activity detail line: a shell command from the last
// tool detail ("Bash: go test" shows as "$ go test"), anything else.
const (
	herdCommandMarker  = "$ "
	herdActivityMarker = "› "
)

// activityMarker picks the marker for an activity line and the text to show
// after it: a Bash tool detail drops its "Bash: " label for "$ ".
func activityMarker(r herd.Row, text string) (marker, shown string) {
	if r.LastToolDetail != "" {
		if cmd, ok := strings.CutPrefix(text, "Bash: "); ok {
			return herdCommandMarker, cmd
		}
	}
	return herdActivityMarker, text
}

// rowDetailStyled renders rowDetail's lines with the bar, indent and the
// tree continuation for the row's (already clamped) prefix.
func rowDetailStyled(r herd.Row, treePrefix string, cw int) []string {
	indent := herdProjectBar(r.Project) + strings.Repeat(" ", herdDetailIndent-1) + herdDimStyle.Render(herdContinuation(treePrefix))
	var out []string
	for _, d := range rowDetail(r, lipgloss.Width(treePrefix), cw) {
		text := herdActivityStyle.Render(d.text)
		if d.marker != "" {
			text = herdDimStyle.Render(d.marker) + text
		}
		out = append(out, indent+text)
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
		return 1 + len(rowDetail(l.row, lipgloss.Width(herdKeptPrefix(l.treePrefix, cw)), cw))
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
	if key := msg.home + "|" + msg.window; msg.home != "" && msg.window != "" && key != m.viewerSeen && key != m.liveShown {
		m.revealRow(msg.home, msg.window) // a switch to a hidden row unfolds it
	}
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
	msg := "chrome bar hidden in the hub"
	if m.hubChrome {
		msg = "chrome bar shown in the hub"
	}
	m.jumpErr = ""
	m.setSuccess(msg)
	return herdZoomSyncCmd(m.hubChrome)
}

// HubChrome reports whether the live pane shows the agent's chrome bar.
func (m HerdModel) HubChrome() bool { return m.hubChrome }

// dimContextRow mutes a rendered row the attention filter keeps only as an
// ancestor of a matching row: the project bar keeps its colour, everything
// after it goes dim.
func dimContextRow(r herd.Row, line string) string {
	plain := ansi.Strip(line)
	_, rest, ok := strings.Cut(plain, "▎")
	if !ok {
		return herdDimStyle.Render(plain)
	}
	return herdProjectBar(r.Project) + herdDimStyle.Render(rest)
}
