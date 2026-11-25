// Package serve implements `agentctl serve`: a per-machine HTTPS server that
// the phone web app talks to. See docs/design/mobile-web-app.md.
//
// Request pipeline (Server.chain): source-CIDR filter, security headers,
// CSRF header check for non-GET methods, then the Router, which applies
// per-route device-cookie auth and the allow_write gate.
package serve

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/buildid"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/phoneview"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
	"github.com/blsmth/agentctl/internal/sweeprun"
	"github.com/blsmth/agentctl/internal/tmux"
)

// Options configures a Server.
type Options struct {
	// Config is the global serve block.
	Config config.ServeConfig
	// Dir is the serve state dir (<StateDir>/serve).
	Dir string
	// Version is reported by /healthz and `serve status`.
	Version string
	// CA is the local CA; pairing serves it as a configuration profile.
	CA *CA
	// AdvertiseHost is the host (usually an IP in the certificate) put into
	// the pairing URLs.
	AdvertiseHost string
	// HTTPSPort is the port the HTTPS listener is bound to.
	HTTPSPort int
	// BootstrapAddr is where the plain-HTTP pairing listener binds while a
	// pairing window is open. Empty disables it.
	BootstrapAddr string
	// Public is the optional publicly trusted certificate for
	// serve.public_hostname; nil means the feature is off. While it holds a
	// valid certificate, advertised URLs (status, artifact links, ntfy
	// Click) use its hostname; pairing URLs always use AdvertiseHost.
	Public *PublicCert
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	// Read API dependencies; nil means the production implementation (fleet,
	// tmux, agent discovery, caffeinate on darwin). Tests inject fakes.
	Fleet  FleetSource
	Panes  PaneSource
	Agents AgentLister
	// HandleIndex is the session index behind /v1/handles/{handle} and the
	// schedule run handles; nil means fleet.LoadHandleIndex (no tmux).
	HandleIndex func() *fleet.Index
	Awake       Holder
	// Host collects machine stats for GET /v1/host; nil means gopsutil.
	// TmuxWindows counts tmux windows; nil means tmux.ListWindowsErr.
	Host        HostSource
	TmuxWindows func() (int, error)
	// Write API dependencies; nil means tmux and in-process dispatch.
	Keys       KeySender
	Injector   Injector
	Closer     WindowCloser
	Dispatcher Dispatcher
	// Children finds and closes a window's sub-sessions before the window
	// itself is closed; nil means tmux and the close_children config.
	Children ChildCloser
	// Shells opens and recognises plain shell windows; nil means tmux.
	Shells Shells
	// Sweep runs `agentctl sweep` for a session; nil means the
	// sweeprun.Run subprocess. Tests inject a fake.
	Sweep sweeprun.Func
	// Schedules is the cron editing core for /v1/schedules; nil means the
	// real jobs file and state dir (cronedit.Default). Its ValidateAgent and
	// Windows seams, when nil, are filled from Agents and Fleet.
	Schedules *cronedit.Store
	// RunSchedule fires a job now (plain `cron run`); nil means the
	// cronedit.RunSubprocess subprocess. Tests inject a fake.
	RunSchedule cronedit.RunFunc
	// AgentPermissionMode returns the permission_mode configured for an
	// agent ("" = none); a cron fire resolves its launch mode from it. nil
	// reads the global config.
	AgentPermissionMode func(agent string) string
	// MainPanes finds and captures the pane a window write checks and types
	// into; nil means tmux.
	MainPanes MainPanes
	// LaunchConfig reads launch defaults for GET /v1/launch-options and
	// dispatch validation; nil means the real config. OllamaModels lists a
	// backend's live models; nil means dispatch.ListOllamaModels.
	LaunchConfig LaunchConfigSource
	OllamaModels OllamaLister
	// Live terminal dependencies; nil means tmux viewer sessions and
	// creack/pty. TerminalSizeEvery is the window-size poll; 0 means 2s.
	Terminal          TerminalTmux
	PTY               PTYStarter
	TerminalSizeEvery time.Duration
	// PhoneViewDir is where live terminals record which windows the phone is
	// watching (internal/phoneview); empty means <StateDir>/phone-viewed.
	PhoneViewDir string
	// GroupOrderPath resolves the saved project group order's path (see
	// fleet.GroupOrderPath); nil means the real one. Resolved per request.
	GroupOrderPath func() (string, error)
	// BusOptions tunes the fleet event bus (debounce, rediscovery).
	BusOptions fleetwatch.Options
	// PingEvery is the SSE keep-alive interval; 0 means 20s.
	PingEvery time.Duration

	// Artifacts is the artifact store behind /v1/artifacts and /a/; nil
	// means the machine-wide store (artifacts.New). ArtifactPoll is the
	// watcher's fallback rescan interval (0 = 30s) and ArtifactDebounce its
	// fsnotify debounce (0 = 200ms).
	Artifacts        *artifacts.Store
	ArtifactPoll     time.Duration
	ArtifactDebounce time.Duration
	// RenderPDF renders an artifact PDF for /v1/artifacts/{id}/pdf; nil
	// means artifactpdf.Render (a headless browser). Tests stub it.
	RenderPDF RenderPDFFunc

	// Push dependencies; nil means production (webpush-go, ntfy.sh).
	Pusher   Pusher
	NtfyBase string
	// LaptopIdle reports time since the last laptop input, for
	// serve.push.quiet_when_laptop_active; nil means ioreg HIDIdleTime on
	// macOS, else agentctl tmux client activity.
	LaptopIdle LaptopIdleFunc

	// Build is the server binary's identity, reported to the control-socket
	// ping so approval hooks can check they talk to the same agentctl build.
	Build buildid.Identity
}

