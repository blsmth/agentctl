package hooks

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestJanitorConcurrentAttemptsSpawnOnce(t *testing.T) {
	rd := t.TempDir()
	var spawns atomic.Int32
	spawn := func(string) error { spawns.Add(1); return nil }
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); runtimeJanitor(rd, now, spawn) }()
	}
	wg.Wait()
	if got := spawns.Load(); got != 1 {
		t.Fatalf("spawns = %d, want 1", got)
	}
}

func TestJanitorThrottleAndNoRuntimeDir(t *testing.T) {
	rd := t.TempDir()
	var spawns int
	spawn := func(string) error { spawns++; return nil }
	now := time.Now()
	for i, tc := range []struct {
		at   time.Time
		dir  string
		want bool
	}{
		{now, rd, true},
		{now.Add(30 * time.Second), rd, false},
		{now.Add(61 * time.Second), rd, true},
		{now.Add(2 * time.Hour), "", false},
	} {
		if got := runtimeJanitor(tc.dir, tc.at, spawn); got != tc.want {
			t.Errorf("case %d: got %v, want %v", i, got, tc.want)
		}
	}
	if spawns != 2 {
		t.Errorf("spawns = %d, want 2", spawns)
	}
}

func TestWatchdogTryAttemptLosesRace(t *testing.T) {
	d := watchdogDeps{
		now:           time.Now,
		monitorAlive:  func() bool { return false },
		sessionExists: func() bool { return true },
		lastAttempt:   func() time.Time { return time.Time{} },
		markAttempt:   func(time.Time) { t.Error("markAttempt used with tryAttempt set") },
		tryAttempt:    func(time.Time) bool { return false },
		spawn:         func() error { t.Error("spawned after losing the attempt race"); return nil },
	}
	if got := runWatchdog(d); got != watchdogRateLimited {
		t.Fatalf("result = %s, want %s", got, watchdogRateLimited)
	}
}

// The janitor starts the metrics serve on the same stamp as housekeeping: once
// per interval, not on every hook.
func TestJanitorEnsuresMetricsServeOncePerStamp(t *testing.T) {
	rd := t.TempDir()
	oj, oe := spawnJanitorFn, ensureMetricsServeFn
	t.Cleanup(func() { spawnJanitorFn, ensureMetricsServeFn = oj, oe })
	spawnJanitorFn = func(string) error { return nil }
	var ensured int
	ensureMetricsServeFn = func() bool { ensured++; return false }

	now := time.Now()
	janitorAt(rd, now)
	janitorAt(rd, now.Add(10*time.Second))
	janitorAt(rd, now.Add(61*time.Second))
	if ensured != 2 {
		t.Errorf("ensure calls = %d, want 2", ensured)
	}
}
