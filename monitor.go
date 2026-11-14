package main

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/blsmth/agentctl/internal/chromebar"
	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/hooks"
	"github.com/blsmth/agentctl/internal/improve"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/metrics"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/pidfile"
	"github.com/blsmth/agentctl/internal/ratelimit"
	"github.com/blsmth/agentctl/internal/reaper"
	"github.com/blsmth/agentctl/internal/registry"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tokens"
	"github.com/blsmth/agentctl/internal/watcher"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// handleCompleteFn is dispatch.HandleComplete, indirected for testing.
var handleCompleteFn = dispatch.HandleComplete

// safeHandle runs fn, recovering any panic so one misbehaving transition
// handler cannot kill the monitor loop. The panic value and stack are logged
// with the handler name and agent/window context.
func safeHandle(name string, t state.Transition, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			logging.Log("monitor: panic in %s handler agent=%s window=%s: %v\n%s",
				name, t.Agent, t.WindowID, r, debug.Stack())
		}
	}()
	fn()
}

// fireHandlers invokes each transition handler individually under safeHandle
// so a panic in one does not skip the others. stateEdge selects which
// handlers run: the done edge (stateEdge false) fires only HandleComplete.
func (m *monitorModel) fireHandlers(t state.Transition, agentCfg config.AgentConfig, stateEdge bool) {
	if stateEdge {
		if m.transitionTracker != nil {
			safeHandle("RecordTransition", t, func() { m.transitionTracker.RecordTransition(t) })
		}
		safeHandle("improve.HandleTransition", t, func() { improve.HandleTransition(t, agentCfg) })
	}
	safeHandle("HandleComplete", t, func() { handleCompleteFn(t, agentCfg) })
}

// instanceState holds all per-instance monitoring state for a single agent window.
type instanceState struct {
	// Identity
	instance string // instance name (e.g. "go-engineer:2")
	base     string // base agent name (e.g. "go-engineer")

	// Log tracking
	logPath    string // cached log file path
	logTTL     int64  // unix time when log lookup expires
	lastSize   int64  // last observed log file size (used for /clear detection only)
	lastChange int64  // unix time of last size change (used for /clear detection only)

	// State detection
	prevState state.AgentStatus // previous state for transition detection
	hasPrev   bool              // whether prevState has been set at least once

	// Activity detail (cached from stateTracker; see updateMetrics)
	lastToolDetail string // last tool detail from stateTracker (e.g. "monitor.go"), cleared when idle
	lastText       string // last assistant text block from stateTracker (already truncated upstream)

	// Token metrics (cumulative totals for this session)
	lastCtxPct int // last known context percentage

	// ctxPctSource and pushedContextWindow/pushedContextTokens cache the
	// outcome of applyVisuals' ctx%-authority resolution:
	// ctxPctSource is "statusline" when a fresh statusline-pushed value won
	// (and pushedContextWindow/pushedContextTokens mirror
	// windowstate.WindowState's ContextWindowSize/ContextTokens for that
	// same push), or "derived" when this monitor's own tokens.CtxPct
	// fallback won (in which case both push fields are left zero).
	// persistState reads these to decide whether AgentState.ContextWindow/
	// ContextTokens should also come from the pushed payload rather than
	// tokens.UsableWindow/lastTurnInput.
	ctxPctSource          string
	pushedContextWindow   int
	pushedContextTokens   int
	lastInputTokens       int    // last known cumulative input token count
	lastOutputTokens      int    // last known cumulative output token count
	cumulativeInput       int    // running total of input tokens for this session
	cumulativeOutput      int    // running total of output tokens for this session
	cumulativeCacheRead   int    // running total of cache-read tokens for this session
	cumulativeCacheCreate int    // running total of cache-create tokens for this session
	lastCacheReadTokens   int    // last known cumulative cache-read token count
	lastCacheCreateTokens int    // last known cumulative cache-create token count
	lastModelName         string // last seen model name (e.g. "claude-sonnet-4-20250514")

	// Lifecycle
	createdAt time.Time // time this instance was first seen
	lastName  string    // last rendered window name for dedup
	seen      bool      // true after user has focused this window in idle/blocked state
	done      bool      // true when @agentctl_done is set (hook-stop with done=1)
	prevDone  bool      // previous tick's hookDone, for done-edge detection (see detectAndFireTransitions)

	// Turn tracking
	turnCount      int   // cumulative assistant turn count
	turnScanOffset int64 // byte offset up to which we've scanned for turns
	ticket         int   // GitHub issue number from windowstate (0 if not dispatch)

	// mainPane is the tmux pane ID of the agent content pane, cached from
	// windowstate.WindowState.MainPane (see backfillMainPane for windows that
	// predate this field).
	mainPane string

	// Per-turn metrics (most recent turn only)
	lastTurnInput       int // most recent turn's input tokens
	lastTurnOutput      int // most recent turn's output tokens
	lastTurnCacheRead   int // most recent turn's cache-read tokens
	lastTurnCacheCreate int // most recent turn's cache-create tokens

	// Persistent JSONL stream reader, usage accumulator, and state tracker.
	stream         *logstream.Stream
	usage          *logstream.UsageAccumulator
	stateTracker   *logstream.StateTracker
	agentCollector *metrics.AgentCollector
	costEstimator  *metrics.CostEstimator

	// Session metadata tracking (for incremental .meta.json writes).
	firstUserMessage string          // cached first user message text (extracted once)
	firstUserMsgDone bool            // true once we've attempted first user message extraction
	sessionStartTime time.Time       // actual session start time from JSONL (read once)
	sessionStartDone bool            // true once we've read the session start time
	filesEditedSet   map[string]bool // accumulated file paths from Edit/Write tool calls
	metaLastInput    int             // cumulativeInput at the time of the last meta write
	metaLastOutput   int             // cumulativeOutput at the time of the last meta write

	// mailboxPending is a cached count of this window's undelivered mailbox
	// messages, refreshed on a throttled tick (see monitorModel.mailboxTick)
	// rather than every 500ms. Count-only: the monitor must never drain or
	// deliver mail itself -- see internal/cmd/wake.go for the one path that
	// does, and docs/agent-messaging-design.md §3 for why automatic
	// monitor-driven injection was rejected.
	mailboxPending int
}

// tickMsg is sent every 500ms to drive the monitor poll cycle.
type tickMsg struct{}

