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

// HourlyBudget caps title requests per rolling hour across every window and
// session on the machine, so a bug that keeps changing the input cannot run
// up a bill.
const HourlyBudget = 30

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

// TakeBudget records one request at now and reports true, unless
// HourlyBudget requests already happened in the hour before now. The
// read-modify-write runs under an exclusive flock on the budget file, so
// concurrent hooks cannot overspend. Any error returns false: a cap that
// cannot record itself must not let every caller through.
func TakeBudget(now time.Time) bool {
	ok, err := takeBudget(now, HourlyBudget)
	return err == nil && ok
}

// BackfillReserve is the part of HourlyBudget the housekeeping backfill
// leaves to the hooks, so titling old windows never starves new ones.
const BackfillReserve = 10

// TakeBackfillBudget is TakeBudget for the housekeeping backfill: it only
// spends while more than BackfillReserve requests remain in the hour.
func TakeBackfillBudget(now time.Time) bool {
	ok, err := takeBudget(now, HourlyBudget-BackfillReserve)
	return err == nil && ok
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
