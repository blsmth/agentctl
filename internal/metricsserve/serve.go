// Package metricsserve is `agentctl metrics serve`: one process per machine
// that owns the Prometheus registry, the /metrics HTTP endpoint and remote
// write for every agentctl project. It replaces the per-project monitor's
// metrics duties:
//
//   - transitions come from the NDJSON event logs the state writers append
//     (internal/metrics/events), tailed from the end at startup;
//   - usage (tokens, tools, turns, cost) comes from each window's Claude log,
//     read through metrics.UsageSet;
//   - every series carries a project label (the project dir's basename).
package metricsserve

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/metrics"
	"github.com/blsmth/agentctl/internal/metrics/events"
	"github.com/blsmth/agentctl/internal/metricsproc"
	"github.com/blsmth/agentctl/internal/registry"
	"github.com/blsmth/agentctl/internal/selfexec"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	pollInterval     = time.Second
	discoverInterval = 5 * time.Second
	// IdleExit is how long the serve lingers with no agentctl tmux sessions.
	IdleExit = 10 * time.Minute
	// eventRetention is how many days of event files are kept.
	eventRetention = events.Retention
	// absenceExpiry is how long a project may be missing from discovery
	// before its event tailer and usage streams (and their read offsets) are
	// dropped. Shorter misses (a failed tmux query, a session restart) keep
	// them, so returning projects are never re-read from the start and
	// double counted.
	absenceExpiry = 6 * time.Hour
)

// Project is one agentctl project the serve watches.
type Project struct {
	Name       string // project label value (basename of the project dir)
	RuntimeDir string
}

// Server is the metrics serve's state. Drive it with Step, or Run for the
// full loop.
type Server struct {
	tracker   *metrics.TransitionTracker
	usage     *metrics.UsageSet
	started   time.Time
	tailers   map[string]*events.Tailer // by runtime dir
	lastSeen  map[string]time.Time      // runtime dir -> last discovery that listed it
	known     map[string]bool           // runtime dirs that ever had a tailer
	projects  []Project
	firstDisc bool
}

// NewServer registers the transition and usage collectors on reg and returns a
// server. Discovered projects are added with SetProjects.
func NewServer(reg prometheus.Registerer, now time.Time) *Server {
	return &Server{
		tracker:  metrics.NewTransitionTracker(reg),
		usage:    metrics.NewUsageSet(metrics.NewCollector(reg)),
		started:  now,
		tailers:  make(map[string]*events.Tailer),
		lastSeen: make(map[string]time.Time),
		known:    make(map[string]bool),
	}
}

// SetProjects replaces the watched project set. Projects present on the first
// call are tailed from the end of their event files (no replay of history);
// projects that appear later are read from the start but only for events at or
// after the serve's start. A project that drops out of discovery keeps its
// tailer and usage streams for absenceExpiry, so a transient miss followed by
// a return does not re-read (and double count) anything.
func (s *Server) SetProjects(ps []Project) { s.SetProjectsAt(ps, time.Now()) }

// SetProjectsAt is SetProjects with an explicit clock.
func (s *Server) SetProjectsAt(ps []Project, now time.Time) {
	for _, p := range ps {
		s.lastSeen[p.RuntimeDir] = now
		if _, ok := s.tailers[p.RuntimeDir]; ok {
			continue
		}
		switch {
		case s.known[p.RuntimeDir]:
			// Returning after its state expired: events from the absence are
			// gone, but replaying from the serve start would double count.
			s.tailers[p.RuntimeDir] = events.NewTailer(p.RuntimeDir)
		case s.firstDisc:
			s.seedActive(p, now)
			s.tailers[p.RuntimeDir] = events.NewTailerSince(p.RuntimeDir, s.started)
		default:
			s.seedActive(p, now)
			s.tailers[p.RuntimeDir] = events.NewTailer(p.RuntimeDir)
		}
		s.known[p.RuntimeDir] = true
	}
	s.firstDisc = true
	for dir := range s.tailers {
		if now.Sub(s.lastSeen[dir]) > absenceExpiry {
			delete(s.tailers, dir)
			delete(s.lastSeen, dir)
		}
	}
	s.projects = ps
}

