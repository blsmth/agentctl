package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

const (
	// chromeBarClockInterval is how often the clock line is refreshed and the
	// tmux window list is re-read in file-watch mode.
	chromeBarClockInterval = 30 * time.Second

	// chromeBarDebounce coalesces a burst of windowstate writes (statusline
	// pushes and PreTool writes hit them constantly) into one rebuild.
	chromeBarDebounce = 100 * time.Millisecond

	// chromeBarPrefixInterval is how often the active window's bar polls
	// #{client_prefix} for the prefix indicator.
	chromeBarPrefixInterval = 250 * time.Millisecond

	// chromeBarPollInterval drives rebuilds when fsnotify is unavailable.
	chromeBarPollInterval = time.Second
)

// TabInfo holds display data for one agent (or shell) window tab.
type TabInfo struct {
	Name         string
	State        string // working/idle/blocked/done
	CtxPct       int
	Ticket       int
	InputTokens  int
	OutputTokens int
	WindowID     string
	IsActive     bool
}

// windowsChangedMsg is sent by the watcher when windowstate changed (after
// debouncing). relist is true when a record was created or removed, i.e. the
// tmux window list may have changed and must be re-read.
type windowsChangedMsg struct{ relist bool }

// activeWindowChangedMsg is sent by the watcher when the active-window file
// has changed. This provides near-instant active-tab highlight updates driven
// by the tmux after-select-window hook.
type activeWindowChangedMsg struct{}

// chromeBarClockTickMsg fires every 30s in file-watch mode to refresh the
// clock and re-read the tmux window list.
type chromeBarClockTickMsg struct{}

// prefixTickMsg fires every 250ms; only the active window's bar acts on it.
type prefixTickMsg struct{}

// chromeBarReloadTickMsg fires every few seconds to check for a new binary.
type chromeBarReloadTickMsg struct{}

// prefixResultMsg carries the polled #{client_prefix} value.
type prefixResultMsg struct{ active bool }

// ChromeBarModel is a Bubble Tea model that renders the 2-row bottom chrome:
//
//	Line 1: keybind hints (ctrl+b badge + key list) — or toast when active
//	Line 2: agent tabs (state indicators, spinner, right-aligned clock)
//
// Rate limits and per-window context/token metrics used to live here (lines
// 1-2 of a 4-row bar) but moved to the "rich" Claude Code status line
// (internal/statusline) — that surface already receives the same
// pushed data and can render it without agentctl polling for it, so the
// chrome bar shrank to just the rows the status line structurally cannot
// cover (it is per-process, hides during permission prompts, and doesn't
// exist in non-Claude panes).
//
// Running both rows as a single Bubble Tea program eliminates the tmux
// pane-border line that would otherwise appear between separate panes.
//
// When a toast is active the toast line replaces line 1 (keybinds) for the
// duration of the notification, returning to normal once the toast finishes.
// Any model in the codebase can enqueue a toast with one line:
//
//	return NewToast("dispatch complete", WithToastLevel(ToastInfo))
type ChromeBarModel struct {
	width       int
	windowID    string       // target window for per-window context% (toast thresholds)
	runtimeDir  string       // .agentctl dir holding windows/ and active-window; "" disables watching
	watchEvents chan tea.Msg // persistent watcher channel; nil when runtimeDir is ""

	// Seams for tests. listFn lists the session's tmux windows; prefixFn
	// reports #{client_prefix}.
	listFn   func() ([]tmux.WindowInfo, error)
	prefixFn func() bool

	wins     []tmux.WindowInfo // last successful tmux list-windows result
	haveWins bool
	rebuilds int // number of view rebuilds (diagnostic and test hook)

	// ── Toast notifications ──────────────────────────────────────────────────
	toast ToastModel

	// ── Tab bar state (line 2) ───────────────────────────────────────────────
	tabs           []TabInfo
	clock          string
	spinner        spinner.Model
	activeWindowID string

	// ── Context warn/critical toast state ────────────────────────────────────
	ctxPct int

	// ctxWarnFired / ctxCritFired latch the warn/critical context toasts so
	// they fire once per threshold crossing rather than on every tick.
	// Both re-arm with hysteresis (10 points below their threshold) so a
	// new session (after /clear, when ctxPct drops back to 0) reliably gets
	// warned again instead of the latch staying permanently tripped.
	ctxWarnFired bool
	ctxCritFired bool

	// warnPct / critPct are the configured context thresholds (defaults
	// 75/90), refreshed from config when the view changes rather than on
	// every View() render, since config.Load() touches disk.
	warnPct int
	critPct int

	// ── Status bar state (line 1) ────────────────────────────────────────────
	prefixActive bool
	hints        []KeyHint

	// reload watches the binary for hot reload; nil in tests.
	reload *reloader
}

