package cron

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/safefs"
)

// File names under <StateDir>/cron.
const (
	lockFile  = "tick.lock"
	stateFile = "state.json"
	runsFile  = "runs.ndjson"
)

// Dir returns <StateDir>/cron.
func Dir() (string, error) {
	sd, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(sd, "cron"), nil
}

// JobState is the per-job tick state.
type JobState struct {
	// LastFire is the newest scheduled time already handled (fired or
	// skipped). It is persisted before a launch, so a crash cannot fire the
	// same scheduled time twice.
	LastFire time.Time `json:"last_fire,omitzero"`
	// TimedOut lists windows this job's timeout already interrupted, so a
	// window gets one C-c, not one per tick.
	TimedOut []string `json:"timed_out,omitempty"`
}

// State is <StateDir>/cron/state.json.
type State struct {
	// LastTick is when a tick last ran (health reports its age).
	LastTick time.Time           `json:"last_tick,omitzero"`
	Jobs     map[string]JobState `json:"jobs"`
}

// LoadState reads state.json from dir. A missing file is an empty state; a
// corrupt one is reported and replaced by an empty state, since every field
// can be rebuilt (a lost last_fire is re-seeded, never back-filled).
func LoadState(dir string) (*State, error) {
	s := &State{Jobs: map[string]JobState{}}
	data, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("reading cron state: %w", err)
	}
	if err := json.Unmarshal(data, s); err != nil {
		return &State{Jobs: map[string]JobState{}}, fmt.Errorf("parsing cron state: %w", err)
	}
	if s.Jobs == nil {
		s.Jobs = map[string]JobState{}
	}
	return s, nil
}

// SaveState writes state.json atomically.
func SaveState(dir string, s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding cron state: %w", err)
	}
	if err := safefs.ReplaceFile(filepath.Join(dir, stateFile), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing cron state: %w", err)
	}
	return nil
}

// Lock takes an exclusive flock on <dir>/tick.lock. With wait false it
// returns ok=false at once when another process holds it (a tick that finds
// one running just exits); with wait true it blocks (a manual `cron run` or
// `cron add` waits for the tick to finish).
func Lock(dir string, wait bool) (unlock func(), ok bool, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, fmt.Errorf("creating %s: %w", dir, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("opening cron lock: %w", err)
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close() //nolint:errcheck
		if !wait {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("locking cron state: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
		f.Close()                                   //nolint:errcheck
	}, true, nil
}
