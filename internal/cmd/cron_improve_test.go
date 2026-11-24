package cmd

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/improve"
)

func TestImproveAgentConstantsAgree(t *testing.T) {
	if cron.ImproveAgent != improve.DefaultFleetAgent {
		t.Fatalf("cron.ImproveAgent %q != improve.DefaultFleetAgent %q", cron.ImproveAgent, improve.DefaultFleetAgent)
	}
}

func TestCronImproveOptions(t *testing.T) {
	scheduled := time.Date(2026, 11, 24, 22, 0, 0, 0, time.Local)
	job := cron.Job{Name: "fleet-improve", Schedule: "0 22 * * *", Dir: "/p", Mode: cron.ModeImprove,
		Model: "opus", Effort: "high", Backend: "none", Account: "personal", Prompt: "permission prompts"}
	got, err := cronImproveOptions(cron.LaunchRequest{Job: job, Scheduled: scheduled})
	if err != nil {
		t.Fatal(err)
	}
	want := improve.Options{
		Agent: improve.DefaultFleetAgent, Fleet: true, Since: 24 * time.Hour, Theme: "permission prompts",
		Model: "opus", Effort: "high", Backend: "none", Account: "personal",
		Dir: "/p", CallerDir: "/p", Unattended: true, ScheduledBy: "fleet-improve",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("opts = %+v\nwant %+v", got, want)
	}
	if got.Focus || got.DryRun {
		t.Error("a cron improve run must be unfocused and real")
	}

	job.Agent, job.Schedule = "brandon", "0 22 * * 1"
	got, _ = cronImproveOptions(cron.LaunchRequest{Job: job, Scheduled: scheduled})
	if got.Agent != "brandon" || got.Since != 7*24*time.Hour {
		t.Errorf("agent %q since %s", got.Agent, got.Since)
	}
}

func TestCronImproveMissingTemplate(t *testing.T) {
	stubCronTemplates(t)
	job := cron.Job{Name: "f", Schedule: "0 22 * * *", Dir: "/p", Mode: cron.ModeImprove, Template: "gone"}
	_, err := cronImproveOptions(cron.LaunchRequest{Job: job})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, cron.ErrTemplateNotFound) {
		t.Errorf("err %v is not cron.ErrTemplateNotFound", err)
	}
}

func TestCronAddImproveFleet(t *testing.T) {
	var launches []cron.LaunchRequest
	e := testCronEnv(t, &launches)
	e.validateAgent = func(string, string) error { return nil }
	var out bytes.Buffer
	err := e.add(&out, CronAddOptions{Name: "fleet-improve", Schedule: "0 22 * * *", Dir: t.TempDir(), Mode: cron.ModeImprove, Model: "opus"})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := e.jobsPath()
	jobs, err := cron.LoadJobs(path)
	if err != nil || len(jobs.Jobs) != 1 {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
	j := jobs.Jobs[0]
	if j.Mode != cron.ModeImprove || j.Overlap != cron.OverlapSkip || j.Prompt != "" || j.Agent != "" {
		t.Errorf("job = %+v", j)
	}

	// Edit keeps every other field.
	mode := cron.ModeWindow
	if err := e.edit(&out, CronEditOptions{Name: "fleet-improve", Mode: &mode}); err == nil {
		t.Error("switching to window mode without a prompt must fail validation")
	}
	timeout := "3h"
	if err := e.edit(&out, CronEditOptions{Name: "fleet-improve", Timeout: &timeout}); err != nil {
		t.Fatal(err)
	}
	jobs, _ = cron.LoadJobs(path)
	want := j
	want.Timeout = "3h"
	if jobs.Jobs[0].Mode != want.Mode || jobs.Jobs[0].Model != want.Model || jobs.Jobs[0].Timeout != "3h" {
		t.Errorf("after edit = %+v", jobs.Jobs[0])
	}

	// An add without --improve-fleet still needs a prompt.
	if err := e.add(&out, CronAddOptions{Name: "plain", Schedule: "0 22 * * *", Dir: t.TempDir(), Agent: "brandon"}); err == nil {
		t.Error("window job without a prompt accepted")
	}
}

func TestRenderCronListImprove(t *testing.T) {
	j := cron.Job{Name: "fleet-improve", Schedule: "0 22 * * *", Dir: "/p", Mode: cron.ModeImprove}
	j.Normalize()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.Local)
	if got := promptColumn(j, now); got != "fleet improve, last 24h" {
		t.Errorf("promptColumn = %q", got)
	}
	if got := agentColumn(j); got != "fleet-improver" {
		t.Errorf("agentColumn = %q", got)
	}
}