// NewChromeBarModel creates a new ChromeBarModel for the given tmux window ID.
// windowID is used to read per-window context% for the warn/critical toast.
// runtimeDir is the .agentctl directory whose windows/ directory and
// active-window file are watched; pass "" to disable watching (tabs stay
// empty). The view is built from `tmux list-windows` plus windowstate; it
// never reads state.json.
func NewChromeBarModel(windowID string, runtimeDir string) *ChromeBarModel {
	m := newChromeBarModel(windowID, runtimeDir,
		func() ([]tmux.WindowInfo, error) { return tmux.ListWindowsErr() },
		tmux.PrefixActive)
	if runtimeDir != "" {
		m.refreshThresholds()
		m.rebuild(true)
		m.watchEvents = startWatcher(runtimeDir)
	}
	m.reload = newReloader("chrome-bar", windowID)
	return m
}

// newChromeBarModel builds a model with injected seams and no I/O.
func newChromeBarModel(windowID, runtimeDir string, list func() ([]tmux.WindowInfo, error), prefix func() bool) *ChromeBarModel {
	s := spinner.New()
	s.Spinner = spinner.MiniDot
	// Zero-value ModelsConfig yields the documented defaults (75/90) via its
	// Effective* accessors — the single source of truth for these numbers.
	var zeroModelsCfg config.ModelsConfig
	return &ChromeBarModel{
		width:      80,
		windowID:   windowID,
		runtimeDir: runtimeDir,
		listFn:     list,
		prefixFn:   prefix,
		hints:      defaultStatusBarHints,
		spinner:    s,
		toast:      newToastModel(),
		warnPct:    zeroModelsCfg.EffectiveContextWarnPct(),
		critPct:    zeroModelsCfg.EffectiveContextCriticalPct(),
	}
}

// Init starts the spinner, the watcher wait, and the clock and prefix ticks.
func (m ChromeBarModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick, chromeBarPrefixTick()}
	if m.runtimeDir != "" {
		cmds = append(cmds,
			waitForStateChange(m.watchEvents),
			chromeBarClockTick(),
		)
	}
	if m.reload != nil && m.reload.changed != nil {
		cmds = append(cmds, chromeBarReloadTick())
	}
	return tea.Batch(cmds...)
}

// Reexec reports whether the bar quit because the binary changed; the caller
// must exec the new image after the program returns.
func (m ChromeBarModel) Reexec() bool { return m.reload.triggered() }

// WithoutReload disables hot reload after a failed exec.
func (m ChromeBarModel) WithoutReload() ChromeBarModel { m.reload.disable(); return m }

func chromeBarReloadTick() tea.Cmd {
	return tea.Tick(reloadCheckInterval, func(time.Time) tea.Msg { return chromeBarReloadTickMsg{} })
}

// chromeBarClockTick schedules a 30-second clock refresh used in file-watch mode.
func chromeBarClockTick() tea.Cmd {
	return tea.Tick(chromeBarClockInterval, func(_ time.Time) tea.Msg {
		return chromeBarClockTickMsg{}
	})
}

// chromeBarPrefixTick schedules the next prefix poll.
func chromeBarPrefixTick() tea.Cmd {
	return tea.Tick(chromeBarPrefixInterval, func(_ time.Time) tea.Msg {
		return prefixTickMsg{}
	})
}

// watchedEvent is one filtered fsnotify event after classification.
type watchedEvent struct {
	active bool // active-window file written
	write  bool // an existing windowstate record changed
	relist bool // a windowstate record was created or removed
}

