package cmd

import (
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

func TestRestoreStale(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	max := config.DefaultRestoreMaxAge
	tests := []struct {
		name   string
		age    time.Duration
		maxAge time.Duration
		want   bool
	}{
		{"fresh", time.Minute, max, false},
		{"old but within limit", 46 * time.Hour, max, false},
		{"beyond limit", 8 * 24 * time.Hour, max, true},
		{"gate disabled (zero)", 100 * 24 * time.Hour, 0, false},
		{"gate disabled (negative)", 100 * 24 * time.Hour, -time.Second, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restoreStale(now.Add(-tt.age), now, tt.maxAge); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
