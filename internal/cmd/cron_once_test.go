package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/cron"
)

func TestCronAddAt(t *testing.T) {
	tests := []struct {
		name     string
		schedule string
		at       string
		wantErr  string
		wantOut  string
	}{
		{name: "one-shot", at: "2026-12-01 09:00", wantOut: "Added remind (once, Tue 1 Dec 09:00), fires once at Tue 1 Dec 09:"},
		{name: "rfc3339", at: "2026-12-01T14:00:00Z", wantOut: "once, Tue 1 Dec 09:00"},
		{name: "neither", wantErr: "exactly one of --schedule"},
		{name: "both", schedule: "@daily", at: "2026-12-01 09:00", wantErr: "exactly one of --schedule"},
		{name: "past", at: "2026-11-16 09:00", wantErr: "in the past"},
		{name: "unparseable", at: "next tuesday", wantErr: "YYYY-MM-DD HH:MM"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var launches []cron.LaunchRequest
			e := testCronEnv(t, &launches)
			var out bytes.Buffer
			err := e.add(&out, CronAddOptions{Name: "remind", Schedule: tt.schedule, At: tt.at, Agent: "brandon", Dir: t.TempDir(), Prompt: "check the deploy"})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("out = %q, want %q", out.String(), tt.wantOut)
			}
		})
	}
}

// TestCronOnceLifecycle walks a one-shot through the CLI: add, manual run
// (completes it), resume refused, list shows it done, edit --at resets it,
// rm, then list --all and runs still describe it.
func TestCronOnceLifecycle(t *testing.T) {
	var launches []cron.LaunchRequest
	e := testCronEnv(t, &launches)
	dir := t.TempDir()
	var out bytes.Buffer
	if err := e.add(&out, CronAddOptions{Name: "remind", At: "2026-12-01 09:00", Agent: "brandon", Dir: dir, Prompt: "check the deploy"}); err != nil {
		t.Fatal(err)
	}
	if err := e.run(&out, "remind", false, false); err != nil || len(launches) != 1 {
		t.Fatalf("run: %v, launches %d", err, len(launches))
	}
	if err := e.run(&out, "remind", false, false); err == nil || !strings.Contains(err.Error(), "cron edit remind --at") {
		t.Fatalf("second run err = %v", err)
	}
	if err := e.setEnabled(&out, "remind", false); err != nil {
		t.Fatal(err)
	}
	if err := e.setEnabled(&out, "remind", true); err == nil || !strings.Contains(err.Error(), "cron edit remind --at") {
		t.Fatalf("resume err = %v", err)
	}

	out.Reset()
	if err := e.list(&out, false); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "done, fired Tue 17 Nov 09:00") || !strings.Contains(s, "fired ") {
		t.Errorf("list:\n%s", s)
	}

	at := "2026-12-02 10:00"
	if err := e.edit(&out, CronEditOptions{Name: "remind", At: &at}); err != nil {
		t.Fatal(err)
	}
	path, _ := e.jobsPath()
	jobs, _ := cron.LoadJobs(path)
	if j := jobs.Jobs[0]; j.State != "" || cron.OnceSummary(j) != "once, Wed 2 Dec 10:00" {
		t.Fatalf("after edit --at: %+v", j)
	}
	sched := "@daily"
	if err := e.edit(&out, CronEditOptions{Name: "remind", Schedule: &sched, At: &at}); err == nil {
		t.Error("edit with --schedule and --at accepted")
	}

	if err := e.remove(&out, "remind"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := e.list(&out, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "remind") {
		t.Errorf("list without --all shows a deleted job:\n%s", out.String())
	}
	out.Reset()
	if err := e.list(&out, true); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "remind") || !strings.Contains(s, "deleted") || !strings.Contains(s, "once, Wed 2 Dec 10:00") ||
		!strings.Contains(s, "fired ") || !strings.Contains(s, "check the deploy") {
		t.Errorf("list --all:\n%s", s)
	}

	out.Reset()
	if err := e.runs(&out, "remind", 0); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"created", "fired", "paused", "edited", "deleted", "by cli", "agent brandon", "prompt: check the deploy"} {
		if !strings.Contains(s, want) {
			t.Errorf("runs missing %q:\n%s", want, s)
		}
	}
}

func TestRenderCronRunsLifecycle(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.Local)
	j := cron.Job{Name: "n", Schedule: "0 9 * * *", Dir: "/p", Agent: "brandon", PromptKey: "spec"}
	j.Normalize()
	e := cron.LifecycleEntry(now, cron.ResultDeleted, j, cron.Actor{Kind: cron.ActorWindow, WindowID: "@4", SessionID: "abcdef0123"}, "")
	var out bytes.Buffer
	renderCronRuns(&out, []cron.Entry{e}, 0, now)
	if s := out.String(); !strings.Contains(s, "deleted") || !strings.Contains(s, "by window @4 (session abcdef01); 0 9 * * * · agent brandon · prompt: key spec · /p") {
		t.Errorf("runs:\n%s", s)
	}
}