// Server is the agentctl serve HTTP application.
type Server struct {
	opts    Options
	now     func() time.Time
	started time.Time
	cidrs   cidrFilter
	devices *DeviceStore
	pairing pairing
	router  *Router
	handler http.Handler

	fleet     FleetSource
	panes     PaneSource
	agents    AgentLister
	handles   func() *fleet.Index
	bus       *fleetwatch.Bus
	holder    Holder
	host      HostSource
	hostWin   func() (int, error)
	hostWinMu sync.Mutex
	hostWinAt time.Time
	hostWinN  int
	hostWinOK bool
	pingEvery time.Duration
	// groupOrderPath is the saved group order's path (Options.GroupOrderPath).
	groupOrderPath func() (string, error)
	subs           *SubscriptionStore
	vapid          VAPIDKeys
	sender         *Sender
	presence       *Presence
	ntfy           *Ntfy
	notifier       *Notifier
	pushQ          *pushDispatcher

	artifacts     *artifacts.Store
	artifactWatch *artifactWatcher
	artifactKey   []byte
	pdfs          *pdfExporter

	keys       KeySender
	injector   Injector
	closer     WindowCloser
	children   ChildCloser
	dispatcher Dispatcher
	shells     Shells
	sweep      sweeprun.Func
	sweeps     sweepGuard
	// schedules is the cron editing core (see scheduleStore); runSchedule
	// fires a job now; scheduleRuns allows one run-now per job at a time.
	schedules   cronedit.Store
	runSchedule cronedit.RunFunc
	// agentPermission resolves an agent's configured permission_mode.
	agentPermission func(agent string) string
	scheduleRuns    sweepGuard
	mainPanes       MainPanes
	launchCfg       LaunchConfigSource
	ollama          *ollamaCache
	// answered remembers recently answered dialogs (see handleAnswer);
	// answerSettle bounds the wait for an answered dialog to redraw.
	answered     answeredDialogs
	answerSettle time.Duration
	limiter      writeLimiter
	audit        *auditLog
	winLocks     windowLocks
	term         *terminals
	approvals    *approvalStore
	// phone is the phone access switch (phone.go).
	phone *phoneSwitch

	// done is closed by Close so open SSE streams end before shutdown.
	done      chan struct{}
	closeOnce sync.Once
	cancel    context.CancelFunc
	bgWG      sync.WaitGroup
	// ctlMu guards ctlStopped; ctlWG counts control-socket approval
	// handlers, which Close waits for so none outlives the server.
	ctlMu      sync.Mutex
	ctlStopped bool
	ctlWG      sync.WaitGroup

	bootMu    sync.Mutex
	boot      *http.Server
	bootPort  int
	bootTimer *time.Timer
	bootGen   int

	// restart asks Run to re-exec (after `serve ca rotate`).
	restart chan struct{}
}