// seedActive counts the project's already-running windows in the active gauge.
func (s *Server) seedActive(p Project, now time.Time) {
	for id, ws := range windowstate.AllFrom(windowstate.WindowsDirFor(p.RuntimeDir)) {
		if _, _, ok := events.Identity(ws); !ok {
			continue
		}
		s.tracker.SeedActive(id, p.Name, parseStatus(events.Status(ws.State)), now)
	}
}

// Step runs one poll: it feeds new events to the transition tracker, then
// syncs and ticks the per-window usage streams.
func (s *Server) Step() {
	for _, tl := range s.tailers {
		for _, e := range tl.Poll() {
			s.Feed(e)
		}
	}
	s.syncUsage()
	s.usage.Tick()
}

// Feed turns one event into a state.Transition and records it, using the
// event's own timestamp for durations.
func (s *Server) Feed(e events.Event) {
	s.tracker.RecordTransitionAt(Transition(e), e.TS)
}

// Transition reconstructs the state.Transition an event describes.
func Transition(e events.Event) state.Transition {
	return state.Transition{
		Agent:        e.Agent,
		Project:      filepath.Base(e.Project),
		WindowID:     e.WindowID,
		From:         parseStatus(e.From),
		To:           parseStatus(e.To),
		LastToolName: e.Tool,
	}
}

func parseStatus(s string) state.AgentStatus {
	switch s {
	case "working":
		return state.StatusWorking
	case "blocked":
		return state.StatusBlocked
	case "gone":
		return state.StatusGone
	}
	return state.StatusIdle
}

// syncUsage attaches a usage stream for every window record with a log.
func (s *Server) syncUsage() {
	keep := make(map[string]bool)
	for _, p := range s.projects {
		for id, ws := range windowstate.AllFrom(windowstate.WindowsDirFor(p.RuntimeDir)) {
			if ws.LogPath == "" {
				continue
			}
			_, base, ok := events.Identity(ws)
			if !ok {
				continue
			}
			key := p.RuntimeDir + "|" + id
			keep[key] = true
			s.usage.Attach(key, p.Name, base, ws.LogPath)
		}
	}
	// Streams of a project missing from discovery keep their offsets until
	// the project has been absent for absenceExpiry.
	present := make(map[string]bool, len(s.projects))
	for _, p := range s.projects {
		present[p.RuntimeDir] = true
	}
	for dir := range s.tailers {
		if present[dir] {
			continue
		}
		for _, k := range s.usage.Keys() {
			if strings.HasPrefix(k, dir+"|") {
				keep[k] = true
			}
		}
	}
	s.usage.Retain(keep)
}

// DiscoverProjects lists the projects behind every live agentctl tmux session
// (tmux is the source of truth; the registry only enriches, as for the hub).
//
// It returns an error when tmux could not be queried, so the caller keeps its
// previous project set rather than treating a failed query as "no projects".
func DiscoverProjects() ([]Project, error) {
	sessions, err := tmux.ListSessionsWithPathsErr()
	if err != nil {
		return nil, err
	}
	entries, _ := registry.List()
	merged := fleet.MergeSessions(sessions, entries)
	var out []Project
	seen := make(map[string]bool)
	for _, e := range merged {
		if (e.Host != "" && e.Host != "local") || e.RuntimeDir == "" || seen[e.RuntimeDir] {
			continue
		}
		seen[e.RuntimeDir] = true
		out = append(out, Project{Name: projectName(e), RuntimeDir: e.RuntimeDir})
	}
	return out, nil
}

func projectName(e registry.Entry) string {
	dir := e.ProjectDir
	if dir == "" {
		dir = filepath.Dir(e.RuntimeDir)
	}
	return filepath.Base(dir)
}

// Options are Run's seams.
type Options struct {
	Discover func() ([]Project, error)
	Poll     time.Duration
	Idle     time.Duration
}