// classifyEvent maps a raw fsnotify event to a watchedEvent. known is the set
// of record file names (e.g. "@3.json") seen so far and is updated in place:
// windowstate writes are tmp+rename, which fsnotify reports as Create, so
// "created" only counts as a new window when the name was not known before.
func classifyEvent(ev fsnotify.Event, windowsDir, activePath string, known map[string]bool) (watchedEvent, bool) {
	if ev.Name == activePath {
		if ev.Op&(fsnotify.Create|fsnotify.Write) != 0 {
			return watchedEvent{active: true}, true
		}
		return watchedEvent{}, false
	}
	if filepath.Dir(ev.Name) != windowsDir || !strings.HasSuffix(ev.Name, ".json") {
		return watchedEvent{}, false
	}
	name := filepath.Base(ev.Name)
	switch {
	case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
		if known[name] {
			delete(known, name)
			return watchedEvent{relist: true}, true
		}
		return watchedEvent{}, false
	case ev.Op&(fsnotify.Create|fsnotify.Write) != 0:
		if !known[name] {
			known[name] = true
			return watchedEvent{relist: true}, true
		}
		return watchedEvent{write: true}, true
	}
	return watchedEvent{}, false
}

// startWatcher creates one persistent fsnotify.Watcher over runtimeDir/windows
// and runtimeDir (for the active-window file) and returns a channel on which
// it delivers windowsChangedMsg (debounced) and activeWindowChangedMsg
// values. The watcher runs for the lifetime of the process. When fsnotify is
// unavailable it falls back to polling at chromeBarPollInterval.
func startWatcher(runtimeDir string) chan tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() {
		w, err := fsnotify.NewWatcher()
		if err == nil {
			defer func() { _ = w.Close() }()
			err = watchDirs(w, runtimeDir)
		}
		if err != nil {
			pollLoop(ch)
			return
		}
		watchLoop(w, runtimeDir, ch, chromeBarDebounce)
	}()
	return ch
}

func watchDirs(w *fsnotify.Watcher, runtimeDir string) error {
	windowsDir := windowstate.WindowsDirFor(runtimeDir)
	// windows/ may not exist yet; watching it is best-effort so the
	// runtime dir (and the active-window file) still works.
	_ = w.Add(windowsDir)
	return w.Add(runtimeDir)
}

// pollLoop is the fallback when fsnotify cannot be used: rebuild every
// chromeBarPollInterval, re-listing tmux each time.
func pollLoop(ch chan<- tea.Msg) {
	for {
		time.Sleep(chromeBarPollInterval)
		ch <- windowsChangedMsg{relist: true}
	}
}

// watchLoop turns raw events into debounced messages: a burst of events inside
// one debounce window yields at most one windowsChangedMsg (relist OR'd
// together) plus at most one activeWindowChangedMsg.
func watchLoop(w *fsnotify.Watcher, runtimeDir string, ch chan<- tea.Msg, debounce time.Duration) {
	windowsDir := windowstate.WindowsDirFor(runtimeDir)
	activePath := filepath.Join(runtimeDir, "active-window")
	known := map[string]bool{}
	if entries, err := readJSONNames(windowsDir); err == nil {
		for _, e := range entries {
			known[e] = true
		}
	}

	var (
		timer   *time.Timer
		fire    <-chan time.Time
		pending bool
		relist  bool
		active  bool
	)
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			we, relevant := classifyEvent(ev, windowsDir, activePath, known)
			if !relevant {
				continue
			}
			if we.active {
				active = true
			} else {
				pending = true
				relist = relist || we.relist
			}
			if fire == nil {
				timer = time.NewTimer(debounce)
				fire = timer.C
			}
		case _, ok := <-w.Errors:
			if !ok {
				return
			}
			pending, relist = true, true
			if fire == nil {
				timer = time.NewTimer(debounce)
				fire = timer.C
			}
		case <-fire:
			fire = nil
			if active {
				ch <- activeWindowChangedMsg{}
				active = false
			}
			if pending {
				ch <- windowsChangedMsg{relist: relist}
				pending, relist = false, false
			}
		}
	}
}

