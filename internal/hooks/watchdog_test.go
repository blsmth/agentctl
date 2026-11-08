package hooks

import (
	"errors"
	"testing"
	"time"
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
