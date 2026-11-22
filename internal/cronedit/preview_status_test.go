package cronedit

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
)

func TestPreview(t *testing.T) {
	ts := newTestStore(t) // Tue 17 Nov 2026 09:00
	jitter := cron.Jitter("triage")

	t.Run("picker model sets the schedule", func(t *testing.T) {
		p := ts.Preview(ts.job("triage"), "", &SimpleSchedule{Kind: KindWeekdays, Minute: 30, Hour: 7})
		if !p.Valid || p.Schedule != "30 7 * * 1-5" || p.Words != "weekdays 07:30" || p.Simple == nil || p.Simple.Kind != KindWeekdays {
			t.Fatalf("preview = %+v", p)
		}
		want := []time.Time{
			time.Date(2026, 11, 18, 7, 30, 0, 0, time.Local).Add(jitter),
			time.Date(2026, 11, 19, 7, 30, 0, 0, time.Local).Add(jitter),
			time.Date(2026, 11, 20, 7, 30, 0, 0, time.Local).Add(jitter),
		}
		if !slices.EqualFunc(p.NextFires, want, time.Time.Equal) {
			t.Errorf("next fires = %v, want %v", p.NextFires, want)
		}
		if len(p.NextFiresText) != 3 || !strings.HasPrefix(p.NextFiresText[0], "Wed 18 Nov 07:3") || p.TimeZone == "" {
			t.Errorf("text = %v zone %q", p.NextFiresText, p.TimeZone)
		}
	})
	t.Run("custom expression has no picker model", func(t *testing.T) {
		j := ts.job("triage")
		j.Schedule = "*/30 9-17 * * 1-5"
		p := ts.Preview(j, "", nil)
		if !p.Valid || p.Simple != nil || p.Words != "" || len(p.NextFires) != 3 {
			t.Errorf("preview = %+v", p)
		}
	})
	t.Run("errors are listed, never fatal", func(t *testing.T) {
		j := ts.job("triage")
		j.Agent, j.Schedule = "nobody", "nope"
		p := ts.Preview(j, "", nil)
		if p.Valid || !slices.Equal(fields(p.Errors), []string{"agent", "schedule"}) || len(p.NextFires) != 0 || p.NextFires == nil {
			t.Errorf("preview = %+v", p)
		}
		p = ts.Preview(ts.job("triage"), "", &SimpleSchedule{Kind: KindDays, Hour: 7})
		if p.Valid || !slices.Contains(fields(p.Errors), "schedule") {
			t.Errorf("empty days: %+v", p.Errors)
		}
	})
	t.Run("edit preview lists changes and rejects a rename", func(t *testing.T) {
		if _, err := ts.Create(ts.job("triage")); err != nil {
			t.Fatal(err)
		}
		j := ts.job("triage")
		j.Prompt = "other"
		p := ts.Preview(j, "triage", nil)
		if !p.Valid || !slices.Equal(p.Changed, []string{"prompt"}) {
			t.Errorf("edit preview = %+v", p)
		}
		if p := ts.Preview(ts.job("triage"), "", nil); p.Valid || !strings.Contains(p.Errors[0].Message, "already exists") {
			t.Errorf("create over an existing name: %+v", p.Errors)
		}
		if p := ts.Preview(ts.job("other"), "triage", nil); p.Valid || p.Errors[0].Field != "name" {
			t.Errorf("rename preview: %+v", p.Errors)
		}
		if p := ts.Preview(ts.job("gone"), "gone", nil); p.Valid || !strings.Contains(p.Errors[0].Message, "no cron job") {
			t.Errorf("missing original: %+v", p.Errors)
		}
	})
}

func TestBuildOverview(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.Local)
	off := false
	mk := func(name, sched string) cron.Job {
		j := cron.Job{Name: name, Schedule: sched, Dir: "/p", Agent: "a", Prompt: "p"}
		j.Normalize()
		return j
	}
	paused := mk("paused", "0 8 * * *")
	paused.Enabled = &off
	jobs := []cron.Job{mk("later", "0 20 * * *"), mk("soon", "0 10 * * *"), paused, mk("running", "0 22 * * *"), mk("broken", "0 21 * * *"), mk("bad", "nope")}
	var runs []cron.Entry
	for i := range 7 {
		runs = append(runs, cron.Entry{Time: now.Add(time.Duration(i-10) * time.Hour), Job: "later", Result: cron.ResultFired})
	}
	runs = append(runs,
		cron.Entry{Time: now.Add(-time.Hour), Job: "broken", Result: cron.Failed("launch"), Note: "boom"},
		cron.Entry{Time: now.Add(-time.Minute), Job: "soon", Result: cron.ResultSkippedUsage, Note: "five_hour usage 82% >= 80%"})
	windows := []cron.Window{{ID: "@4", Job: "running", Session: "agentctl-p"}, {ID: "@5", Job: "later", Done: true}}
	ov := BuildOverview(OverviewInput{Now: now, Jobs: jobs, Runs: runs, Windows: windows, SchedulerInstalled: false})
	if ov.Active != 1 || ov.SchedulerInstalled || len(ov.Jobs) != len(jobs) {
		t.Fatalf("overview = %+v", ov)
	}
	by := map[string]Status{}
	for _, s := range ov.Jobs {
		by[s.Name] = s
	}
	if s := by["later"]; len(s.Recent) != RecentRuns || !s.Recent[0].Time.Equal(runs[6].Time) || s.Running || s.FailedLast {
		t.Errorf("later = %+v", s)
	}
	if s := by["running"]; !s.Running || len(s.Windows) != 1 || s.Windows[0].WindowID != "@4" {
		t.Errorf("running = %+v", s)
	}
	if s := by["broken"]; !s.FailedLast || s.Last == nil || s.Last.Note != "boom" {
		t.Errorf("broken = %+v", s)
	}
	if s := by["paused"]; s.Enabled || s.NextFire != nil {
		t.Errorf("paused = %+v", s)
	}
	if s := by["bad"]; s.ScheduleError == "" || s.Invalid == "" || s.NextFire != nil {
		t.Errorf("bad = %+v", s)
	}
	if s := by["soon"]; s.NextFire == nil || s.NextFire.Hour() != 10 || s.Simple == nil || s.Words != "daily 10:00" {
		t.Errorf("soon = %+v", s)
	}

	SortForDisplay(ov.Jobs)
	var order []string
	for _, s := range ov.Jobs {
		order = append(order, s.Name)
	}
	want := []string{"broken", "running", "soon", "later", "bad", "paused"}
	if !slices.Equal(order, want) {
		t.Errorf("display order = %v, want %v", order, want)
	}
}

