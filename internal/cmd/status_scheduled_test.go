package cmd

import (
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/fleet"
)

func TestStatusScheduled(t *testing.T) {
	fired := time.Date(2026, 11, 18, 7, 0, 0, 0, time.Local)
	tests := []struct {
		name       string
		meta       fleet.Meta
		wantMode   string
		wantDetail string
	}{
		{"cron", fleet.Meta{Mode: "adhoc", ScheduledBy: "nightly-triage", ScheduledAt: fired}, "cron", "nightly-triage  fired Wed 18 Nov 07:00"},
		{"cron no time", fleet.Meta{Mode: "adhoc", ScheduledBy: "nightly-triage"}, "cron", "nightly-triage"},
		{"dispatch", fleet.Meta{Mode: "dispatch"}, "dispatch", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusMode(tt.meta); got != tt.wantMode {
				t.Errorf("statusMode = %q, want %q", got, tt.wantMode)
			}
			if got := scheduledDetail(tt.meta); got != tt.wantDetail {
				t.Errorf("scheduledDetail = %q, want %q", got, tt.wantDetail)
			}
		})
	}
}