// monitorModel is the Bubble Tea model for the headless background monitor.
type monitorModel struct {
	exeStart            exeIdentity                                 // recorded identity of this binary; zero Path disables hot reload
	lastReexecTry       time.Time                                   // last re-exec attempt, to rate limit retries after failure
	execFn              func(path string, argv, env []string) error // nil = syscall.Exec
	sessionID           string                                      // tmux #{session_id} of the session this monitor serves ("" = unknown)
	runtimeDir          string                                      // runtime dir holding monitor.pid ("" disables the pidfile guard)
	windows             map[string]*instanceState
	transitionTracker   *metrics.TransitionTracker // nil when metrics disabled
	watcher             *watcher.ContentWatcher
	firstTick           bool
	snap                *state.Snapshot
	lastHeader          map[string]string             // paneID -> last rendered header (HUD dedup cache)
	lastFooter          map[string]string             // paneID -> last rendered footer (HUD dedup cache)
	chromeTick          int                           // tick counter for throttling EnforceAllChromeSizes
	reaperTick          int                           // tick counter for throttling orphan reaper (~60 s)
	registryTick        int                           // tick counter for throttling registry.Heartbeat (~5 s)
	mailboxTick         int                           // tick counter for throttling per-window mailbox.PendingCount (~5 s)
	mailboxPruneTick    int                           // tick counter for throttling mailbox.PruneStale (~5 min)
	sessionLogPruneTick int                           // tick counter for throttling logging.PruneSessionLogs (~5 min)
	projectsDir         string                        // override for ~/.claude/projects (tests); "" = default
	rateLimiter         *ratelimit.RateLimitProber    // background rate limit prober
	tokenScanner        *ratelimit.WindowTokenScanner // background window-token scanner
	metricsCollector    *metrics.Collector            // per-entry Prometheus metrics collector
	activeWindowFile    string                        // path to hook-written active-window file
	registryEntry       registry.Entry                // this session's registry entry, kept so a failed
	// Heartbeat (entry pruned while the monitor is still
	// alive) can Register it again instead of leaving
	// the session invisible to the hub forever
}

// newMonitorModel creates the initial monitor model with watcher and metrics registered.
// activeWindowFile is the path to the hook-written active-window file; pass ""
// to always fall back to tmux display-message. regEntry is this session's
// registry entry, already registered by the caller; the model keeps a copy so
// it can re-register on a failed heartbeat (see the registryTick block below).
func newMonitorModel(activeWindowFile string, regEntry registry.Entry) monitorModel {
	cfg := config.Load()

	metricsCfg := cfg.EffectiveMetricsConfig()
	// Initialise the shared registry with static labels (including auto-instance)
	// before any metrics are registered so every series carries those labels.
	metrics.InitRegistry(metricsCfg.Labels)

	var transitionTracker *metrics.TransitionTracker
	if metricsCfg.Enabled {
		transitionTracker = metrics.NewTransitionTracker(metrics.Registerer())
		logging.Log("monitor: transition tracker initialized")

		if port := metricsCfg.EffectivePort(); port > 0 {
			metrics.StartHTTPServer(port)
			logging.Log("monitor: metrics enabled on :%d", port)
		}

		if metricsCfg.HasRemoteWrite() {
			rwCfg := metrics.RemoteWriteConfig{
				Endpoint:     metricsCfg.Endpoint,
				PushInterval: metricsCfg.EffectivePushInterval(),
				Labels:       metricsCfg.Labels,
			}
			if metricsCfg.BasicAuth != nil {
				rwCfg.BasicAuth = &metrics.BasicAuth{
					Username: metricsCfg.BasicAuth.Username,
					Password: metricsCfg.BasicAuth.Password,
				}
			}
			metrics.StartRemoteWrite(rwCfg)
			logging.Log("monitor: remote write enabled → %s (interval %s)", metricsCfg.Endpoint, metricsCfg.EffectivePushInterval())
		}
	}

	cw := watcher.New()
	cw.Register(watcher.ContentRule{
		Name:    "agent-signal",
		Pattern: regexp.MustCompile(`AGENTCTL:(\w+)`),
		Action: func(m watcher.ContentMatch) {
			logging.Log("agent %s signaled: %s", m.Instance, m.Submatches[0])
		},
	})

	snap, _ := state.LoadSnapshot()

	metricsCollector := metrics.NewCollector(metrics.Registerer())

	return monitorModel{
		windows:           make(map[string]*instanceState),
		transitionTracker: transitionTracker,
		watcher:           cw,
		firstTick:         true,
		snap:              snap,
		lastHeader:        make(map[string]string),
		lastFooter:        make(map[string]string),
		rateLimiter:       ratelimit.NewRateLimitProber(),
		tokenScanner:      ratelimit.NewWindowTokenScanner(),
		metricsCollector:  metricsCollector,
		activeWindowFile:  activeWindowFile,
		registryEntry:     regEntry,
	}
}

// cmdMonitor launches the Bubble Tea monitor program in headless mode.
func cmdMonitor() {
	// Bail out immediately when running under `go test` to prevent the monitor
	// from connecting to a real tmux session and leaking chrome-bar/tickets
	// child processes that outlive the test's process-group kill.
	if os.Getenv("AGENTCTL_TEST") == "1" {
		logging.Log("monitor: AGENTCTL_TEST=1, skipping tmux operations")
		return
	}

	// Force TrueColor — monitor runs via `tmux run-shell -b` with no TTY,
	// so colorprofile auto-detects Ascii and strips all ANSI colors.
	lipgloss.Writer.Profile = colorprofile.TrueColor

	logging.Log("monitor starting")

	runtimeDir := paths.RuntimeDir()

	// Belt-and-suspenders: exit immediately if another agentctl monitor is
	// already alive for this instance. The Kill in applyConfig covers the normal
	// restart path, but rapid restarts or re-attaches can race past it. Checking
	// here guarantees at-most-one monitor per runtime directory.
	if pidfile.Validate(runtimeDir, "monitor.pid") {
		logging.Log("monitor: another agentctl monitor is already running for this instance (pidfile check), exiting")
		return
	}

	// Write a PID file so applyConfig can kill this process before relaunching
	// a replacement, preventing duplicate monitors from accumulating.
	if err := pidfile.Write(runtimeDir, "monitor.pid"); err != nil {
		logging.Log("monitor: write pidfile: %v", err)
	}
	defer pidfile.Remove(runtimeDir, "monitor.pid")

	// Register this session in the herd registry so a future central hub can
	// discover it. Best-effort: a registry problem must never take down the
	// monitor.
	now := time.Now().Unix()
	regEntry := registry.Entry{
		SessionName: tmux.SessionName,
		ProjectDir:  filepath.Dir(runtimeDir),
		RuntimeDir:  runtimeDir,
		Host:        "local",
		PID:         os.Getpid(),
		StartedAt:   now,
		LastSeen:    now,
	}
	if err := registry.Register(regEntry); err != nil {
		logging.Log("monitor: registry.Register: %v", err)
	}
	defer func() {
		if err := registry.Deregister(tmux.SessionName); err != nil {
			logging.Log("monitor: registry.Deregister: %v", err)
		}
	}()

	// Compute the path to the active-window file written by the
	// after-select-window tmux hook. Passed into the monitor model so that
	// processTick can use tmux.ActiveWindowIDFast without relying on a
	// package-level variable.
	activeWindowFile := filepath.Join(runtimeDir, "active-window")

	// Ensure the shell-prefix bootstrap script is up to date. This runs every
	// time the monitor starts (i.e. on every agentctl session launch) so the
	// script always reflects the current binary path.
	if err := hooks.EnsureShellPrefixScript(); err != nil {
		logging.Log("monitor: hooks.EnsureShellPrefixScript: %v", err)
	}

	// Run the monitor as a headless Bubble Tea program (no terminal output).
	devNull, _ := os.Open(os.DevNull)
	model := newMonitorModel(activeWindowFile, regEntry)
	model.runtimeDir = runtimeDir
	if id, ok := recordExeIdentity(runtimeDir); ok {
		model.exeStart = id
	}
	model.sessionID = lookupSessionID(tmux.SessionID, 5, 200*time.Millisecond)
	if model.sessionID == "" {
		logging.Log("monitor: WARNING could not determine tmux session id for %q; falling back to name-based liveness (replaced-session protection disabled)", tmux.SessionName)
	}
	logging.Log("monitor: serving tmux session id %q", model.sessionID)
	p := tea.NewProgram(model, tea.WithoutRenderer(), tea.WithInput(devNull))
	if _, err := p.Run(); err != nil {
		logging.Log("monitor: tea.Program error: %v", err)
	}
}

