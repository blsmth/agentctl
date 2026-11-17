package reconcile

import (
	"path/filepath"
	"time"

	"github.com/blsmth/agentctl/internal/approval"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/reaper"
	"github.com/blsmth/agentctl/internal/stamp"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// PruneInterval is how often the mailbox and session-log sweeps run (each has
// its own stamp), mirroring the monitor's ~5 minute cadence.
const PruneInterval = 5 * time.Minute

// HousekeepingDeps are the periodic sweeps the janitor runs beyond Run.
type HousekeepingDeps struct {
	RuntimeDir       string
	Now              func() time.Time
	ReapOrphans      func()
	PruneMailbox     func() (int, error)
	PruneSessionLogs func(protected []string) (int, error)
	Records          func() map[string]windowstate.WindowState
	SessionLogDir    func() string
}

// DefaultHousekeepingDeps wires the sweeps to the real implementations for
// the current session.
func DefaultHousekeepingDeps(runtimeDir string) HousekeepingDeps {
	return HousekeepingDeps{
		RuntimeDir:  runtimeDir,
		Now:         time.Now,
		ReapOrphans: func() { reaper.ReapOrphans(tmux.SessionName, paths.SettingsDir()) },
		PruneMailbox: func() (int, error) {
			return mailbox.PruneStale(mailbox.MaxPendingAge)
		},
		PruneSessionLogs: func(protected []string) (int, error) {
			return logging.PruneSessionLogs(logging.MaxSessionLogAge, protected)
		},
		Records:       windowstate.All,
		SessionLogDir: paths.SessionLogDir,
	}
}

// Housekeep runs the janitor's periodic work: orphan reaping (every run, the
// janitor itself is throttled to about a minute), approval-marker GC, and the
// mailbox and session-log sweeps, each behind its own PruneInterval stamp so
// concurrent janitors sweep once. Every failure is logged, never fatal.
func Housekeep(d HousekeepingDeps) {
	if d.RuntimeDir == "" {
		return
	}
	now := d.Now()
	if d.ReapOrphans != nil {
		d.ReapOrphans()
	}
	if n := approval.GC(d.RuntimeDir, now); n > 0 {
		logging.Log("janitor: removed %d stale approval-pending marker(s)", n)
	}
	if stamp.Try(filepath.Join(d.RuntimeDir, "prune-mailbox.stamp"), PruneInterval, now) && d.PruneMailbox != nil {
		// TTL expiry inside mailbox.DrainFor is lazy, so mail for a window that
		// closed (or whose @N was recycled) would rot without this sweep.
		n, err := d.PruneMailbox()
		switch {
		case err != nil:
			logging.Log("janitor: mailbox.PruneStale: %v", err)
		case n > 0:
			logging.Warn("janitor: pruned %d stale pending mailbox message(s) older than %s", n, mailbox.MaxPendingAge)
		}
	}
	if stamp.Try(filepath.Join(d.RuntimeDir, "prune-session-logs.stamp"), PruneInterval, now) && d.PruneSessionLogs != nil {
		protected := ProtectedSessionLogs(d.SessionLogDir(), d.Records())
		n, err := d.PruneSessionLogs(protected)
		switch {
		case err != nil:
			logging.Log("janitor: logging.PruneSessionLogs: %v", err)
		case n > 0:
			logging.Log("janitor: pruned %d stale session log file(s) older than %s", n, logging.MaxSessionLogAge)
		}
	}
}