// waitForStateChange returns a Bubble Tea Cmd that blocks until the persistent
// watcher delivers the next message, then forwards it to the Bubble Tea runtime.
func waitForStateChange(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

// refreshThresholds reloads the context warn/critical thresholds from config.
func (m *ChromeBarModel) refreshThresholds() {
	cfg := config.Load()
	m.warnPct = cfg.Models.EffectiveContextWarnPct()
	m.critPct = cfg.Models.EffectiveContextCriticalPct()
}

// rebuild recomputes the tabs, active window and this window's ctx% from
// `tmux list-windows` (only when relist is set or nothing is cached) joined
// with windowstate, and reports whether any rendered field changed. A failed
// tmux call keeps the last render and reports no change.
func (m *ChromeBarModel) rebuild(relist bool) bool {
	m.rebuilds++
	if relist || !m.haveWins {
		wins, err := m.listFn()
		if err != nil {
			return false
		}
		m.wins, m.haveWins = wins, true
	}

	records := windowstate.AllFrom(windowstate.WindowsDirFor(m.runtimeDir))
	agents := fleet.BuildAgents(m.wins, records, nil, time.Now())

	active := tmux.ActiveWindowIDFast(filepath.Join(m.runtimeDir, "active-window"))

	// The persistent shell window is not an agent; its ID comes from the same
	// window list.
	shellID := ""
	for _, w := range m.wins {
		if tmux.StripAgentPrefix(w.Name) == "shell" {
			shellID = w.ID
			break
		}
	}
	tabs := make([]TabInfo, 0, len(agents)+1)
	tabs = append(tabs, TabInfo{Name: "shell", State: "idle", WindowID: shellID, IsActive: shellID != "" && shellID == active})
	ctxPct := m.ctxPct
	for _, a := range agents {
		st := a.State
		if a.Done || (a.TurnDone && st != "working" && st != "blocked") {
			st = "done"
		}
		tabs = append(tabs, TabInfo{
			Name:     tmux.BaseAgentName(a.Name),
			State:    st,
			CtxPct:   a.ContextPct,
			Ticket:   a.Ticket,
			WindowID: a.WindowID,
			IsActive: a.WindowID == active,
		})
		if a.WindowID == m.windowID {
			ctxPct = a.ContextPct
		}
	}

	changed := !slices.Equal(tabs, m.tabs) || active != m.activeWindowID || ctxPct != m.ctxPct
	if changed {
		m.tabs, m.activeWindowID, m.ctxPct = tabs, active, ctxPct
	}
	return changed
}

// Update handles Bubble Tea messages.
func (m ChromeBarModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyPressMsg:
		// No key quits the bar: focus can land here (prefix+arrow,
		// select-pane), and quitting would leave the window without chrome.
		// Any key hands focus back to the agent pane instead.
		return m, tabClickCmd(m.windowID, "")

	case tea.MouseClickMsg:
		return m, m.handleClick(msg)

	case activeWindowChangedMsg:
		// Fast path: re-read the active-window file and update the highlight
		// without touching tmux.
		id := tmux.ActiveWindowIDFast(filepath.Join(m.runtimeDir, "active-window"))
		if id != "" && id != m.activeWindowID {
			m.activeWindowID = id
			tabs := slices.Clone(m.tabs)
			for i := range tabs {
				tabs[i].IsActive = tabs[i].WindowID == id
			}
			m.tabs = tabs
		}
		return m, waitForStateChange(m.watchEvents)

	case chromeBarReloadTickMsg:
		if m.reload.due() {
			return m, tea.Quit
		}
		return m, chromeBarReloadTick()

	case prefixTickMsg:
		next := chromeBarPrefixTick()
		if m.windowID == "" || m.windowID != m.activeWindowID {
			// Only the active window's bar shows the indicator: one tmux
			// exec per 250ms in total, not one per window.
			if m.prefixActive {
				m.prefixActive = false
			}
			return m, next
		}
		prefixFn := m.prefixFn
		return m, tea.Batch(next, func() tea.Msg { return prefixResultMsg{active: prefixFn()} })

	case prefixResultMsg:
		m.prefixActive = msg.active
		return m, nil

	case windowsChangedMsg:
		oldCtx := m.ctxPct
		changed := m.rebuild(msg.relist)
		if msg.relist || (changed && m.ctxPct != oldCtx) {
			m.refreshThresholds()
		}
		if changed {
			m.clock = time.Now().Format("15:04")
		}

		warnPct, critPct := m.warnPct, m.critPct

		// Re-arm the latches with hysteresis (10 points below their
		// threshold) before checking for a new crossing. Without this,
		// ctxWarnFired/ctxCritFired would never reset, so a new session
		// after /clear (ctxPct back to 0) would silently get no warning.
		if m.ctxWarnFired && m.ctxPct < warnPct-10 {
			m.ctxWarnFired = false
		}
		if m.ctxCritFired && m.ctxPct < critPct-10 {
			m.ctxCritFired = false
		}

		var toastCmd tea.Cmd
		switch {
		case m.ctxPct >= critPct && !m.ctxCritFired:
			m.ctxCritFired = true
			m.ctxWarnFired = true // critical implies the warn threshold too
			toastCmd = NewToast(
				fmt.Sprintf("context at %d%% — compaction imminent, start a new session", m.ctxPct),
				WithToastLevel(ToastError),
				WithToastDuration(5*time.Second),
			)
		case m.ctxPct >= warnPct && !m.ctxWarnFired:
			m.ctxWarnFired = true
			toastCmd = NewToast(
				fmt.Sprintf("context at %d%% — consider starting a new session", m.ctxPct),
				WithToastLevel(ToastWarn),
				WithToastDuration(5*time.Second),
			)
		}
		return m, tea.Batch(waitForStateChange(m.watchEvents), toastCmd)

	case chromeBarClockTickMsg:
		// The slow tick also re-reads the tmux window list, catching windows
		// created without a windowstate record.
		m.clock = time.Now().Format("15:04")
		m.rebuild(true)
		m.refreshThresholds()
		return m, chromeBarClockTick()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case ToastMsg, toastAnimMsg, toastHoldDoneMsg:
		var cmd tea.Cmd
		m.toast, cmd = m.toast.Update(msg)
		return m, cmd
	}
	return m, nil
}

