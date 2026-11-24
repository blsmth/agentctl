package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/improve"
	"github.com/blsmth/agentctl/internal/improverun"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
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

func TestImproveRunDone(t *testing.T) {
	old := improveRunFinished
	finished := map[string]bool{"done": true, "open": false}
	improveRunFinished = func(id string) bool { return finished[id] }
	t.Cleanup(func() { improveRunFinished = old })
	for _, tc := range []struct {
		name string
		ws   windowstate.WindowState
		want bool
	}{
		{"finished improve run", windowstate.WindowState{Mode: "improve", ImproveRun: "done"}, true},
		{"unfinished improve run still blocks", windowstate.WindowState{Mode: "improve", ImproveRun: "open"}, false},
		{"improve window without a run", windowstate.WindowState{Mode: "improve"}, false},
		{"other modes are untouched", windowstate.WindowState{Mode: "adhoc", ImproveRun: "done"}, false},
	} {
		if got := improveRunDone(tc.ws); got != tc.want {
			t.Errorf("%s: improveRunDone = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// cronLaunchImprove maps the improve result onto the launch result and
// surfaces a refused second run.
func TestCronLaunchImprove(t *testing.T) {
	old, oldV, oldE := improveLaunch, validateCronAgent, ensureCronSession
	t.Cleanup(func() { improveLaunch, validateCronAgent, ensureCronSession = old, oldV, oldE })
	validateCronAgent = func(string, string) error { return nil }
	started := 0
	ensureCronSession = func(string, cronProject) error { started++; return nil }
	job := cron.Job{Name: "fleet-improve", Schedule: "0 22 * * *", Dir: "/p", Mode: cron.ModeImprove}
	job.Normalize()
	req := cron.LaunchRequest{Job: job, Scheduled: time.Now()}

	var got improve.Options
	improveLaunch = func(o improve.Options) (improve.Result, error) {
		got = o
		return improve.Result{RunID: "r1", WindowID: "@7", SessionID: "sess-1"}, nil
	}
	var res cron.LaunchResult
	if err := cronLaunchImprove(req, cronProject{session: "agentctl-p"}, &res); err != nil {
		t.Fatal(err)
	}
	if res != (cron.LaunchResult{WindowID: "@7", Session: "agentctl-p", SessionID: "sess-1"}) {
		t.Errorf("res = %+v", res)
	}
	if started != 1 {
		t.Errorf("session ensured %d times", started)
	}
	if !got.Unattended || got.ScheduledBy != "fleet-improve" || !got.Fleet {
		t.Errorf("opts = %+v", got)
	}

	improveLaunch = func(improve.Options) (improve.Result, error) { return improve.Result{}, improverun.ErrRunActive }
	if err := cronLaunchImprove(req, cronProject{session: "agentctl-p"}, &res); !errors.Is(err, improverun.ErrRunActive) {
		t.Errorf("err = %v, want ErrRunActive", err)
	}
}

// cronWindows must report an improve window whose run is finished as Done
// (so the next fire is not skipped as overlap), and an unfinished one as not.
func TestCronWindowsImproveDone(t *testing.T) {
	rt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rt, "windows"), 0o700); err != nil {
		t.Fatal(err)
	}
	for id, run := range map[string]string{"@1": "fin", "@2": "open"} {
		rec := fmt.Sprintf(`{"scheduled_by":"fleet-improve","mode":"improve","improve_run":%q}`, run)
		if err := os.WriteFile(filepath.Join(rt, "windows", id+".json"), []byte(rec), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldP, oldL, oldF := cronProjectFor, cronListWindows, improveRunFinished
	t.Cleanup(func() { cronProjectFor, cronListWindows, improveRunFinished = oldP, oldL, oldF })
	cronProjectFor = func(cron.Job) cronProject {
		return cronProject{root: filepath.Dir(rt), runtimeDir: rt, session: "agentctl-p"}
	}
	cronListWindows = func(string) ([]tmux.WindowInfo, error) {
		return []tmux.WindowInfo{{ID: "@1"}, {ID: "@2"}}, nil
	}
	improveRunFinished = func(id string) bool { return id == "fin" }

	ws, err := cronWindows([]cron.Job{{Name: "fleet-improve"}})
	if err != nil {
		t.Fatal(err)
	}
	done := map[string]bool{}
	for _, w := range ws {
		done[w.ID] = w.Done
	}
	if len(done) != 2 || !done["@1"] || done["@2"] {
		t.Errorf("done = %v, want @1 done and @2 not", done)
	}
}