// Run is the `agentctl metrics serve` entry point. It returns when the serve
// should exit (idle, metrics disabled, context done); a binary change re-execs
// in place instead.
func Run(ctx context.Context, opts Options) error {
	if opts.Discover == nil {
		opts.Discover = DiscoverProjects
	}
	if opts.Poll <= 0 {
		opts.Poll = pollInterval
	}
	if opts.Idle <= 0 {
		opts.Idle = IdleExit
	}

	// A per-machine process logs beside its pidfile, not in whichever
	// project's runtime dir its spawner happened to run in.
	if dir, err := metricsproc.Dir(); err == nil {
		logging.InitFile(filepath.Join(dir, "metrics-serve.log"))
	}

	cfg := config.Load()
	mc := cfg.EffectiveMetricsConfig()
	if !mc.Enabled {
		logging.Log("metrics serve: metrics are disabled, exiting")
		return nil
	}

	release, ok, err := metricsproc.Acquire()
	if err != nil {
		return err
	}
	if !ok {
		logging.Log("metrics serve: another serve is already running, exiting")
		return nil
	}
	defer release()

	metrics.InitRegistry(mc.Labels)
	srv := NewServer(metrics.Registerer(), time.Now())

	if port := mc.EffectivePort(); port > 0 {
		if err := metrics.StartHTTPServer(port); err != nil {
			// e.g. an old-binary monitor still holds the port: keep going so
			// remote write still works.
			logging.Log("metrics serve: %v (continuing without the local /metrics endpoint)", err)
		} else {
			metricsproc.WritePort(port)
			logging.Log("metrics serve: /metrics on :%d", port)
		}
	}
	if mc.HasRemoteWrite() {
		rw := metrics.RemoteWriteConfig{
			Endpoint:     mc.Endpoint,
			PushInterval: mc.EffectivePushInterval(),
			Labels:       mc.Labels,
		}
		if mc.BasicAuth != nil {
			rw.BasicAuth = &metrics.BasicAuth{Username: mc.BasicAuth.Username, Password: mc.BasicAuth.Password}
		}
		stop := metrics.StartRemoteWrite(rw)
		defer stop()
		logging.Log("metrics serve: remote write → %s (interval %s)", mc.Endpoint, mc.EffectivePushInterval())
	}

	exeStart, exeErr := selfexec.Capture()
	if exeErr != nil {
		logging.Log("metrics serve: hot reload disabled: %v", exeErr)
	}

	return loop(ctx, srv, opts, exeStart)
}

func loop(ctx context.Context, srv *Server, opts Options, exeStart selfexec.Identity) error {
	tick := time.NewTicker(opts.Poll)
	defer tick.Stop()
	var lastDiscover, lastPrune time.Time
	lastSeen := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-tick.C:
			if now.Sub(lastDiscover) >= discoverInterval || lastDiscover.IsZero() {
				lastDiscover = now
				ps, derr := opts.Discover()
				if derr != nil {
					// Keep the previous project set; do not count a failed
					// query towards idleness either.
					logging.Log("metrics serve: discovery failed, keeping previous projects: %v", derr)
					lastSeen = now
					ps = srv.projects
				} else {
					srv.SetProjects(ps)
				}
				if len(ps) > 0 {
					lastSeen = now
				} else if now.Sub(lastSeen) >= opts.Idle {
					logging.Log("metrics serve: no agentctl sessions for %s, exiting", opts.Idle)
					return nil
				}
				if !config.Load().EffectiveMetricsConfig().Enabled {
					logging.Log("metrics serve: metrics disabled in config, exiting")
					return nil
				}
				if selfexec.CheckChanged(exeStart, now) {
					logging.Log("metrics serve: binary changed, re-executing")
					if err := selfexec.Reexec(exeStart.Path); err != nil {
						logging.Log("metrics serve: re-exec failed: %v", err)
					}
				}
			}
			if now.Sub(lastPrune) >= time.Hour {
				lastPrune = now
				for _, p := range srv.projects {
					events.Prune(p.RuntimeDir, now, eventRetention)
				}
			}
			srv.Step()
		}
	}
}

// Describe is a one-line summary, for logs.
func (s *Server) Describe() string {
	return fmt.Sprintf("%d projects, %d usage streams", len(s.projects), s.usage.Len())
}