// View returns the 2-row chrome bar as a newline-joined string:
//
//	line1 (keybinds or active toast) + "\n" + line2 (tabs)
//
// When a toast is animating or visible, line 1 is replaced by the toast
// notification. The chrome bar remains exactly 2 rows — zero height impact
// when the queue is idle.
func (m ChromeBarModel) View() tea.View {
	w := m.width
	if w < 20 {
		w = 20
	}
	var line1 string
	if m.toast.Active() {
		line1 = m.toast.View(w)
	} else {
		line1 = m.renderKeybindLine(w)
	}
	line2 := m.renderTabLine(w)
	v := tea.NewView(line1 + "\n" + line2)
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// ── Line 1: keybind hints ────────────────────────────────────────────────────

// renderKeybindLine renders the ctrl+b badge and keybinding hint list.
// Hints are dropped from the end when the line would otherwise overflow the
// requested width -- see renderKeybindHints (keyhints.go) for the
// droppable-segment logic.
func (m ChromeBarModel) renderKeybindLine(w int) string {
	t := theme.Current()
	return renderKeybindHints(w, m.hints, m.prefixActive, t)
}

// ── Line 2: agent tabs ───────────────────────────────────────────────────────

// tabSpan is the half-open cell column range [Start, End) a tab occupies on
// line 2 of the chrome bar.
type tabSpan struct {
	WindowID string
	Start    int
	End      int
}

// tabStyles bundles the line-2 styles so rendering and hit-testing share them.
type tabStyles struct {
	session, active, working, blocked, done, idle, sep, fill, clock lipgloss.Style
}

func newTabStyles() tabStyles {
	t := theme.Current()
	base := func(fg string) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(t.Base))
	}
	return tabStyles{
		session: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(t.Bright)).
			Background(lipgloss.Color(t.Accent)).
			Padding(0, 1),
		active: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(t.Bright)).
			Background(lipgloss.Color(t.Surface)).
			Padding(0, 1),
		working: base(t.Accent),
		blocked: base(t.Yellow),
		done:    base(t.Green),
		idle:    base(t.Subtle),
		sep:     base(t.Muted),
		fill:    base(t.Muted),
		clock:   base(t.Subtle),
	}
}

// renderTabLabel renders one tab label (no leading separator).
func renderTabLabel(tab TabInfo, spinnerView string, st tabStyles) string {
	displayName := tab.Name
	if tab.Ticket > 0 {
		displayName = fmt.Sprintf("%s #%d", tab.Name, tab.Ticket)
	}
	var icon string
	var style lipgloss.Style
	switch tab.State {
	case "working":
		icon, style = spinnerView, st.working
	case "blocked":
		icon, style = "⏸", st.blocked
	case "done":
		icon, style = "✓", st.done
	default:
		icon, style = "○", st.idle
	}
	if tab.IsActive {
		style = st.active
	}
	return style.Render(icon + " " + displayName)
}