// New builds a Server. It loads devices.json from opts.Dir and parses
// allow_cidrs; it does not listen.
func New(opts Options) (*Server, error) {
	cidrs, err := parseCIDRs(opts.Config.EffectiveAllowCIDRs())
	if err != nil {
		return nil, err
	}
	devices, err := OpenDeviceStore(opts.Dir)
	if err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	s := &Server{opts: opts, now: now, started: now(), cidrs: cidrs, devices: devices, restart: make(chan struct{}, 1)}
	s.phone = newPhoneSwitch(opts.Dir)
	s.fleet, s.panes, s.agents, s.holder = opts.Fleet, opts.Panes, opts.Agents, opts.Awake
	if s.fleet == nil {
		s.fleet = fleetwatch.LiveSource{}
	}
	s.fleet = lockedFleet{src: s.fleet}
	s.keys, s.injector, s.closer, s.dispatcher = opts.Keys, opts.Injector, opts.Closer, opts.Dispatcher
	if s.keys == nil {
		s.keys = tmuxKeys{}
	}
	if s.injector == nil {
		s.injector = tmuxInjector{}
	}
	if s.closer == nil {
		s.closer = tmuxCloser{}
	}
	s.children = opts.Children
	if s.children == nil {
		s.children = tmuxChildren()
	}
	s.shells = opts.Shells
	if s.shells == nil {
		s.shells = tmuxShells{}
	}
	if s.dispatcher == nil {
		s.dispatcher = newLaunchDispatcher()
	}
	s.sweep = opts.Sweep
	if s.sweep == nil {
		s.sweep = sweeprun.Run
	}
	if opts.Schedules != nil {
		s.schedules = *opts.Schedules
	} else {
		s.schedules = cronedit.Default(nil)
	}
	s.agentPermission = opts.AgentPermissionMode
	if s.agentPermission == nil {
		s.agentPermission = func(agent string) string { return config.Load().Agents[agent].PermissionMode }
	}
	s.runSchedule = opts.RunSchedule
	if s.runSchedule == nil {
		s.runSchedule = cronedit.RunSubprocess
	}
	s.host, s.hostWin = opts.Host, opts.TmuxWindows
	if s.host == nil {
		s.host = newLiveHost(now)
	}
	if s.hostWin == nil {
		s.hostWin = tmux.CountAllWindows
	}
	s.launchCfg = opts.LaunchConfig
	if s.launchCfg == nil {
		s.launchCfg = configLaunchSource{}
	}
	s.ollama = &ollamaCache{list: opts.OllamaModels, now: now}
	if s.ollama.list == nil {
		s.ollama.list = dispatch.ListOllamaModels
	}
	s.mainPanes, s.answerSettle = opts.MainPanes, answerSettleTimeout
	if s.mainPanes == nil {
		s.mainPanes = tmuxMainPanes{}
	}
	s.audit = newAuditLog(opts.Dir)
	s.approvals = newApprovalStore()
	s.term = newTerminals(opts.Terminal, opts.PTY, opts.TerminalSizeEvery)
	s.term.markers = opts.PhoneViewDir
	if s.term.markers == "" {
		if d, err := phoneview.DefaultDir(); err == nil {
			s.term.markers = d
		}
	}
	if s.panes == nil {
		s.panes = tmuxPanes{}
	}
	if s.agents == nil {
		s.agents = discoveredAgents{}
	}
	if s.handles = s.opts.HandleIndex; s.handles == nil {
		s.handles = fleet.LoadHandleIndex
	}
	if s.holder == nil {
		s.holder = newHolder()
	}
	s.groupOrderPath = opts.GroupOrderPath
	if s.groupOrderPath == nil {
		s.groupOrderPath = fleet.GroupOrderPath
	}
	s.pingEvery = opts.PingEvery
	if s.pingEvery <= 0 {
		s.pingEvery = 20 * time.Second
	}
	if err := s.initPush(opts); err != nil {
		return nil, err
	}
	s.done = make(chan struct{})
	s.bus = fleetwatch.NewBus(s.fleet, opts.BusOptions)
	if s.artifactKey, err = loadOrCreateArtifactKey(opts.Dir); err != nil {
		return nil, err
	}
	s.artifacts = opts.Artifacts
	if s.artifacts == nil {
		s.artifacts = artifacts.New()
	}
	s.pdfs = newPDFExporter(s.artifacts, opts.RenderPDF)
	s.artifactWatch = newArtifactWatcher(s.artifacts, func(c fleetwatch.ArtifactChange) {
		if c.Action == fleetwatch.ArtifactRemove {
			s.pdfs.forget(c.ID)
		}
		s.bus.Publish(fleetwatch.Event{Kind: fleetwatch.KindArtifact, Artifact: c})
	}, opts.ArtifactDebounce, opts.ArtifactPoll)
	s.router = newRouter(devices, opts.Config.AllowWrite, now)
	s.router.onWriteDenied = s.auditRejected
	s.router.phoneOff = s.phone.Off
	s.routes(s.router)
	s.handler = s.chain(s.router)
	return s, nil
}

