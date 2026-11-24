package shorttitle

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

// NewSessionReserve is the part of the hourly cap that only first titles for
// brand-new sessions and the Stop refinement may spend. Requests for restored
// or resumed windows and the housekeeping backfill cannot take the last
// NewSessionReserve slots, so a crash restore of dozens of windows never
// leaves new sessions untitled.
const NewSessionReserve = 30

// hourlyCapFn resolves the effective cap; tests replace it. Read per call
// (titles.hourly_cap, global-only), never at package init.
var hourlyCapFn = func() int { return config.Load().Titles.EffectiveHourlyCap() }

// HourlyCap caps title requests per rolling hour across every window and
// session on the machine, so a bug that keeps changing the input cannot run
// up a bill.
func HourlyCap() int { return hourlyCapFn() }

// stateDir is the state-dir resolver; tests replace it. Resolved per call,
// never at package init.
var stateDir = config.StateDir

// budgetPath is <stateDir>/titles/budget.json.
func budgetPath() (string, error) {
	d, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "titles", "budget.json"), nil
}

type budget struct {
	Calls []int64 `json:"calls"` // unix seconds
}

// TakeBudget records one request at now and reports true, unless the hourly
// cap was already reached in the hour before now. The read-modify-write runs
// under an exclusive flock on the budget file, so concurrent hooks cannot
// overspend. Any error returns false: a cap that cannot record itself must
// not let every caller through.
func TakeBudget(now time.Time) bool { return TakeBudgetReserve(now, 0) }

// TakeBudgetReserve is TakeBudget for a lower-priority request: it only
// spends while more than reserve slots of the hourly cap remain.
func TakeBudgetReserve(now time.Time, reserve int) bool {
	ok, err := takeBudget(now, HourlyCap()-reserve)
	return err == nil && ok
}

// TakeRestoredBudget is TakeBudgetReserve for a request that is not a new
// session's first title (resume or compact SessionStart): it leaves
// NewSessionReserve slots to new sessions.
func TakeRestoredBudget(now time.Time) bool {
	return TakeBudgetReserve(now, NewSessionReserve)
}

// BackfillReserve is the part of the hourly cap the housekeeping backfill
// leaves to the hooks: the new-session reserve plus 10 more for resumed
// windows, so titling old windows never starves live ones.
const BackfillReserve = NewSessionReserve + 10

// TakeBackfillBudget is TakeBudget for the housekeeping backfill: it only
// spends while more than BackfillReserve requests remain in the hour.
func TakeBackfillBudget(now time.Time) bool {
	return TakeBudgetReserve(now, BackfillReserve)
}

// takeBudget records one request at now unless limit requests already
// happened in the hour before now.
func takeBudget(now time.Time, limit int) (bool, error) {
	p, err := budgetPath()
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return false, fmt.Errorf("shorttitle: mkdir: %w", err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return false, fmt.Errorf("shorttitle: open budget: %w", err)
	}
	defer f.Close() //nolint:errcheck
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return false, fmt.Errorf("shorttitle: lock budget: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck

	var b budget
	if data, err := io.ReadAll(io.LimitReader(f, 64<<10)); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &b) // a corrupt file starts a fresh window
	}
	cutoff, nowS := now.Add(-time.Hour).Unix(), now.Unix()
	kept := b.Calls[:0]
	for _, c := range b.Calls {
		if c > cutoff && c <= nowS {
			kept = append(kept, c)
		}
	}
	b.Calls = kept
	if len(b.Calls) >= limit {
		return false, nil
	}
	b.Calls = append(b.Calls, nowS)
	data, err := json.Marshal(b)
	if err != nil {
		return false, err
	}
	if err := f.Truncate(0); err != nil {
		return false, err
	}
	if _, err := f.WriteAt(data, 0); err != nil {
		return false, err
	}
	return true, nil
}
