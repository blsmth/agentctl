// Package servetest runs the real `agentctl serve` HTTP application (Router,
// middleware, embedded web assets) over fake backends, for browser tests of
// the phone web app. Nothing here touches tmux, the real state dirs or the
// serve control socket: the fleet is a fixture, pane captures are canned,
// and every write action is recorded instead of performed.
//
// The device is paired for real (a DeviceStore entry in the harness's own
// state dir), so requests still pass the device-cookie middleware.
//
// The package is imported only by tests and by the devserver command; it is
// never linked into the agentctl binary.
package servetest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/serve"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
)

// ControlPrefix is where the control API is mounted, beside the app.
const ControlPrefix = "/__test/"

// DeviceName is the pre-paired device.
const DeviceName = "e2e-phone"

// Options configures a Harness.
type Options struct {
	// Dir is the serve state dir (devices.json, VAPID keys, audit log). It
	// must be a fresh temp dir.
	Dir string
	// ReadOnly turns serve.allow_write off.
	ReadOnly bool
	// Rediscover is how often the fleet bus re-reads the fixture, which is
	// how fixture edits become SSE events; 0 means 50ms.
	Rediscover time.Duration
	// Fleet is the initial fixture, and what Reset returns to; nil means
	// DefaultFixture.
	Fleet []Group
}

// Harness is a running serve.Server over fakes plus its control API.
type Harness struct {
	// Server is the real application.
	Server *serve.Server
	// Token is the paired device's cookie value.
	Token string

	world      *world
	initial    []Group
	handler    http.Handler
	rediscover time.Duration
	cancel     context.CancelFunc

	apMu      sync.Mutex
	approvals map[string]net.Conn

	scheds    *schedules
	artifacts *artifactWorld
}