// Init starts the 500ms poll tick.
func (m monitorModel) Init() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// Update handles incoming messages.
func (m monitorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case tickMsg:
		if !m.processTick() {
			return m, tea.Quit
		}
		return m, tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })

	case tea.KeyPressMsg:
		// Headless — ignore all keyboard input.
		return m, nil
	}

	return m, nil
}

// View returns an empty string — the monitor is headless.
func (m monitorModel) View() tea.View {
	return tea.NewView("")
}

// applyVisuals is the single owner of all tmux visual output for a window:
// window renaming (with static indicator) and display writes for ctx/usage.
// It is called on every tick. Changes are deduped against last-known values
// to avoid unnecessary tmux calls.
func (m *monitorModel) applyVisuals(windowID string, inst *instanceState, curState state.AgentStatus, activeWindowID string) {
	// Window name — static indicator for working (● ), ⏸ for blocked, ✓ for done+unseen, bare name otherwise.
	// The tab bar Bubble Tea pane handles animated spinners; the window name uses a static indicator.
	var wantName string
	switch curState {
	case state.StatusWorking:
		wantName = "● " + inst.instance
	case state.StatusBlocked:
		wantName = "⏸ " + inst.instance
	default:
		if inst.done && !inst.seen {
			wantName = "✓ " + inst.instance
		} else {
			wantName = inst.instance
		}
	}
	if wantName != inst.lastName {
		logging.Debug("monitor: rename %s → %q", windowID, wantName)
		tmux.RenameWindow(windowID, wantName)
		inst.lastName = wantName
	}

	// Track seen state — mark as seen when the window is active.
	if windowID == activeWindowID {
		inst.seen = true
		inst.done = false
	}

	// Ctx% authoritative-source resolution: a fresh
	// statusline-pushed value (see internal/statusline) wins and is left
	// untouched; otherwise this monitor's own tokens.CtxPct-derived value is
	// written and becomes the new source of truth. The check and the write
	// happen inside the same windowstate.Update closure so they're atomic
	// against a statusline push landing mid-tick -- a read-then-write in two
	// steps could race a fresh push right out from under itself.
	now := time.Now()
	var wonCtxPct, wonContextWindow, wonContextTokens int
	var wonSource string
	windowstate.Update(windowID, func(ws *windowstate.WindowState) { //nolint:errcheck
		if ws.CtxFresh(now) {
			wonCtxPct = ws.CtxPct
			wonContextWindow = ws.ContextWindowSize
			wonContextTokens = ws.ContextTokens
			wonSource = "statusline"
			return
		}
		ws.CtxPct = inst.lastCtxPct
		ws.CtxPctSource = "derived"
		wonCtxPct = inst.lastCtxPct
		wonSource = "derived"
	})
	// inst.lastCtxPct is populated from whichever value won so state.json's
	// ContextPct and windowstate's CtxPct always agree -- one number, one
	// source, everywhere.
	inst.lastCtxPct = wonCtxPct
	inst.ctxPctSource = wonSource
	inst.pushedContextWindow = wonContextWindow
	inst.pushedContextTokens = wonContextTokens
}

// processTick runs one full monitor poll cycle: discovers windows, reads hook
// state, fires plugin transitions, updates metrics displays, and persists state.
// It delegates each phase to a named method for readability and testability.
//
// It returns false when the monitor should exit: the session it serves is gone
// or has been replaced by a same-named one, or another monitor owns the
// pidfile. In that case nothing is saved and no Gone transitions fire.
func (m *monitorModel) processTick() bool {
	if !sessionIdentityOK(m.sessionID, tmux.SessionID(), tmux.SessionExists) {
		logging.Log("monitor: tmux session %q gone or replaced (want id %q), exiting without saving state", tmux.SessionName, m.sessionID)
		return false
	}
	if m.runtimeDir != "" && pidfile.Validate(m.runtimeDir, "monitor.pid") {
		logging.Log("monitor: pidfile names another live monitor, exiting without saving state")
		return false
	}
	// Re-exec on a new binary; returns only if no exec happened. Same PID, so
	// the pidfile and registry entry stay valid.
	m.maybeReexec(time.Now())

	cfg := config.Load()
	windows := tmux.ListWindows()
	activeWindowID := tmux.ActiveWindowIDFast(m.activeWindowFile)
	now := time.Now().Unix()

	// Throttle counters for this tick, checked (not incremented) per-window
	// below -- incrementing once here keeps every window's mailbox refresh
	// aligned to the same tick rather than drifting relative to each other.
	m.mailboxTick++

	m.reconcileWindows(cfg, windows, now)

	for _, w := range windows {
		if w.ID == "" {
			continue
		}
		inst := m.windows[w.ID]
		if inst == nil || inst.instance == "shell" || strings.HasPrefix(inst.instance, "improve~") {
			continue
		}

		ws, _ := windowstate.Read(w.ID)
		ws = backfillMainPane(w.ID, ws, tmux.FindMainPane, windowstate.Update)
		m.linkLogs(inst, w.ID, ws, now, w.Path)
		m.updateMetrics(inst, w.ID)
		curState := m.resolveState(inst, ws)
		m.detectAndFireTransitions(inst, w.ID, curState, ws, cfg, activeWindowID)
		m.applyVisuals(w.ID, inst, curState, activeWindowID)
	}

	m.persistState(cfg, windows, activeWindowID)
	return true
}

// lookupSessionID calls lookup up to attempts times, sleeping between tries,
// and returns the first non-empty id ("" if none).
func lookupSessionID(lookup func() string, attempts int, delay time.Duration) string {
	for i := 0; i < attempts; i++ {
		if id := lookup(); id != "" {
			return id
		}
		if i < attempts-1 {
			time.Sleep(delay)
		}
	}
	return ""
}

// sessionIdentityOK reports whether the session currently named
// tmux.SessionName is the one the monitor was started for. want is the
// session_id recorded at startup; got is the current lookup ("" when the
// lookup failed or the session does not exist). When want is non-empty the ids
// must match. When want is empty (identity unknown) an empty got is not proof
// the session is gone, so it degrades to the name-based alive check.
func sessionIdentityOK(want, got string, alive func() bool) bool {
	if want != "" {
		return got == want
	}
	if got != "" {
		return true
	}
	return alive()
}