// Handler returns the full HTTPS request pipeline.
func (s *Server) Handler() http.Handler { return s.handler }

// ResetWriteLimits refills every device's write budget. For the browser-test
// harness, whose tests share one device token; never called in production.
func (s *Server) ResetWriteLimits() { s.limiter.reset() }

// chain wraps h in the middleware every HTTPS request passes, outermost
// first: the /a/ sandbox headers (artifact content, enforced on every
// response there whoever writes it), CIDR filter, security headers, CSRF.
func (s *Server) chain(h http.Handler) http.Handler {
	return sandboxArtifacts(s.cidrs.middleware(securityHeaders(requireCSRF(h, s.auditRejected))))
}

// routes registers every endpoint. Later PRs add one line each here
// (s.registerRead(rt), s.registerPush(rt), s.registerWrite(rt), ...),
// implemented in their own routes_*.go files.
func (s *Server) routes(rt *Router) {
	s.registerCore(rt)
	s.registerStatic(rt)
	s.registerRead(rt)
	s.registerHandles(rt)
	s.registerPush(rt)
	s.registerPresence(rt)
	s.registerWrite(rt)
	s.registerSchedules(rt)
	s.registerTerminal(rt)
	s.registerApprovals(rt)
	s.registerArtifacts(rt)
}

// registerCore registers the foundation endpoints.
func (s *Server) registerCore(rt *Router) {
	rt.Public("GET /healthz", s.handleHealthz)
	rt.Public("GET /pair", s.handlePair)
	rt.Device("GET /{$}", s.handleIndex)
	rt.Device("/", s.handleNotFound)
}

