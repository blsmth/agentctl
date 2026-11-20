package tui

import (
	"fmt"
	"hash/fnv"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/hubtarget"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/selfexec"
	"github.com/blsmth/agentctl/internal/servectl"
	"github.com/blsmth/agentctl/internal/sweeprun"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/tmux"
)

// fleetPollInterval is the hub's refresh cadence; polling faster than ~1s
// buys nothing (per the design doc).
const fleetPollInterval = 1 * time.Second

// fleetChromeRows is the number of non-list lines reserved above and below
// the scrollable line list: title(1) + the "↑ N more" row(1), blank when
// nothing is scrolled off, above; the "↓ N more" row(1) + message line(1) +
// hint bar(1) below, plus one spare row.
const fleetChromeRows = 6

// fleetSidebarRatio is the fraction of content width the list column takes
// (see fleetLiveLeftCols); fleetMinSidebarWidth / fleetMinMainPanelWidth are the
// SplitPane minimums, which here only feed its cursor bookkeeping.
const (
	fleetSidebarRatio      = 0.3
	fleetMinSidebarWidth   = 28
	fleetMinMainPanelWidth = 40
)

// fleetMaxNameWidth caps how much of a row's width the agent name can claim,
// even in the wide flat-list fallback, so the recap always keeps some room
// and a long name can't crowd it out entirely.
const fleetMaxNameWidth = 24

// fleetSidebarLeftInset is the left margin sidebar content lines start at —
// specifically the width of a group header's unselected cursor marker (see
// renderGroupHeader). fleetLineDivider uses the same constant so a divider
// between two groups starts at the same column as the headers it separates,
// instead of running flush to the sidebar's left edge.
const fleetSidebarLeftInset = 2

// fleetSidebarRightGutter is the number of blank columns sidebar content lines
// reserve at their right edge so they never touch the hub pane's edge (see
// renderRow's "available" calculation). The close button, the "+ new"
// button and the divider all end this many columns short of the pane width.
const fleetSidebarRightGutter = 1

// ── Styles ───────────────────────────────────────────────────────────────────

var (
	fleetTitleStyle    lipgloss.Style
	fleetDimStyle      lipgloss.Style
	fleetErrStyle      lipgloss.Style
	fleetSuccessStyle  lipgloss.Style // transient success notices in the footer
	fleetSelectedStyle lipgloss.Style
	fleetHoverStyle    lipgloss.Style // row tint, weaker than fleetSelectedStyle

	fleetCloseHoverStyle lipgloss.Style // ✕ under the pointer
	fleetNewHoverStyle   lipgloss.Style // "+ new" under the pointer

	fleetWorkingStyle lipgloss.Style
	fleetBlockedStyle lipgloss.Style
	fleetDoneStyle    lipgloss.Style
	fleetIdleStyle    lipgloss.Style

	fleetProjectStyle lipgloss.Style
	// fleetGroupNameStyle is a group header's project name: neutral bold, so
	// a palette colour never competes with the state colours (the ▎ bar
	// keeps the project colour).
	fleetGroupNameStyle lipgloss.Style
	fleetCountStyle     lipgloss.Style
	fleetActivityStyle  lipgloss.Style
	fleetRecapStyle     lipgloss.Style

	// Row weight by state (see rowWeight): the glyph carries the state
	// colour, the name and recap only get brighter or dimmer.
	fleetNameWorkingStyle  lipgloss.Style
	fleetNameDoneStyle     lipgloss.Style
	fleetNameIdleStyle     lipgloss.Style
	fleetRecapWorkingStyle lipgloss.Style
	fleetRecapIdleStyle    lipgloss.Style

	// Recap styles for a hovered row: the theme's primary text colour, with
	// the activity fallback still italic so its source stays distinguishable.
	fleetRecapHoverStyle    lipgloss.Style
	fleetActivityHoverStyle lipgloss.Style

	// fleetEmptyStyle is the muted italic fallback sentence of a row with no
	// recap text (see rowRecapText).
	fleetEmptyStyle lipgloss.Style

	// fleetPaletteColors is the stable accent palette (theme colours) that
	// project groups hash into: the left bar only.
	fleetPaletteColors []string

	// fleetStylesTheme is the theme name the fleet styles were last built
	// from; the poll tick compares it to the active theme (see
	// refreshThemeIfChanged).
	fleetStylesTheme string
)

// initFleetStyles initialises the hub view's styles from the active theme.
// Reuses the same state colours as the chrome bar's agent tabs (workingTab =
// Accent, blockedTab = Yellow, doneTab = Green, idleTab = Subtle) so the hub
// and the per-session chrome bar agree visually. Those colours belong to the
// state glyphs; agent names use plain text weights so a palette colour can
// never be mistaken for a state.
func initFleetStyles() {
	t := theme.Current()
	fleetStylesTheme = t.Name

	fleetTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Accent))

	fleetDimStyle = tableStyles.Dim
	fleetErrStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Red))
	fleetSuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green))
	fleetSelectedStyle = lipgloss.NewStyle().Background(lipgloss.Color(t.Surface))
	fleetHoverStyle = lipgloss.NewStyle().Background(lipgloss.Color(midColor(t.Base, t.Surface)))
	fleetCloseHoverStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Red))
	fleetNewHoverStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Accent))

	fleetWorkingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent))
	fleetBlockedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Yellow))
	fleetDoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green))
	fleetIdleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))

	fleetProjectStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Sky))
	fleetGroupNameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Text))
	fleetCountStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	fleetActivityStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	fleetRecapStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))

	fleetNameWorkingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Bright))
	fleetNameDoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	fleetNameIdleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))
	fleetRecapWorkingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	fleetRecapIdleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))

	fleetRecapHoverStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	fleetActivityHoverStyle = fleetRecapHoverStyle.Italic(true)
	fleetEmptyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)).Italic(true)

	fleetPaletteColors = []string{t.Accent, t.Sky, t.Blue, t.Info, t.Orange, t.Green}
}

