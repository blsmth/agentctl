package reconcile

import (
	"path/filepath"
	"time"

	"github.com/blsmth/agentctl/internal/approval"
	"github.com/blsmth/agentctl/internal/auditnarrate"
	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/llmusage"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/metrics/events"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/reaper"
	"github.com/blsmth/agentctl/internal/stamp"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/uploads"
	"github.com/blsmth/agentctl/internal/verdicts"
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
	// PruneEvents removes aged metrics event files (the serve prunes only
	// while alive, so housekeeping covers machines where it is not running).
	PruneEvents func(now time.Time)
	// PruneUploads removes phone-attached images older than uploads.MaxAge.
	PruneUploads func(now time.Time) (int, error)
	// PruneLLMUsage removes usage-ledger files older than llmusage.MaxAge.
	PruneLLMUsage func(now time.Time) (int, error)
	// PruneClosed removes closed-window ledger files older than
	// closedlog.MaxAge.
	PruneClosed func(now time.Time) (int, error)
	// PruneVerdicts removes review-verdict ledger files older than
	// verdicts.MaxAge.
	PruneVerdicts func(now time.Time) (int, error)
	// PruneLaunch removes old prompt and launch-script files from
	// <runtime dir>/launch (see tmux.PruneLaunchDir).
	PruneLaunch   func(now time.Time) (int, error)
	Records       func() map[string]windowstate.WindowState
	SessionLogDir func() string
	// BackfillTitles titles this session's live windows that never got a
	// short title (hooks.BackfillTitles, wired by internal/cmd so this
	// package stays clear of the hooks dependency tree). Nil skips it.
	BackfillTitles func(now time.Time)
}

// TitleBackfillInterval is how often housekeeping runs the title backfill.
const TitleBackfillInterval = 5 * time.Minute

// LLMUsagePruneInterval is how often housekeeping prunes the usage ledger.
const LLMUsagePruneInterval = 24 * time.Hour

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
		PruneEvents: func(now time.Time) {
			events.Prune(runtimeDir, now, events.Retention)
		},
		PruneUploads: func(now time.Time) (int, error) {
			return uploads.Prune(runtimeDir, now, uploads.MaxAge)
		},
		PruneLLMUsage: func(now time.Time) (int, error) {
			n, err := llmusage.Prune(now, llmusage.MaxAge)
			// Cached audit narratives age out with the usage ledger.
			m, _ := auditnarrate.Prune(now, auditnarrate.MaxAge)
			return n + m, err
		},
		PruneClosed: func(now time.Time) (int, error) {
			return closedlog.Prune(now, closedlog.MaxAge)
		},
		PruneVerdicts: func(now time.Time) (int, error) {
			return verdicts.Prune(now, verdicts.MaxAge)
		},
		PruneLaunch: func(now time.Time) (int, error) {
			return tmux.PruneLaunchDir(filepath.Join(runtimeDir, "launch"), now, tmux.LaunchFileMaxAge, tmux.RunningProcessArgs)
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
	if d.PruneEvents != nil {
		d.PruneEvents(now)
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
	if stamp.Try(filepath.Join(d.RuntimeDir, "prune-uploads.stamp"), PruneInterval, now) && d.PruneUploads != nil {
		n, err := d.PruneUploads(now)
		switch {
		case err != nil:
			logging.Log("janitor: uploads.Prune: %v", err)
		case n > 0:
			logging.Log("janitor: pruned %d phone upload(s) older than %s", n, uploads.MaxAge)
		}
	}
	if d.PruneLLMUsage != nil && stamp.Try(filepath.Join(d.RuntimeDir, "prune-llm-usage.stamp"), LLMUsagePruneInterval, now) {
		n, err := d.PruneLLMUsage(now)
		switch {
		case err != nil:
			logging.Log("janitor: llmusage.Prune: %v", err)
		case n > 0:
			logging.Log("janitor: pruned %d LLM usage file(s) older than %s", n, llmusage.MaxAge)
		}
	}
	if d.PruneClosed != nil && stamp.Try(filepath.Join(d.RuntimeDir, "prune-closed.stamp"), LLMUsagePruneInterval, now) {
		n, err := d.PruneClosed(now)
		switch {
		case err != nil:
			logging.Log("janitor: closedlog.Prune: %v", err)
		case n > 0:
			logging.Log("janitor: pruned %d closed-window ledger file(s) older than %s", n, closedlog.MaxAge)
		}
	}
	if d.PruneVerdicts != nil && stamp.Try(filepath.Join(d.RuntimeDir, "prune-verdicts.stamp"), LLMUsagePruneInterval, now) {
		n, err := d.PruneVerdicts(now)
		switch {
		case err != nil:
			logging.Log("janitor: verdicts.Prune: %v", err)
		case n > 0:
			logging.Log("janitor: pruned %d verdict ledger file(s) older than %s", n, verdicts.MaxAge)
		}
	}
	if d.PruneLaunch != nil && stamp.Try(filepath.Join(d.RuntimeDir, "prune-launch.stamp"), LLMUsagePruneInterval, now) {
		n, err := d.PruneLaunch(now)
		switch {
		case err != nil:
			logging.Log("janitor: pruning launch dir: %v", err)
		case n > 0:
			logging.Log("janitor: pruned %d launch file(s) older than %s", n, tmux.LaunchFileMaxAge)
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
	// Last: it is the only sweep that can wait on the network.
	if d.BackfillTitles != nil && stamp.Try(filepath.Join(d.RuntimeDir, "backfill-titles.stamp"), TitleBackfillInterval, now) {
		d.BackfillTitles(now)
	}
}
