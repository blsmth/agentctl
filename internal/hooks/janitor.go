package hooks

import (
	"path/filepath"
	"time"

	"github.com/blsmth/agentctl/internal/detach"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/metricsproc"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/stamp"
)

const (
	janitorStampName   = "janitor.stamp"
	janitorMinInterval = 60 * time.Second
)

// spawnJanitorFn starts the detached janitor process; a seam for tests.
var spawnJanitorFn = func(runtimeDir string) error {
	return detach.StartSelfPinned([]string{"reconcile", "--housekeeping"}, runtimeDir)
}

// janitor is called next to monitorWatchdog from the frequent, non-per-tool
// hooks. At most once a minute across all concurrent hooks it spawns a
// detached `agentctl reconcile --housekeeping` (Gone detection, orphan reaping,
// mailbox and session-log pruning), so housekeeping needs no monitor. It never
// blocks or fails the hook.
func janitor() {
	defer func() {
		if r := recover(); r != nil {
			logging.Log("janitor: recovered: %v", r)
		}
	}()
	janitorAt(paths.RuntimeDir(), time.Now())
}

// janitorAt is janitor for an explicit runtime dir and time.
func janitorAt(runtimeDir string, now time.Time) {
	if runtimeJanitor(runtimeDir, now, spawnJanitorFn) {
		// Same 60s stamp: make sure the per-machine metrics serve is up
		// (no-op unless metrics are enabled).
		ensureMetricsServeFn()
	}
}

// ensureMetricsServeFn starts the metrics serve when needed; a seam for tests.
var ensureMetricsServeFn = metricsproc.EnsureStarted

// runtimeJanitor spawns spawn at most once per janitorMinInterval for
// runtimeDir, reporting whether it did.
func runtimeJanitor(runtimeDir string, now time.Time, spawn func(string) error) bool {
	if runtimeDir == "" {
		return false
	}
	if !stamp.Try(filepath.Join(runtimeDir, janitorStampName), janitorMinInterval, now) {
		return false
	}
	if err := spawn(runtimeDir); err != nil {
		logging.Log("janitor: spawn: %v", err)
		return false
	}
	return true
}
