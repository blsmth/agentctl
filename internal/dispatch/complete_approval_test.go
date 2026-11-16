package dispatch

import (
	"testing"
	"time"
)

func TestWaitForApprovals(t *testing.T) {
	tests := []struct {
		name       string
		pendingFor int // number of checks that report pending (-1 = forever)
		wantOpen   bool
		wantSleeps int
		nilCheck   bool
	}{
		{name: "nil check opens", nilCheck: true, wantOpen: true},
		{name: "none pending", pendingFor: 0, wantOpen: true, wantSleeps: 0},
		{name: "clears after two", pendingFor: 2, wantOpen: true, wantSleeps: 2},
		{name: "forever skips", pendingFor: -1, wantOpen: false, wantSleeps: approvalDeferRetries},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checks, sleeps := 0, 0
			h := &completeHandler{sleep: func(time.Duration) { sleeps++ }}
			if !tc.nilCheck {
				h.approvalPending = func() bool {
					checks++
					return tc.pendingFor < 0 || checks <= tc.pendingFor
				}
			}
			if got := h.waitForApprovals(1); got != tc.wantOpen {
				t.Fatalf("open = %v, want %v", got, tc.wantOpen)
			}
			if sleeps != tc.wantSleeps {
				t.Fatalf("sleeps = %d, want %d", sleeps, tc.wantSleeps)
			}
		})
	}
}
