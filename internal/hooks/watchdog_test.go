package hooks

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/buildid"
)

func TestRunWatchdog(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		alive       bool
		session     bool
		last        time.Time
		spawnErr    error
		want        watchdogResult
		wantSession bool // sessionExists consulted
		wantSpawn   bool
		wantMark    bool
	}{
		{"pid alive", true, true, time.Time{}, nil, watchdogAlive, false, false, false},
		{"dead + session", false, true, time.Time{}, nil, watchdogSpawned, true, true, true},
		{"dead + no session", false, false, time.Time{}, nil, watchdogNoSession, true, false, false},
		{"dead, rate limited", false, true, now.Add(-10 * time.Second), nil, watchdogRateLimited, false, false, false},
		{"dead, attempt long ago", false, true, now.Add(-time.Minute), nil, watchdogSpawned, true, true, true},
		{"spawn fails", false, true, time.Time{}, errors.New("boom"), watchdogSpawnFailed, true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sessionCalls, spawns int
			var marked time.Time
			got := runWatchdog(watchdogDeps{
				now:           func() time.Time { return now },
				monitorAlive:  func() bool { return tt.alive },
				sessionExists: func() bool { sessionCalls++; return tt.session },
				lastAttempt:   func() time.Time { return tt.last },
				markAttempt:   func(ts time.Time) { marked = ts },
				spawn:         func() error { spawns++; return tt.spawnErr },
			})
			if got != tt.want {
				t.Errorf("result = %s, want %s", got, tt.want)
			}
			if (sessionCalls > 0) != tt.wantSession {
				t.Errorf("session consulted = %v, want %v", sessionCalls > 0, tt.wantSession)
			}
			if (spawns > 0) != tt.wantSpawn {
				t.Errorf("spawned = %v, want %v", spawns > 0, tt.wantSpawn)
			}
			if !marked.IsZero() != tt.wantMark {
				t.Errorf("marked = %v, want %v", !marked.IsZero(), tt.wantMark)
			}
		})
	}
}

func TestRunWatchdogStale(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		stale     bool
		last      time.Time
		want      watchdogResult
		wantKill  bool
		wantSpawn bool
	}{
		{"alive and current", false, time.Time{}, watchdogAlive, false, false},
		{"stale, limit open", true, time.Time{}, watchdogReplacedStale, true, true},
		{"stale, rate limited", true, now.Add(-5 * time.Second), watchdogRateLimited, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var kills, spawns int
			var order []string
			got := runWatchdog(watchdogDeps{
				now:           func() time.Time { return now },
				monitorAlive:  func() bool { return true },
				monitorStale:  func() bool { return tt.stale },
				sessionExists: func() bool { return true },
				lastAttempt:   func() time.Time { return tt.last },
				markAttempt:   func(time.Time) {},
				killMonitor:   func() { kills++; order = append(order, "kill") },
				spawn:         func() error { spawns++; order = append(order, "spawn"); return nil },
			})
			if got != tt.want {
				t.Errorf("result = %s, want %s", got, tt.want)
			}
			if (kills > 0) != tt.wantKill || (spawns > 0) != tt.wantSpawn {
				t.Errorf("kills=%d spawns=%d, want kill=%v spawn=%v", kills, spawns, tt.wantKill, tt.wantSpawn)
			}
			if tt.wantKill && (len(order) != 2 || order[0] != "kill") {
				t.Errorf("order = %v, want kill then spawn", order)
			}
		})
	}
}

func TestStaleMonitor(t *testing.T) {
	rec := buildid.Identity{Path: "/bin/agentctl", Dev: 1, Ino: 2, Size: 3, MtimeUnix: 4, PID: 99}
	statAs := func(id buildid.Identity, err error) func(string) (buildid.Identity, error) {
		return func(string) (buildid.Identity, error) { return id, err }
	}
	changed := rec
	changed.Ino = 7
	tests := []struct {
		name string
		rec  buildid.Identity
		err  error
		pid  int
		exe  string
		stat func(string) (buildid.Identity, error)
		want bool
	}{
		{"current", rec, nil, 99, "/bin/agentctl", statAs(rec, nil), false},
		{"build file missing", buildid.Identity{}, os.ErrNotExist, 99, "/bin/agentctl", statAs(rec, nil), true},
		{"pid mismatch", rec, nil, 100, "/bin/agentctl", statAs(rec, nil), true},
		{"same path changed", rec, nil, 99, "/bin/agentctl", statAs(changed, nil), true},
		{"different path is current", rec, nil, 99, "/dev/agentctl", statAs(changed, nil), false},
		{"stat fails", rec, nil, 99, "/bin/agentctl", statAs(buildid.Identity{}, os.ErrNotExist), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := staleMonitor(tt.rec, tt.err, tt.pid, tt.exe, tt.stat); got != tt.want {
				t.Errorf("staleMonitor = %v, want %v", got, tt.want)
			}
		})
	}
}
