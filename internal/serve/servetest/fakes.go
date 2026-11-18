package servetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/panestate"
	"github.com/blsmth/agentctl/internal/serve"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
)

// Action is one side effect the server asked a backend for. Tests read
// them through the control API instead of watching tmux.
type Action struct {
	// Kind is close, key (answer and interrupt), reply, queue, dispatch or
	// approval.
	Kind     string `json:"kind"`
	Session  string `json:"session,omitempty"`
	Window   string `json:"window,omitempty"`
	Pane     string `json:"pane,omitempty"`
	Key      string `json:"key,omitempty"`
	Text     string `json:"text,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Dir      string `json:"dir,omitempty"`
	ID       string `json:"id,omitempty"`
	Decision string `json:"decision,omitempty"`
	Device   string `json:"device,omitempty"`
}

// world is the fake machine: the fixture fleet, its panes and the recorded
// actions. Every fake backend reads and writes it under one lock.
type world struct {
	root string
	now  func() time.Time

	mu      sync.Mutex
	groups  []Group
	actions []Action
}

func (w *world) setGroups(g []Group) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.groups = g
}

func (w *world) snapshot() []Group {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Group, len(w.groups))
	for i, g := range w.groups {
		out[i] = g
		out[i].Windows = slices.Clone(g.Windows)
	}
	return out
}

func (w *world) record(a Action) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.actions = append(w.actions, a)
}

func (w *world) recorded() []Action {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Action{}, w.actions...)
}

func (w *world) clearActions() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.actions = nil
}

// errNoWindow is returned for a window the fixture does not have.
var errNoWindow = errors.New("no such fixture window")

// update applies fn to the window with id in session.
func (w *world) update(session, id string, fn func(*Window)) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for gi := range w.groups {
		if w.groups[gi].Session != session {
			continue
		}
		for wi := range w.groups[gi].Windows {
			if w.groups[gi].Windows[wi].ID == id {
				fn(&w.groups[gi].Windows[wi])
				return nil
			}
		}
	}
	return fmt.Errorf("%w: %s:%s", errNoWindow, session, id)
}

// remove deletes a window, as killing it in tmux would.
func (w *world) remove(session, id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for gi := range w.groups {
		if w.groups[gi].Session == session {
			w.groups[gi].Windows = slices.DeleteFunc(w.groups[gi].Windows, func(x Window) bool { return x.ID == id })
		}
	}
}

// find returns the window with the given ID, or the one whose pane it is.
func (w *world) find(match func(Window) bool) (Window, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, g := range w.groups {
		for _, x := range g.Windows {
			if match(x) {
				return x, true
			}
		}
	}
	return Window{}, false
}

// Fleet implements serve.FleetSource.
func (w *world) Fleet() ([]fleetwatch.Group, error) {
	return build(w.snapshot(), w.root, w.now()), nil
}

// panes is serve.PaneSource, serve.PaneSizer and serve.MainPanes.
type panes struct{ w *world }

func (p panes) Capture(_, windowID string, _ int) (string, error) {
	win, ok := p.w.find(func(x Window) bool { return x.ID == windowID })
	if !ok {
		return "", errNoWindow
	}
	return win.paneText(), nil
}

func (p panes) Size(string) (int, int, error) { return 80, 24, nil }

func (p panes) MainPane(_, windowID, _, _ string) (string, error) {
	if _, ok := p.w.find(func(x Window) bool { return x.ID == windowID }); !ok {
		return "", errNoWindow
	}
	return paneID(windowID), nil
}

func (p panes) CapturePane(pane string, _ int) (string, error) {
	win, ok := p.w.find(func(x Window) bool { return paneID(x.ID) == pane })
	if !ok {
		return "", errNoWindow
	}
	return win.paneText(), nil
}

// keys records answers and interrupts. A key sent to a dialog dismisses it,
// so the server's post-answer settle loop returns at once. The
// AskUserQuestion screens step as Claude Code does: a digit on the
// single-select tab moves to the multi-select tab, where a digit toggles its
// checkbox and Tab moves to the review tab; any answer there (or Escape
// anywhere) ends the dialog.
type keys struct{ w *world }

func (k keys) SendKey(t serve.WindowTarget, key string) error {
	k.w.record(Action{Kind: "key", Session: t.Session, Window: t.WindowID, Pane: t.Pane, Key: key})
	_ = k.w.update(t.Session, t.WindowID, func(x *Window) { stepPane(x, key) })
	return nil
}

// stepPane applies key to the screen x shows.
func stepPane(x *Window, key string) {
	text := x.paneText()
	switch {
	case text == DialogPane || text == AskUserReviewPane || (key == "Escape" && isAskUser(x)):
		x.Pane, x.PaneText = "idle", ""
	case text == AskUserPane:
		x.Pane, x.PaneText = "askuser-multi", ""
	case isAskUserMulti(x) && key == "Tab":
		x.Pane, x.PaneText = "askuser-review", ""
	case isAskUserMulti(x):
		x.PaneText = toggleBox(text, key)
	}
}

func isAskUser(x *Window) bool {
	return x.Pane == "askuser" || x.Pane == "askuser-review" || isAskUserMulti(x)
}

func isAskUserMulti(x *Window) bool { return x.Pane == "askuser-multi" }

// toggleBox flips the checkbox of option key on a multi-select screen.
func toggleBox(pane, key string) string {
	lines := strings.Split(pane, "\n")
	for i, l := range lines {
		plain := strings.TrimLeft(panestate.StripANSI(l), " ❯")
		if !strings.HasPrefix(plain, key+". [") {
			continue
		}
		if strings.Contains(l, "[ ]") {
			lines[i] = strings.Replace(l, "[ ]", "[✔]", 1)
		} else {
			lines[i] = strings.Replace(l, "[✔]", "[ ]", 1)
		}
	}
	return strings.Join(lines, "\n")
}

type injector struct{ w *world }

func (i injector) Inject(t serve.WindowTarget, text string) error {
	i.w.record(Action{Kind: "reply", Session: t.Session, Window: t.WindowID, Pane: t.Pane, Text: text})
	return nil
}

// Queue implements serve.Queuer, so a message queued for a working agent
// is recorded as its own kind.
func (i injector) Queue(t serve.WindowTarget, text string) error {
	i.w.record(Action{Kind: "queue", Session: t.Session, Window: t.WindowID, Pane: t.Pane, Text: text})
	return nil
}

// closer records the close and drops the window from the fleet, so the bus
// reports it gone as it would after a real kill.
type closer struct{ w *world }

func (c closer) CloseWindow(t serve.WindowTarget) error {
	c.w.record(Action{Kind: "close", Session: t.Session, Window: t.WindowID})
	c.w.remove(t.Session, t.WindowID)
	return nil
}

type dispatcher struct{ w *world }

func (d dispatcher) Dispatch(req serve.DispatchRequest) (serve.DispatchResult, error) {
	d.w.record(Action{Kind: "dispatch", Session: req.Session, Agent: req.Agent, Text: req.Prompt, Dir: req.Dir})
	return serve.DispatchResult{WindowID: "@900", Session: req.Session, Branch: "agent/" + req.Agent + "/adhoc"}, nil
}

type agents struct{}

func (agents) Agents([]string) ([]serve.AgentInfo, error) {
	return []serve.AgentInfo{
		{Name: "go-engineer", Description: "Writes Go", Scope: "global"},
		{Name: "reviewer", Description: "Reviews PRs", Scope: "project"},
	}, nil
}

type launchConfig struct{}

func (launchConfig) LaunchConfig(string, string, string) (serve.LaunchConfig, error) {
	return serve.LaunchConfig{}, nil
}

func noOllama(context.Context, config.BackendConfig) ([]string, error) { return nil, nil }

// errNoTerminal: the live terminal needs tmux, which the harness never runs.
var errNoTerminal = errors.New("servetest: live terminal not available")

type terminal struct{}

func (terminal) CreateViewer(string, string) (string, error) { return "", errNoTerminal }
func (terminal) WindowSize(string) (int, int, error)         { return 80, 24, nil }
func (terminal) KillViewer(string) error                     { return nil }
func (terminal) Viewers() ([]string, error)                  { return nil, nil }

type ptys struct{}

func (ptys) Start(string, []string, int, int) (serve.PTY, error) { return nil, errNoTerminal }

type holder struct{}

func (holder) Start() error { return nil }
func (holder) Stop()        {}

// pusher swallows Web Push: no request leaves the machine.
type pusher struct{}

func (pusher) Push(context.Context, serve.Subscription, []byte, serve.PushOptions) (int, error) {
	return 201, nil
}
