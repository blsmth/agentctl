// Package hubtarget makes agentctl's server-global prefix keys act on the
// agent being viewed in the herd hub, not on the hub window itself.
//
// In the hub (session agentctl-hub, window herd-list) every binding resolves
// #{window_id} and the session environment to the hub. The binding sets
// AGENTCTL_KEY_WINDOW=#{window_id} (tmux.KeyWindowEnv); Apply, called once
// from main before paths are resolved, asks tmux whether that window is the
// hub and, if so, rewrites the invocation (window-id argument, runtime dir env)
// to the agent shown in the live viewer. Nothing is done per command.
package hubtarget

import (
	"fmt"
	"os"

	"github.com/blsmth/agentctl/internal/tmux"
)

// Scope classifies how a keybinding subcommand relates to the hub.
type Scope int

const (
	// ScopeOther commands are not redirected (cross-project or global).
	ScopeOther Scope = iota
	// ScopeWindow commands act on one window (their window-id argument).
	ScopeWindow
	// ScopeProject commands act on a project session.
	ScopeProject
	// ScopeHubQuit is `q`: in the hub it closes the hub.
	ScopeHubQuit
	// ScopeHubRefused commands are refused in the hub.
	ScopeHubRefused
)

var scopes = map[string]Scope{
	"kill-menu":         ScopeWindow,
	"logs":              ScopeWindow,
	"settings-menu":     ScopeWindow,
	"improve-menu":      ScopeWindow,
	"hud-toggle":        ScopeWindow,
	"hud":               ScopeWindow,
	"context-push-menu": ScopeWindow,

	"menu":                 ScopeProject,
	"launch":               ScopeProject,
	"create-menu":          ScopeProject,
	"ticket-launch":        ScopeProject,
	"ticket-menu":          ScopeProject,
	"permissions-menu":     ScopeProject,
	"open-tickets-window":  ScopeProject,
	"open-sessions-window": ScopeProject,
	"open-agents-window":   ScopeProject,

	"quit-menu":     ScopeHubQuit,
	"restart-menu":  ScopeHubRefused,
	"kill-all-menu": ScopeHubRefused,
}

// Classify returns the scope of an agentctl subcommand.
func Classify(subcmd string) Scope { return scopes[subcmd] }

// Action is what Apply decided to do.
type Action int

const (
	// ActionNone: not in the hub, or a command the hub does not touch.
	ActionNone Action = iota
	// ActionRedirect: run as if invoked from the viewed agent.
	ActionRedirect
	// ActionRefuse: show Message and do nothing.
	ActionRefuse
	// ActionHubQuit: close the hub (return to the previous session).
	ActionHubQuit
)

// NoViewerMessage is shown when a hub key needs a viewed agent and none exists.
const NoViewerMessage = "select an agent in the hub first"

// RefusedMessage is shown for keys that make no sense in the hub.
const RefusedMessage = "not available in the hub; open the project session"

// Facts are the inputs to Resolve. Viewer* are empty when no agent is viewed.
type Facts struct {
	Subcmd        string
	KeySessionHub bool   // the key was pressed in the hub session
	ViewedWindow  string // active window id of the current viewer session
	HomeSession   string // home session of that window
	HomeRuntime   string // AGENTCTL_RUNTIME_DIR of the home session
}

// Decision is Resolve's verdict.
type Decision struct {
	Action     Action
	Window     string // replacement window id (window-scoped redirects)
	RuntimeDir string // AGENTCTL_RUNTIME_DIR to run under
	Message    string // for ActionRefuse
}

// Resolve is the pure core: given the facts, decide what to do.
func Resolve(f Facts) Decision {
	if !f.KeySessionHub {
		return Decision{}
	}
	scope := Classify(f.Subcmd)
	switch scope {
	case ScopeHubQuit:
		return Decision{Action: ActionHubQuit}
	case ScopeHubRefused:
		return Decision{Action: ActionRefuse, Message: RefusedMessage}
	case ScopeWindow, ScopeProject:
		if f.ViewedWindow == "" || f.HomeSession == "" || f.HomeRuntime == "" {
			return Decision{Action: ActionRefuse, Message: NoViewerMessage}
		}
		d := Decision{Action: ActionRedirect, RuntimeDir: f.HomeRuntime}
		if scope == ScopeWindow {
			d.Window = f.ViewedWindow
		}
		return d
	}
	return Decision{}
}

// RewriteArgs replaces every argument equal to hubWindow with d.Window.
func RewriteArgs(args []string, hubWindow string, d Decision) []string {
	out := make([]string, len(args))
	copy(out, args)
	if d.Window == "" {
		return out
	}
	for i, a := range out {
		if a == hubWindow {
			out[i] = d.Window
		}
	}
	return out
}

// Apply inspects a keybinding-spawned invocation (AGENTCTL_KEY_WINDOW set)
// and, when it came from the hub, redirects it. It must run before
// paths.Init. It returns the (possibly rewritten) args and whether the
// process should exit immediately (refused or fully handled).
func Apply(args []string) (newArgs []string, done bool) {
	keyWin := os.Getenv(tmux.KeyWindowEnv)
	if keyWin == "" || len(args) < 2 {
		return args, false
	}
	_ = os.Unsetenv(tmux.KeyWindowEnv)
	if Classify(args[1]) == ScopeOther || tmux.SessionOfWindow(keyWin) != tmux.HubSessionName {
		return args, false
	}
	v := tmux.HubViewedAgent()
	d := Resolve(Facts{
		Subcmd:        args[1],
		KeySessionHub: true,
		ViewedWindow:  v.Window,
		HomeSession:   v.Home,
		HomeRuntime:   v.RuntimeDir,
	})
	switch d.Action {
	case ActionRefuse:
		tmux.DisplayMessage(d.Message)
		return args, true
	case ActionHubQuit:
		if err := tmux.SwitchToLastSession(); err != nil {
			tmux.DisplayMessage(fmt.Sprintf("leaving hub: %v", err))
		}
		return args, true
	case ActionRedirect:
		_ = os.Setenv("AGENTCTL_RUNTIME_DIR", d.RuntimeDir)
		// Identity vars of the hub must not leak into the viewed project.
		_ = os.Unsetenv("AGENTCTL_WINDOW_ID")
		_ = os.Unsetenv("AGENTCTL_SESSION_ID")
		return RewriteArgs(args, keyWin, d), false
	}
	return args, false
}