// reconcileWindows bootstraps instanceState for new windows, fires Gone
// transitions for disappeared windows, and cleans up associated resources.
func (m *monitorModel) reconcileWindows(cfg *config.Config, windows []tmux.WindowInfo, now int64) {
	// Bootstrap instanceState for windows seen for the first time.
	for _, w := range windows {
		if w.ID == "" {
			continue
		}
		if _, ok := m.windows[w.ID]; ok {
			continue
		}
		name := tmux.StripAgentPrefix(w.Name)
		inst := &instanceState{
			instance: name,
			base:     tmux.BaseAgentName(name),
		}
		if !m.firstTick {
			inst.createdAt = time.Now()
		}
		m.windows[w.ID] = inst
		if m.snap != nil {
			if saved := m.snap.ByWindowID(w.ID); saved != nil && saved.LogPath != "" {
				info, err := os.Stat(saved.LogPath)
				if err == nil && time.Since(info.ModTime()) < time.Hour {
					inst.logPath = saved.LogPath
					inst.logTTL = now + 5
					inst.lastSize = info.Size()
				}
			}
		}
	}

	// Detect disappeared windows (agent session ended) and fire Gone transitions.
	currentIDs := make(map[string]bool)
	for _, w := range windows {
		currentIDs[w.ID] = true
	}
	for id, inst := range m.windows {
		if inst.instance == "shell" || strings.HasPrefix(inst.instance, "improve~") {
			continue
		}
		if currentIDs[id] {
			continue
		}
		logging.Log("monitor: window gone instance=%s (was %s)", inst.instance, inst.prevState)
		if inst.hasPrev {
			t := state.Transition{
				Agent:    inst.base,
				WindowID: id,
				From:     inst.prevState,
				To:       state.StatusGone,
				Active:   false,
			}
			agentCfg := cfg.ForAgent(inst.base)
			m.fireHandlers(t, agentCfg, true)
		}
		// Remove the cursor entry so memory doesn't grow unboundedly as
		// agents come and go over the lifetime of a session.
		m.watcher.ResetCursor(inst.logPath)
		// Release the persistent stream reader.
		if inst.stream != nil {
			inst.stream.Close()
		}
		// Clean up the windowstate file for this window.
		windowstate.Remove(id) //nolint:errcheck
		delete(m.windows, id)
	}
}

// backfillMainPane fills in ws.MainPane for windows that already had a chrome
// bar before the main_pane field existed -- tmux.LaunchChromeBar returns
// early once ChromeBarPane is set, so those windows would otherwise never run
// the code path that populates MainPane. It is gated strictly on
// ws.MainPane == "" so it performs at most one tmux call (via findMainPane)
// per window for the lifetime of that window, not once per 500ms tick: once
// persisted, ws.MainPane is non-empty on every subsequent Read.
//
// findMainPane and update are injected (rather than calling tmux.FindMainPane
// and windowstate.Update directly) so this is unit-testable without a real
// tmux session.
func backfillMainPane(windowID string, ws windowstate.WindowState, findMainPane func(string) string, update func(string, func(*windowstate.WindowState)) error) windowstate.WindowState {
	if ws.ChromeBarPane == "" || ws.MainPane != "" {
		return ws
	}
	mainPane := findMainPane(windowID)
	if mainPane == "" {
		return ws
	}
	ws.MainPane = mainPane
	_ = update(windowID, func(wst *windowstate.WindowState) {
		wst.MainPane = mainPane
	})
	return ws
}