// healthz is the /healthz body. PhoneOff tells the web app, which reaches
// /healthz without a cookie, that phone access is switched off.
type healthz struct {
	OK       bool     `json:"ok"`
	Version  string   `json:"version"`
	Started  string   `json:"started"`
	Features []string `json:"features"`
	PhoneOff bool     `json:"phone_off,omitempty"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthz{
		OK:       true,
		Version:  s.opts.Version,
		Started:  s.started.UTC().Format(time.RFC3339),
		Features: s.router.Features(),
		PhoneOff: s.phone.Off(),
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	writeHTML(w, http.StatusOK, pageIndex)
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, codeNotFound, "no such endpoint: "+r.URL.Path)
}

// PairInfo is what `agentctl serve pair` prints.
type PairInfo struct {
	Code         string    `json:"code"`
	Expires      time.Time `json:"expires"`
	PairURL      string    `json:"pair_url"`
	BootstrapURL string    `json:"bootstrap_url,omitempty"`
	Fingerprint  string    `json:"fingerprint"`
}

// OpenPairing opens (or restarts) the pairing window: a fresh one-time code
// and, when configured, the plain-HTTP bootstrap listener serving the CA
// profile. Both end PairingWindow later.
func (s *Server) OpenPairing(name string) (PairInfo, error) {
	if s.opts.CA == nil {
		return PairInfo{}, errors.New("no CA loaded")
	}
	if s.phone.Off() {
		return PairInfo{}, errors.New("phone access is off: turn it on with `agentctl serve start` first")
	}
	s.bootMu.Lock()
	defer s.bootMu.Unlock()
	port := 0
	if s.opts.BootstrapAddr != "" {
		p, err := s.startBootstrapLocked()
		if err != nil {
			return PairInfo{}, err
		}
		port = p
	}
	code, expires, err := s.pairing.open(name, s.now())
	if err != nil {
		return PairInfo{}, err
	}
	if s.bootTimer != nil {
		s.bootTimer.Stop()
	}
	s.bootGen++
	gen := s.bootGen
	s.bootTimer = time.AfterFunc(expires.Sub(s.now()), func() { s.expirePairing(gen) })

	info := PairInfo{
		Code:        code,
		Expires:     expires,
		Fingerprint: s.opts.CA.Fingerprint(),
		PairURL: (&url.URL{
			Scheme:   "https",
			Host:     net.JoinHostPort(s.opts.AdvertiseHost, strconv.Itoa(s.opts.HTTPSPort)),
			Path:     "/pair",
			RawQuery: url.Values{"code": {code}}.Encode(),
		}).String(),
	}
	if port != 0 {
		info.BootstrapURL = "http://" + net.JoinHostPort(s.opts.AdvertiseHost, strconv.Itoa(port)) + "/"
	}
	logging.Log("serve: pairing window open until %s", expires.Format(time.RFC3339))
	return info, nil
}

// startBootstrapLocked starts the bootstrap listener if it is not running
// and returns its port. Callers hold bootMu.
func (s *Server) startBootstrapLocked() (int, error) {
	if s.boot != nil {
		return s.bootPort, nil
	}
	ln, err := net.Listen("tcp", s.opts.BootstrapAddr)
	if err != nil {
		return 0, fmt.Errorf("starting pairing bootstrap listener on %s: %w", s.opts.BootstrapAddr, err)
	}
	srv := newHTTPServer(s.BootstrapHandler())
	s.boot = srv
	s.bootPort = ln.Addr().(*net.TCPAddr).Port
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logging.Error("serve: bootstrap listener: %v", err)
		}
	}()
	return s.bootPort, nil
}

// expirePairing is the window timer. It does nothing when the window was
// reopened (a newer generation) after this timer was armed.
func (s *Server) expirePairing(gen int) {
	s.bootMu.Lock()
	defer s.bootMu.Unlock()
	if gen == s.bootGen {
		s.closePairingLocked()
	}
}

// closePairingLocked ends the pairing window: the code is discarded and the
// bootstrap listener stops. Callers hold bootMu.
func (s *Server) closePairingLocked() {
	s.pairing.close()
	if s.bootTimer != nil {
		s.bootTimer.Stop()
		s.bootTimer = nil
	}
	if s.boot != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.boot.Shutdown(ctx)
		s.boot = nil
		logging.Log("serve: pairing window closed")
	}
}

// Start runs the fleet event bus and, when serve.keep_awake is on, the
// keep-awake follower, until Close. It returns immediately.
func (s *Server) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.bgWG.Add(1)
	go func() {
		defer s.bgWG.Done()
		s.bus.Run(ctx)
	}()
	s.bgWG.Add(2)
	go func() {
		defer s.bgWG.Done()
		runNotifier(ctx, s.bus, s.notifier)
	}()
	go func() {
		defer s.bgWG.Done()
		s.pushQ.run(ctx)
	}()
	s.bgWG.Add(1)
	go func() {
		defer s.bgWG.Done()
		s.phone.run(ctx, phonePollEvery)
	}()
	// Seeded here, before Start returns, so an artifact published right
	// after start is an add, not part of the baseline.
	s.artifactWatch.seed()
	s.bgWG.Add(1)
	go func() {
		defer s.bgWG.Done()
		s.artifactWatch.run(ctx)
	}()
	if s.opts.Config.KeepAwakeEnabled() {
		s.bgWG.Add(1)
		go func() {
			defer s.bgWG.Done()
			runKeepAwake(ctx, s.bus, s.holder)
		}()
	}
}

// Close ends any pairing window, stops the bootstrap listener and the
// background watchers, ends live terminals (killing their tmux clients and
// viewer sessions), and releases keep-awake. It must complete before a
// selfexec re-exec so no caffeinate or tmux child outlives the old image.
// Idempotent.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.cancel != nil {
			s.cancel()
		}
		s.term.close()
		s.bgWG.Wait()
		s.ctlMu.Lock()
		s.ctlStopped = true
		s.ctlMu.Unlock()
		s.ctlWG.Wait()
		s.holder.Stop()
	})
	s.bootMu.Lock()
	defer s.bootMu.Unlock()
	s.closePairingLocked()
}

// BootstrapHandler serves the plain-HTTP pairing landing page and the CA
// configuration profile, behind the same CIDR filter as HTTPS. It serves
// nothing secret: the CA certificate is public, and the pairing code only
// ever travels over HTTPS.
func (s *Server) BootstrapHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		host, _ := os.Hostname()
		body, err := renderBootstrap(bootstrapPage{
			Host:        host,
			CAName:      s.opts.CA.Cert.Subject.CommonName,
			Fingerprint: s.opts.CA.Fingerprint(),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, codeInternal, "rendering page")
			return
		}
		writeHTML(w, http.StatusOK, body)
	})
	mux.HandleFunc("GET /ca.mobileconfig", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", MobileconfigContentType)
		w.Header().Set("Content-Disposition", `attachment; filename="agentctl-ca.mobileconfig"`)
		_, _ = w.Write(Mobileconfig(s.opts.CA))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, codeNotFound, "no such endpoint: "+r.URL.Path)
	})
	return s.cidrs.middleware(securityHeaders(mux))
}

// RequestRestart asks the running server to re-exec itself, picking up new
// certificates. It never blocks.
func (s *Server) RequestRestart() {
	select {
	case s.restart <- struct{}{}:
	default:
	}
}

// StatusInfo is the running server's self-report for `serve status`.
type StatusInfo struct {
	PID      int       `json:"pid"`
	Version  string    `json:"version"`
	Started  time.Time `json:"started"`
	URL      string    `json:"url"`
	Pairing  bool      `json:"pairing"`
	Features []string  `json:"features"`
	// PushDevices names the devices with a push subscription; LastPush is
	// the most recent notification outcome (nil before the first).
	PushDevices []string  `json:"push_devices,omitempty"`
	LastPush    *LastPush `json:"last_push,omitempty"`
	// PublicURL is https://<public_hostname>:<port>/ while a valid, publicly
	// trusted certificate is held; empty otherwise. Artifact links (/a/<id>,
	// public routes) use it when set. URL stays on AdvertiseHost: the app
	// needs the device cookie, which is host-only and set at pairing there.
	PublicURL string `json:"public_url,omitempty"`
	// PublicCert is the serve.public_hostname certificate state; nil when
	// the key is unset.
	PublicCert *PublicCertStatus `json:"public_cert,omitempty"`
}

// Status reports the running server's state.
func (s *Server) Status() StatusInfo {
	st := StatusInfo{
		PID:        os.Getpid(),
		Version:    s.opts.Version,
		Started:    s.started,
		URL:        "https://" + net.JoinHostPort(s.opts.AdvertiseHost, strconv.Itoa(s.opts.HTTPSPort)) + "/",
		Pairing:    s.pairing.active(s.now()),
		Features:   s.router.Features(),
		PublicCert: s.opts.Public.Status(),
	}
	if h := s.opts.Public.AdvertiseHost(); h != "" {
		st.PublicURL = "https://" + net.JoinHostPort(h, strconv.Itoa(s.opts.HTTPSPort)) + "/"
	}
	if s.subs != nil && s.devices != nil {
		st.PushDevices = s.pushDeviceNames()
	}
	if s.sender != nil {
		st.LastPush = s.sender.LastPush()
	}
	return st
}

// pushDeviceNames lists the names of paired devices that have a subscription.
func (s *Server) pushDeviceNames() []string {
	all, err := s.subs.All()
	if err != nil {
		return nil
	}
	devs, err := s.devices.List()
	if err != nil {
		return nil
	}
	var names []string
	for _, d := range devs {
		if _, ok := all[d.ID]; ok {
			names = append(names, d.Name)
		}
	}
	sort.Strings(names)
	return names
}

// newHTTPServer returns an http.Server with timeouts so a slow or malicious
// client cannot hold connections open indefinitely (as internal/metrics).
// Long-lived handlers (SSE, the terminal WebSocket) lift the timeouts per
// request with http.NewResponseController(w).SetWriteDeadline(time.Time{})
// (and SetReadDeadline for the WebSocket).
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}
