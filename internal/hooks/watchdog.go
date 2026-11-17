package hooks

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/buildid"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/pidfile"
	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/stamp"
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
	disabled      func() bool // optional; true when config monitor:false switches the monitor off
	monitorStale  func() bool // optional; true when the live monitor runs an outdated binary
	killMonitor   func()
	sessionExists func() bool
	lastAttempt   func() time.Time
	markAttempt   func(time.Time)
	// tryAttempt, when set, atomically claims the attempt slot (check and
	// stamp under one lock) and replaces markAttempt: with the two split,
	// concurrent hooks all passed the lastAttempt check before any stamped.
	tryAttempt func(time.Time) bool
	spawn      func() error
}

// watchdogResult says what runWatchdog did.
type watchdogResult string

const (
	watchdogAlive       watchdogResult = "monitor-alive"
	watchdogRateLimited watchdogResult = "rate-limited"
	watchdogNoSession   watchdogResult = "no-session"
	watchdogSpawned     watchdogResult = "spawned"
	watchdogSpawnFailed watchdogResult = "spawn-failed"

	watchdogReplacedStale watchdogResult = "replaced-stale"
	watchdogDisabled      watchdogResult = "monitor-disabled"
)

// runWatchdog restarts a dead monitor, and replaces a live one running a stale
// binary (this upgrades monitors that predate self re-exec). It is ordered cheapest-first: a live
// pidfile returns before anything else, and tmux is only consulted once the
// monitor is dead and the rate limit allows an attempt.
func runWatchdog(d watchdogDeps) watchdogResult {
	// The kill switch wins over everything: no spawn and no replacement. A
	// monitor that is already running is left alone.
	if d.disabled != nil && d.disabled() {
		return watchdogDisabled
	}
	if d.monitorAlive() {
		if d.monitorStale == nil || !d.monitorStale() {
			return watchdogAlive
		}
		return replaceStale(d)
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
	if !claimAttempt(d, now) {
		return watchdogRateLimited
	}
	if err := d.spawn(); err != nil {
		return watchdogSpawnFailed
	}
	return watchdogSpawned
}

// claimAttempt records an attempt at now, reporting false when another process
// holds the slot.
func claimAttempt(d watchdogDeps, now time.Time) bool {
	if d.tryAttempt != nil {
		return d.tryAttempt(now)
	}
	d.markAttempt(now)
	return true
}

func replaceStale(d watchdogDeps) watchdogResult {
	now := d.now()
	if now.Sub(d.lastAttempt()) < watchdogMinInterval {
		return watchdogRateLimited
	}
	if !d.sessionExists() {
		return watchdogNoSession
	}
	if !claimAttempt(d, now) {
		return watchdogRateLimited
	}
	d.killMonitor()
	if err := d.spawn(); err != nil {
		return watchdogSpawnFailed
	}
	return watchdogReplacedStale
}

// staleMonitor reports whether the recorded monitor build is outdated relative
// to hookExe. rec/recErr come from buildid.Read, monitorPID is the pid in
// monitor.pid (0 if unreadable), and stat reads the current identity of a path.
// A monitor is stale when monitor.build is unreadable, its pid disagrees with
// monitor.pid, or it recorded the same path as hookExe but that file changed.
// A different recorded path is never stale, so a dev build and an installed
// build do not kill each other.
func staleMonitor(rec buildid.Identity, recErr error, monitorPID int, hookExe string, stat func(string) (buildid.Identity, error)) bool {
	if recErr != nil || rec.PID != monitorPID {
		return true
	}
	if rec.Path != hookExe {
		return false
	}
	cur, err := stat(hookExe)
	if err != nil {
		return false // mid-install; try again later
	}
	return !buildid.SameBuild(rec, cur)
}

func monitorIsStale(runtimeDir string) bool {
	rec, recErr := buildid.Read(runtimeDir)
	pid := 0
	if data, err := os.ReadFile(filepath.Join(runtimeDir, monitorPidName)); err == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	return staleMonitor(rec, recErr, pid, selfpath.Path(), buildid.Stat)
}

// watchdogSpawnFn starts a monitor for projectRoot; a seam for tests.
var watchdogSpawnFn = func(projectRoot string) error {
	exe, err := selfpath.Spawnable()
	if err != nil {
		return err
	}
	return tmux.StartMonitor(exe, projectRoot)
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
	stampPath := filepath.Join(runtimeDir, watchdogStampName)
	res := runWatchdog(watchdogDeps{
		now:           time.Now,
		disabled:      func() bool { return !config.Load().MonitorEnabled() },
		monitorAlive:  func() bool { return pidfile.Validate(runtimeDir, monitorPidName) },
		monitorStale:  func() bool { return monitorIsStale(runtimeDir) },
		killMonitor:   func() { pidfile.Kill(runtimeDir, monitorPidName) },
		sessionExists: func() bool { return tmux.HasSessionNamed(session) },
		lastAttempt: func() time.Time {
			if fi, err := os.Stat(stampPath); err == nil {
				return fi.ModTime()
			}
			return time.Time{}
		},
		tryAttempt: func(t time.Time) bool {
			return stamp.Try(stampPath, watchdogMinInterval, t)
		},
		markAttempt: func(t time.Time) {
			if err := os.WriteFile(stampPath, nil, 0o600); err == nil {
				_ = os.Chtimes(stampPath, t, t)
			}
		},
		spawn: func() error { return watchdogSpawnFn(projectRoot) },
	})
	switch res {
	case watchdogReplacedStale:
		logging.Log("monitor-watchdog: monitor binary stale, replaced it (session %s)", session)
	case watchdogSpawned, watchdogSpawnFailed:
		logging.Log("monitor-watchdog: monitor dead, session %s exists: %s", session, res)
	}
}