// claudeProjectsDir returns the directory holding Claude Code's per-project
// log directories. m.projectsDir overrides the default for tests.
func (m *monitorModel) claudeProjectsDir() string {
	if m.projectsDir != "" {
		return m.projectsDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// legacyScanDir returns the single Claude project directory a window with no
// recorded SessionID may scan: the slug of the window's worktree if it has
// one, else its pane cwd, else the agentctl project dir. Returns "" when
// nothing usable is known, in which case no scan is performed at all --
// scanning every project would link another project's log.
func (m *monitorModel) legacyScanDir(ws windowstate.WindowState, panePath string) string {
	root := m.claudeProjectsDir()
	if root == "" {
		return ""
	}
	dir := ws.WorktreeDir
	if dir == "" {
		dir = panePath
	}
	if dir == "" {
		dir = paths.ProjectDir()
	}
	slug := claude.ProjectSlug(dir)
	if slug == "" {
		return ""
	}
	return filepath.Join(root, slug)
}

// excludedLogs returns the logs already claimed by other instances of the
// same base agent.
func (m *monitorModel) excludedLogs(inst *instanceState) map[string]bool {
	exclude := make(map[string]bool)
	for _, other := range m.windows {
		if other.instance != inst.instance && other.base == inst.base && other.logPath != "" {
			exclude[other.logPath] = true
		}
	}
	return exclude
}

// resetInstanceLog drops inst's current log link and all per-session derived
// state so a new log can be linked from scratch.
func (m *monitorModel) resetInstanceLog(inst *instanceState, now int64) {
	m.watcher.ResetCursor(inst.logPath)
	inst.lastCtxPct = 0
	inst.lastInputTokens = 0
	inst.lastOutputTokens = 0
	inst.lastCacheReadTokens = 0
	inst.lastCacheCreateTokens = 0
	inst.logPath = ""
	inst.logTTL = 0
	inst.lastSize = 0
	inst.lastChange = now
	inst.firstUserMessage = ""
	inst.firstUserMsgDone = false
	inst.sessionStartTime = time.Time{}
	inst.sessionStartDone = false
	inst.filesEditedSet = nil
	inst.metaLastInput = 0
	inst.metaLastOutput = 0
}

// attachLog points inst at path, wiring up (or switching) the persistent
// stream reader and resetting session-metadata tracking for the new log.
func (m *monitorModel) attachLog(inst *instanceState, path string) {
	inst.logPath = path
	inst.lastSize = fileSize(path)
	inst.turnScanOffset = 0
	if inst.stream == nil {
		inst.usage = &logstream.UsageAccumulator{}
		inst.stateTracker = &logstream.StateTracker{}
		inst.stream = logstream.NewStream(path)
		inst.stream.Subscribe(inst.usage)
		inst.stream.Subscribe(inst.stateTracker)
		inst.agentCollector = m.metricsCollector.ForAgent(inst.base)
		inst.stream.Subscribe(inst.agentCollector)
		inst.costEstimator = m.metricsCollector.NewCostEstimator(inst.base)
		inst.stream.Subscribe(inst.costEstimator)
	} else {
		inst.stream.SetPath(path)
	}
	inst.firstUserMessage = ""
	inst.firstUserMsgDone = false
	inst.sessionStartTime = time.Time{}
	inst.sessionStartDone = false
	inst.filesEditedSet = nil
	inst.metaLastInput = 0
	inst.metaLastOutput = 0
}

// linkLogs handles log discovery, stream creation/switching, and session
// metadata reset when a new log is linked.
//
// The window's SessionID (written by the SessionStart hook, including after
// /clear) is authoritative: when it is set, no filesystem scan for "the newest
// log of this agent" is ever run, because such a scan sees every project
// under ~/.claude/projects and would link or rotate onto another project's
// session. A real /clear fires SessionStart, which rewrites
// SessionID+LogPath and is picked up by the windowstate branch below.
//
// Only windows with no SessionID (predating hooks) fall back to scanning, and
// that scan is confined to the window's own Claude project directory.
func (m *monitorModel) linkLogs(inst *instanceState, windowID string, ws windowstate.WindowState, now int64, panePath string) {
	if ws.SessionID != "" {
		// A log whose basename is not <SessionID>.jsonl is poisoned (linked
		// by an older, scan-based monitor). Drop it so it gets relinked.
		if inst.logPath != "" && filepath.Base(inst.logPath) != ws.SessionID+".jsonl" {
			logging.Log("agent %s: log %s does not match session %s, resetting", inst.instance, inst.logPath, ws.SessionID)
			m.resetInstanceLog(inst, now)
		}
	} else if inst.logPath != "" && now-inst.lastChange >= 3 {
		m.detectLegacyRotation(inst, windowID, ws, now, panePath)
	}

	// Primary source is the windowstate file (written by hooks). Checked on
	// every tick so log path changes (e.g. after /clear) are picked up
	// immediately.
	if ws.LogPath != "" && ws.LogPath != inst.logPath {
		alreadyClaimed := false
		for otherID, other := range m.windows {
			if otherID != windowID && other.logPath == ws.LogPath {
				alreadyClaimed = true
				break
			}
		}
		if alreadyClaimed {
			logging.Log("agent %s: windowstate log %s already claimed by another instance, ignoring", inst.instance, ws.LogPath)
		} else {
			m.attachLog(inst, ws.LogPath)
			logging.Log("agent %s: log linked via windowstate %s", inst.instance, ws.LogPath)
		}
	}

	if inst.logPath == "" && now > inst.logTTL {
		inst.logTTL = now + 5
		if ws.SessionID != "" {
			m.linkBySessionID(inst, windowID, ws.SessionID)
		} else {
			m.linkByScan(inst, windowID, ws, panePath)
		}
	}

	// Track file size for /clear detection (not for state detection).
	size := fileSize(inst.logPath)
	if size != inst.lastSize {
		inst.lastChange = now
		inst.lastSize = size
	}
}

// detectLegacyRotation detects /clear via log rotation for windows with no
// SessionID, looking only inside the window's own project directory.
func (m *monitorModel) detectLegacyRotation(inst *instanceState, windowID string, ws windowstate.WindowState, now int64, panePath string) {
	dir := m.legacyScanDir(ws, panePath)
	if dir == "" {
		return
	}
	latest := sessions.FindLatestLogInProject(dir, inst.base, inst.createdAt, m.excludedLogs(inst))
	if latest == "" || latest == inst.logPath {
		return
	}
	// Guard against multi-instance ping-pong: only treat as /clear if the
	// candidate log was born after the current one.
	newBirth := fileBirthTime(latest)
	oldBirth := fileBirthTime(inst.logPath)
	if !(newBirth.IsZero() || oldBirth.IsZero() || newBirth.After(oldBirth)) {
		return
	}
	logging.Log("agent %s: /clear detected via log rotation (old=%s new=%s)", inst.instance, inst.logPath, latest)
	m.resetInstanceLog(inst, now)
	// Visual resets (ctx/usage display) are applied via applyVisuals at end of tick.
	windowstate.Update(windowID, func(wst *windowstate.WindowState) { //nolint:errcheck
		wst.LogPath = ""
	})
}

// linkBySessionID derives the log path from the window's SessionID and
// backfills windowstate's LogPath if it is still empty for that session.
func (m *monitorModel) linkBySessionID(inst *instanceState, windowID, sessionID string) {
	root := m.claudeProjectsDir()
	if root == "" {
		return
	}
	path := sessions.FindLogBySessionIDIn(root, sessionID)
	if path == "" {
		return
	}
	for otherID, other := range m.windows {
		if otherID != windowID && other.logPath == path {
			return
		}
	}
	m.attachLog(inst, path)
	windowstate.Update(windowID, func(wst *windowstate.WindowState) { //nolint:errcheck
		if wst.LogPath == "" && wst.SessionID == sessionID {
			wst.LogPath = path
		}
	})
	logging.Log("agent %s: log linked via session id %s", inst.instance, path)
}

// linkByScan is the legacy fallback for windows with no SessionID, confined
// to the window's own project directory.
func (m *monitorModel) linkByScan(inst *instanceState, windowID string, ws windowstate.WindowState, panePath string) {
	dir := m.legacyScanDir(ws, panePath)
	if dir == "" {
		return
	}
	newLog := sessions.FindLatestLogInProject(dir, inst.base, inst.createdAt, m.excludedLogs(inst))
	if newLog == "" {
		return
	}
	m.attachLog(inst, newLog)
	// Cache the discovered path and session ID in windowstate so the HUD can
	// resolve the log path without waiting for the SessionStart hook. Only
	// populate empty fields -- the hook is authoritative.
	sessionID := strings.TrimSuffix(filepath.Base(newLog), ".jsonl")
	windowstate.Update(windowID, func(wst *windowstate.WindowState) { //nolint:errcheck
		if wst.LogPath == "" {
			wst.LogPath = newLog
		}
		if wst.SessionID == "" {
			wst.SessionID = sessionID
		}
	})
	logging.Log("agent %s: discovered log via scan %s", inst.instance, newLog)
}

// updateMetrics ticks the persistent log stream, copies UsageAccumulator fields
// to inst, computes ctxPct, scans log content for registered patterns, extracts
// session metadata, and writes the .meta.json sidecar when token counts change.
func (m *monitorModel) updateMetrics(inst *instanceState, windowID string) {
	// Tick the persistent stream to read any new log entries since last poll.
	// This runs before state detection so that StateTracker and UsageAccumulator
	// are up to date before resolveState and detectAndFireTransitions read them.
	if inst.stream != nil {
		inst.stream.Tick()
		inst.cumulativeInput = inst.usage.InputTokens
		inst.cumulativeOutput = inst.usage.OutputTokens
		inst.cumulativeCacheRead = inst.usage.CacheReadTokens
		inst.cumulativeCacheCreate = inst.usage.CacheCreateTokens
		inst.turnCount = inst.usage.TurnCount
		inst.lastTurnInput = inst.usage.LastTurnInput
		inst.lastTurnOutput = inst.usage.LastTurnOutput
		inst.lastTurnCacheRead = inst.usage.LastTurnCacheRead
		inst.lastTurnCacheCreate = inst.usage.LastTurnCacheCreate
		if inst.usage.ModelName != "" {
			inst.lastModelName = inst.usage.ModelName
		}
		// Cache activity detail from stateTracker for persistence to state.json.
		// inst.stateTracker is always non-nil here since it's created alongside
		// inst.stream (see log-discovery blocks above) — never read it outside
		// this guard.
		inst.lastToolDetail = inst.stateTracker.ToolDetail
		inst.lastText = inst.stateTracker.LastText
	}

	// Compute ctxPct from the cached LastTurnInput, model-aware and
	// reserve-aware via tokens.CtxPct. inst.lastTurnInput is populated by the
	// stream-tick block above when a stream is active; when the log hasn't
	// been discovered yet it stays at 0 and tokens.CtxPct correctly returns 0
	// without any nil/zero guard needed here.
	ctxPct := tokens.CtxPct(inst.lastModelName, inst.lastTurnInput)

	// Store latest metrics so applyVisuals and detectAndFireTransitions can read them.
	inst.lastCtxPct = ctxPct
	inst.lastInputTokens = inst.cumulativeInput
	inst.lastOutputTokens = inst.cumulativeOutput
	inst.lastCacheReadTokens = inst.cumulativeCacheRead
	inst.lastCacheCreateTokens = inst.cumulativeCacheCreate

	// Refresh the cached mailbox-pending count on a throttled cadence
	// (~5 s, matching registryTick's cadence) rather than every 500ms tick:
	// mailbox.PendingCount is one os.ReadDir per window, cheap in isolation
	// but unnecessary at full tick rate for a number that only matters to a
	// human glancing at the chrome bar. Count-only -- this must never drain
	// or deliver; see internal/cmd/wake.go for the one path that does.
	if m.firstTick || m.mailboxTick%10 == 0 {
		if n, err := mailbox.PendingCount(windowID); err != nil {
			logging.Debug("monitor: mailbox.PendingCount(%s): %v", windowID, err)
		} else {
			inst.mailboxPending = n
		}
	}

	// Scan log content for registered patterns (incremental — no re-read).
	m.watcher.Scan(inst.logPath, inst.base, inst.instance, windowID)

	// Incremental session metadata updates. We extract lightweight fields
	// once and accumulate tool-use data, then write a .meta.json sidecar
	// whenever token counts change. We never call ParseSummary here — that
	// is a full file scan reserved for the backfill (agentctl sessions sync).
	logPath := inst.logPath
	if logPath != "" {
		// Extract session start time once per log file.
		if !inst.sessionStartDone {
			inst.sessionStartTime = sessions.ReadStartTime(logPath)
			inst.sessionStartDone = true
		}
		// Extract first user message once per log file.
		if !inst.firstUserMsgDone {
			inst.firstUserMessage = sessions.ReadFirstUserMessage(logPath)
			inst.firstUserMsgDone = true
		}
		// Accumulate files edited from the current tool call (stateTracker tail).
		var toolName, toolDetail string
		if inst.stateTracker != nil {
			toolName = inst.stateTracker.ToolName
			toolDetail = inst.stateTracker.ToolDetail
		}
		if (toolName == "Edit" || toolName == "Write") && toolDetail != "" {
			if inst.filesEditedSet == nil {
				inst.filesEditedSet = make(map[string]bool)
			}
			inst.filesEditedSet[toolDetail] = true
		}
		// Write meta when token counts change (or on first write for this session).
		if inst.cumulativeInput != inst.metaLastInput || inst.cumulativeOutput != inst.metaLastOutput {
			meta := buildMonitorMeta(inst, logPath)
			if err := sessions.WriteMeta(meta); err != nil {
				logging.Debug("monitor: write meta for %s: %v", inst.instance, err)
			} else {
				inst.metaLastInput = inst.cumulativeInput
				inst.metaLastOutput = inst.cumulativeOutput
			}
		}
	}
}

// resolveState determines the current AgentStatus from the hook-driven
// windowstate. ws.State is the authoritative source — set by:
//
//	working  ← hook-pre-tool (PreToolUse), hook-post-tool (PostToolUse), hook-user-prompt
//	blocked  ← hook-notify (permission_prompt)
//	idle     ← hook-notify (idle_prompt), hook-stop-failure, hook-post-tool-failure (interrupt)
func (m *monitorModel) resolveState(inst *instanceState, ws windowstate.WindowState) state.AgentStatus {
	hookState := ws.State
	var curState state.AgentStatus
	switch hookState {
	case "working":
		curState = state.StatusWorking
	case "blocked":
		curState = state.StatusBlocked
	case "idle":
		curState = state.StatusIdle
	default:
		// No hook state — agent window not configured with hooks or just launched.
		// Default to idle so we don't animate a spinner with no authoritative signal.
		curState = state.StatusIdle
	}
	logging.Debug("monitor: %s hookState=%s → %s", inst.instance, hookState, curState)
	return curState
}

// detectAndFireTransitions compares the previous state to curState, constructs
// a Transition, records metrics, and fires dispatch/improve handlers on change.
// It also updates inst.prevState, inst.hasPrev, inst.done, inst.ticket, and
// inst.mainPane.
func (m *monitorModel) detectAndFireTransitions(inst *instanceState, windowID string, curState state.AgentStatus, ws windowstate.WindowState, cfg *config.Config, activeWindowID string) {
	hookState := ws.State
	hookDone := ws.Done

	// Tool/text details come from StateTracker (populated by stream tick in
	// updateMetrics). Clear tool info for idle state — only lastText is relevant.
	var toolName, toolDetail, lastText string
	if inst.stateTracker != nil {
		toolName = inst.stateTracker.ToolName
		toolDetail = inst.stateTracker.ToolDetail
		lastText = inst.stateTracker.LastText
	}
	if curState == state.StatusIdle {
		toolName = ""
		toolDetail = ""
		// Mirror the clear onto the cached copy so persistState (which reads
		// inst.lastToolDetail directly) doesn't advertise a stale tool detail
		// for an idle agent. LastText stays — it's still relevant when idle.
		inst.lastToolDetail = ""
	}

	// Track done flag from hook.
	inst.done = hookDone

	// Read dispatch-mode metadata from windowstate (set by hook-session-start).
	windowMode := ws.Mode
	windowTicketStr := ws.Ticket
	windowTicket := 0
	if windowTicketStr != "" {
		fmt.Sscanf(windowTicketStr, "%d", &windowTicket)
	}
	windowBranch := ""
	windowWorkDir := ""
	if windowMode == "dispatch" || windowMode == "adhoc" {
		windowWorkDir = tmux.PaneWorkDir(windowID)
		windowBranch = gitBranchForWorkDir(windowWorkDir)
	}
	inst.ticket = windowTicket
	inst.mainPane = ws.MainPane

	// Fire transition handlers on state changes, and also on the "done edge"
	// (hookDone flips false -> true with no accompanying state change).
	//
	// Why the done edge matters: hook-check-done writes windowstate TWICE --
	// State="idle" immediately (to stop the spinner), then Done=true seconds
	// later once the slower completion checks (git status, build) finish. The
	// monitor ticks every 500ms, so it almost always observes the state change
	// (working -> idle, Done=false) well before Done flips true. Gating
	// dispatch.HandleComplete on stateChanged alone means it is called once
	// with Done=false (an early no-op return) and never again, so
	// auto-open-PR-in-browser, dispatch label swaps, and dispatch_complete
	// hooks never fire. Firing again on the done edge closes that gap.
	//
	// doneEdge is gated on inst.hasPrev, same as stateChanged: a brand-new
	// instanceState starts with prevDone=false, so without this guard the
	// very first tick on a window that is already done=true (e.g. every
	// agent window still sitting at done+idle across a monitor restart)
	// would compute a false->true edge and fire handlers for a PR that was
	// already opened by the previous monitor process. Requiring hasPrev
	// means the first tick only establishes the prevDone baseline; only an
	// edge actually observed by this monitor process fires anything.
	stateChanged := inst.hasPrev && inst.prevState != curState
	doneEdge := inst.hasPrev && hookDone && !inst.prevDone
	if stateChanged || doneEdge {
		t := state.Transition{
			Agent:          inst.base,
			WindowID:       windowID,
			From:           inst.prevState,
			To:             curState,
			Active:         windowID == activeWindowID,
			LastToolName:   toolName,
			LastToolDetail: toolDetail,
			LastText:       lastText,
			CtxPct:         inst.lastCtxPct,
			HookHandled:    hookState != "",
			Done:           hookDone,
			Mode:           windowMode,
			TicketNumber:   windowTicket,
			Branch:         windowBranch,
			WorkDir:        windowWorkDir,
		}
		agentCfg := cfg.ForAgent(inst.base)

		// RecordTransition and improve.HandleTransition stay gated on the state
		// edge only: RecordTransition counts every transition handed to it, so
		// routing the done edge through it would double-count and corrupt the
		// Prometheus counters, and improve.HandleTransition only acts on
		// To == StatusGone (a no-op here either way) -- keeping it on the state
		// edge preserves exactly today's behavior.
		if stateChanged {
			inst.seen = false
			logging.Log("monitor: %s transitioned %s → %s", inst.instance, inst.prevState, curState)
		}
		if doneEdge && !stateChanged {
			logging.Log("monitor: %s done edge (state=%s) - firing completion handlers", inst.instance, curState)
		}
		m.fireHandlers(t, agentCfg, stateChanged)
	}
	inst.prevState = curState
	inst.hasPrev = true
	inst.prevDone = hookDone
}

// computeRateLimitState builds state.RateLimitState from the rate limit
// prober's latest info. A fresh pushed five_hour percentage
// (info.Source == "statusline") is used directly instead of the
// windowTokens/tokenCap estimate -- that estimate is off by roughly 5x for
// real usage against the hardcoded default token cap, which is exactly the
// "526% clamped to 100%" bug this fixes. WindowTokens/TokenCap are left zero
// on the pushed path since they did not produce UsagePct and reporting them
// would be misleading. tokenScanner is only ticked on the derived path;
// WindowTokenScanner.Tick is itself lazy about the pushed-fresh case, so calling it unconditionally would still be correct, just wasted.
func computeRateLimitState(cfg *config.Config, info *ratelimit.RateLimitInfo, tokenScanner *ratelimit.WindowTokenScanner) state.RateLimitState {
	rlState := state.RateLimitState{
		ResetsAt:      info.ResetsAt,
		RateLimitType: info.RateLimitType,
		Status:        info.Status,
	}

	if info.Source == "statusline" && info.FiveHourUsedPct != nil {
		rlState.Source = "statusline"
		rlState.UsagePct = int(math.Round(*info.FiveHourUsedPct))
		if info.SevenDayUsedPct != nil {
			sevenDay := *info.SevenDayUsedPct
			rlState.SevenDayPct = &sevenDay
			rlState.SevenDayResetsAt = info.SevenDayResetsAt
		}
		return rlState
	}

	rlState.Source = "derived"
	tokenCap := cfg.RateLimit.EffectiveTokenCap()
	windowStart := time.Unix(info.ResetsAt, 0).Add(-5 * time.Hour)
	windowTokens := tokenScanner.Tick(windowStart)
	usagePct := 0
	if tokenCap > 0 {
		usagePct = int(windowTokens * 100 / int64(tokenCap))
		if usagePct > 100 {
			usagePct = 100
		}
	}
	rlState.WindowTokens = windowTokens
	rlState.TokenCap = tokenCap
	rlState.UsagePct = usagePct
	return rlState
}

// gitBranchForWorkDir returns the current branch checked out in workDir, or
// "" if workDir is empty, doesn't exist, or isn't a git repo. Uses a short
// timeout since this runs on the monitor's 500ms tick loop (only called on
// state transitions, not every tick).
func gitBranchForWorkDir(workDir string) string {
	if workDir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", workDir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// persistState builds the AgentState slice from the current window map,
// deduplicates log paths, sorts by tmux window index, ticks the rate limit
// prober, writes state.json, drives HUD rendering, enforces chrome sizes
// (throttled), and reaps orphaned processes (throttled).
func (m *monitorModel) persistState(cfg *config.Config, windows []tmux.WindowInfo, activeWindowID string) {
	// Build a set of current window IDs for filtering.
	currentIDs := make(map[string]bool)
	for _, w := range windows {
		currentIDs[w.ID] = true
	}

	// Dedup logPaths before building the slice: if two instances share the same
	// logPath the second one gets its path cleared so it starts fresh on restore.
	seenPaths := make(map[string]string) // logPath -> first windowID that claimed it
	for id, inst := range m.windows {
		if inst.instance == "shell" || strings.HasPrefix(inst.instance, "improve~") || !currentIDs[id] {
			continue
		}
		if inst.logPath == "" {
			continue
		}
		if prev, dup := seenPaths[inst.logPath]; dup {
			logging.Log("state-save: duplicate logPath %s for windows %s and %s, clearing %s",
				inst.logPath, prev, id, id)
			inst.logPath = ""
			continue
		}
		seenPaths[inst.logPath] = id
	}

	// Build a position index from the ordered windows slice so we can sort by
	// tmux display order rather than by window ID string (which breaks at 10+
	// windows due to lexicographic ordering of "@3" vs "@10").
	windowIndex := make(map[string]int, len(windows))
	for i, w := range windows {
		windowIndex[w.ID] = i
	}

	logging.Debug("state-save: total windows=%d currentIDs=%d", len(m.windows), len(currentIDs))
	var agentStates []state.AgentState
	for id, inst := range m.windows {
		if inst.instance == "shell" || strings.HasPrefix(inst.instance, "improve~") || !currentIDs[id] {
			continue
		}
		sessionID := ""
		if inst.logPath != "" {
			sessionID = strings.TrimSuffix(filepath.Base(inst.logPath), ".jsonl")
		}
		workDir := tmux.PaneWorkDir(id)
		var worktreePath string
		if strings.Contains(workDir, "/.worktrees/") {
			worktreePath = workDir
		}
		var startTime int64
		if !inst.createdAt.IsZero() {
			startTime = inst.createdAt.Unix()
		}
		// Never write "working" when done=true: the agent has finished and the
		// hook may have set done before the next tick corrects the state to idle.
		// Overriding to "idle" here prevents stale spinners in chrome bars that
		// read state.json slightly later.
		stateStr := stateString(inst.prevState)
		if inst.done {
			stateStr = "idle"
		}
		// ContextWindow/ContextTokens: prefer the pushed statusline pair
		// (raw context_window_size and the same input-only usage total
		// used_percentage itself is computed from) when it's what won
		// applyVisuals' ctx% resolution this tick; otherwise fall back to
		// the model-aware, reserve-aware derived pair as usual.
		contextWindow := tokens.UsableWindow(inst.lastModelName)
		contextTokens := inst.lastTurnInput
		if inst.ctxPctSource == "statusline" && inst.pushedContextWindow > 0 {
			contextWindow = inst.pushedContextWindow
			contextTokens = inst.pushedContextTokens
		}
		backendWS, _ := windowstate.Read(id)
		agentStates = append(agentStates, state.AgentState{
			Name:                inst.instance,
			AgentName:           inst.base,
			SessionID:           sessionID,
			LogPath:             inst.logPath,
			WorkDir:             workDir,
			WindowID:            id,
			WindowIndex:         windowIndex[id],
			State:               stateStr,
			ContextPct:          inst.lastCtxPct,
			ContextTokens:       contextTokens,
			ContextWindow:       contextWindow,
			WorktreePath:        worktreePath,
			Done:                inst.done,
			InputTokens:         inst.lastInputTokens,
			OutputTokens:        inst.lastOutputTokens,
			CacheReadTokens:     inst.lastCacheReadTokens,
			CacheCreateTokens:   inst.lastCacheCreateTokens,
			ModelName:           inst.lastModelName,
			TurnCount:           inst.turnCount,
			LastTurnInput:       inst.lastTurnInput,
			LastTurnOutput:      inst.lastTurnOutput,
			LastTurnCacheRead:   inst.lastTurnCacheRead,
			LastTurnCacheCreate: inst.lastTurnCacheCreate,
			StartTime:           startTime,
			Ticket:              inst.ticket,
			LastToolDetail:      inst.lastToolDetail,
			LastText:            inst.lastText,
			MailboxPending:      inst.mailboxPending,
			Backend:             backendWS.Backend,
			MainPane:            inst.mainPane,
		})
	}
	slices.SortFunc(agentStates, func(a, b state.AgentState) int {
		return cmp.Compare(a.WindowIndex, b.WindowIndex)
	})

	// Tick the rate limit prober and compute rate limit state.
	var rlState state.RateLimitState
	if info := m.rateLimiter.Tick(); info != nil {
		rlState = computeRateLimitState(cfg, info, m.tokenScanner)
	}

	if err := state.Save(&state.State{
		Agents:         agentStates,
		Timestamp:      time.Now(),
		ActiveWindowID: activeWindowID,
		PrefixActive:   tmux.PrefixActive(),
		RateLimit:      rlState,
	}); err != nil {
		logging.Log("monitor: state.Save: %v", err)
	}

	// Drive HUD rendering in-process using the already-computed agent state,
	// eliminating the need for a separate hud-server process.
	chromebar.Tick(agentStates, m.lastHeader, m.lastFooter)

	// Enforce chrome bar pane sizes for all windows (including the shell window
	// and any window skipped by the per-agent loop above). Throttled to every
	// 5 ticks (~2.5 s) to limit tmux IPC overhead.
	m.chromeTick++
	if m.chromeTick%5 == 0 {
		tmux.EnforceAllChromeSizes()
	}

	// Reap orphaned Claude processes: on the first tick (startup sweep) and
	// then every ~60 s (120 ticks × 500 ms). Only processes referencing this
	// instance's settings directory AND provably descended from this session's
	// tmux server are ever signaled; see internal/reaper for the invariant.
	m.reaperTick++
	if m.firstTick || m.reaperTick%120 == 0 {
		go reaper.ReapOrphans(tmux.SessionName, paths.SettingsDir())
	}

	// Heartbeat this session's registry entry so the herd hub can tell we're
	// still alive. Throttled to every ~5 s (10 ticks x 500 ms) — writing a
	// file twice a second per session is exactly the kind of churn that
	// produced multi-gigabyte log files in this repo before. Best-effort:
	// a registry problem must never take down the monitor.
	m.registryTick++
	if m.firstTick || m.registryTick%10 == 0 {
		m.registryEntry = heartbeatOrReregister(m.registryEntry)
	}

	// Sweep stale pending mailbox messages: every ~5 min (600 ticks × 500 ms).
	// TTL expiry inside mailbox.DrainFor is lazy -- it only runs when a hook
	// fires for a still-live window -- so mail addressed to a window that
	// has since closed (or whose "@N" ID tmux later recycles onto an
	// unrelated session) would otherwise rot on disk forever with no hook
	// ever firing to catch it. This is a hard backstop independent of any
	// hook. Logged at Warn (not Debug) when non-zero: silent cleanup is
	// exactly how the original unbounded-backlog bug went unnoticed for
	// days (see docs/agent-messaging-design.md §3).
	m.mailboxPruneTick++
	if m.mailboxPruneTick%600 == 0 {
		go func() {
			n, err := mailbox.PruneStale(mailbox.MaxPendingAge)
			if err != nil {
				logging.Log("monitor: mailbox.PruneStale: %v", err)
				return
			}
			if n > 0 {
				logging.Warn("monitor: pruned %d stale pending mailbox message(s) older than %s", n, mailbox.MaxPendingAge)
			}
		}()
	}

	// Sweep old session log files: every ~5 min (600 ticks x 500 ms), same
	// cadence as the mailbox sweep above. Session logs get a brand-new
	// filename per session, so lumberjack's MaxAge/MaxBackups retention
	// (which is per-filename) never sees old session log files again to
	// prune them -- this is the backstop that actually bounds the
	// directory's total size and file count. protected is built from the
	// live windows this monitor already has in scope so a log file backing
	// an active session is never deleted out from under it.
	m.sessionLogPruneTick++
	if m.sessionLogPruneTick%600 == 0 {
		protected := make([]string, 0, len(m.windows))
		for windowID := range m.windows {
			ws, err := windowstate.Read(windowID)
			if err != nil || ws.LogPath == "" {
				continue
			}
			protected = append(protected, ws.LogPath)
		}
		go func() {
			n, err := logging.PruneSessionLogs(logging.MaxSessionLogAge, protected)
			if err != nil {
				logging.Log("monitor: logging.PruneSessionLogs: %v", err)
				return
			}
			if n > 0 {
				logging.Log("monitor: pruned %d stale session log file(s) older than %s", n, logging.MaxSessionLogAge)
			}
		}()
	}

	m.firstTick = false
}

// heartbeatOrReregister refreshes this session's registry entry, re-registering
// it from scratch if the heartbeat fails. A failure here means the hub pruned
// the entry (most likely because a heartbeat lagged past registry.StaleAfter —
// laptop sleep, a long GC pause, a busy tick) while this monitor is still very
// much alive. Without re-registering, every subsequent Heartbeat call would
// fail forever and the session would vanish from the hub until agentctl is
// restarted — machine sleep makes this routine, not theoretical.
// Returns the entry with LastSeen refreshed, for the caller to keep for next
// time.
func heartbeatOrReregister(entry registry.Entry) registry.Entry {
	if err := registry.Heartbeat(entry.SessionName); err == nil {
		return entry
	}
	entry.LastSeen = time.Now().Unix()
	if err := registry.Register(entry); err != nil {
		logging.Log("monitor: registry.Register (re-register after failed heartbeat): %v", err)
	}
	return entry
}

// stateString converts an AgentStatus to the lowercase string written to state.json.
func stateString(s state.AgentStatus) string {
	return s.String()
}

func fileSize(path string) int64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// buildMonitorMeta constructs a SessionMeta from the current instanceState.
// It uses accumulated incremental values — ParseSummary is never called here.
func buildMonitorMeta(inst *instanceState, logPath string) sessions.SessionMeta {
	sessionID := strings.TrimSuffix(filepath.Base(logPath), ".jsonl")
	projectEncoded := filepath.Base(filepath.Dir(logPath))
	project := "/" + projectEncoded // decodeDirName equivalent

	// Worktree detection: -.worktrees- separator in the lossy-decoded path.
	parentProject := project
	if idx := strings.Index(project, "-.worktrees-"); idx >= 0 {
		parentProject = project[:idx]
	}

	filesEdited := make([]string, 0, len(inst.filesEditedSet))
	for f := range inst.filesEditedSet {
		filesEdited = append(filesEdited, f)
	}
	slices.Sort(filesEdited)

	startTime := inst.sessionStartTime
	if startTime.IsZero() {
		startTime = inst.createdAt
	}

	agent := inst.base
	if agent == "" {
		agent = "(unknown)"
	}

	return sessions.SessionMeta{
		SchemaVersion: sessions.SchemaVersion,
		SessionID:     sessionID,
		Agent:         agent,
		Project:       project,
		ParentProject: parentProject,
		StartTime:     startTime,
		// EndTime is zero while the session is active.
		Turns:             inst.turnCount,
		InputTokens:       inst.cumulativeInput,
		OutputTokens:      inst.cumulativeOutput,
		CacheReadTokens:   inst.cumulativeCacheRead,
		CacheCreateTokens: inst.cumulativeCacheCreate,
		ModelName:         inst.lastModelName,
		FirstUserMessage:  inst.firstUserMessage,
		FilesEdited:       filesEdited,
		SourcePath:        logPath,
	}
}
