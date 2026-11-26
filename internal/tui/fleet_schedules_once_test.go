package tui

import (
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
)

// TestSchedOnceRowText: a one-shot row shows its summary, never an empty
// schedule, and a done one reads done/expired rather than "never".
func TestSchedOnceRowText(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.Local)
	at := time.Date(2026, 12, 1, 9, 0, 0, 0, time.Local)
	j := cron.Job{Name: "remind", At: cron.FormatAt(at), Dir: "/p", Agent: "a", Prompt: "p"}
	j.Normalize()
	done := j
	done.State, done.CompletedAt, done.Result = cron.StateCompleted, at.Add(time.Minute), cron.ResultFired
	expired := j
	expired.State = cron.StateExpired
	ov := cronedit.BuildOverview(cronedit.OverviewInput{Now: now, Jobs: []cron.Job{j, done, expired}})
	tests := []struct {
		st        cronedit.Status
		wantWords string
		wantNext  string
	}{
		{ov.Jobs[0], "once, Tue 1 Dec 09:00", "Tue 09:"},
		{ov.Jobs[1], "done, fired Tue 1 Dec 09:01", "done"},
		{ov.Jobs[2], "expired, missed Tue 1 Dec 09:00", "expired"},
	}
	for _, tt := range tests {
		if got := schedWords(tt.st); got != tt.wantWords {
			t.Errorf("schedWords = %q, want %q", got, tt.wantWords)
		}
		if got := schedNextText(tt.st, now); len(got) < len(tt.wantNext) || got[:len(tt.wantNext)] != tt.wantNext {
			t.Errorf("schedNextText = %q, want prefix %q", got, tt.wantNext)
		}
	}
}