// refreshThemeIfChanged rebuilds every package-level style when the resolved
// theme name differs from the one the styles were built from, and reports
// whether it did. Nothing in the hub caches rendered strings or colours (the
// hover card and row lines are rendered from the styles in View), so
// rebuilding the styles is enough; selection, folds, filter and the viewer
// are untouched.
func refreshThemeIfChanged() bool {
	if theme.Current().Name == fleetStylesTheme {
		return false
	}
	InitStyles()
	return true
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

// fleetPalette returns the accent palette, falling back to the active theme
// if styles have not been initialised.
func fleetPalette() []string {
	if len(fleetPaletteColors) == 0 {
		t := theme.Current()
		return []string{t.Accent, t.Sky, t.Blue, t.Info, t.Orange, t.Green}
	}
	return fleetPaletteColors
}

// fleetColorFor picks a stable palette colour for name: the same name always
// maps to the same colour.
func fleetColorFor(name string) string {
	pal := fleetPalette()
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

// fleetWeight is a row's visual weight, derived from its state the same way
// rowGlyph picks the glyph.
type fleetWeight int

const (
	fleetWeightIdle fleetWeight = iota
	fleetWeightDone
	fleetWeightWorking
	fleetWeightBlocked
)

// rowWeight classifies r for name and recap styling.
func rowWeight(r fleet.Row) fleetWeight {
	switch {
	case r.State == "blocked":
		return fleetWeightBlocked
	case r.State == "working":
		return fleetWeightWorking
	case r.Done:
		return fleetWeightDone
	default:
		return fleetWeightIdle
	}
}

// fleetNameStyle is the agent name's style for a row of weight w: working is
// bold bright, done normal text, idle subtle and blocked bold warning. The
// state colour lives on the glyph; names never use the project palette.
func fleetNameStyle(w fleetWeight) lipgloss.Style {
	switch w {
	case fleetWeightBlocked:
		return fleetBlockedStyle.Bold(true)
	case fleetWeightWorking:
		return fleetNameWorkingStyle
	case fleetWeightDone:
		return fleetNameDoneStyle
	default:
		return fleetNameIdleStyle
	}
}

// renderAgentName renders the agent name in its row-weight style and keeps
// ":N" muted. The caller passes the already-truncated plain name.
func renderAgentName(name string, w fleetWeight) string {
	return renderAgentNameHover(name, w, false)
}

// renderAgentNameHover is renderAgentName; a hovered name is also bold.
func renderAgentNameHover(name string, w fleetWeight, hovered bool) string {
	base, suffix := splitAgentName(name)
	style := fleetNameStyle(w)
	if hovered {
		style = style.Bold(true)
	}
	out := style.Render(base)
	if suffix != "" {
		out += fleetCountStyle.Render(suffix)
	}
	return out
}

// ── Messages ─────────────────────────────────────────────────────────────────

// fleetTickMsg drives the hub's poll loop.
type fleetTickMsg struct{}

// fleetLoadedMsg carries the result of an async fleet.Load() call.
type fleetLoadedMsg struct {
	groups  []fleet.Group
	err     error
	display fleetDisplay
}

// ── Flattened line model ─────────────────────────────────────────────────────

// fleetLineKind distinguishes the three kinds of renderable lines in the
// hub's flattened, scrollable list.
type fleetLineKind int

const (
	fleetLineHeader      fleetLineKind = iota // a group's session/project/rollup header
	fleetLinePlaceholder                      // "no agents" / "snapshot unavailable"
	fleetLineRow                              // a selectable agent row
	fleetLineDivider                          // a non-selectable rule between two groups
	fleetLineSubheader                        // a non-selectable "dispatched (N)" label
	fleetLineClosedLabel                      // a non-selectable Closed section or project label
	fleetLineClosed                           // a selectable closed (ledger) row; see fleet_closed.go
)

// fleetLine is one renderable line. Every kind except fleetLineDivider is also
// selectable — see the "selection" section below.
type fleetLine struct {
	kind       fleetLineKind
	group      fleet.Group // set for fleetLineHeader / fleetLinePlaceholder
	row        fleet.Row   // set for fleetLineRow
	groupHead  bool        // true for the first row of a group (blank line above)
	label      string      // set for fleetLineSubheader, e.g. "dispatched (2)"
	treePrefix string      // connector prefix for fleetLineRow ("" for roots)
	// nameCol is the group's uncapped name-column width (tree prefix plus
	// base name, max over its rows) for fleetLineRow; renderTreeRow caps it
	// at render time so recaps line up within a group.
	nameCol int
	// dimmed marks a fleetLineRow the attention filter keeps only as the
	// ancestor of a matching row; it renders muted.
	dimmed bool
	// closed is the ledger record of a fleetLineClosed row.
	closed closedlog.Record
}

// selectable reports whether the cursor may rest on this line. Dividers and
// sub-section labels are decoration only.
func (l fleetLine) selectable() bool {
	return l.kind != fleetLineDivider && l.kind != fleetLineSubheader && l.kind != fleetLineClosedLabel
}

// ── FleetModel ────────────────────────────────────────────────────────────────

// FleetModel is the full-screen Bubble Tea model for the fleet hub: the
// cross-session aggregator that runs as the sole window of the agentctl-hub
// tmux session. Data is loaded from internal/fleet, which does all the
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
type FleetModel struct {
	termWidth  int
	termHeight int

	groups []fleet.Group
	lines  []fleetLine

	// display is the config the row chips depend on (context thresholds,
	// default account), refreshed with every fleet load.
	display fleetDisplay
	// except is the list-wide state of the rows' model and context exception
	// columns, recomputed by rebuildLines.
	except fleetExceptions

	// splitPane owns cursor position and scrolling bookkeeping. FleetModel does
	// NOT delegate key handling to SplitPane.Update(): the list mixes
	// selectable headers and rows, which is simpler to express directly in
	// handleKey/moveCursor below.
	splitPane SplitPane

	// scrollOffset is the list's vertical scroll position.
	scrollOffset int
	// wheelHold is set by a mouse-wheel scroll and stops ensureCursorVisible
	// from snapping the viewport back to the cursor (notably on the reload
	// tick). A key press or a selection change clears it.
	wheelHold bool

	// selSession/selWindowID/selIsHeader identify the selection by
	// (session, windowID) — or by session alone when a header/placeholder
	// is selected — rather than by raw index, so a refresh that reorders
	// lines (a state change) doesn't move the cursor out from under the
	// user.
	selSession  string
	selWindowID string
	selIsHeader bool
	// selClosed is the selected closed row's closedlog Key ("" when the
	// selection is not a closed row; selSession is then empty, so the live
	// viewer is never handed a window that no longer exists).
	selClosed string

	// lastSelection is the selection last written to the hub selection file
	// for the follow-hub HUD (selPublished: one has been written). See
	// publishSelection.
	lastSelection HubSelection
	selPublished  bool

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

	// sweepSession/sweepLine/sweepPlan are the group whose sweep preview is
	// awaiting y/n, the cursor line it was armed on and the previewed plan;
	// empty when none. Cleared when the cursor moves off sweepLine.
	// sweepBusy is the session a sweep subprocess (preview or apply) is
	// running for; a second sweep is refused until it reports.
	sweepSession string
	sweepLine    int
	sweepPlan    sweeprun.Report
	sweepBusy    string

	// closePending remembers where a just-closed row sat so the next reload
	// can select its neighbour instead of jumping to the first row. Cleared
	// after one use.
	closePending *fleetClosePending

	// menuOpen/menuLine: the right-click context menu (rendered in the footer)
	// and the line it was opened on.
	menuOpen bool
	menuLine int
	// menuHelp marks the open menu as the "?" key reference (every binding,
	// see fleetHelpItems) rather than a line's context menu.
	menuHelp bool

	// liveShown is the "session|window" last handed to the viewer, so
	// selection changes (not every tick) drive it; a failed attempt is retried
	// only once the selection changes. liveLeft/liveRight are the column split
	// of the hub window (list column / live pane).
	liveShown string
	// liveFailed is the liveShown key whose hand-off to the viewer failed, so
	// the viewer is not showing it.
	liveFailed string
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
	pendingFocus *fleetPendingFocus

	// focusOnShow is the "session|window" key of a launched row whose live
	// pane should take tmux focus once it is showing.
	focusOnShow string

	// hover is the line/region under the pointer (see fleet_mouse.go);
	// hoverX/hoverY/hasPointer remember the last pointer cell so the target
	// can be recomputed when the content moves under a still pointer.
	hover      fleetHover
	hoverX     int
	hoverY     int
	hasPointer bool

	// card is the visible hover card (see fleet_card.go); hoverGen invalidates
	// pending dwell ticks; cardCache holds parsed log stats by log path.
	card      *fleetCard
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

	// folded is the set of folded groups by session name (see fleet_fold.go):
	// a folded group renders as its header line only. focusMode records that
	// `Z` folded the quiet groups, so the next `Z` unfolds everything. filter
	// is the attention filter `f` cycles. All three survive reloads, the hot
	// reload re-exec (HubViewEnv) and hub restarts (viewPath, "" = not
	// persisted).
	folded    map[string]bool
	focusMode bool
	// sortRecent orders rows inside each group by latest activity (`o`).
	sortRecent bool
	// showShells shows plain shell rows ("$ shell"); hidden by default
	// (fleetShellsKey).
	showShells bool
	// showClosed shows the Closed section (fleetClosedKey, fleet_closed.go);
	// closed is its last ledger read, closedCache the reader (shared by the
	// model's copies).
	showClosed  bool
	closed      []closedlog.Record
	closedCache *closedlog.Cache
	filter      fleetFilter
	viewPath    string
	// orderPath is the saved group order (see fleet_order.go); "" persists
	// nothing. drag is a pending or active header drag.
	orderPath string
	drag      *fleetDrag
	// viewDirty marks a fold/filter change made on the way to a jump
	// (revealRow) that flushView has not persisted yet.
	viewDirty bool

	// serveState is agentctl serve's state from the last probe (see
	// fleet_serve.go); serveKnown is false until the first one lands.
	// serveProbing and serveBusy keep one probe and one toggle in flight.
	serveState   servectl.State
	serveKnown   bool
	serveProbing bool
	serveBusy    bool

	// dirPrompt is the open new-session (N) directory prompt (see
	// fleet_newsession.go); nil when closed.
	dirPrompt *fleetNewSessionState

	// rename is the open title editor (see fleet_rename.go); nil when closed.
	// titleHolds are just-saved titles shown until a load reflects them, by
	// "session|windowID".
	rename     *fleetRenameState
	titleHolds map[string]fleetTitleHold

	// activity is the activity pane (see fleet_activity.go). paneQuietUntil
	// is set while it opens, slides or closes: list-pane resizes until then
	// are tmux reflowing the panes, not the user dragging the divider.
	activity       fleetActivity
	paneQuietUntil time.Time
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
func (m FleetModel) ReexecEnv() []string {
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
func (m FleetModel) Reexec() bool { return m.reexec }

// WithoutReload returns the model reset to keep running after a failed
// re-exec: hot reload is disabled and the current selection is re-applied once
// the list loads.
func (m FleetModel) WithoutReload() FleetModel {
	m.exeChanged, m.reexec = nil, false
	m.pendingFocus = nil
	if m.selSession != "" && m.selWindowID != "" && !m.selIsHeader {
		m.pendingFocus = &fleetPendingFocus{session: m.selSession, windowID: m.selWindowID}
	}
	m.loaded = false
	return m
}

// SelectionEnv returns the HubSelectEnv entry ("KEY=session|windowID") for the
// current selection, or "" when no agent row is selected.
func (m FleetModel) SelectionEnv() string {
	if m.selIsHeader || m.selSession == "" || m.selWindowID == "" {
		return ""
	}
	return HubSelectEnv + "=" + m.selSession + "|" + m.selWindowID
}

// NewFleetModel creates the hub aggregator model. Data is loaded
// asynchronously via Init().
func NewFleetModel() FleetModel {
	m := newFleetModel()
	if id, err := selfexec.Capture(); err != nil {
		logging.Log("hub: hot reload disabled: %v", err)
	} else {
		m.exeChanged = func() bool { return selfexec.CheckChanged(id, time.Now()) }
	}
	if v, ok := os.LookupEnv(HubSelectEnv); ok {
		_ = os.Unsetenv(HubSelectEnv) // consume once; don't leak to child processes
		if session, win, ok := parseHubSelect(v); ok {
			m.pendingFocus = &fleetPendingFocus{session: session, windowID: win}
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

func newFleetModel() FleetModel {
	const defaultWidth = 120
	left, right, _ := fleetLiveSplit(defaultWidth)
	return FleetModel{
		termWidth:   defaultWidth,
		termHeight:  40,
		liveLeft:    left,
		liveRight:   right,
		closedCache: &closedlog.Cache{},
		splitPane: SplitPane{
			Orientation:   OrientHorizontal,
			ListRatio:     fleetSidebarRatio,
			MinListRows:   fleetMinSidebarWidth,
			MinDetailRows: fleetMinMainPanelWidth,
		},
	}
}

// Init kicks off the first load and starts the poll loop.
func (m FleetModel) Init() tea.Cmd {
	return tea.Batch(fleetLoadCmd(), fleetTickCmd(), fleetZoomSyncCmd(m.hubChrome), fleetServeProbeOnce(), m.closedLoadCmd())
}

// fleetServeProbeOnce is the first serve probe. Init has a value receiver,
// so it cannot mark the probe in flight; the first tick may then start a
// second one, which is harmless.
func fleetServeProbeOnce() tea.Cmd {
	return func() tea.Msg { return fleetServeProbedMsg{state: fleetServeProbe()} }
}

func fleetTickCmd() tea.Cmd {
	return tea.Tick(fleetPollInterval, func(time.Time) tea.Msg { return fleetTickMsg{} })
}

func fleetLoadCmd() tea.Cmd {
	return func() tea.Msg {
		groups, err := fleet.LoadWithShells()
		return fleetLoadedMsg{groups: groups, err: err, display: fleetDisplayLoad()}
	}
}

// maybeShowCmd hands the selected agent to the live viewer, or returns nil if
// nothing selectable is an agent row (a header, placeholder, or empty hub is
// selected): the live pane then keeps showing the last viewed agent.
func (m *FleetModel) maybeShowCmd() tea.Cmd {
	if m.selIsHeader || m.selSession == "" || m.selWindowID == "" {
		return nil
	}
	return m.liveShowCmd()
}

// Update handles Bubble Tea messages, then publishes the selection for the
// follow-hub HUD if it changed.
func (m FleetModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if fm, ok := next.(FleetModel); ok {
		fm.publishSelection()
		return fm, cmd
	}
	return next, cmd
}

func (m FleetModel) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.configureSplitPane()
		return m, fleetProbeCmd(msg.Width)

	case fleetProbeMsg:
		if msg.win == m.liveWinWidth && fleetNow().Before(m.paneQuietUntil) {
			return m, nil // the activity pane reflowed the split
		}
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
		if m.activity.open() {
			// Three panes: pin the list and activity columns and let the
			// viewer between them take the rest (resizing the viewer would
			// move the wrong divider).
			return m, m.applyActivityLayout(msg.win)
		}
		left, right := d.left, d.right
		return m, func() tea.Msg {
			return fleetLiveSizedMsg{left: left, right: right, err: tmux.ResizeLiveViewer(right)}
		}

	case fleetTickMsg:
		m.expireInfo(fleetNow())
		// A hot reload waits for an open rename editor: it would lose the
		// typed text.
		if m.exeChanged != nil && !m.renameOpen() && m.exeChanged() {
			m.reexec = true
			return m, tea.Quit
		}
		refreshThemeIfChanged()
		show := m.maybeShowCmd()
		probe := m.serveProbeCmd()
		return m, tea.Batch(fleetLoadCmd(), fleetTickCmd(), show, fleetViewerPollCmd(), fleetZoomSyncCmd(m.hubChrome), probe, m.activityPollCmd(), m.closedLoadCmd())

	case fleetClosedLoadedMsg:
		m.applyClosedLoaded(msg)
		return m, nil

	case fleetActivityOpenedMsg:
		return m, m.applyActivityOpened(msg)

	case fleetActivitySlideMsg:
		return m, m.applyActivitySlide(msg)

	case fleetActivityPanesMsg:
		return m, m.applyActivityPanes(msg)

	case fleetActivityErrMsg:
		m.jumpErr = "activity: " + msg.err.Error()
		return m, nil

	case fleetServeProbedMsg:
		m.applyServeProbe(msg)
		return m, nil

	case fleetServeToggledMsg:
		return m, m.applyServeToggled(msg)

	case fleetViewerMsg:
		m = m.syncFromViewer(msg)
		return m, m.flushView()

	case fleetFocusErrMsg:
		m.jumpErr = msg.err.Error()
		return m, nil
	case fleetLiveMsg:
		if msg.hidden {
			// The activity pane replaces the viewer at this window width.
			m.liveShown = ""
			if !m.activity.open() {
				return m, m.maybeShowCmd() // it closed meanwhile: show it after all
			}
			m.activity.hidesViewer = true
			return m, nil
		}
		if msg.err != nil {
			// No fallback: surface the error; the next selection change retries.
			m.liveFailed = msg.key
			m.ensureCursorVisible() // the selected row regains its activity line
			m.jumpErr = msg.err.Error()
			return m, func() tea.Msg {
				_ = tmux.HideLiveViewer() // drop any half-built viewer
				return nil
			}
		}
		m.jumpErr = ""
		if m.liveFailed == msg.key {
			m.liveFailed = ""
		}
		m.liveLeft, m.liveRight = msg.left, msg.right
		if msg.win > 0 {
			m.liveWinWidth = msg.win
		}
		m.configureSplitPane()
		if m.focusOnShow != "" {
			sel := m.selSession + "|" + m.selWindowID
			if msg.key == m.focusOnShow && msg.key == sel && msg.key == m.liveShown {
				m.focusOnShow = ""
				return m, focusViewerCmd()
			}
			if m.focusOnShow != sel {
				m.focusOnShow = "" // the user moved on
			}
		}
		return m, nil

	case fleetLiveSizedMsg:
		if msg.err != nil {
			m.jumpErr = msg.err.Error()
			return m, nil
		}
		m.liveLeft, m.liveRight = msg.left, msg.right
		m.configureSplitPane()
		return m, nil

	case fleetLaunchedMsg:
		if msg.err != nil {
			m.clearInfo()
			m.jumpErr = msg.err.Error()
			return m, nil
		}
		m.jumpErr = ""
		m.setSuccess("launched new agent in " + m.projectLabel(msg.session))
		m.pendingFocus = nil
		if msg.windowID == "" {
			return m, fleetLoadCmd()
		}
		m.pendingFocus = &fleetPendingFocus{session: msg.session, windowID: msg.windowID, focus: true}
		return m, tea.Batch(fleetLoadCmd(), fleetFocusRetryCmd(msg.session, msg.windowID))

	case fleetShellOpenedMsg:
		if msg.err != nil {
			m.clearInfo()
			m.jumpErr = msg.err.Error()
			return m, nil
		}
		m.jumpErr = ""
		m.setSuccess("opened shell in " + m.projectLabel(msg.session))
		m.pendingFocus = nil
		var save tea.Cmd
		if !m.showShells {
			// The new shell must be visible to be focused when it appears.
			m.setShowShells(true)
			save = m.saveViewCmd()
		}
		if msg.windowID == "" {
			return m, tea.Batch(fleetLoadCmd(), save)
		}
		m.pendingFocus = &fleetPendingFocus{session: msg.session, windowID: msg.windowID, focus: true}
		return m, tea.Batch(fleetLoadCmd(), fleetFocusRetryCmd(msg.session, msg.windowID), save)

	case fleetFocusRetryMsg:
		return m, m.focusRetry(msg)

	case fleetSweptMsg:
		return m, m.applySwept(msg)

	case fleetClosedMsg:
		if msg.err != nil {
			m.closePending = nil
			m.clearInfo()
			m.jumpErr = msg.err.Error()
			return m, fleetLoadCmd()
		}
		m.jumpErr = ""
		if m.closePending != nil {
			m.closePending.killed = true
		}
		m.setSuccess("closed " + msg.name + " (worktree kept; see agentctl sweep)")
		return m, fleetLoadCmd()

	case fleetLoadedMsg:
		justApplied := false
		m.loaded = true
		m.loadErr = msg.err
		if msg.err == nil {
			m.groups = msg.groups
			m.display = msg.display
			m.applyTitleHolds()
			m.rebuildLines()
			m.validateNew()
			m.validateRename()
			m.validateSweep()
			m.refreshHover()
			m.applyClosePending()
			selBefore := [3]any{m.selSession, m.selWindowID, m.selIsHeader}
			m.resolveSelection()
			if selBefore != [3]any{m.selSession, m.selWindowID, m.selIsHeader} {
				m.wheelHold = false // the selection moved: follow it
				m.ensureCursorVisible()
			}
			p := m.pendingFocus
			if m.applyPendingFocus() && p != nil && p.focus {
				m.focusOnShow = m.selSession + "|" + m.selWindowID
				justApplied = true
			}
			m.clampScroll()
		}
		show := m.maybeShowCmd()
		var focus tea.Cmd
		if show == nil && justApplied && m.focusOnShow == m.selSession+"|"+m.selWindowID && m.liveShown == m.focusOnShow && m.liveFailed != m.focusOnShow {
			// Already showing: focus now.
			m.focusOnShow = ""
			focus = focusViewerCmd()
		}
		return m, tea.Batch(show, focus, m.flushView())

	case tea.MouseClickMsg:
		m.hideCard()
		return m.pressHeader(msg)

	case tea.MouseReleaseMsg:
		return m.releaseDrag(msg)

	case tea.MouseWheelMsg:
		m.hideCard()
		m.handleWheel(msg)
		return m, nil

	case fleetCardTickMsg:
		return m.handleCardTick(msg)

	case fleetCardStatsMsg:
		m.storeCardStats(msg)
		return m, nil

	case tea.MouseMotionMsg:
		if m.drag != nil {
			if next, cmd, ok := m.dragMotion(msg); ok {
				return next, cmd
			}
		}
		return m.handleMotion(msg)

	case tea.PasteMsg:
		if m.newSessionOpen() {
			var cmd tea.Cmd
			m.dirPrompt.input, cmd = m.dirPrompt.input.Update(msg)
			return m, cmd
		}
		if m.renameOpen() {
			var cmd tea.Cmd
			m.rename.input, cmd = m.rename.input.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.KeyPressMsg:
		m.hideCard()
		m.wheelHold = false
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *FleetModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	// An error stays on screen until the next key, which dismisses it.
	m.jumpErr = ""
	if m.drag != nil {
		m.drag = nil // any key abandons a drag; esc is just that
		if key == "esc" {
			return *m, nil
		}
	}
	if m.newSessionOpen() {
		return *m, m.handleNewSessionKey(msg)
	}
	if m.renameOpen() {
		return *m, m.handleRenameKey(msg)
	}
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
		for _, it := range m.fleetMenuItems(m.menuLine) {
			if it.key == key {
				return *m, m.runMenuKey(key)
			}
		}
		m.closeMenu()
		return *m, nil
	}
	if m.sweepArmed() {
		// y applies; anything else cancels, and keys other than n/esc then
		// run as usual.
		if key == "y" || key == "Y" {
			return *m, m.confirmSweep()
		}
		m.cancelSweep()
		if key == "n" || key == "N" || key == "esc" {
			return *m, nil
		}
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
func (m *FleetModel) runKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "?":
		m.openHelp()
		return *m, nil

	case "n":
		m.armNew(m.splitPane.Cursor())
		return *m, nil

	case fleetNewSessionKey:
		m.openNewSession()
		return *m, nil

	case fleetRenameKey:
		m.openRename(m.splitPane.Cursor())
		return *m, nil

	case "x":
		m.startClose(m.splitPane.Cursor())
		return *m, nil

	case fleetSweepKey:
		return *m, m.startSweep(m.splitPane.Cursor())

	case "p":
		return *m, m.openSelectedPR()

	case "ctrl+c":
		m.quitting = true
		return *m, tea.Quit

	case "q", "esc":
		home, _ := tmux.ViewerActiveWindow()
		hubtarget.LeaveHub(fleetLeaver{m: m, home: home}, "")
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
		if c := m.selectedClosed(); c != nil {
			return *m, m.resumeClosed(*c)
		}
		if m.selectedRow() == nil {
			return *m, nil
		}
		return m.focusLive()

	case "tab":
		if m.selectedClosed() != nil {
			return *m, nil // nothing live to focus
		}
		return m.focusLive()

	case "J":
		return m.jumpToSelectedHome()

	case "b":
		return m.jumpToNextBlocked()

	case "r":
		m.jumpErr = ""
		m.clearInfo()
		return *m, fleetLoadCmd()

	case "c":
		return *m, m.toggleChrome()

	case "shift+up":
		m.moveSelectedGroup(-1)
		return *m, nil

	case "shift+down":
		m.moveSelectedGroup(1)
		return *m, nil

	case "z":
		return *m, m.toggleFoldAt(m.splitPane.Cursor())

	case "Z":
		return *m, m.toggleFocusMode()

	case "f":
		return *m, m.cycleFilter()

	case "o":
		return *m, m.toggleSort()

	case fleetShellsKey:
		return *m, m.toggleShells()

	case fleetClosedKey:
		return *m, m.toggleClosed()

	case fleetServeKey:
		return *m, m.toggleServe()

	case fleetActivityKey:
		return *m, m.toggleActivity()
	}
	return *m, nil
}

// fleetLiveMsg reports the outcome of handing a selection to the live viewer,
// along with the column split that was applied.
type fleetLiveMsg struct {
	key         string
	win         int // hub window width the split was computed for
	left, right int
	err         error
	// hidden reports that nothing was shown: the activity pane takes the
	// viewer's place at this window width.
	hidden bool
}

// fleetLiveSizedMsg reports the outcome of re-applying the live split sizes.
type fleetLiveSizedMsg struct {
	left, right int
	err         error
}

// Live-mode list column bounds. The list column is the sidebar width the
// split layout would use (fleetSidebarRatio of the content width) plus the
// content margin, floored so rows stay readable and capped so a very wide
// terminal does not hand the list most of the screen.
const (
	fleetLiveMinLeft  = 44
	fleetLiveMaxLeft  = 56
	fleetLiveMinRight = 20
)

// fleetLiveLeftCols is the width of the list column for a hub window
// totalWidth columns wide.
func fleetLiveLeftCols(totalWidth int) int {
	left := int(float64(totalWidth-4)*fleetSidebarRatio) + 4
	if left < fleetLiveMinLeft {
		left = fleetLiveMinLeft
	}
	if left > fleetLiveMaxLeft {
		left = fleetLiveMaxLeft
	}
	return left
}

// fleetLiveUserMinLeft is the narrowest list column a mouse drag may leave.
const fleetLiveUserMinLeft = 30

// clampUserLeft bounds a user-chosen list width to [fleetLiveUserMinLeft,
// totalWidth-fleetLiveMinRight-1] so the live pane always stays usable.
func clampUserLeft(left, totalWidth int) int {
	maxLeft := totalWidth - fleetLiveMinRight - 1
	if left > maxLeft {
		left = maxLeft
	}
	if left < fleetLiveUserMinLeft {
		left = fleetLiveUserMinLeft
	}
	return left
}

// fleetLiveSplit is fleetLiveSplitFor with no user-chosen width.
func fleetLiveSplit(totalWidth int) (left, right int, err error) {
	return fleetLiveSplitFor(totalWidth, 0)
}

// fleetLiveSplitFor splits a window totalWidth columns wide into the list
// column and the live pane (one column goes to the pane border). userLeft, when
// positive, is the list width the user dragged the divider to and replaces the
// computed default (clamped). err is set when the live pane would be unusably
// narrow.
func fleetLiveSplitFor(totalWidth, userLeft int) (left, right int, err error) {
	left = fleetLiveLeftCols(totalWidth)
	if userLeft > 0 {
		left = clampUserLeft(userLeft, totalWidth)
	}
	right = totalWidth - left - 1
	if right < fleetLiveMinRight {
		return left, right, fmt.Errorf("terminal too narrow for live pane")
	}
	return left, right, nil
}

// fleetResizeDecision is the outcome of decideLiveResize.
type fleetResizeDecision struct {
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
//   - window width unchanged and the pane differs from what we applied (and
//     is narrower than the window, so a divider exists): the user dragged
//     the divider; remember it (clamped) and leave tmux alone
//     unless clamping moved it;
//   - otherwise nothing.
func decideLiveResize(winWidth, lastWin, appliedLeft, paneWidth, userLeft int) fleetResizeDecision {
	d := fleetResizeDecision{userLeft: userLeft, winWidth: lastWin}
	if winWidth <= 0 {
		return d
	}
	if winWidth != lastWin {
		left, right, err := fleetLiveSplitFor(winWidth, userLeft)
		d.winWidth, d.left, d.right, d.err = winWidth, left, right, err
		d.apply = err == nil
		return d
	}
	// A list pane as wide as the window has no divider to drag: there is no
	// live pane (it has not started, or died), so nothing to remember.
	if paneWidth <= 0 || paneWidth == appliedLeft || paneWidth >= winWidth {
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
	if w, err := hubWindowWidth(); err == nil && w > 0 {
		return w
	}
	return fallback
}

// fleetProbeMsg carries the hub window's width (0 when tmux could not say)
// alongside the list pane width that triggered the probe.
type fleetProbeMsg struct {
	pane, win int
}

// fleetProbeCmd asks tmux for the hub window width after a pane resize.
func fleetProbeCmd(paneWidth int) tea.Cmd {
	return func() tea.Msg {
		w, _ := tmux.HubWindowWidth()
		return fleetProbeMsg{pane: paneWidth, win: w}
	}
}

// liveShowCmd hands the current selection to the live viewer, once per
// selection change (a failed attempt is not retried until the selection
// changes again). The split honours a user-dragged divider.
func (m *FleetModel) liveShowCmd() tea.Cmd {
	if m.activity.hidesViewer {
		return nil // the activity pane has the viewer's place
	}
	key := m.selSession + "|" + m.selWindowID
	if key == m.liveShown {
		return nil
	}
	m.liveShown = key
	session, window := m.selSession, m.selWindowID
	fallback, userLeft := m.termWidth, m.userLeft
	shown := m.hubChrome
	activity, userAct := m.activity.open(), m.activity.userWidth
	return func() tea.Msg {
		win := liveWindowWidth(fallback)
		left, right, err := fleetLiveSplitFor(win, userLeft)
		if err == nil && activity {
			lay := decideHubLayout(win, left, userAct)
			if lay.hideViewer {
				return fleetLiveMsg{key: key, win: win, hidden: true}
			}
			if lay.err == nil {
				right = lay.viewer
			}
		}
		if err == nil {
			err = showLiveViewer(session, window, right)
			if err == nil {
				hubZoomSync(!shown)
			}
		}
		return fleetLiveMsg{key: key, left: left, right: right, win: win, err: err}
	}
}

// configureSplitPane sizes the SplitPane from the current terminal
// dimensions, reserving the same chrome rows as the flat fallback so both
// layouts occupy the same vertical footprint.
func (m *FleetModel) configureSplitPane() {
	h := m.termHeight - fleetChromeRows
	if h < 3 {
		h = 3
	}
	m.splitPane.SetSize(m.contentWidth(), h)
	m.clampScroll() // the list area may have grown
}

// ── Selection / navigation ───────────────────────────────────────────────────

// selectedRow returns the currently selected agent row, or nil if the
// cursor is on a group header/placeholder or nothing is selected.
func (m *FleetModel) selectedRow() *fleet.Row {
	idx := m.splitPane.Cursor()
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != fleetLineRow {
		return nil
	}
	r := m.lines[idx].row
	return &r
}

// selectedGroup returns the currently selected group, when the cursor is on
// that group's header or placeholder line (not one of its agent rows).
func (m *FleetModel) selectedGroup() *fleet.Group {
	idx := m.splitPane.Cursor()
	if idx < 0 || idx >= len(m.lines) {
		return nil
	}
	l := m.lines[idx]
	if l.kind == fleetLineHeader || l.kind == fleetLinePlaceholder {
		g := l.group
		return &g
	}
	return nil
}

// syncSelectionFromCursor refreshes selSession/selWindowID/selIsHeader from
// whatever line the SplitPane's cursor currently points at.
func (m *FleetModel) syncSelectionFromCursor() {
	defer func() {
		if m.closeArmed() && (m.selSession != m.confirmSession || m.selWindowID != m.confirmWindow) {
			m.cancelClose()
		}
		if m.newArmed() && m.splitPane.Cursor() != m.newLine {
			m.cancelNew()
		}
		if m.sweepArmed() && m.splitPane.Cursor() != m.sweepLine {
			m.cancelSweep()
		}
		m.validateNew()
		if m.menuOpen && m.splitPane.Cursor() != m.menuLine {
			m.closeMenu()
		}
	}()
	idx := m.splitPane.Cursor()
	if idx < 0 || idx >= len(m.lines) {
		m.selSession, m.selWindowID, m.selIsHeader, m.selClosed = "", "", false, ""
		return
	}
	l := m.lines[idx]
	if l.kind != fleetLineDivider && l.kind != fleetLineSubheader && l.kind != fleetLineClosedLabel {
		m.selClosed = ""
	}
	switch l.kind {
	case fleetLineRow:
		m.selSession = l.row.Session
		m.selWindowID = l.row.WindowID
		m.selIsHeader = false
	case fleetLineClosed:
		m.selSession, m.selWindowID, m.selIsHeader = "", "", false
		m.selClosed = l.closed.Key()
	case fleetLineDivider, fleetLineSubheader, fleetLineClosedLabel:
		// Unreachable in practice — moveCursor/resolveSelection never park
		// the cursor on a divider or subheader — but defend against adopting a
		// divider's zero-value Group as the selection rather than leaving
		// whatever was previously selected in place.
	default: // fleetLineHeader / fleetLinePlaceholder
		m.selSession = l.group.Session
		m.selWindowID = ""
		m.selIsHeader = true
	}
}

// moveCursor moves the SplitPane cursor by delta lines, skipping over any
// fleetLineDivider it would otherwise land on (headers, placeholders, and
// rows are all still selectable, as in the pre-divider design).
func (m *FleetModel) moveCursor(delta int) {
	if len(m.lines) == 0 {
		return
	}
	// Content itself is rebuilt fresh in render(); this call just keeps
	// SplitPane's totalRows (used for cursor clamping) in sync immediately,
	// rather than waiting for the next View().
	m.splitPane.SetListContent(m.splitPane.listContent, len(m.lines))
	m.pendingFocus = nil
	m.focusOnShow = ""
	m.wheelHold = false
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
func (m *FleetModel) nextSelectable(cursor, delta int) int {
	if delta == 0 || len(m.lines) == 0 {
		return cursor
	}
	idx := cursor
	for {
		next := idx + delta
		if next < 0 || next >= len(m.lines) {
			// Boundary reached: stay put unless the walk ended on a stop
			// (the Closed section can end in a label line).
			if m.lines[idx].selectable() {
				return idx
			}
			return cursor
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
func (m *FleetModel) resolveSelection() {
	// Content itself is rebuilt fresh in render(); this call just keeps
	// SplitPane's totalRows (used for cursor clamping) in sync immediately,
	// rather than waiting for the next View().
	m.splitPane.SetListContent(m.splitPane.listContent, len(m.lines))

	if m.selClosed != "" {
		for i, l := range m.lines {
			if l.kind == fleetLineClosed && l.closed.Key() == m.selClosed {
				m.splitPane.SetCursor(i)
				m.syncSelectionFromCursor()
				m.ensureCursorVisible()
				return
			}
		}
		m.selClosed = ""
	}

	if m.selSession != "" {
		for i, l := range m.lines {
			if m.selIsHeader {
				if (l.kind == fleetLineHeader || l.kind == fleetLinePlaceholder) && l.group.Session == m.selSession {
					m.splitPane.SetCursor(i)
					m.syncSelectionFromCursor()
					m.ensureCursorVisible()
					return
				}
				continue
			}
			if l.kind == fleetLineRow && l.row.Session == m.selSession && l.row.WindowID == m.selWindowID {
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
				if l.kind == fleetLineHeader && l.group.Session == m.selSession {
					m.splitPane.SetCursor(i)
					m.syncSelectionFromCursor()
					m.ensureCursorVisible()
					return
				}
			}
		}
	}

	for i, l := range m.lines {
		if l.kind == fleetLineRow {
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

	m.selSession, m.selWindowID, m.selIsHeader, m.selClosed = "", "", false, ""
}

func (m *FleetModel) visibleLines() int {
	h := m.termHeight - fleetChromeRows - m.liveHintRows()
	if h < 3 {
		h = 3
	}
	return h
}

// ensureCursorVisible keeps the flat fallback's own scroll position
// (scrollOffset) following the cursor. It's a no-op while wheelHold is set
// (the user wheel-scrolled; a key press or selection change clears it).
func (m *FleetModel) ensureCursorVisible() {
	if len(m.lines) == 0 || m.wheelHold {
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
func (m *FleetModel) rowsBetween(from, to, cw int) int {
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
	focusViewerPane       = tmux.FocusViewerPane
	hubWindowWidth        = tmux.HubWindowWidth
	showLiveViewer        = tmux.ShowLiveViewer
)

// focusLive is Enter's and tab's action: move tmux focus to the live pane so
// keystrokes go to the viewed agent. Prefix+o returns to the list.
func (m *FleetModel) focusLive() (tea.Model, tea.Cmd) {
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
func (m *FleetModel) jumpToSelectedHome() (tea.Model, tea.Cmd) {
	r := m.selectedRow()
	if r == nil {
		return *m, nil
	}
	if err := switchToSessionWindow(r.Session, r.WindowID); err != nil {
		m.jumpErr = err.Error()
		return *m, fleetLoadCmd()
	}
	m.jumpErr = ""
	m.clearInfo()
	return *m, nil
}

// jumpToNextBlocked selects the first blocked agent across all sessions,
// using the same internal/fleet ordering the view itself renders with, shows
// it in the live pane, and moves tmux focus there so the user can answer the
// prompt immediately. It never switches the client out of the hub; J is the
// explicit way to leave.
func (m *FleetModel) jumpToNextBlocked() (tea.Model, tea.Cmd) {
	r := fleet.FirstBlocked(m.groups)
	if r == nil {
		m.jumpErr = "no blocked agents"
		return *m, nil
	}
	m.revealRow(r.Session, r.WindowID) // unfold its group first
	found := false
	for i, l := range m.lines {
		if l.kind == fleetLineRow && l.row.Session == r.Session && l.row.WindowID == r.WindowID {
			m.splitPane.SetCursor(i)
			found = true
			break
		}
	}
	if !found {
		m.jumpErr = "blocked agent not in the list"
		return *m, fleetLoadCmd()
	}
	m.jumpErr = ""
	m.syncSelectionFromCursor()
	m.ensureCursorVisible()
	focus := func() tea.Msg {
		if err := focusLivePane(); err != nil {
			return fleetFocusErrMsg{err: err}
		}
		return nil
	}
	if show := m.maybeShowCmd(); show != nil {
		return *m, tea.Batch(tea.Sequence(show, focus), m.flushView())
	}
	return *m, tea.Batch(focus, m.flushView())
}

// focusViewerCmd moves tmux focus to the live pane (idempotent). Failures are
// ignored: with no live pane there is nothing to focus.
func focusViewerCmd() tea.Cmd {
	return func() tea.Msg {
		_ = focusViewerPane()
		return nil
	}
}

// fleetFocusErrMsg reports a failure to move focus to the live pane.
type fleetFocusErrMsg struct{ err error }

// ── Line model construction ──────────────────────────────────────────────────

// rebuildLines flattens m.groups into the scrollable line list. Called after
// every successful reload.
//
// A non-selectable fleetLineDivider is inserted between adjacent groups —
// never before the first group or after the last — so the groups read as
// distinct blocks in the sidebar. Cursor movement (moveCursor) and selection
// re-resolution (resolveSelection) both skip fleetLineDivider explicitly;
// see those functions for why a divider can never become the selection.
//
// Folding and the attention filter are applied here, so the line list,
// selection, overflow counts and mouse hit-testing all see exactly what is
// drawn: a folded group contributes its header only; under a filter a group
// keeps its matching rows plus their ancestors (marked dimmed), and a group
// with nothing left shows its header only. Two consecutive header-only groups
// get no divider between them, so a folded hub reads as a compact list.
func (m *FleetModel) rebuildLines() {
	defer func() {
		// Keep SplitPane's row count (cursor clamping) in step at once.
		m.splitPane.SetListContent(m.splitPane.listContent, len(m.lines))
	}()
	var lines []fleetLine
	prevBody := false
	for i, g := range m.groups {
		all := m.shownRows(g.Rows)
		rows, context := all, map[string]bool(nil)
		if !m.isFolded(g.Session) {
			src := all
			if m.sortRecent {
				src = fleet.SortRowsRecent(all)
			}
			rows, context = filterRows(src, m.filter)
		}
		body := !m.isFolded(g.Session) && (len(rows) > 0 || (len(all) == 0 && m.filter == fleetFilterAll))
		if i > 0 && (body || prevBody) {
			lines = append(lines, fleetLine{kind: fleetLineDivider})
		}
		prevBody = body
		lines = append(lines, fleetLine{kind: fleetLineHeader, group: g})
		if !body {
			continue
		}
		if len(all) == 0 {
			lines = append(lines, fleetLine{kind: fleetLinePlaceholder, group: g})
			continue
		}
		first := true
		tree := treeRows(rows)
		nameCol := 0
		for _, t := range tree {
			if t.Label == "" {
				nameCol = max(nameCol, lipgloss.Width(t.Prefix)+lipgloss.Width(fleet.BaseName(t.Row)))
			}
		}
		for _, t := range tree {
			if t.Label != "" {
				lines = append(lines, fleetLine{kind: fleetLineSubheader, group: g, label: t.Label})
				continue
			}
			lines = append(lines, fleetLine{kind: fleetLineRow, row: t.Row, groupHead: first, treePrefix: t.Prefix, nameCol: nameCol, dimmed: context[t.Row.WindowID]})
			first = false
		}
	}
	if closed := m.closedLines(); len(closed) > 0 {
		if len(lines) > 0 {
			lines = append(lines, fleetLine{kind: fleetLineDivider})
		}
		lines = append(lines, closed...)
	}
	m.lines = lines
	// The usual model is fleet-wide; column widths come from drawn rows only,
	// so a folded or filtered-out row cannot cost visible rows their age.
	var shown []fleet.Row
	for _, l := range lines {
		if l.kind == fleetLineRow {
			shown = append(shown, l.row)
		}
	}
	m.except = computeExceptions(m.groups, []fleet.Group{{Rows: shown}}, m.display)
}

// shownRows is rows without the shell rows while they are hidden.
func (m FleetModel) shownRows(rows []fleet.Row) []fleet.Row {
	if m.showShells {
		return rows
	}
	var out []fleet.Row
	for _, r := range rows {
		if !r.Shell {
			out = append(out, r)
		}
	}
	return out
}

// ── Rendering ────────────────────────────────────────────────────────────────

// contentWidth returns the usable content width based on terminal size.
func (m FleetModel) contentWidth() int {
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
func (m FleetModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	return v
}

func (m FleetModel) render() string {
	out := m.renderList()
	if m.card != nil {
		out = m.withCard(out)
	}
	return out
}

// renderHeader renders the title line and the row below it, which is blank
// unless the list is scrolled and it carries the "↑ N more" indicator. It
// always ends in two line breaks, so the list (and every click target) starts
// at the same row (fleetListTop).
func (m FleetModel) renderHeader(cw int) string {
	return m.renderTitle(cw) + "\n" + m.renderOverflowAbove(cw) + "\n"
}

// fleetCounts tallies every row by its glyph.
func (m FleetModel) fleetCounts() (blocked, working, done, idle int) {
	for _, g := range m.groups {
		for _, r := range g.Rows {
			if r.Shell {
				continue // not an agent
			}
			switch rowWeight(r) {
			case fleetWeightBlocked:
				blocked++
			case fleetWeightWorking:
				working++
			case fleetWeightDone:
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
// ("· needs you"), then come the serve indicator and the "· sort: recent"
// tag. There is no new-session button: N (in the ? reference) opens it, and
// the footer's "n new" launches into a group. See titleLayout for how narrow
// widths degrade.
func (m FleetModel) renderTitle(cw int) string {
	return m.titleLayout(cw).line
}

// fleetTitleLayout is the rendered title row plus the columns of its click
// target; a span is [0,0] when that target is not shown.
type fleetTitleLayout struct {
	line     string
	badge    [2]int
	activity [2]int // the [activity] button
}

// titleLayout lays out the title row. Narrow widths drop the totals, then
// tighten the counts and shorten the filter tag, then drop the counts, then
// truncate. Rendering and hit-testing both come from here, so they cannot
// disagree.
func (m FleetModel) titleLayout(cw int) fleetTitleLayout {
	tags := []string{""}
	if m.filter != fleetFilterAll {
		short, _, _ := strings.Cut(m.filter.label(), " ")
		tags = []string{m.filter.label(), short}
	}
	fit := func(l fleetTitleLayout, left, right string) fleetTitleLayout {
		line := left
		if right != "" {
			pad := cw - fleetSidebarRightGutter - lipgloss.Width(left) - lipgloss.Width(right)
			line += strings.Repeat(" ", pad) + right
		}
		l.line = fleetFitLine(line, false, cw)
		if l.badge[1] > cw-fleetSidebarRightGutter {
			l.badge = [2]int{}
		}
		if l.activity[1] > cw-fleetSidebarRightGutter {
			l.activity = [2]int{}
		}
		return l
	}
	// bare is the title without counts.
	bare := func() fleetTitleLayout {
		left, l := m.titleLeft(tags[0], true)
		return fit(l, left, "")
	}
	if !m.loaded || m.loadErr != nil {
		return bare()
	}
	blocked, working, done, idle := m.fleetCounts()
	count := func(glyph string, st lipgloss.Style, n int) string {
		return st.Render(glyph) + " " + fleetRecapWorkingStyle.Render(fmt.Sprint(n))
	}
	var cells []string
	if blocked > 0 {
		cells = append(cells, fleetBlockedStyle.Bold(true).Reverse(true).Render(fmt.Sprintf(" ⏸ %d ", blocked)))
	}
	cells = append(cells, count("●", fleetWorkingStyle, working), count("✓", fleetDoneStyle, done), count("○", fleetIdleStyle, idle))
	counts := strings.Join(cells, "  ")
	total := working + blocked + done + idle
	suffix := fleetDimStyle.Render(fmt.Sprintf(" · %d agent%s, %d session%s", total, plural(total), len(m.groups), plural(len(m.groups))))

	type candidate struct{ tag, right string }
	candidates := []candidate{{tags[0], counts + suffix}, {tags[0], counts}}
	if m.filter != fleetFilterAll {
		tight := strings.Join(cells, " ")
		candidates = append(candidates, candidate{tags[0], tight}, candidate{tags[1], tight})
	}
	// The [activity] button outranks the counts' suffix but not the counts:
	// without room for both, the button is dropped (v still toggles the
	// pane).
	for _, button := range []bool{true, false} {
		for _, c := range candidates {
			left, l := m.titleLeft(c.tag, button)
			room := cw - fleetSidebarRightGutter - lipgloss.Width(left) - 1
			if lipgloss.Width(c.right) <= room {
				return fit(l, left, c.right)
			}
		}
	}
	return bare()
}

// titleLeft builds the title row's left part: the title, the filter tag
// (when tag is not empty), the serve indicator with its columns, the
// [activity] button with its columns (when button is set), and the sort tag.
func (m FleetModel) titleLeft(tag string, button bool) (string, fleetTitleLayout) {
	var l fleetTitleLayout
	left := strings.Repeat(" ", fleetSidebarLeftInset) + fleetTitleStyle.Render("agentctl hub")
	if tag != "" {
		left += fleetCountStyle.Render(" · " + tag)
	}
	if badge := m.serveBadge(); badge != "" {
		left += fleetServeGap
		start := lipgloss.Width(left)
		left += badge
		l.badge = [2]int{start, lipgloss.Width(left)}
	}
	if button {
		left += fleetActivityGap
		start := lipgloss.Width(left)
		left += m.activityButton()
		l.activity = [2]int{start, lipgloss.Width(left)}
	}
	if m.sortRecent {
		left += fleetCountStyle.Render(fleetSortTag)
	}
	return left, l
}

// fleetSortTag marks the title while rows are sorted by recent activity.
const fleetSortTag = " · sort: recent"

// renderStatusOrEmpty renders the loading/error/empty states shared by both
// layouts. Returns ("", false) when there's real data to render (the caller
// should proceed to render its list/split content instead).
func (m FleetModel) renderStatusOrEmpty() (string, bool) {
	switch {
	case !m.loaded:
		return fleetDimStyle.Render("  loading…") + "\n", true
	case m.loadErr != nil:
		return fleetErrStyle.Render(fmt.Sprintf("  error loading sessions: %v", m.loadErr)) + "\n", true
	case len(m.groups) == 0:
		return fleetDimStyle.Render("  no agentctl sessions running") + "\n", true
	}
	return "", false
}

// fleetNoViewHint is shown in the list area until an agent has been viewed in
// the live pane (e.g. the cursor sits on a header and no agent was ever
// selected).
const fleetNoViewHint = "select an agent to view it live"

// renderList renders the hub's list column: header, rows, footer. The list is
// padded to its full height so the footer (and click targets derived from it)
// never move.
func (m FleetModel) renderList() string {
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
			if m.liveShown == "" && !m.activity.hidesViewer {
				b.WriteString(fleetDimStyle.Render("  " + truncateToWidth(fleetNoViewHint, cw-2)))
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
func (m FleetModel) shownEnd(cw int) int {
	visible := m.visibleLines()
	used := 0
	i := m.scrollOffset
	for ; i < len(m.lines) && used < visible; i++ {
		used += m.lineHeight(i, cw)
	}
	return i
}

// hiddenRows counts the agent rows among lines[from:to].
func (m FleetModel) hiddenRows(from, to int) (rows, other int) {
	for i := max(from, 0); i < min(to, len(m.lines)); i++ {
		switch m.lines[i].kind {
		case fleetLineRow, fleetLineClosed:
			rows++
		case fleetLineHeader, fleetLinePlaceholder:
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
	return fleetCountStyle.Render(strings.Repeat(" ", fleetSidebarLeftInset+1) + truncateToWidth(text, cw-fleetSidebarLeftInset-1-fleetSidebarRightGutter))
}

// renderOverflowAbove is the indicator for lines scrolled off the top, drawn
// on the header row just above the list.
func (m FleetModel) renderOverflowAbove(cw int) string {
	if !m.clickable() {
		return ""
	}
	rows, other := m.hiddenRows(0, m.scrollOffset)
	return renderOverflow("↑", rows, other, cw)
}

// renderOverflowBelow is the indicator for lines past the bottom, drawn on the
// footer's blank row just below the list so the footer itself never moves.
func (m FleetModel) renderOverflowBelow(cw int) string {
	if !m.clickable() {
		return ""
	}
	rows, other := m.hiddenRows(m.shownEnd(cw), len(m.lines))
	return renderOverflow("↓", rows, other, cw)
}

func (m FleetModel) renderLine(i int, cw int) string {
	l := m.lines[i]
	selected := i == m.splitPane.Cursor()
	if at, ok := m.dragIndicatorLine(); ok && at == i {
		return renderDragIndicator(cw)
	}
	switch l.kind {
	case fleetLineHeader:
		out := m.renderGroupHeaderHover(l.group, selected, m.hover.region(i), cw)
		if m.dragging() && l.group.Session == m.drag.session {
			out = dimDraggedHeader(l.group.Project, out)
		}
		return out
	case fleetLinePlaceholder:
		marker := "    "
		if selected {
			marker = "  " + cursorActiveStyle.Render("▸") + " "
		}
		return marker + fleetDimStyle.Render("no agents")
	case fleetLineDivider:
		return renderFleetDivider(cw)
	case fleetLineClosedLabel:
		return renderClosedLabel(l, cw)
	case fleetLineClosed:
		return renderClosedRow(l.closed, selected, cw)
	case fleetLineSubheader:
		// The group's bar keeps the spine unbroken; the label aligns under
		// agent names: bar + cursor slot + glyph + space.
		return fleetProjectBar(l.group.Project) + strings.Repeat(" ", fleetDetailIndent-1) + fleetDimStyle.Render(l.label)
	default: // fleetLineRow
		out := m.renderTreeRowHover(l.row, l.treePrefix, l.nameCol, selected, m.hover.region(i), cw)
		if l.dimmed && !selected && m.hover.region(i) == fleetHoverNone {
			out = dimContextRow(l.row, out)
		}
		return out
	}
}

// renderFleetDivider renders the non-selectable gap between two adjacent
// groups: an empty row. The line kind stays so hit-testing and navigation
// treat it as before. Never rendered before the first group or after the
// last; see rebuildLines.
func renderFleetDivider(int) string {
	return ""
}

// fleetProjectBar is the project-coloured left bar shared by a group's rows,
// detail lines and sub-section labels.
func fleetProjectBar(project string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(fleetColorFor(project))).Render("▎")
}

// rowGlyph returns the glyph and style for a single agent row, matching the
// window-rename convention in internal/winname: ● working,
// ⏸ blocked, ✓ done (unseen), bare/○ otherwise.
func rowGlyph(r fleet.Row) (string, lipgloss.Style) {
	if r.Shell {
		return "$", fleetDimStyle
	}
	switch rowWeight(r) {
	case fleetWeightBlocked:
		return "⏸", fleetBlockedStyle
	case fleetWeightWorking:
		return "●", fleetWorkingStyle
	case fleetWeightDone:
		return "✓", fleetDoneStyle
	default:
		if r.TurnDone {
			// Bare window at a turn end: ✓ display only. Not Done, so
			// counts, filters and sweep are unchanged.
			return "✓", fleetDoneStyle
		}
		return "○", fleetIdleStyle
	}
}

// renderGroupHeader renders a session's group header line, now selectable
// (a group header/placeholder can be the cursor's target, showing that
// session's summary in the main panel) — hence the cursor marker, matching
// renderRow's convention.
func (m FleetModel) renderGroupHeader(g fleet.Group, selected bool, cw int) string {
	return m.renderGroupHeaderHover(g, selected, fleetHoverNone, cw)
}

// groupCustomSession reports whether g's tmux session name is not the one
// agentctl derives from its project, and so is worth showing. Session names
// are sanitized ("energy-solutions.ops" runs as
// "agentctl-energy-solutions-ops"), so compare against the derived name.
func groupCustomSession(g fleet.Group) bool {
	if g.Session == "" || g.Session == tmux.SessionNameForDirName(g.Project) {
		return false
	}
	return g.ProjectDir == "" || g.Session != tmux.SessionNameForProjectRoot(g.ProjectDir)
}

// fleetMinSessionLabel is the narrowest custom-session label worth drawing.
const fleetMinSessionLabel = 6

// renderGroupHeaderHover is renderGroupHeader with a hovered region: the "+ new"
// button, or one side of the shell | agent selector when it is armed.
//
// Layout: marker, a ⏸ flag slot (yellow when the group has a blocked agent),
// the project name, a custom session name when there is one, then the
// per-state counts right-aligned in fixed slots (see groupStateCounts) and the
// button. The counts never truncate; the session name gives way first, then
// the project name.
func (m FleetModel) renderGroupHeaderHover(g fleet.Group, selected bool, hover fleetHoverRegion, cw int) string {
	line, _ := m.groupHeaderLayout(g, selected, hover, cw)
	return line
}

// Fold markers drawn before a group's project name.
const (
	fleetFoldedGlyph   = "▸"
	fleetUnfoldedGlyph = "▾"
)

// fleetHeaderNameCol is the column a group header's fold marker starts at:
// the cursor marker, the ⏸ flag slot and a space.
const fleetHeaderNameCol = fleetSidebarLeftInset + 2

// fleetNewGap is the blank columns between a header's counts and its "+ new"
// button, so the button never reads as part of the counts.
const fleetNewGap = 2

// headerNameSpan is the [start, end) columns of group g's fold marker and
// project name on its header: the click target that toggles the fold.
func (m FleetModel) headerNameSpan(g fleet.Group, cw int) [2]int {
	return m.pointerHeaderSpans(g, cw).name
}

// headerSweepSpan is the [start, end) columns of group g's sweep button, or
// the zero span when the header does not show one.
func (m FleetModel) headerSweepSpan(g fleet.Group, cw int) [2]int {
	return m.pointerHeaderSpans(g, cw).sweep
}

// headerNewSpan is the [start, end) columns of group g's "+ new" button (or
// the armed shell | agent selector), or the zero span when it is not shown.
func (m FleetModel) headerNewSpan(g fleet.Group, cw int) [2]int {
	return m.pointerHeaderSpans(g, cw).add
}

// pointerHeaderSpans lays out g's header as it is drawn with the pointer on
// it (hit testing only ever asks about the header under the pointer, which
// shows its "+ new"), so click targets follow what is drawn.
func (m FleetModel) pointerHeaderSpans(g fleet.Group, cw int) fleetHeaderSpans {
	selected := false
	if c := m.splitPane.Cursor(); c >= 0 && c < len(m.lines) {
		l := m.lines[c]
		selected = l.kind == fleetLineHeader && l.group.Session == g.Session
	}
	_, spans := m.groupHeaderLayout(g, selected, fleetHoverHeader, cw)
	return spans
}

// fleetHeaderSpans are the clickable column ranges of a group header.
type fleetHeaderSpans struct {
	name  [2]int // fold marker and project name
	sweep [2]int // sweep button; zero when not shown
	add   [2]int // "+ new" button or armed selector; zero when not shown
}

// fleetSweepSep is the blank columns between the sweep button and "+ new".
const fleetSweepSep = 2

// fleetMinSweepName is the project-name width the sweep button never takes
// below: on a narrower header the button is dropped instead.
const fleetMinSweepName = 4

// groupSweepable is how many of g's rows a sweep would close, as far as the
// rows say: done agent rows. Sweep's own classification (cleanup.
// ClassifyWindow) also checks worktrees and open PRs, which is too costly
// for every frame, so this is the upper bound the header advertises.
func groupSweepable(g fleet.Group) int {
	n := 0
	for _, r := range g.Rows {
		if !r.Shell && rowWeight(r) == fleetWeightDone {
			n++
		}
	}
	return n
}

// sweepLabel is g's sweep button text: "sweep N", or plain "sweep" while a
// sweep of a group with nothing advertised runs or is armed.
func sweepLabel(n int) string {
	if n > 0 {
		return fmt.Sprintf("%s %d", fleetSweepLabel, n)
	}
	return fleetSweepLabel
}

// groupHeaderLayout renders a group header (see renderGroupHeaderHover) and
// reports the columns of its fold toggle and buttons.
//
// The per-state counts sit flush right; buttons, when shown, follow them:
// "sweep N" only when the group has N > 0 sweepable rows (or its sweep is
// running or armed), and "+ new" only on the selected or hovered header (or
// as the armed shell | agent selector). The sweep button is left out while
// the selector is armed or when it would squeeze the project name below
// fleetMinSweepName columns; when sweep fits alone but not beside "+ new",
// the hovered or selected header drops "+ new" instead, so hovering never
// hides a button that was showing.
func (m FleetModel) groupHeaderLayout(g fleet.Group, selected bool, hover fleetHoverRegion, cw int) (string, fleetHeaderSpans) {
	marker := strings.Repeat(" ", fleetSidebarLeftInset)
	if selected {
		marker = " " + cursorActiveStyle.Render("▸") + " "
	}
	counts := groupStateCounts(g, m.countDigits(), fleetCountGap(cw))
	flag := " "
	if counts.blocked > 0 {
		flag = fleetBlockedStyle.Render("⏸")
	}
	limit := cw - fleetSidebarRightGutter

	var add string
	armed := m.newArmedFor(g)
	if groupCanLaunch(g) && (armed || selected || hover != fleetHoverNone) {
		if armed {
			shell, agent := fleetDimStyle, fleetDimStyle
			switch hover {
			case fleetHoverNewShell:
				shell = fleetNewHoverStyle
			case fleetHoverNewAgent:
				agent = fleetNewHoverStyle
			}
			add = shell.Render(fleetShellLabel) + fleetDimStyle.Render(fleetSelSep) + agent.Render(fleetAgentLabel)
		} else {
			style := fleetDimStyle
			if hover == fleetHoverNew {
				style = fleetNewHoverStyle
			}
			add = style.Render(fleetNewLabel)
		}
	}
	var sweep string
	if n := groupSweepable(g); groupCanSweep(g) && !armed && (n > 0 || m.sweepBusy == g.Session || m.sweepArmedFor(g)) {
		sweep = m.sweepButtonStyle(g, hover).Render(sweepLabel(n))
	}

	var spans fleetHeaderSpans
	headW := fleetHeaderNameCol + 2 // marker, flag, fold marker and its space
	build := func(sweep, add string) string {
		right := counts.text
		btns := sweep
		if add != "" {
			if btns != "" {
				btns += strings.Repeat(" ", fleetSweepSep)
			}
			btns += add
		}
		if btns != "" {
			right += strings.Repeat(" ", fleetNewGap) + btns
		}
		return right
	}
	fits := func(right string) bool { return limit-lipgloss.Width(right)-1 >= headW+fleetMinSweepName }
	if sweep != "" && !fits(build(sweep, add)) {
		// Too narrow for both: a hover or selection never takes away a
		// sweep button that was showing without it, so "+ new" gives way
		// (n still launches). Sweep goes only when it never fit.
		if add != "" && !armed && fits(build(sweep, "")) {
			add = ""
		} else {
			sweep = ""
		}
	}
	right := build(sweep, add)
	if add != "" {
		spans.add = [2]int{limit - lipgloss.Width(add), limit}
	}
	if sweep != "" {
		end := limit
		if add != "" {
			end = spans.add[0] - fleetSweepSep
		}
		spans.sweep = [2]int{end - lipgloss.Width(sweep), end}
	}
	leftW := limit - lipgloss.Width(right) - 1

	projStyle := fleetGroupNameStyle
	if hover == fleetHoverFold {
		projStyle = projStyle.Underline(true)
	}
	fold := fleetDimStyle.Render(fleetUnfoldedGlyph)
	if m.isFolded(g.Session) {
		fold = fleetCountStyle.Render(fleetFoldedGlyph)
	}
	head := marker + flag + " " + fold + " "
	name := truncateToWidth(g.Project, leftW-lipgloss.Width(head))
	span := &spans.name
	*span = [2]int{fleetHeaderNameCol, lipgloss.Width(head) + lipgloss.Width(name)}
	line := head + projStyle.Render(name)
	if groupCustomSession(g) {
		if room := leftW - lipgloss.Width(line) - 2; room >= fleetMinSessionLabel {
			line += "  " + fleetCountStyle.Render(truncateToWidth(g.Session, room))
		}
	}
	if leftW < lipgloss.Width(head) {
		// Too narrow for any name: keep the right side intact.
		line = truncateToWidth(line, max(leftW, 0))
		span[1] = min(span[1], max(leftW, 0))
	}
	pad := max(limit-lipgloss.Width(right)-lipgloss.Width(line), 1)
	return fleetFitLine(line+strings.Repeat(" ", pad)+right, selected, cw), spans
}

// countDigits is the widest per-state count across all groups, so the count
// slots line up down the whole sidebar.
func (m FleetModel) countDigits() int {
	d := 1
	for _, g := range m.groups {
		c := groupStateCounts(g, 1, 1)
		for _, n := range []int{c.blocked, c.working, c.done, c.idle} {
			d = max(d, len(fmt.Sprint(n)))
		}
	}
	return d
}

// fleetCountGap is the space between header count slots: two columns when the
// sidebar has room, one at the narrow end.
func fleetCountGap(cw int) int {
	if cw >= 56 {
		return 2
	}
	return 1
}

// fleetGroupCounts is a group's per-state tally and its rendered slots.
type fleetGroupCounts struct {
	blocked, working, done, idle int
	text                         string
}

// groupStateCounts tallies a group's rows by state and renders them in fixed
// slots, in the title's order ⏸ ● ✓ ○: each slot is the glyph, a space and
// the count right-aligned to digits columns; an empty bucket is a blank slot
// of the same width, so the counts form columns across groups.
func groupStateCounts(g fleet.Group, digits, gap int) fleetGroupCounts {
	var c fleetGroupCounts
	for _, r := range g.Rows {
		if r.Shell {
			continue
		}
		switch rowWeight(r) {
		case fleetWeightBlocked:
			c.blocked++
		case fleetWeightWorking:
			c.working++
		case fleetWeightDone:
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
		{"⏸", fleetBlockedStyle, c.blocked},
		{"●", fleetWorkingStyle, c.working},
		{"✓", fleetDoneStyle, c.done},
		{"○", fleetIdleStyle, c.idle},
	}
	parts := make([]string, len(slots))
	for i, s := range slots {
		if s.n == 0 {
			parts[i] = strings.Repeat(" ", 2+digits)
			continue
		}
		parts[i] = s.st.Render(s.glyph) + " " + fleetCountStyle.Render(fmt.Sprintf("%*d", digits, s.n))
	}
	c.text = strings.Join(parts, strings.Repeat(" ", gap))
	return c
}

// renderRow renders one agent row.
func (m FleetModel) renderRow(r fleet.Row, selected bool, cw int) string {
	return m.renderTreeRow(r, "", 0, selected, cw)
}

// fleetNow is the clock row ages are measured against; tests replace it.
var fleetNow = time.Now

// fleetFitLine clamps a sidebar line to the columns left of the right gutter.
// A selected line is highlighted up to the gutter (never into it) and then
// padded with plain gutter columns, so it measures exactly cw.
func fleetFitLine(line string, selected bool, cw int) string {
	if selected {
		return fleetFitLineStyled(line, &fleetSelectedStyle, cw)
	}
	return fleetFitLineStyled(line, nil, cw)
}

// fleetFitLineStyled is fleetFitLine with an arbitrary highlight (nil = none).
func fleetFitLineStyled(line string, hl *lipgloss.Style, cw int) string {
	inner := cw - fleetSidebarRightGutter
	if inner < 1 {
		return ansi.Truncate(line, max(cw, 0), "")
	}
	line = ansi.Truncate(line, inner, "")
	if hl == nil {
		return line
	}
	line = reapplyBackground(line, hl)
	return hl.Width(inner).Render(line) + strings.Repeat(" ", fleetSidebarRightGutter)
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

// fleetRowCursor is a row's left bar in the group colour plus the cursor slot.
func fleetRowCursor(r fleet.Row, selected bool) string {
	bar := fleetProjectBar(r.Project)
	if selected {
		return bar + cursorActiveStyle.Render("▸") + " "
	}
	return bar + "  "
}

// fleetNameBudget is the most columns a row's name (plus tree prefix) may take
// at width cw: fleetMaxNameWidth, or a third of a narrow sidebar so the recap
// keeps most of the row.
func fleetNameBudget(cw int) int {
	return max(min(fleetMaxNameWidth, cw/3), 0)
}

// Fixed right-hand columns of a row: the PR badge slot ("#1025" is the widest
// seen) and the age slot, both right-aligned. The exception columns (model,
// context; see fleetExceptions) sit left of the PR.
const (
	fleetPRSlotW  = 5
	fleetAgeSlotW = 3
)

// fleetRightCols is which right-hand columns a row draws and how wide the
// model column is. Narrow rows drop the age first, then the model, then the
// PR; a context warning is never dropped (it is fleetCtxSlotW columns). The
// PR goes after the model because it is the one link to the outcome of the
// work; the age goes first because the glyph already says the state and the
// hover card the time.
type fleetRightCols struct {
	modelW       int  // model column width; 0 = not drawn
	ctx          bool // context column drawn (blank on rows below the warn threshold)
	pr, age      bool
	ownCtxWarned bool // this row shows a context warning
}

// joinedWidth is the columns of the given column widths one space apart,
// skipping zero widths.
func joinedWidth(ws ...int) int {
	total, n := 0, 0
	for _, w := range ws {
		if w > 0 {
			total += w
			n++
		}
	}
	return total + max(n-1, 0)
}

// fitRightCols picks the right-hand columns for a row with available columns
// between its name and its close button. The age is the rightmost column,
// so whether it shows depends only on the width and the list-wide
// exceptions (the PR and age then line up down the list). The model and
// context sit left of the PR, so they move nothing when absent: they count
// only on the rows that draw them (ownModel, ownCtx), and a blank leading
// column is trimmed at render time, so its width goes back to the recap.
// Order as the row narrows: age, then the model, then the PR; the context
// warning is never dropped.
func fitRightCols(available int, ex fleetExceptions, ownCtx, ownModel bool) fleetRightCols {
	fits := func(w int) bool { return available-w-1 >= fleetMinRecapWidth }
	ctxAll, ctxRow := 0, 0
	if ex.ctx || ownCtx {
		ctxAll = fleetCtxSlotW
	}
	if ownCtx {
		ctxRow = fleetCtxSlotW
	}
	c := fleetRightCols{ctx: ctxAll > 0, ownCtxWarned: ownCtx}
	c.pr = fits(joinedWidth(ctxRow, fleetPRSlotW))
	if ownModel && ex.modelW > 0 && fits(joinedWidth(ex.modelW, ctxAll, fleetPRSlotW)) {
		c.modelW = ex.modelW
	}
	c.age = c.pr && fits(joinedWidth(ex.modelW, ctxAll, fleetPRSlotW, fleetAgeSlotW))
	return c
}

// padLeft right-aligns a rendered s in w columns.
func padLeft(s string, w int) string {
	return strings.Repeat(" ", max(w-lipgloss.Width(s), 0)) + s
}

// rowAge is the dim age cell: the time in the current state (blocked style
// on a blocked row), else the time since the last activity or the start,
// else a dim "–". Shell rows have none.
func rowAge(r fleet.Row) string {
	if r.Shell {
		return ""
	}
	now := fleetNow()
	switch {
	case !r.StateSince.IsZero():
		style := fleetDimStyle
		if r.State == "blocked" {
			style = fleetBlockedStyle
		}
		return style.Render(fleet.CompactAge(now.Sub(r.StateSince)))
	case r.LastActivity > 0:
		return fleetDimStyle.Render(fleet.CompactAge(now.Sub(time.Unix(r.LastActivity, 0))))
	case r.StartTime > 0:
		return fleetDimStyle.Render(fleet.CompactAge(now.Sub(time.Unix(r.StartTime, 0))))
	}
	return fleetDimStyle.Render(fleetNoAge)
}

// fleetNoAge fills the age cell of a row with no known time.
const fleetNoAge = "–"

// rowRight renders the right-aligned status columns before the close button:
// the model exception (ex.rowModel), the context warning in its threshold
// colour, the PR badge ("#60", in the done style on a done row) and the age
// (rowAge), each right-aligned in its slot one space apart. Blank leading
// columns are trimmed so the recap may use them; inner blanks stay so the
// columns line up down the list.
func rowRight(r fleet.Row, c fleetRightCols, ex fleetExceptions, d fleetDisplay) string {
	var parts []string
	if c.modelW > 0 {
		var model string
		if mn := ex.rowModel(r); mn != "" {
			model = fleetDimStyle.Render(mn)
		}
		parts = append(parts, padLeft(model, c.modelW))
	}
	if c.ctx {
		var ctx string
		if c.ownCtxWarned {
			ctx = d.ctxStyle(r.ContextPct).Render(ctxLabel(r.ContextPct))
		}
		parts = append(parts, padLeft(ctx, fleetCtxSlotW))
	}
	if c.pr {
		var pr string
		if r.PRNumber > 0 {
			bs := fleetProjectStyle
			if r.Done {
				bs = fleetDoneStyle
			}
			pr = bs.Render(fmt.Sprintf("#%d", r.PRNumber))
		}
		parts = append(parts, padLeft(pr, fleetPRSlotW))
	}
	if c.age {
		parts = append(parts, padLeft(rowAge(r), fleetAgeSlotW))
	}
	return strings.TrimLeft(strings.Join(parts, " "), " ")
}

// recapStyle is the style for a row's recap text, by row weight: working in
// normal text, done subtle, idle muted, blocked subtle as before. The
// activity fallback is the weakest signal and stays italic; a row with no
// recap at all (src RecapNone, see rowRecap) gets the muted italic fallback.
func recapStyle(w fleetWeight, src fleet.RecapSource) lipgloss.Style {
	if src == fleet.RecapNone {
		return fleetEmptyStyle
	}
	var st lipgloss.Style
	switch w {
	case fleetWeightWorking:
		st = fleetRecapWorkingStyle
	case fleetWeightIdle:
		st = fleetRecapIdleStyle
	default: // done, blocked
		st = fleetRecapStyle
	}
	if src == fleet.RecapActivity {
		if w == fleetWeightBlocked {
			st = fleetActivityStyle
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
func (m FleetModel) renderTreeRow(r fleet.Row, treePrefix string, nameCol int, selected bool, cw int) string {
	return m.renderTreeRowHover(r, treePrefix, nameCol, selected, fleetHoverNone, cw)
}

// renderTreeRowHover is renderTreeRow with the hover region of this row: a
// body hover tints an unselected row, a close hover recolours the ✕. The ✕
// is drawn only on the selected and the hovered row; every other row keeps
// its one-cell slot blank so alignment and hit regions never change.
func (m FleetModel) renderTreeRowHover(r fleet.Row, treePrefix string, nameCol int, selected bool, hover fleetHoverRegion, cw int) string {
	out, _ := m.treeRowLayout(r, treePrefix, nameCol, selected, hover, cw)
	if !selected {
		return out
	}
	for _, d := range rowDetailStyled(r, treePrefix, cw, m.display, m.viewerShows(r)) {
		out += "\n" + fleetFitLine(d, true, cw)
	}
	return out
}

// treeRowLayout renders a row's own line (no detail lines) and reports the
// [start, end) columns of its title text: the recap title or truncated recap
// sentence as drawn, which is the rename click target (see onRowTitle).
// The span is zero when no title text is drawn (a shell row, a close
// confirmation, a column too narrow). While the row is being renamed the
// editor is drawn there and the span covers the editor.
func (m FleetModel) treeRowLayout(r fleet.Row, treePrefix string, nameCol int, selected bool, hover fleetHoverRegion, cw int) (string, [2]int) {
	var titleSpan [2]int
	glyph, style := rowGlyph(r)
	weight := rowStyleWeight(r, fleetNow())
	cursor := fleetRowCursor(r, selected)
	glyphStr := style.Render(glyph)

	hovered := hover == fleetHoverBody || hover == fleetHoverClose
	closeW := 1 + lipgloss.Width(fleetCloseLabel)
	inner := cw - fleetSidebarRightGutter
	nameBudget := fleetRowNameCap(cw)
	col := min(nameCol, nameBudget)
	treePrefix = fleetKeptPrefix(treePrefix, cw)
	nameBudget -= lipgloss.Width(treePrefix)
	name := truncateToWidth(fleet.BaseName(r), nameBudget)

	ctxWarn := rowCtxWarn(r, m.display)
	// A context warning is never dropped: on a sidebar too narrow for it and
	// the minimum recap, the name gives way instead (contentWidth keeps the
	// hub wider than this, so it only bites below 40 columns).
	if ctxWarn && !m.confirmingRow(r) {
		used := lipgloss.Width(cursor) + 2 + max(col, lipgloss.Width(treePrefix)+lipgloss.Width(name)) + 2 + closeW
		if short := fleetMinRecapWidth + fleetCtxSlotW + 1 - (inner - used); short > 0 {
			nameBudget = max(nameBudget-short, 1)
			name = truncateToWidth(fleet.BaseName(r), nameBudget)
			col = min(col, lipgloss.Width(treePrefix)+nameBudget)
		}
	}

	padW := max(col-lipgloss.Width(treePrefix)-lipgloss.Width(name), 0)
	prefix := cursor + glyphStr + " " + fleetDimStyle.Render(treePrefix) + renderAgentNameHover(name, weight, hovered) + strings.Repeat(" ", padW) + "  "
	available := inner - lipgloss.Width(prefix) - closeW

	closeStyle := fleetDimStyle
	showClose := selected || hovered
	var text, right string
	if m.confirmingRow(r) {
		// The full instance name: this is the identifier being closed.
		text = fleetBlockedStyle.Render(truncateToWidth("close "+r.Name+"? y/n", available))
		closeStyle = fleetBlockedStyle
		showClose = true
	} else {
		if hover == fleetHoverClose {
			closeStyle = fleetCloseHoverStyle
		}
		// The columns sit at fixed positions, but the recap may run into a
		// slot this row leaves blank: only what is drawn is reserved.
		if !r.Shell {
			cols := fitRightCols(available, m.except, ctxWarn, m.except.rowModel(r) != "")
			right = rowRight(r, cols, m.except, m.display)
		}
		if rw := lipgloss.Width(right); rw > 0 {
			available -= rw + 1
		}
		recap := rowRecap(r)
		rs := recapStyle(weight, recap.Source)
		if hovered && recap.Source != fleet.RecapNone {
			rs = fleetRecapHoverStyle
			if recap.Source == fleet.RecapActivity {
				rs = fleetActivityHoverStyle
			}
		}
		var tag string
		if t := scheduledTag(r); t != "" && available > 0 {
			tag = truncateToWidth(t, min(available, fleetScheduledTagMaxW))
			available -= lipgloss.Width(tag) + 1
			tag = fleetDimStyle.Render(tag) + " "
		}
		start := lipgloss.Width(prefix) + lipgloss.Width(tag)
		if m.renamingRow(r) {
			editor := m.renameView(max(available, 0))
			text = tag + editor
			titleSpan = [2]int{start, start + max(available, lipgloss.Width(editor))}
		} else {
			title := fleet.TruncateWords(recap.Short(), max(available, 0))
			text = tag + renderRecapTitle(title, rs)
			if w := lipgloss.Width(title); w > 0 {
				titleSpan = [2]int{start, start + w}
			}
		}
	}

	closeBtn := strings.Repeat(" ", lipgloss.Width(fleetCloseLabel))
	if showClose {
		closeBtn = closeStyle.Render(fleetCloseLabel)
	}
	line := prefix + text
	pad := inner - closeW - lipgloss.Width(line) - lipgloss.Width(right)
	line += strings.Repeat(" ", max(pad, 0)) + right + " " + closeBtn
	var hl *lipgloss.Style
	switch {
	case selected:
		hl = &fleetSelectedStyle
	case hovered:
		hl = &fleetHoverStyle
	}
	return fleetFitLineStyled(line, hl, cw), titleSpan
}

// leadingIssue matches a title's leading issue number ("#21 Crotch…").
var leadingIssue = regexp.MustCompile(`^#\d+\b`)

// renderRecapTitle renders a row's (already truncated) title in style rs,
// with a leading issue number dim so it does not read like the PR column.
func renderRecapTitle(title string, rs lipgloss.Style) string {
	if loc := leadingIssue.FindStringIndex(title); loc != nil {
		return fleetDimStyle.Render(title[:loc[1]]) + rs.Render(title[loc[1]:])
	}
	return rs.Render(title)
}

// fleetDoneDecay is how long a done-unseen row keeps its done weight; after
// it the row renders at idle weight but keeps its green ✓ (and still counts
// as done).
const fleetDoneDecay = 24 * time.Hour

// rowStyleWeight is the weight a row's name and recap render at: rowWeight,
// except a done row whose state is older than fleetDoneDecay is idle.
func rowStyleWeight(r fleet.Row, now time.Time) fleetWeight {
	w := rowWeight(r)
	if w == fleetWeightDone && !r.StateSince.IsZero() && now.Sub(r.StateSince) > fleetDoneDecay {
		return fleetWeightIdle
	}
	return w
}

// rowNoPrompt reports whether r is an agent still waiting for its first
// prompt: no turn yet and nothing to recap.
func rowNoPrompt(r fleet.Row) bool {
	return !r.Shell && r.TurnCount == 0 && rowRecap(r).Source == fleet.RecapNone
}

// viewerShows reports whether the live viewer is showing r's window, so its
// last-activity line would only repeat what the viewer shows.
func (m FleetModel) viewerShows(r fleet.Row) bool {
	key := r.Session + "|" + r.WindowID
	return r.WindowID != "" && m.liveShown == key && m.liveFailed != key
}

// fleetScheduledGlyph marks a window launched by `agentctl cron`. It is drawn
// in the recap column, never the state-glyph slot, so it cannot be mistaken
// for ● ⏸ ✓ or $.
const fleetScheduledGlyph = "⏲"

// fleetScheduledTagMaxW caps the "⏲ job" tag so a long job name cannot crowd
// out the recap.
const fleetScheduledTagMaxW = 24

// scheduledTag is the "⏲ <job>" marker for a cron-launched row, or "".
func scheduledTag(r fleet.Row) string {
	if r.ScheduledBy == "" {
		return ""
	}
	return fleetScheduledGlyph + " " + r.ScheduledBy
}

// rowRecap is the row's recap as loaded (fleet.TagLogs), or, for a row that
// never went through it, the log-free part of the chain (task, activity).
// A row with nothing at all gets a fallback sentence built from what the row
// already knows (see emptyRecapText), still with Source RecapNone so it
// renders as a fallback; fleet.Recap itself stays pure.
func rowRecap(r fleet.Row) fleet.RecapLine {
	if r.Shell {
		return fleet.RecapLine{} // the name already says "shell"
	}
	if r.Recap.Source != fleet.RecapNone {
		return r.Recap
	}
	if rc := fleet.Recap(r, fleet.LogInfo{}); rc.Source != fleet.RecapNone {
		return rc
	}
	return fleet.RecapLine{Text: emptyRecapText(r, fleetNow())}
}

// emptyRecapText is the sentence a row with no recap shows: "no prompt yet
// · 4m" before the first turn, otherwise "no activity yet ·
// started 2d ago". The age is omitted when the start time is unknown.
func emptyRecapText(r fleet.Row, now time.Time) string {
	var since time.Time
	if r.StartTime > 0 {
		since = time.Unix(r.StartTime, 0)
	} else if !r.StateSince.IsZero() {
		since = r.StateSince
	}
	if r.TurnCount == 0 {
		if since.IsZero() {
			return "no prompt yet"
		}
		return "no prompt yet · " + fleet.CompactAge(now.Sub(since))
	}
	if since.IsZero() {
		return "no activity yet"
	}
	return "no activity yet · started " + fleet.CompactAge(now.Sub(since)) + " ago"
}

// fleetKeptPrefix clamps a row's tree prefix the way renderTreeRowHover does,
// so deep nesting cannot starve the name. Rendering and detail-line layout
// both go through it.
func fleetKeptPrefix(treePrefix string, cw int) string {
	tw := lipgloss.Width(treePrefix)
	if tw == 0 {
		return ""
	}
	keep := min(tw, fleetRowNameCap(cw)-6)
	return ansi.Truncate(treePrefix, max(keep, 0), "")
}

// fleetRowNameCap is the most columns a row's tree prefix plus name may take
// at width cw: the name budget, less whatever would push the recap below
// fleetMinRecapWidth. The columns before the name are the bar, cursor slot,
// glyph and a space; after it come the two-space gap and the close button.
func fleetRowNameCap(cw int) int {
	inner := cw - fleetSidebarRightGutter
	maxCol := inner - 5 - 2 - (1 + lipgloss.Width(fleetCloseLabel)) - fleetMinRecapWidth
	return min(fleetNameBudget(cw), max(maxCol, 0))
}

// rowNameFits reports whether the row's name column shows its whole base
// name (see renderTreeRowHover) for a kept tree prefix of width prefixW.
func rowNameFits(r fleet.Row, prefixW, cw int) bool {
	return lipgloss.Width(fleet.BaseName(r)) <= fleetRowNameCap(cw)-prefixW
}

// fleetContinuation turns a (kept) tree prefix into the continuation drawn
// beside detail lines: "│  " where siblings follow at that depth, spaces
// otherwise. The result has the same display width as the prefix.
func fleetContinuation(prefix string) string {
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

// fleetMinRecapWidth is the narrowest recap the right-aligned age/badge may
// leave; below it the age/badge is dropped.
const fleetMinRecapWidth = 8

// fleetDetailIndent is how far a selected row's detail lines are indented
// before the tree prefix: the bar, the cursor slot and the glyph column, so
// they align with the name.
const fleetDetailIndent = 5

// fleetDetailMaxLines caps a selected row's recap and activity lines; the
// chip line (see rowChips), when there is one, comes on top.
const fleetDetailMaxLines = 3

// fleetDetailLine is one plain-text detail line and its marker ("" for recap
// text, fleetActivityMarker or fleetCommandMarker for an activity line).
type fleetDetailLine struct {
	text   string
	marker string
	// styled, when set, is the line pre-rendered (the chip line); text is
	// then its plain form.
	styled string
}

// rowDetail returns the plain detail lines a selected row renders beneath
// itself at width cw: the full recap wrapped, then the last activity line
// when it differs from the recap, at most fleetDetailMaxLines in total, then
// one chip line (rowChips) when the row has any. A recap that is itself the
// activity (fleet.RecapActivity: the last tool command or text) is one
// marked, truncated line and never wraps. viewed drops the activity line:
// the live viewer is showing that window, so it would only repeat it. Hit
// testing and scrolling count these lines, so rendering must use exactly
// this. prefixW is the width of the row's kept tree prefix
// (fleetKeptPrefix).
func rowDetail(r fleet.Row, prefixW, cw int, d fleetDisplay, viewed bool) []fleetDetailLine {
	w := cw - fleetSidebarRightGutter - fleetDetailIndent - prefixW
	if w < fleetMinRecapWidth {
		return nil
	}
	out := rowDetailText(r, w, viewed)
	if chips := rowChips(r, rowNameFits(r, prefixW, cw), d); len(chips) > 0 {
		styled := renderChips(chips, w)
		out = append(out, fleetDetailLine{text: ansi.Strip(styled), styled: styled})
	}
	return out
}

// rowDetailText is rowDetail's recap and activity lines at text width w.
func rowDetailText(r fleet.Row, w int, viewed bool) []fleetDetailLine {
	activity := fleet.ActivityLine(r)
	rc := rowRecap(r)
	if rc.Source == fleet.RecapNone {
		return nil // the fallback sentence is already the whole story
	}
	if rc.Source == fleet.RecapActivity {
		if viewed {
			return nil
		}
		marker, text := activityMarker(r, rc.Text)
		return []fleetDetailLine{{text: truncateToWidth(text, w-lipgloss.Width(marker)), marker: marker}}
	}
	recap := rc.Text
	showActivity := !viewed && activity != "" && activity != recap

	recapMax := fleetDetailMaxLines
	if showActivity {
		recapMax--
	}
	var out []fleetDetailLine
	for _, l := range wrapLines(recap, w, recapMax) {
		out = append(out, fleetDetailLine{text: l})
	}
	if showActivity {
		marker, text := activityMarker(r, activity)
		out = append(out, fleetDetailLine{text: truncateToWidth(text, w-lipgloss.Width(marker)), marker: marker})
	}
	return out
}

// Markers leading an activity detail line: a shell command from the last
// tool detail ("Bash: go test" shows as "$ go test"), anything else.
const (
	fleetCommandMarker  = "$ "
	fleetActivityMarker = "› "
)

// activityMarker picks the marker for an activity line and the text to show
// after it: a Bash tool detail drops its "Bash: " label for "$ ".
func activityMarker(r fleet.Row, text string) (marker, shown string) {
	if r.LastToolDetail != "" {
		if cmd, ok := strings.CutPrefix(text, "Bash: "); ok {
			return fleetCommandMarker, cmd
		}
	}
	return fleetActivityMarker, text
}

// rowDetailStyled renders rowDetail's lines with the bar, indent and the
// tree continuation for the row's (already clamped) prefix.
func rowDetailStyled(r fleet.Row, treePrefix string, cw int, disp fleetDisplay, viewed bool) []string {
	indent := fleetProjectBar(r.Project) + strings.Repeat(" ", fleetDetailIndent-1) + fleetDimStyle.Render(fleetContinuation(treePrefix))
	var out []string
	for _, d := range rowDetail(r, lipgloss.Width(treePrefix), cw, disp, viewed) {
		if d.styled != "" {
			out = append(out, indent+d.styled)
			continue
		}
		text := fleetActivityStyle.Render(d.text)
		if d.marker != "" {
			text = fleetDimStyle.Render(d.marker) + text
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
func (m FleetModel) lineHeight(i, cw int) int {
	if i < 0 || i >= len(m.lines) {
		return 0
	}
	if l := m.lines[i]; l.kind == fleetLineRow && i == m.splitPane.Cursor() {
		return 1 + len(rowDetail(l.row, lipgloss.Width(fleetKeptPrefix(l.treePrefix, cw)), cw, m.display, m.viewerShows(l.row)))
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

// fleetClosePending records a closed row's position within its group.
type fleetClosePending struct {
	session  string
	windowID string
	nextID   string // window id of the row directly after it, if any
	prevID   string // window id of the row directly before it, if any
	killed   bool   // the close succeeded
	tries    int    // reloads that still showed the window
}

// fleetClosePendingMaxTries bounds how many reloads wait for the monitor's
// snapshot to drop a closed window before giving up.
const fleetClosePendingMaxTries = 6

// recordClose captures the neighbours of the row being closed.
func (m *FleetModel) recordClose(r fleet.Row) {
	p := &fleetClosePending{session: r.Session, windowID: r.WindowID}
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
func (m *FleetModel) applyClosePending() {
	p := m.closePending
	if p == nil || !p.killed {
		return
	}
	has := func(id string) bool {
		for _, l := range m.lines {
			if l.kind == fleetLineRow && l.row.Session == p.session && l.row.WindowID == id {
				return true
			}
		}
		return false
	}
	if has(p.windowID) {
		if p.tries++; p.tries >= fleetClosePendingMaxTries {
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

// fleetViewerMsg reports the window the live viewer currently shows.
type fleetViewerMsg struct{ home, window string }

// fleetViewerPollCmd asks tmux which window the viewer shows.
func fleetViewerPollCmd() tea.Cmd {
	return func() tea.Msg {
		home, win := tmux.ViewerActiveWindow()
		return fleetViewerMsg{home: home, window: win}
	}
}

// viewerSyncTarget decides whether the hub selection must follow the viewer.
// The viewer shows home|win; seen is what the previous poll saw and liveShown
// what the hub itself last handed to the viewer. Only a change the hub did not
// cause (observed differs from both) is followed, and only when the window is
// a row of the list. It returns the key to remember as seen.
func viewerSyncTarget(lines []fleetLine, liveShown, seen, home, win string) (key string, follow bool) {
	if home == "" || win == "" {
		return seen, false
	}
	key = home + "|" + win
	if key == seen || key == liveShown {
		return key, false
	}
	for _, l := range lines {
		if l.kind == fleetLineRow && l.row.Session == home && l.row.WindowID == win {
			return key, true
		}
	}
	return key, false
}

// syncFromViewer moves the list selection to the window the viewer switched
// to by other means (a chrome-bar tab click inside the live pane).
func (m FleetModel) syncFromViewer(msg fleetViewerMsg) FleetModel {
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
	m.wheelHold = false // the selection changed: follow it
	m.resolveSelection()
	return m
}

// fleetLeaver adapts the hub list to hubtarget.Leaver. The fleet process runs in
// the hub pane (no popup), so the current client is right and no -c is needed;
// failures surface through jumpErr rather than display-message.
type fleetLeaver struct {
	m    *FleetModel
	home string
}

func (fleetLeaver) SwitchLast() error { return tmux.SwitchClientToLast("") }
func (fleetLeaver) SwitchSession(n string) error {
	return tmux.SwitchClientToSession("", n)
}
func (l fleetLeaver) ViewedHome() string             { return l.home }
func (fleetLeaver) Sessions() []tmux.SessionActivity { return tmux.ListSessionActivity() }
func (l fleetLeaver) Message(msg string)             { l.m.jumpErr = msg }

// Chrome-zoom seams, overridden in tests so they never touch tmux.
var (
	hubZoomSync      = tmux.SyncHubZoom
	hubChromeDefault = func() bool { return config.Load().HubChrome }
)

// fleetZoomSyncCmd brings the viewed window's zoom in line with the chrome
// toggle (shown = chrome bar visible in the live pane). Run on start (which
// also sweeps zooms leaked by a previous hub), every poll tick (the viewer
// can change window without the hub: chrome-bar tab clicks, leaving and
// returning to the hub) and on toggle.
func fleetZoomSyncCmd(shown bool) tea.Cmd {
	return func() tea.Msg {
		hubZoomSync(!shown)
		return nil
	}
}

// toggleChrome flips whether the live pane shows the agent's chrome bar and
// applies it to the viewed window at once.
func (m *FleetModel) toggleChrome() tea.Cmd {
	m.hubChrome = !m.hubChrome
	msg := "chrome bar hidden in the hub"
	if m.hubChrome {
		msg = "chrome bar shown in the hub"
	}
	m.jumpErr = ""
	m.setSuccess(msg)
	return fleetZoomSyncCmd(m.hubChrome)
}

// HubChrome reports whether the live pane shows the agent's chrome bar.
func (m FleetModel) HubChrome() bool { return m.hubChrome }

// dimContextRow mutes a rendered row the attention filter keeps only as an
// ancestor of a matching row: the project bar keeps its colour, everything
// after it goes dim.
func dimContextRow(r fleet.Row, line string) string {
	plain := ansi.Strip(line)
	_, rest, ok := strings.Cut(plain, "▎")
	if !ok {
		return fleetDimStyle.Render(plain)
	}
	return fleetProjectBar(r.Project) + fleetDimStyle.Render(rest)
}

// fleetWheelLines is how many lines one wheel notch scrolls the list.
const fleetWheelLines = 3

// maxScrollOffset is the largest scrollOffset that still fills the list area:
// the first line from which everything to the end fits on screen (0 when the
// whole list fits).
func (m FleetModel) maxScrollOffset() int {
	if len(m.lines) == 0 {
		return 0
	}
	cw := m.contentWidth()
	visible := m.visibleLines()
	used := 0
	i := len(m.lines)
	for i > 0 {
		h := m.lineHeight(i-1, cw)
		if used+h > visible {
			break
		}
		used += h
		i--
	}
	return i
}

// clampScroll pulls scrollOffset back into range (the list may have shrunk).
func (m *FleetModel) clampScroll() {
	if mx := m.maxScrollOffset(); m.scrollOffset > mx {
		m.scrollOffset = mx
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
}

// handleWheel scrolls the viewport (not the selection) by fleetWheelLines per
// notch, clamped to the list. Ignored while the menu or key help is open, and
// for events over the live pane column.
func (m *FleetModel) handleWheel(msg tea.MouseWheelMsg) {
	if m.menuOpen || !m.clickable() {
		return
	}
	if m.liveLeft > 0 && msg.X >= m.liveLeft {
		return
	}
	var delta int
	switch msg.Button {
	case tea.MouseWheelUp:
		delta = -fleetWheelLines
	case tea.MouseWheelDown:
		delta = fleetWheelLines
	default:
		return
	}
	before := m.scrollOffset
	m.scrollOffset += delta
	m.clampScroll()
	m.wheelHold = true
	if m.scrollOffset != before {
		m.refreshHover() // content moved under the pointer
	}
}
