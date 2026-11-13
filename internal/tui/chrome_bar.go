package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/tmux"
)

// chromeBarClockInterval is how often the clock line is refreshed when running
// in file-watch mode.
const chromeBarClockInterval = 30 * time.Second

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

// stateFileChangedMsg is sent by watchStateFile when state.json has changed.
type stateFileChangedMsg struct{}

// activeWindowChangedMsg is sent by watchStateFile when the active-window file
// has changed. This provides near-instant active-tab highlight updates driven
// by the tmux after-select-window hook rather than the 500ms state.json tick.
type activeWindowChangedMsg struct{}

// chromeBarClockTickMsg fires every 30s in file-watch mode to refresh the clock.
type chromeBarClockTickMsg struct{}

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
	statePath   string       // path to state.json; empty string enables polling fallback
	watchEvents chan tea.Msg // persistent watcher channel; nil when statePath is ""

	// ── Toast notifications ──────────────────────────────────────────────────
	toast ToastModel

	// ── Tab bar state (line 2) ───────────────────────────────────────────────
	tabs           []TabInfo
	clock          string
	spinner        spinner.Model
	activeWindowID string
	shellWindowID  string // cached ID of the "shell" tmux window

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
	// 75/90), refreshed from config on every readStateFile() call rather
	// than on every View() render, since config.Load() touches disk.
	warnPct int
	critPct int

	// ── Status bar state (line 1) ────────────────────────────────────────────
	prefixActive bool
	hints        []KeyHint
}

// NewChromeBarModel creates a new ChromeBarModel for the given tmux window ID.
// windowID is used to read per-window context% for the warn/critical toast.
// statePath is the path to state.json for file-watch mode; pass "" to fall back
// to the legacy tmux polling behavior.
func NewChromeBarModel(windowID string, statePath string) *ChromeBarModel {
	s := spinner.New()
	s.Spinner = spinner.MiniDot
	// Zero-value ModelsConfig yields the documented defaults (75/90) via its
	// Effective* accessors — the single source of truth for these numbers.
	var zeroModelsCfg config.ModelsConfig
	m := &ChromeBarModel{
		width:     80,
		windowID:  windowID,
		statePath: statePath,
		hints:     defaultStatusBarHints,
		spinner:   s,
		toast:     newToastModel(),
		warnPct:   zeroModelsCfg.EffectiveContextWarnPct(),
		critPct:   zeroModelsCfg.EffectiveContextCriticalPct(),
	}
	if statePath != "" {
		m.watchEvents = startWatcher(statePath)
	}
	return m
}

// Init starts the file-watch and clock tick commands.
// statePath must be set; if it is empty a warning is logged and no tabs are polled.
func (m ChromeBarModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick}
	if m.statePath != "" {
		// File-watch mode: read state.json via fsnotify; use a slow clock timer.
		m.readStateFile()
		cmds = append(cmds,
			waitForStateChange(m.watchEvents),
			chromeBarClockTick(),
		)
	}
	// If statePath is empty, no tab data is available — tabs remain empty.
	return tea.Batch(cmds...)
}

// chromeBarClockTick schedules a 30-second clock refresh used in file-watch mode.
func chromeBarClockTick() tea.Cmd {
	return tea.Tick(chromeBarClockInterval, func(_ time.Time) tea.Msg {
		return chromeBarClockTickMsg{}
	})
}

// startWatcher creates one persistent fsnotify.Watcher for the given state.json
// path and returns a channel on which it delivers stateFileChangedMsg or
// activeWindowChangedMsg values. The watcher runs for the lifetime of the
// process — it is never closed — so no FDs are churned on each state update.
func startWatcher(path string) chan tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() {
		w, err := fsnotify.NewWatcher()
		if err != nil {
			ch <- stateFileChangedMsg{}
			return
		}
		defer func() { _ = w.Close() }()

		dir := filepath.Dir(path)
		if err := w.Add(dir); err != nil {
			ch <- stateFileChangedMsg{}
			return
		}

		activeWindowPath := filepath.Join(dir, "active-window")

		for {
			select {
			case event, ok := <-w.Events:
				if !ok {
					return
				}
				if event.Name == path && (event.Op&(fsnotify.Create|fsnotify.Write) != 0) {
					ch <- stateFileChangedMsg{}
				}
				if event.Name == activeWindowPath && (event.Op&(fsnotify.Create|fsnotify.Write) != 0) {
					ch <- activeWindowChangedMsg{}
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
				ch <- stateFileChangedMsg{}
			}
		}
	}()
	return ch
}

