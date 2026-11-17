package tui

import (
	"hash/fnv"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/selfexec"
)

const (
	// reloadCheckInterval is how often a pane stats its own binary.
	reloadCheckInterval = 3 * time.Second
	// reloadMaxJitter bounds the per-window delay between detecting a new
	// binary and quitting, so dozens of panes do not re-exec in one instant.
	reloadMaxJitter = 2 * time.Second
)

// reloadJitter returns a deterministic delay in [0, reloadMaxJitter) derived
// from key (a window or pane ID).
func reloadJitter(key string) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return time.Duration(h.Sum32()) % reloadMaxJitter
}

// reloader decides when a long-lived pane should quit so its command can
// re-exec the new binary. The model quits cleanly first; the caller execs
// after tea.Program.Run returns (see cmd.runWithReload).
type reloader struct {
	changed   func() bool // nil disables hot reload
	now       func() time.Time
	jitter    time.Duration
	lastCheck time.Time
	detected  time.Time
	fired     bool
}

// newReloader captures the running binary's identity. On error hot reload is
// disabled for this process.
func newReloader(name, key string) *reloader {
	r := &reloader{now: time.Now, jitter: reloadJitter(key)}
	id, err := selfexec.Capture()
	if err != nil {
		logging.Log("%s: hot reload disabled: %v", name, err)
		return r
	}
	r.changed = func() bool { return selfexec.CheckChanged(id, time.Now()) }
	return r
}

// due reports whether the pane should quit now for a re-exec. It is cheap to
// call on every tick: the binary is stat'd at most once per check interval.
func (r *reloader) due() bool {
	if r == nil || r.changed == nil || r.fired {
		return false
	}
	now := r.now()
	if r.detected.IsZero() {
		if !r.lastCheck.IsZero() && now.Sub(r.lastCheck) < reloadCheckInterval {
			return false
		}
		r.lastCheck = now
		if !r.changed() {
			return false
		}
		r.detected = now
	}
	if now.Sub(r.detected) < r.jitter {
		return false
	}
	r.fired = true
	return true
}

// triggered reports whether the pane quit in order to re-exec.
func (r *reloader) triggered() bool { return r != nil && r.fired }

// disable turns hot reload off (after a failed exec).
func (r *reloader) disable() {
	if r != nil {
		r.changed, r.fired = nil, false
	}
}
