package cron

import (
	"strings"
	"testing"
	"time"
)

func TestDescribe(t *testing.T) {
	tests := []struct {
		spec, want string
	}{
		{"30 7 * * 1-5", "weekdays 07:30"},
		{"30 7 * * MON-FRI", "weekdays 07:30"},
		{"0 9 * * *", "daily 09:00"},
		{"15 8 * * 1", "Mondays 08:15"},
		{"15 8 * * sun", "Sundays 08:15"},
		{"0 10 * * 0,6", "weekends 10:00"},
		{"0 6 * * 1,3,5", "Mon, Wed, Fri 06:00"},
		{"5 * * * *", "hourly at :05"},
		{"@daily", "daily 00:00"},
		{"@hourly", "hourly at :00"},
		{"@weekly", "Sundays 00:00"},
		// Shapes it does not know stay undescribed rather than guessed.
		{"*/15 * * * *", ""},
		{"0 9 1 * *", ""},
		{"0 9-17 * * *", ""},
		{"5 * * * 1", ""},
		{"0 9 * * 1-3", ""},
		{"garbage", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			if got := Describe(tt.spec); got != tt.want {
				t.Errorf("Describe(%q) = %q, want %q", tt.spec, got, tt.want)
			}
		})
	}
}

func TestSummary(t *testing.T) {
	// Tuesday 17 Nov 2026, 12:00 local.
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	tests := []struct {
		name, spec, wantPrefix, wantNext, wantErr string
	}{
		{name: "weekdays", spec: "30 7 * * 1-5", wantPrefix: "30 7 * * 1-5 (weekdays 07:30), next: ", wantNext: "Wed 18 Nov 07:"},
		{name: "undescribed", spec: "*/15 * * * *", wantPrefix: "*/15 * * * *, next: ", wantNext: "Tue 17 Nov 12:"},
		{name: "invalid", spec: "61 * * * *", wantErr: "invalid schedule"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Summary(tt.spec, "job", now)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !strings.HasPrefix(got, tt.wantPrefix) || !strings.Contains(got, tt.wantNext) {
				t.Errorf("Summary = %q, want prefix %q containing %q", got, tt.wantPrefix, tt.wantNext)
			}
		})
	}
}