// waitForStateChange returns a Bubble Tea Cmd that blocks until the persistent
// watcher delivers the next message, then forwards it to the Bubble Tea runtime.
func waitForStateChange(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

// readStateFile reads state.json and updates all model fields from the snapshot.
// It is safe to call even when the file does not exist — errors are silently ignored.
func (m *ChromeBarModel) readStateFile() {
	// Refresh context thresholds from config. Cheap relative to the disk
	// read below and keeps the warn/critical toast thresholds in sync with
	// ModelsConfig without re-reading config on every View() render.
	cfg := config.Load()
	m.warnPct = cfg.Models.EffectiveContextWarnPct()
	m.critPct = cfg.Models.EffectiveContextCriticalPct()

	snap, err := state.LoadSnapshot()
	if err != nil || snap == nil {
		return
	}

	// Rebuild the tab list from the snapshot agents.
	m.tabs = make([]TabInfo, 0, len(snap.Agents))
	for _, a := range snap.Agents {
		if strings.HasPrefix(a.Name, "improve~") {
			continue
		}
		st := a.State
		if a.Done {
			st = "done"
		}
		m.tabs = append(m.tabs, TabInfo{
			Name:     tmux.BaseAgentName(a.Name),
			State:    st,
			CtxPct:   a.ContextPct,
			Ticket:   a.Ticket,
			WindowID: a.WindowID,
			IsActive: a.WindowID == snap.ActiveWindowID(),
		})
	}

	// Shell is always idle and not tracked in state.json. Look up its window ID
	// once and cache it; the ID is stable for the lifetime of the session.
	if m.shellWindowID == "" {
		m.shellWindowID = tmux.WindowIDByName("shell")
	}
	shellWindowID := m.shellWindowID
	shellTab := TabInfo{
		Name:     "shell",
		State:    "idle",
		WindowID: shellWindowID,
		IsActive: shellWindowID != "" && shellWindowID == snap.ActiveWindowID(),
	}
	m.tabs = append([]TabInfo{shellTab}, m.tabs...)

	m.activeWindowID = snap.ActiveWindowID()
	m.prefixActive = snap.PrefixActive()

	// Prefer the active-window file (written by the tmux hook) over the
	// state.json ActiveWindowID, which may be stale by up to one monitor tick.
	if m.statePath != "" {
		awPath := filepath.Join(filepath.Dir(m.statePath), "active-window")
		if data, err := os.ReadFile(awPath); err == nil {
			if id := strings.TrimSpace(string(data)); id != "" {
				m.activeWindowID = id
				for i := range m.tabs {
					m.tabs[i].IsActive = (m.tabs[i].WindowID == id)
				}
			}
		}
	}

	// Update per-window context% for our own window (warn/critical toast).
	if agent := snap.ByWindowID(m.windowID); agent != nil {
		m.ctxPct = agent.ContextPct
	}
}

// Update handles Bubble Tea messages.
func (m ChromeBarModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil

	case tea.MouseClickMsg:
		return m, m.handleClick(msg)

	case activeWindowChangedMsg:
		// Fast path: read the active-window file and update the tab highlight.
		data, err := os.ReadFile(filepath.Join(filepath.Dir(m.statePath), "active-window"))
		if err == nil {
			id := strings.TrimSpace(string(data))
			if id != "" && id != m.activeWindowID {
				m.activeWindowID = id
				for i := range m.tabs {
					m.tabs[i].IsActive = (m.tabs[i].WindowID == id)
				}
			}
		}
		return m, waitForStateChange(m.watchEvents)

	case stateFileChangedMsg:
		m.readStateFile() // also refreshes m.warnPct / m.critPct from config
		m.clock = time.Now().Format("15:04")

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
		m.clock = time.Now().Format("15:04")
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

// tabClickCmd switches to target (if non-empty) and restores focus to the
// agent pane in both the origin and target windows. It runs off the Update
// goroutine as a tea.Cmd.
func tabClickCmd(origin, target string) tea.Cmd {
	return func() tea.Msg {
		focusAgentPane(origin)
		if target != "" {
			if target != origin {
				_ = exec.Command("tmux", "select-window", "-t", target).Run()
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
