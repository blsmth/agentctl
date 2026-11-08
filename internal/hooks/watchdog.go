package hooks

import (
	"os"
	"path/filepath"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/pidfile"
	"github.com/blsmth/agentctl/internal/tmux"
)

const (
	monitorPidName      = "monitor.pid"
	watchdogStampName   = "monitor-respawn.stamp"
	watchdogMinInterval = 30 * time.Second
)

// watchdogDeps are the side effects of the monitor watchdog, injectable so the
// decision logic can be tested without processes or tmux.
type watchdogDeps struct {
	now           func() time.Time
	monitorAlive  func() bool
	sessionExists func() bool
	lastAttempt   func() time.Time
	markAttempt   func(time.Time)
	spawn         func() error
}

// watchdogResult says what runWatchdog did.
type watchdogResult string

const (
	watchdogAlive       watchdogResult = "monitor-alive"
	watchdogRateLimited watchdogResult = "rate-limited"
	watchdogNoSession   watchdogResult = "no-session"
	watchdogSpawned     watchdogResult = "spawned"
	watchdogSpawnFailed watchdogResult = "spawn-failed"
)

// runWatchdog restarts a dead monitor. It is ordered cheapest-first: a live
// pidfile returns before anything else, and tmux is only consulted once the
// monitor is dead and the rate limit allows an attempt.
func runWatchdog(d watchdogDeps) watchdogResult {
	if d.monitorAlive() {
		return watchdogAlive
	}
	now := d.now()
	if now.Sub(d.lastAttempt()) < watchdogMinInterval {
		return watchdogRateLimited
	}
	if !d.sessionExists() {
		return watchdogNoSession
	}
	// Stamp before spawning so a monitor that crashes on start is not
	// respawned by every prompt.
	d.markAttempt(now)
	if err := d.spawn(); err != nil {
		return watchdogSpawnFailed
	}
	return watchdogSpawned
}

// monitorWatchdog is called from frequent, non-per-tool hooks. It never
// blocks or fails the hook: all errors are logged and swallowed.
func monitorWatchdog() {
	defer func() {
		if r := recover(); r != nil {
			logging.Log("monitor-watchdog: recovered: %v", r)
		}
	}()
	runtimeDir := paths.RuntimeDir()
	if runtimeDir == "" {
		return
	}
	projectRoot := filepath.Dir(runtimeDir)
	session := tmux.SessionNameForProjectRoot(projectRoot)
	stamp := filepath.Join(runtimeDir, watchdogStampName)
	res := runWatchdog(watchdogDeps{
		now:           time.Now,
		monitorAlive:  func() bool { return pidfile.Validate(runtimeDir, monitorPidName) },
		sessionExists: func() bool { return tmux.HasSessionNamed(session) },
		lastAttempt: func() time.Time {
			if fi, err := os.Stat(stamp); err == nil {
				return fi.ModTime()
			}
			return time.Time{}
		},
		markAttempt: func(t time.Time) {
			if err := os.WriteFile(stamp, nil, 0o600); err == nil {
				_ = os.Chtimes(stamp, t, t)
			}
		},
		spawn: func() error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			return tmux.StartMonitor(exe, projectRoot)
		},
	})
	switch res {
	case watchdogSpawned, watchdogSpawnFailed:
		logging.Log("monitor-watchdog: monitor dead, session %s exists: %s", session, res)
	}
}
