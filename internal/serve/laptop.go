package serve

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/tmux"
)

const (
	// laptopCacheTTL is how long one probe answer is reused. Push decisions
	// happen per event, so the probe must not run per event.
	laptopCacheTTL = 5 * time.Second
	// laptopProbeTimeout bounds one ioreg or tmux call.
	laptopProbeTimeout = time.Second
)

// LaptopIdleFunc reports how long the laptop has gone without human input.
// An error means no signal is available (no ioreg, no tmux server, no
// agentctl client attached); the laptop then counts as not active, so the
// phone keeps getting pushes.
type LaptopIdleFunc func(ctx context.Context) (time.Duration, error)

// errNoLaptopSignal: nothing reports laptop input.
var errNoLaptopSignal = errors.New("no laptop activity signal")

// laptopIdleProbe is the production probe used when Options.LaptopIdle is
// nil. It is a variable so this package's tests replace it and never run
// ioreg or tmux.
var laptopIdleProbe LaptopIdleFunc = newSystemIdleProbe(runtime.GOOS, runCommand, tmux.ListClientsTimeout, time.Now)

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// newSystemIdleProbe returns the probe for goos. On macOS the HID idle time
// (any keyboard, mouse or trackpad input, in any app) is authoritative; tmux
// is consulted only when ioreg fails. Elsewhere, and as that fallback, the
// most recent input on an agentctl tmux client counts. Phone terminal clients
// and control-mode clients are not the laptop and are ignored.
//
// tmux is not consulted when ioreg works because a client attached over ssh
// (from the phone, say) is not the person at the laptop.
func newSystemIdleProbe(
	goos string,
	run func(ctx context.Context, name string, args ...string) ([]byte, error),
	listClients func(time.Duration) ([]tmux.Client, error),
	now func() time.Time,
) LaptopIdleFunc {
	return func(ctx context.Context) (time.Duration, error) {
		if goos == "darwin" {
			idle, err := hidIdle(ctx, run)
			if err == nil {
				return idle, nil
			}
			logging.Debug("serve: laptop idle: ioreg: %v; falling back to tmux clients", err)
		}
		clients, err := listClients(laptopProbeTimeout)
		if err != nil {
			return 0, fmt.Errorf("listing tmux clients: %w", err)
		}
		return tmuxIdle(clients, now())
	}
}

// hidIdle reads HIDIdleTime (nanoseconds) from `ioreg -c IOHIDSystem`.
func hidIdle(ctx context.Context, run func(ctx context.Context, name string, args ...string) ([]byte, error)) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, laptopProbeTimeout)
	defer cancel()
	out, err := run(ctx, "ioreg", "-c", "IOHIDSystem", "-d", "4", "-r", "-k", "HIDIdleTime")
	if err != nil {
		return 0, err
	}
	return parseHIDIdle(string(out))
}

// parseHIDIdle finds the first `"HIDIdleTime" = <ns>` line.
func parseHIDIdle(out string) (time.Duration, error) {
	for _, line := range strings.Split(out, "\n") {
		_, rest, ok := strings.Cut(line, `"HIDIdleTime"`)
		if !ok {
			continue
		}
		_, val, ok := strings.Cut(rest, "=")
		if !ok {
			continue
		}
		ns, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil || ns < 0 {
			continue
		}
		return time.Duration(ns), nil
	}
	return 0, errors.New("no HIDIdleTime in ioreg output")
}

// tmuxIdle is the time since the most recent input on a laptop tmux client
// in an agentctl session (project sessions, the hub and its linked viewer
// sessions all start with "agentctl-"). Phone viewer clients and control-mode
// clients are skipped. No such client is errNoLaptopSignal.
func tmuxIdle(clients []tmux.Client, now time.Time) (time.Duration, error) {
	var latest int64
	found := false
	for _, c := range clients {
		if !c.IsCandidateViewer() || !strings.HasPrefix(c.Session, "agentctl-") || c.Activity <= 0 {
			continue
		}
		if !found || c.Activity > latest {
			latest, found = c.Activity, true
		}
	}
	if !found {
		return 0, errNoLaptopSignal
	}
	return max(now.Sub(time.Unix(latest, 0)), 0), nil
}

// laptopActivity answers "is someone at the laptop?" from a probe, cached for
// laptopCacheTTL. It is safe for concurrent use; concurrent callers on an
// expired cache wait for one probe instead of each running their own.
type laptopActivity struct {
	probe     LaptopIdleFunc
	threshold time.Duration
	now       func() time.Time

	mu       sync.Mutex
	probedAt time.Time
	active   bool
}

func newLaptopActivity(probe LaptopIdleFunc, threshold time.Duration, now func() time.Time) *laptopActivity {
	if now == nil {
		now = time.Now
	}
	return &laptopActivity{probe: probe, threshold: threshold, now: now}
}

// Active reports whether the laptop had input within the threshold. A nil
// receiver (the feature is off) is never active.
func (l *laptopActivity) Active() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if !l.probedAt.IsZero() && now.Sub(l.probedAt) < laptopCacheTTL && !now.Before(l.probedAt) {
		return l.active
	}
	idle, err := l.probe(context.Background())
	if err != nil {
		logging.Debug("serve: laptop idle: %v", err)
	}
	l.active = err == nil && idle < l.threshold
	l.probedAt = now
	return l.active
}
