package metrics

import (
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/state"
	"github.com/prometheus/client_golang/prometheus"
)

// TransitionTracker tracks agent state transitions and exposes Prometheus metrics.
type TransitionTracker struct {
	mu        sync.Mutex
	startTime time.Time

	// Per-window state for duration calculations.
	spawnTimes   map[string]time.Time         // windowID -> first seen time
	blockedStart map[string]time.Time         // windowID -> entered blocked time
	workingStart map[string]time.Time         // windowID -> entered working time
	seenWindows  map[string]bool              // windowID -> already counted as spawned
	counted      map[string]state.AgentStatus // windowID -> state the active gauge currently counts it under

	// Prometheus metrics.
	agentsSpawned     *prometheus.CounterVec
	sessionsCompleted *prometheus.CounterVec
	stateTransitions  *prometheus.CounterVec
	toolUses          *prometheus.CounterVec
	failures          *prometheus.CounterVec
	agentsActive      *prometheus.GaugeVec
	monitorUptime     prometheus.GaugeFunc
	sessionDuration   *prometheus.HistogramVec
	blockedDuration   *prometheus.HistogramVec
	workingDuration   *prometheus.HistogramVec
}

// NewTransitionTracker registers all Prometheus metrics on reg and returns a
// ready-to-use tracker. reg may be a plain *prometheus.Registry or a
// WrappedRegisterer (e.g. created by prometheus.WrapRegistererWith). It does
// NOT start an HTTP server — use metrics.StartHTTPServer to expose /metrics.
func NewTransitionTracker(reg prometheus.Registerer) *TransitionTracker {
	tr := &TransitionTracker{
		startTime:    time.Now(),
		spawnTimes:   make(map[string]time.Time),
		blockedStart: make(map[string]time.Time),
		workingStart: make(map[string]time.Time),
		seenWindows:  make(map[string]bool),
		counted:      make(map[string]state.AgentStatus),
	}

	tr.agentsSpawned = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agentctl_agents_spawned_total",
		Help: "Total number of agent windows first seen.",
	}, []string{"agent_name"})

	tr.sessionsCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agentctl_sessions_completed_total",
		Help: "Total number of agent sessions completed (window gone).",
	}, []string{"agent_name", "exit_state"})

	tr.stateTransitions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agentctl_state_transitions_total",
		Help: "Total number of agent state transitions.",
	}, []string{"from_state", "to_state", "agent_name"})

	tr.toolUses = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agentctl_tool_uses_total",
		Help: "Total number of tool uses observed (proxy via Working->Blocked transitions).",
	}, []string{"tool_name", "agent_name"})

	tr.failures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agentctl_failures_total",
		Help: "Total number of likely failures (Working->Gone transitions).",
	}, []string{"agent_name"})

	tr.agentsActive = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "agentctl_agents_active",
		Help: "Number of active agents by state.",
	}, []string{"state"})

	startTime := tr.startTime
	tr.monitorUptime = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "agentctl_monitor_uptime_seconds",
		Help: "Seconds since the monitor started.",
	}, func() float64 {
		return time.Since(startTime).Seconds()
	})

	tr.sessionDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "agentctl_session_duration_seconds",
		Help:    "Duration of agent sessions from first seen to gone.",
		Buckets: prometheus.ExponentialBuckets(60, 2, 10), // 1min to ~17hrs
	}, []string{"agent_name"})

	tr.blockedDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "agentctl_blocked_duration_seconds",
		Help:    "Duration of time agents spend in blocked state.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 12), // 1s to ~1hr
	}, []string{"agent_name"})

	tr.workingDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "agentctl_working_duration_seconds",
		Help:    "Duration of time agents spend in working state.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 12), // 1s to ~1hr
	}, []string{"agent_name"})

	reg.MustRegister(
		tr.agentsSpawned,
		tr.sessionsCompleted,
		tr.stateTransitions,
		tr.toolUses,
		tr.failures,
		tr.agentsActive,
		tr.monitorUptime,
		tr.sessionDuration,
		tr.blockedDuration,
		tr.workingDuration,
	)

	return tr
}

// RecordTransition updates all relevant Prometheus metrics for the given state transition.
func (tr *TransitionTracker) RecordTransition(t state.Transition) {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	// Track first-seen: count as a spawn the first time we see a window.
	if !tr.seenWindows[t.WindowID] {
		tr.seenWindows[t.WindowID] = true
		tr.spawnTimes[t.WindowID] = time.Now()
		tr.agentsSpawned.WithLabelValues(t.Agent).Inc()
	}

	// Count every transition.
	tr.stateTransitions.WithLabelValues(t.From.String(), t.To.String(), t.Agent).Inc()

	// Track tool uses when a tool name is present.
	if t.LastToolName != "" {
		tr.toolUses.WithLabelValues(t.LastToolName, t.Agent).Inc()
	}

	// Update active agent gauges. Decrement only the state the window was
	// actually counted under, so the gauge can never go below zero.
	tr.updateActive(t)

	// Working duration tracking.
	if t.To == state.StatusWorking {
		tr.workingStart[t.WindowID] = time.Now()
	}
	if t.From == state.StatusWorking {
		if start, ok := tr.workingStart[t.WindowID]; ok {
			tr.workingDuration.WithLabelValues(t.Agent).Observe(time.Since(start).Seconds())
			delete(tr.workingStart, t.WindowID)
		}
	}

	// Blocked duration tracking.
	if t.To == state.StatusBlocked {
		tr.blockedStart[t.WindowID] = time.Now()
	}
	if t.From == state.StatusBlocked {
		if start, ok := tr.blockedStart[t.WindowID]; ok {
			tr.blockedDuration.WithLabelValues(t.Agent).Observe(time.Since(start).Seconds())
			delete(tr.blockedStart, t.WindowID)
		}
	}

	// Session completion.
	if t.To == state.StatusGone {
		tr.sessionsCompleted.WithLabelValues(t.Agent, t.From.String()).Inc()

		if spawn, ok := tr.spawnTimes[t.WindowID]; ok {
			tr.sessionDuration.WithLabelValues(t.Agent).Observe(time.Since(spawn).Seconds())
		}

		// Likely crash: was working when the window disappeared.
		if t.From == state.StatusWorking {
			tr.failures.WithLabelValues(t.Agent).Inc()
		}

		// Clean up all per-window state.
		delete(tr.spawnTimes, t.WindowID)
		delete(tr.seenWindows, t.WindowID)
		delete(tr.blockedStart, t.WindowID)
		delete(tr.workingStart, t.WindowID)
		delete(tr.counted, t.WindowID)
	}
}

// updateActive moves the window between agentsActive gauge buckets using the
// tracked counted state rather than t.From. Callers must hold tr.mu.
func (tr *TransitionTracker) updateActive(t state.Transition) {
	if t.From == t.To {
		return // same-state report does not change the gauge
	}
	prev, wasCounted := tr.counted[t.WindowID]
	if wasCounted {
		tr.agentsActive.WithLabelValues(prev.String()).Dec()
		delete(tr.counted, t.WindowID)
	}
	if t.To != state.StatusGone {
		tr.agentsActive.WithLabelValues(t.To.String()).Inc()
		tr.counted[t.WindowID] = t.To
	}
}
