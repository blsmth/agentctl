package serve

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// WindowTarget is a window resolved through the fleet. Write handlers build
// it only from a fleetwatch.Find result, never from request text, so no
// arbitrary tmux target reaches a tmux call.
type WindowTarget struct {
	Session    string
	RuntimeDir string
	WindowID   string
	// Pane is the window's main (agent) pane, resolved through MainPanes:
	// the pane a write captured and checked, and the one it types into.
	Pane string
}

// KeySender sends one tmux key name to a window's main pane (t.Pane).
type KeySender interface {
	SendKey(t WindowTarget, key string) error
}

// Injector types text into a window's main pane (t.Pane) and submits it,
// through the guarded tmux inject path (dialog and long-paste checks
// included).
type Injector interface {
	Inject(t WindowTarget, text string) error
}

// Queuer is optionally implemented by an Injector that delivers a message
// queued for a working agent differently from a reply. The tmux injector
// does not: typing mid-turn is how Claude Code queues input, and the guarded
// inject path already accepts a busy pane.
type Queuer interface {
	Queue(t WindowTarget, text string) error
}

// WindowCloser kills a window.
type WindowCloser interface {
	CloseWindow(t WindowTarget) error
}

// MainPanes finds and captures a window's main pane: the pane that is
// neither the chrome bar nor the HUD, the one Claude Code runs in. Writes
// check this pane, not the window's active pane, because that is where they
// type.
type MainPanes interface {
	// MainPane returns the pane ID ("%12") of session:windowID's main pane,
	// given the window's recorded chrome bar and HUD panes.
	MainPane(session, windowID, chromeBarPane, hudPane string) (string, error)
	// CapturePane captures one pane by ID, with ANSI escapes.
	CapturePane(paneID string, lines int) (string, error)
}

// Dispatcher launches an ad-hoc agent, as `agentctl dispatch --agent A
// --prompt P --dir D` does.
type Dispatcher interface {
	Dispatch(req DispatchRequest) (DispatchResult, error)
}

// DispatchRequest is a validated POST /v1/dispatch, with the target session
// resolved from the fleet.
type DispatchRequest struct {
	Agent  string
	Prompt string
	Model  string
	// Backend and Effort are the launch overrides, already resolved the way
	// the launch popup does ("" = agent config).
	Backend    string
	Effort     string
	Dir        string
	Session    string
	RuntimeDir string
}

// DispatchResult is the POST /v1/dispatch response.
type DispatchResult struct {
	WindowID string `json:"window_id"`
	Session  string `json:"session"`
	Branch   string `json:"branch"`
}

// globalsMu guards the process-global session state that tmux, windowstate,
// config and paths derive from one runtime dir (see withSession). Writers
// bind a session under the write lock; serve's readers of that state (fleet
// loading, agent discovery) take the read lock. A waiting writer blocks new
// readers, so nothing slow (network, git) may run under the write lock.
var globalsMu sync.RWMutex

// errBadTarget is returned when a fleet group does not look like a session
// serve may act on.
var errBadTarget = errors.New("invalid session target")

// withSession runs fn with the process-global session state (paths runtime
// dir, windowstate dir, config project layer, tmux.SessionName) pointed at
// one session, then restores it. tmux's kill and launch helpers and
// dispatch.Launch target that global state; serve, the one process acting on
// many sessions, binds it per action. Calls are serialized.
func withSession(session, runtimeDir string, fn func() error) error {
	if err := checkSessionTarget(session, runtimeDir); err != nil {
		return err
	}
	globalsMu.Lock()
	defer globalsMu.Unlock()

	prevSession := tmux.SessionName
	restore := paths.Bind(runtimeDir)
	tmux.SessionName = session
	windowstate.Init()
	cfgErr := config.Init()
	defer func() {
		restore()
		tmux.SessionName = prevSession
		windowstate.Init()
		_ = config.Init()
	}()
	if cfgErr != nil {
		return cfgErr
	}
	return fn()
}

// checkSessionTarget rejects a fleet group that does not look like a
// session serve may act on.
func checkSessionTarget(session, runtimeDir string) error {
	if session == "" || strings.ContainsAny(session, ":.") ||
		!filepath.IsAbs(runtimeDir) || filepath.Base(runtimeDir) != ".agentctl" {
		return fmt.Errorf("%w: session %q, runtime dir %q", errBadTarget, session, runtimeDir)
	}
	return nil
}