// New builds and starts a Harness on the default fixture.
func New(opts Options) (*Harness, error) {
	if opts.Dir == "" {
		return nil, errors.New("servetest: Options.Dir is required")
	}
	if opts.Rediscover <= 0 {
		opts.Rediscover = 50 * time.Millisecond
	}
	devices, err := serve.OpenDeviceStore(opts.Dir)
	if err != nil {
		return nil, fmt.Errorf("opening device store: %w", err)
	}
	_, token, err := devices.Add(DeviceName, time.Now())
	if err != nil {
		return nil, fmt.Errorf("pairing test device: %w", err)
	}
	ca, err := serve.LoadOrCreateCA(opts.Dir, "servetest", nil, time.Now())
	if err != nil {
		return nil, fmt.Errorf("creating CA: %w", err)
	}

	w := &world{root: opts.Dir, now: time.Now, orderPath: filepath.Join(opts.Dir, "group-order.json")}
	initial := opts.Fleet
	if initial == nil {
		initial = DefaultFixture()
	} else if err := validate(initial); err != nil {
		return nil, fmt.Errorf("servetest: invalid Options.Fleet: %w", err)
	}
	w.setGroups(cloneGroups(initial))
	off := false
	p := panes{w}
	scheds := newSchedules(opts.Dir)
	arts := newArtifactWorld(opts.Dir)
	s, err := serve.New(serve.Options{
		Config: config.ServeConfig{
			AllowCIDRs: []string{"127.0.0.0/8", "::1/128"},
			AllowWrite: !opts.ReadOnly,
			KeepAwake:  &off,
		},
		Dir: opts.Dir, Version: "servetest", CA: ca, GroupOrderPath: func() (string, error) { return w.orderPath, nil },
		AdvertiseHost: "127.0.0.1", HTTPSPort: 8443,
		Fleet: w, Panes: p, MainPanes: p, Agents: agents{}, Awake: holder{},
		Keys: keys{w}, Injector: injector{w}, Closer: closer{w}, Children: noChildren{}, Dispatcher: dispatcher{w}, Shells: shells{w}, Sweep: sweeper{w}.run,
		LaunchConfig: launchConfig{}, OllamaModels: noOllama,
		Terminal: terminal{}, PTY: ptys{},
		Pusher: pusher{}, NtfyBase: "http://127.0.0.1:1",
		BusOptions: fleetwatch.Options{Debounce: 10 * time.Millisecond, Rediscover: opts.Rediscover},
		PingEvery:  5 * time.Second,
		Artifacts:  arts.store, ArtifactDebounce: 10 * time.Millisecond, ArtifactPoll: time.Second,
		Schedules: scheds.store(), RunSchedule: scheds.run, AgentPermissionMode: func(string) string { return "" },
	})
	if err != nil {
		return nil, fmt.Errorf("building server: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	h := &Harness{Server: s, Token: token, world: w, initial: initial, cancel: cancel, rediscover: opts.Rediscover, approvals: map[string]net.Conn{}, scheds: scheds, artifacts: arts}
	mux := http.NewServeMux()
	h.registerControl(mux)
	mux.Handle("/", s.Handler())
	h.handler = loopbackOnly(mux)
	return h, nil
}

// Handler is the app plus the control API under ControlPrefix.
func (h *Harness) Handler() http.Handler { return h.handler }

// Close stops the server and drops open approvals.
func (h *Harness) Close() {
	h.closeApprovals()
	h.cancel()
	h.Server.Close()
}

// Actions returns everything recorded since the last reset.
func (h *Harness) Actions() []Action { return h.world.recorded() }

// SetFleet replaces the fixture; the bus publishes the difference.
func (h *Harness) SetFleet(groups []Group) error {
	if err := validate(groups); err != nil {
		return err
	}
	h.world.setGroups(groups)
	return nil
}

// Reset restores the default fixture, clears recorded actions and drops
// open approvals. It returns once the bus has re-read the fixture often
// enough to publish every resulting change (a gone window takes two
// refreshes), so a page loaded afterwards gets no stray events from the
// previous test mid-gesture.
func (h *Harness) Reset() {
	h.closeApprovals()
	_ = os.Remove(h.world.orderPath) // forget a reorder made by the last test
	h.world.setGroups(cloneGroups(h.initial))
	h.world.clearActions()
	h.Server.ResetWriteLimits()
	_ = h.scheds.set(SchedulesSpec{})
	_ = h.artifacts.clear()
	time.Sleep(3*h.rediscover + 50*time.Millisecond)
}

// ApprovalSpec opens a remote approval as an approval hook would.
type ApprovalSpec struct {
	Kind        string `json:"kind"` // bash (default) or dispatch
	AskingAgent string `json:"asking_agent"`
	Session     string `json:"session"`
	WindowID    string `json:"window_id"`
	TargetAgent string `json:"target_agent"`
	Prompt      string `json:"prompt"`
	Command     string `json:"command"`
	// Details is a dispatch's where/how block.
	Details remoteapproval.Details `json:"details"`
}

// OpenApproval opens an approval on an in-memory connection, as the hook
// does over the control socket, and records the decision serve writes back.
// It returns once the approval is listed.
func (h *Harness) OpenApproval(spec ApprovalSpec) (string, error) {
	id, err := remoteapproval.NewID()
	if err != nil {
		return "", err
	}
	if spec.Kind == "" {
		spec.Kind = remoteapproval.KindBash
	}
	req := remoteapproval.Request{
		Op: remoteapproval.OpRequest, ID: id, Kind: spec.Kind, AskingAgent: spec.AskingAgent,
		Session: spec.Session, WindowID: spec.WindowID, TargetAgent: spec.TargetAgent,
		Prompt: spec.Prompt, Command: spec.Command, Details: spec.Details, Deadline: time.Now().Add(10 * time.Minute),
	}
	if err := req.Validate(); err != nil {
		return "", err
	}
	srv, hook := net.Pipe()
	h.apMu.Lock()
	h.approvals[id] = hook
	h.apMu.Unlock()
	go h.Server.ServeApproval(srv, bufio.NewReader(srv), req)
	go func() {
		line, err := remoteapproval.ReadLine(bufio.NewReader(hook))
		if err != nil {
			return
		}
		var d remoteapproval.Decision
		if json.Unmarshal(line, &d) == nil {
			h.world.record(Action{Kind: "approval", Session: req.Session, Window: req.WindowID, ID: id, Decision: d.Decision, Device: d.Device})
		}
	}()
	if err := h.waitListed(id); err != nil {
		return "", err
	}
	return id, nil
}

// waitListed polls GET /v1/approvals (through the real handler) until id
// shows up: ServeApproval registers it asynchronously.
func (h *Harness) waitListed(id string) error {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r := httptest.NewRequest(http.MethodGet, "/v1/approvals", nil)
		r.RemoteAddr = "127.0.0.1:1"
		r.AddCookie(&http.Cookie{Name: serve.DeviceCookie, Value: h.Token})
		rec := httptest.NewRecorder()
		h.Server.Handler().ServeHTTP(rec, r)
		if strings.Contains(rec.Body.String(), id) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("approval %s was not listed", id)
}

func (h *Harness) closeApprovals() {
	h.apMu.Lock()
	defer h.apMu.Unlock()
	for id, c := range h.approvals {
		_ = c.Close()
		delete(h.approvals, id)
	}
}

// loopbackOnly refuses anything not from a loopback address, whatever the
// listener: the control API has no authentication.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			http.Error(w, "servetest: loopback only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cloneGroups copies groups deeply enough that the world's in-place window
// updates never reach the original (the initial fixture Reset returns to).
func cloneGroups(groups []Group) []Group {
	out := make([]Group, len(groups))
	for i, g := range groups {
		out[i] = g
		out[i].Windows = slices.Clone(g.Windows)
		out[i].Shells = slices.Clone(g.Shells)
	}
	return out
}