// tabLayout is a pure function of its inputs: it returns the rendered session
// badge, the rendered tab labels, and each tab's cell span. Both rendering and
// click hit-testing call it, so they cannot disagree.
func tabLayout(tabs []TabInfo, spinnerView string, st tabStyles) (badge string, labels []string, spans []tabSpan) {
	badge = st.session.Render(" " + tmux.SessionName + " ")
	sepW := lipgloss.Width(st.sep.Render(" "))
	x := lipgloss.Width(badge)
	labels = make([]string, len(tabs))
	spans = make([]tabSpan, len(tabs))
	for i, tab := range tabs {
		x += sepW // the separator belongs to no tab
		labels[i] = renderTabLabel(tab, spinnerView, st)
		w := lipgloss.Width(labels[i])
		spans[i] = tabSpan{WindowID: tab.WindowID, Start: x, End: x + w}
		x += w
	}
	return badge, labels, spans
}

// tabSpans returns the current tab spans for hit-testing.
func (m ChromeBarModel) tabSpans() []tabSpan {
	_, _, spans := tabLayout(m.tabs, m.spinner.View(), newTabStyles())
	return spans
}

// tabAt returns the window ID of the tab covering cell column x.
func (m ChromeBarModel) tabAt(x int) (string, bool) {
	for _, sp := range m.tabSpans() {
		if x >= sp.Start && x < sp.End && sp.WindowID != "" {
			return sp.WindowID, true
		}
	}
	return "", false
}

// renderTabLine renders the session badge, agent tabs, fill, and clock.
func (m ChromeBarModel) renderTabLine(w int) string {
	st := newTabStyles()
	badge, labels, _ := tabLayout(m.tabs, m.spinner.View(), st)

	var sb strings.Builder
	sb.WriteString(badge)
	for _, label := range labels {
		sb.WriteString(st.sep.Render(" "))
		sb.WriteString(label)
	}

	left := sb.String()
	leftW := lipgloss.Width(left)

	clockStr := st.clock.Render(" " + m.clock + " ")
	clockW := lipgloss.Width(clockStr)

	fill := w - leftW - clockW
	if fill < 0 {
		fill = 0
	}
	return left + st.fill.Render(strings.Repeat(" ", fill)) + clockStr
}

// focusAgentPane returns a window's agent (non-chrome) pane to focus.
func focusAgentPane(windowID string) {
	if windowID == "" {
		return
	}
	if pane := tmux.FindMainPane(windowID); pane != "" {
		_ = tmux.SelectPane(pane)
	}
}

// selectTabWindow switches to target. When origin is being viewed through the
// fleet hub's live viewer, the viewer session switches (the hub list follows by
// polling the viewer); otherwise the home session switches as usual.
func selectTabWindow(origin, target string) {
	if viewer := tmux.ViewerForWindow(origin); viewer != "" {
		if tmux.SelectViewerWindow(viewer, target) == nil {
			return
		}
	}
	_ = exec.Command("tmux", "select-window", "-t", target).Run()
}

// tabClickCmd switches to target (if non-empty) and restores focus to the
// agent pane in both the origin and target windows. It runs off the Update
// goroutine as a tea.Cmd.
func tabClickCmd(origin, target string) tea.Cmd {
	return func() tea.Msg {
		focusAgentPane(origin)
		if target != "" {
			if target != origin {
				selectTabWindow(origin, target)
			}
			focusAgentPane(target)
		}
		return nil
	}
}

// clickTarget classifies a mouse click. act is false for clicks that must be
// ignored entirely (wheel, right/middle). Otherwise target is the window to
// select, or "" when the click only needs to return focus to the agent pane
// (line 1, badge, fill, clock).
func (m ChromeBarModel) clickTarget(msg tea.MouseClickMsg) (target string, act bool) {
	if msg.Button != tea.MouseLeft {
		return "", false
	}
	if msg.Y != 1 {
		return "", true
	}
	target, _ = m.tabAt(msg.X)
	return target, true
}

// handleClick maps a mouse click to a command; nil when it should be ignored.
func (m ChromeBarModel) handleClick(msg tea.MouseClickMsg) tea.Cmd {
	target, act := m.clickTarget(msg)
	if !act {
		return nil
	}
	return tabClickCmd(m.windowID, target)
}

// readJSONNames lists the *.json record file names in dir.
func readJSONNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}