// lockedFleet reads the fleet under globalsMu's read lock: fleet loading may
// consult config, which a concurrent withSession rebinds.
type lockedFleet struct{ src FleetSource }

func (l lockedFleet) Fleet() ([]fleetwatch.Group, error) {
	globalsMu.RLock()
	defer globalsMu.RUnlock()
	return l.src.Fleet()
}

type tmuxMainPanes struct{}

func (tmuxMainPanes) MainPane(session, windowID, chromeBarPane, hudPane string) (string, error) {
	return tmux.MainPaneOf(session, windowID, chromeBarPane, hudPane)
}

func (tmuxMainPanes) CapturePane(paneID string, lines int) (string, error) {
	return tmux.CapturePaneByIDANSI(paneID, lines)
}

type tmuxKeys struct{}

func (tmuxKeys) SendKey(t WindowTarget, key string) error {
	return tmux.SendKeyToPane(t.Pane, key)
}

// tmuxInjector types into the resolved main pane. The inject path is
// pane-addressed and reads no session globals, so it does not take
// globalsMu.
type tmuxInjector struct{}

func (tmuxInjector) Inject(t WindowTarget, text string) error {
	return tmux.InjectPromptToPane(t.Pane, text)
}

type tmuxCloser struct{}

func (tmuxCloser) CloseWindow(t WindowTarget) error {
	return withSession(t.Session, t.RuntimeDir, func() error {
		return tmux.KillWindow(t.WindowID)
	})
}

// launchDispatcher runs the CLI's ad-hoc dispatch core in process, inside
// the target session. The window is Detached: it records no spawned_by, since
// the dispatcher is a phone, not a window.
//
// Everything slow runs before the session is bound: the GitHub repo lookup
// (`gh repo view`, network, bounded by dispatch's own timeout) and the
// runtime dir git check. Only agent validation and the launch itself (which
// creates the worktree) run under globalsMu, so a hung gh never stalls the
// fleet readers, SSE and push.
type launchDispatcher struct {
	repoSlug        func(dir string) (string, error)
	checkRuntimeDir func(runtimeDir string) error
	validateAgent   func(name, dir string) error
	launch          func(req dispatch.AdhocRequest) (dispatch.Result, error)
}

func newLaunchDispatcher() launchDispatcher {
	return launchDispatcher{
		repoSlug:        dispatch.RepoSlug,
		checkRuntimeDir: func(dir string) error { return paths.CheckRuntimeDirAt(dir, true) },
		validateAgent:   dispatch.ValidateAgentName,
		launch:          dispatch.LaunchAdhoc,
	}
}

func (d launchDispatcher) Dispatch(req DispatchRequest) (DispatchResult, error) {
	if err := checkSessionTarget(req.Session, req.RuntimeDir); err != nil {
		return DispatchResult{}, err
	}
	// The CLI refuses a planted or tracked runtime dir before writing
	// anything (main's CheckRuntimeDir); so does this path.
	if err := d.checkRuntimeDir(req.RuntimeDir); err != nil {
		return DispatchResult{}, err
	}
	// For ad-hoc dispatch the slug is informational; a directory with no
	// GitHub repo is legitimate (see dispatch.LaunchAdhoc).
	slug, err := d.repoSlug(req.Dir)
	if err != nil {
		logging.Warn("serve: dispatch: no GitHub repo for %s, continuing without repo slug: %v", req.Dir, err)
		slug = ""
	}

	var out DispatchResult
	err = withSession(req.Session, req.RuntimeDir, func() error {
		if err := d.validateAgent(req.Agent, req.Dir); err != nil {
			return err
		}
		res, err := d.launch(dispatch.AdhocRequest{
			Prompt:        req.Prompt,
			AgentName:     req.Agent,
			WorkDir:       req.Dir,
			Model:         req.Model,
			UseWorktree:   true,
			Detached:      true,
			RepoSlug:      slug,
			RepoSlugKnown: true,
		})
		if err != nil {
			return err
		}
		out = DispatchResult{WindowID: res.WindowID, Session: req.Session, Branch: res.Branch}
		return nil
	})
	return out, err
}