func TestDetail(t *testing.T) {
	ts := newTestStore(t)
	j := ts.job("keyed")
	j.Prompt, j.PromptKey = "", "spec-237"
	if _, err := ts.Create(j); err != nil {
		t.Fatal(err)
	}
	for i := range 25 {
		if err := cron.AppendRun(ts.stateDir, cron.Entry{Time: ts.now.Add(time.Duration(i) * time.Minute), Job: "keyed", Result: cron.ResultFired}); err != nil {
			t.Fatal(err)
		}
	}
	d, err := ts.Detail("keyed")
	if err != nil {
		t.Fatal(err)
	}
	if d.PromptResolved != "text of spec-237" || len(d.Runs) != DetailRuns || d.Runs[0].Time.Before(d.Runs[1].Time) {
		t.Errorf("detail = %+v", d)
	}
	ts.ResolvePromptKey = func(string) (string, error) { return "", errors.New("missing") }
	if d, _ := ts.Detail("keyed"); d.PromptKeyError != "missing" {
		t.Errorf("key error = %q", d.PromptKeyError)
	}
	if _, err := ts.Detail("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestWindowsFromRows(t *testing.T) {
	rows := []fleet.Row{
		{Session: "s", AgentState: state.AgentState{WindowID: "@1", ScheduledBy: "nightly"}},
		{Session: "s", AgentState: state.AgentState{WindowID: "@2", ScheduledBy: "nightly", Done: true}},
		{Session: "s", AgentState: state.AgentState{WindowID: "@3"}},
	}
	got := WindowsFromRows(rows)
	if len(got) != 2 || got[0].ID != "@1" || got[0].Job != "nightly" || got[0].Session != "s" || !got[1].Done {
		t.Errorf("windows = %+v", got)
	}
}

func TestParseRunOutput(t *testing.T) {
	e, err := ParseRunOutput("j", []byte(`{"ts":"2026-11-17T09:00:00Z","job":"j","result":"skipped:usage","note":"five_hour usage 85% >= 80%"}`+"\n"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := OutcomeOf(e)
	if o.Fired || o.Result != cron.ResultSkippedUsage || !strings.Contains(o.Reason, "85%") {
		t.Errorf("outcome = %+v", o)
	}
	e, _ = ParseRunOutput("j", []byte(`{"job":"j","result":"fired","window":"@7","session":"agentctl-p"}`), nil, nil)
	if o := OutcomeOf(e); !o.Fired || o.WindowID != "@7" || o.Session != "agentctl-p" {
		t.Errorf("fired outcome = %+v", o)
	}
	if _, err := ParseRunOutput("j", nil, []byte("agentctl: no cron job named \"j\"\n"), errors.New("exit status 1")); err == nil || !strings.Contains(err.Error(), `no cron job named "j"`) {
		t.Errorf("err = %v", err)
	}
	if _, err := ParseRunOutput("j", []byte(`{"job":"other","result":"fired"}`), nil, nil); err == nil {
		t.Error("a result for another job was accepted")
	}
	if _, err := ParseRunOutput("j", []byte(`not json`), nil, nil); err == nil {
		t.Error("garbage accepted")
	}
}

func TestRunArgsAndEnv(t *testing.T) {
	if got := RunArgs("nightly"); !slices.Equal(got, []string{"cron", "run", "--json", "--", "nightly"}) {
		t.Errorf("args = %v (no --force, ever)", got)
	}
	env := RunEnv([]string{"PATH=/bin", "AGENTCTL_WINDOW_ID=@3", "TMUX_PANE=%1", "AGENTCTL_RUNTIME_DIR=/x/.agentctl", "HOME=/h"})
	if !slices.Equal(env, []string{"PATH=/bin", "HOME=/h"}) {
		t.Errorf("env = %v", env)
	}
}

// TestRunSubprocessRefusesInTests: the production RunFunc never spawns
// from a test binary, and rejects a name that is not a job name before
// any exec.
func TestRunSubprocessRefusesInTests(t *testing.T) {
	if _, err := RunSubprocess(t.Context(), "-rf"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad name: err = %v", err)
	}
	if _, err := RunSubprocess(t.Context(), "nightly"); err == nil || !strings.Contains(err.Error(), "locating agentctl") {
		t.Errorf("err = %v, want the test-binary refusal", err)
	}
}
