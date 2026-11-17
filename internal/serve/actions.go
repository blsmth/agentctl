package serve

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
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
}

// KeySender sends one tmux key name to a window's active pane (the pane
// PaneSource captures).
type KeySender interface {
	SendKey(t WindowTarget, key string) error
}

// Injector types text into a window's main pane and submits it, through the
// guarded tmux inject path (dialog and long-paste checks included).
type Injector interface {
	Inject(t WindowTarget, text string) error
}

// WindowCloser kills a window.
type WindowCloser interface {
	CloseWindow(t WindowTarget) error
}

// Dispatcher launches an ad-hoc agent, as `agentctl dispatch --agent A
// --prompt P --dir D` does.
type Dispatcher interface {
	Dispatch(req DispatchRequest) (DispatchResult, error)
}

// DispatchRequest is a validated POST /v1/dispatch, with the target session
// resolved from the fleet.
type DispatchRequest struct {
	Agent      string
	Prompt     string
	Model      string
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
// loading, agent discovery) take the read lock.
var globalsMu sync.RWMutex

// errBadTarget is returned when a fleet group does not look like a session
// serve may act on.
var errBadTarget = errors.New("invalid session target")

// withSession runs fn with the process-global session state (paths runtime
// dir, windowstate dir, config project layer, tmux.SessionName) pointed at
// one session, then restores it. tmux's inject, kill and launch helpers and
// dispatch.Launch all target that global state; serve, the one process
// acting on many sessions, binds it per action. Calls are serialized.
func withSession(session, runtimeDir string, fn func() error) error {
	if session == "" || strings.ContainsAny(session, ":.") ||
		!filepath.IsAbs(runtimeDir) || filepath.Base(runtimeDir) != ".agentctl" {
		return fmt.Errorf("%w: session %q, runtime dir %q", errBadTarget, session, runtimeDir)
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

// lockedFleet reads the fleet under globalsMu's read lock: herd loading may
// consult config, which a concurrent withSession rebinds.
type lockedFleet struct{ src FleetSource }

func (l lockedFleet) Fleet() ([]fleetwatch.Group, error) {
	globalsMu.RLock()
	defer globalsMu.RUnlock()
	return l.src.Fleet()
}

type tmuxKeys struct{}

func (tmuxKeys) SendKey(t WindowTarget, key string) error {
	return tmux.SendKeyToWindow(t.Session, t.WindowID, key)
}

type tmuxInjector struct{}

func (tmuxInjector) Inject(t WindowTarget, text string) error {
	return withSession(t.Session, t.RuntimeDir, func() error {
		return tmux.InjectPromptToWindow(t.WindowID, text)
	})
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
type launchDispatcher struct{}

func (launchDispatcher) Dispatch(req DispatchRequest) (DispatchResult, error) {
	var out DispatchResult
	err := withSession(req.Session, req.RuntimeDir, func() error {
		// The CLI refuses a planted or tracked runtime dir before writing
		// anything (main's CheckRuntimeDir); so does this path.
		if err := paths.CheckRuntimeDir(true); err != nil {
			return err
		}
		if err := dispatch.ValidateAgentName(req.Agent, req.Dir); err != nil {
			return err
		}
		res, err := dispatch.LaunchAdhoc(dispatch.AdhocRequest{
			Prompt:      req.Prompt,
			AgentName:   req.Agent,
			WorkDir:     req.Dir,
			Model:       req.Model,
			UseWorktree: true,
			Detached:    true,
		})
		if err != nil {
			return err
		}
		out = DispatchResult{WindowID: res.WindowID, Session: req.Session, Branch: res.Branch}
		return nil
	})
	return out, err
}
