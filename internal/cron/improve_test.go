package cron

import (
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

func improveJob(name string) Job {
	j := Job{Name: name, Schedule: "0 22 * * *", Dir: "/tmp/p", Mode: ModeImprove, Model: "opus"}
	j.Normalize()
	return j
}

func TestImproveJobValidate(t *testing.T) {
	tests := []struct {
		name    string
		mut     func(j *Job)
		wantErr string // substring of the first error, "" = valid
	}{
		{name: "no prompt, no agent", mut: func(*Job) {}},
		{name: "theme prompt", mut: func(j *Job) { j.Prompt = "tighten permissions" }},
		{name: "template theme", mut: func(j *Job) { j.Template = "fleet-theme" }},
		{name: "named agent", mut: func(j *Job) { j.Agent = "brandon" }},
		{name: "bad agent name", mut: func(j *Job) { j.Agent = "../x" }, wantErr: "invalid agent"},
		{name: "two sources", mut: func(j *Job) { j.Prompt, j.PromptKey = "a", "b" }, wantErr: "not both"},
		{name: "overlap allow refused", mut: func(j *Job) { j.Overlap = OverlapAllow }, wantErr: "cannot overlap"},
		{name: "unknown mode", mut: func(j *Job) { j.Mode, j.Agent, j.Prompt = "headless", "a", "p" }, wantErr: "mode"},
		{name: "window job still needs a prompt", mut: func(j *Job) { j.Mode, j.Agent = ModeWindow, "a" }, wantErr: "prompt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := improveJob("fleet")
			tt.mut(&j)
			err := j.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate = %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("Validate = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestImproveJobDefaultsToSkipOverlap(t *testing.T) {
	j := Job{Name: "f", Schedule: "0 22 * * *", Dir: "/tmp/p", Mode: ModeImprove}
	j.Normalize()
	if j.Overlap != OverlapSkip {
		t.Errorf("overlap = %q, want skip", j.Overlap)
	}
}

func TestImproveSince(t *testing.T) {
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		schedule string
		want     time.Duration
	}{
		{"0 22 * * *", 24 * time.Hour},
		{"0 22 * * 1", 7 * 24 * time.Hour},
		{"*/30 * * * *", time.Hour},          // floored
		{"0 22 29 2 *", 30 * 24 * time.Hour}, // capped
		{"nonsense", DefaultImproveSince},
	} {
		j := improveJob("f")
		j.Schedule = tc.schedule
		if got := j.ImproveSince(at); got != tc.want {
			t.Errorf("ImproveSince(%q) = %s, want %s", tc.schedule, got, tc.want)
		}
	}
}

func TestImproveSummary(t *testing.T) {
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.Local)
	j := improveJob("f")
	if got, want := j.ImproveSummary(at), "Fleet improve, last 24h, agent fleet-improver, model opus"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	j.Schedule, j.Model, j.Agent = "0 22 * * 1", "", "brandon"
	if got, want := j.ImproveSummary(at), "Fleet improve, last 7d, agent brandon, model default"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

// A due improve job goes through the same Launch seam as any job (so the
// job's overlap, usage gate and timeout apply), and a still-active previous
// run skips the fire.
func TestTickFiresImproveJob(t *testing.T) {
	job := improveJob("fleet-improve")
	jit := Jitter(job.Name)
	// Fires at 22:00 plus jitter; tick shortly after.
	now := at(22, 0).Add(jit + 30*time.Second)

	t.Run("fires", func(t *testing.T) {
		dir := t.TempDir()
		seedState(t, dir, map[string]time.Time{job.Name: at(21, 0)})
		f := &fakeEnv{}
		if _, err := Tick(f.deps(dir, now, config.CronConfig{}, job)); err != nil {
			t.Fatal(err)
		}
		if len(f.launches) != 1 || !f.launches[0].Job.IsImprove() {
			t.Fatalf("launches = %+v", f.launches)
		}
		runs, _ := ReadRuns(dir, job.Name)
		if len(runs) != 1 || runs[0].Result != ResultFired || runs[0].SessionID == "" || runs[0].WindowID == "" {
			t.Errorf("runs = %+v, want one fired entry with window and session id", runs)
		}
	})

	t.Run("previous run still active skips", func(t *testing.T) {
		dir := t.TempDir()
		seedState(t, dir, map[string]time.Time{job.Name: at(21, 0)})
		f := &fakeEnv{windows: []Window{{ID: "@3", Job: job.Name}}}
		if _, err := Tick(f.deps(dir, now, config.CronConfig{}, job)); err != nil {
			t.Fatal(err)
		}
		if len(f.launches) != 0 {
			t.Fatalf("launched %d with a live previous run", len(f.launches))
		}
		runs, _ := ReadRuns(dir, job.Name)
		if len(runs) != 1 || runs[0].Result != ResultSkippedOverlap {
			t.Errorf("runs = %+v, want skipped:overlap", runs)
		}
	})
}
