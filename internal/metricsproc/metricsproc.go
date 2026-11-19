// Package metricsproc is the lifecycle plumbing for the per-machine
// `agentctl metrics serve` process: where its pidfile and lock live, whether it
// is running, and starting it detached. It is deliberately light (no tmux,
// fleet or metrics imports) so hooks and session start can use it.
package metricsproc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/detach"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/pidfile"
)

const (
	// PidName is the pidfile name under the state dir.
	PidName = "metrics-serve.pid"
	// LockName is the flock file guarding "one serve per machine".
	LockName = "metrics-serve.lock"
)

// Seams, replaced in tests.
var (
	stateDir = func() (string, error) { return config.StateDir() }
	enabled  = func() bool { return config.Load().EffectiveMetricsConfig().Enabled }
	spawn    = func() error { return detach.StartDaemon([]string{"metrics", "serve"}, "") }
)

// SetSeamsForTest overrides the seams and returns a restore func.
func SetSeamsForTest(dir func() (string, error), en func() bool, sp func() error) (restore func()) {
	od, oe, os_ := stateDir, enabled, spawn
	if dir != nil {
		stateDir = dir
	}
	if en != nil {
		enabled = en
	}
	if sp != nil {
		spawn = sp
	}
	return func() { stateDir, enabled, spawn = od, oe, os_ }
}

// Dir returns the directory holding the pidfile and lock.
func Dir() (string, error) {
	d, err := stateDir()
	if err != nil || d == "" {
		return "", fmt.Errorf("metricsproc: no state dir: %v", err)
	}
	return d, nil
}

// Running reports the pid of a live metrics serve.
func Running() (int, bool) {
	d, err := Dir()
	if err != nil {
		return 0, false
	}
	return pidfile.Running(d, PidName, "metrics")
}

// EnsureStarted starts the metrics serve detached when metrics are enabled and
// none is alive, reporting whether it spawned one. Concurrent callers may both
// spawn; the serve takes a machine-wide lock and the loser exits 0.
func EnsureStarted() bool {
	if os.Getenv("AGENTCTL_TEST") == "1" || !enabled() {
		return false
	}
	if _, ok := Running(); ok {
		return false
	}
	if err := spawn(); err != nil {
		logging.Log("metrics: starting serve: %v", err)
		return false
	}
	return true
}

// Acquire takes the machine-wide serve lock (non-blocking) and writes the
// pidfile. ok is false when another serve holds the lock. release removes the
// pidfile and drops the lock.
func Acquire() (release func(), ok bool, err error) {
	d, err := Dir()
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return nil, false, fmt.Errorf("metricsproc: mkdir %s: %w", d, err)
	}
	f, err := os.OpenFile(filepath.Join(d, LockName), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("metricsproc: opening lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close() //nolint:errcheck
		return nil, false, nil
	}
	if err := pidfile.Write(d, PidName); err != nil {
		logging.Log("metrics: %v", err)
	}
	// A stale port from a previous serve must not be reported for this one.
	os.Remove(filepath.Join(d, PortName)) //nolint:errcheck
	return func() {
		os.Remove(filepath.Join(d, PortName)) //nolint:errcheck
		pidfile.Remove(d, PidName)
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
		f.Close()                                   //nolint:errcheck
	}, true, nil
}

// PortName is the file under the state dir holding the port the serve actually
// bound for /metrics (absent when the bind failed or is disabled).
const PortName = "metrics-serve.port"

// WritePort records the port the running serve bound.
func WritePort(port int) {
	d, err := Dir()
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(d, PortName), []byte(strconv.Itoa(port)+"\n"), 0o600); err != nil {
		logging.Log("metrics: writing port file: %v", err)
	}
}

// BoundPort returns the port the serve recorded, or 0 when it has none.
func BoundPort() int {
	d, err := Dir()
	if err != nil {
		return 0
	}
	b, err := os.ReadFile(filepath.Join(d, PortName))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
