package cronedit

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/cron"
)

func (ts *testStore) once(name string, at time.Time) cron.Job {
	return cron.Job{Name: name, At: cron.FormatAt(at), Dir: ts.projDir, Agent: "brandon", Prompt: "remind me"}
}

// complete marks a stored one-shot done, as the tick does.
func (ts *testStore) complete(t *testing.T, name, state string) {
	t.Helper()
	path, _ := ts.JobsPath()
	jobs, err := cron.LoadJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	i := jobs.Find(name)
	jobs.Jobs[i].State, jobs.Jobs[i].CompletedAt, jobs.Jobs[i].Result = state, *ts.now, cron.ResultFired
	if err := cron.SaveJobs(path, jobs); err != nil {
		t.Fatal(err)
	}
}

func (ts *testStore) history(t *testing.T, name string) []cron.Entry {
	t.Helper()
	runs, err := cron.ReadRuns(ts.stateDir, name)
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

func TestCreateOnce(t *testing.T) {
	tests := []struct {
		name    string
		mut     func(ts *testStore, j *cron.Job)
		wantErr string
	}{
		{name: "future at accepted", mut: func(*testStore, *cron.Job) {}},
		{name: "past at rejected", mut: func(ts *testStore, j *cron.Job) { j.At = cron.FormatAt(ts.now.Add(-time.Minute)) }, wantErr: "in the past"},
		{name: "now rejected", mut: func(ts *testStore, j *cron.Job) { j.At = cron.FormatAt(*ts.now) }, wantErr: "in the past"},
		{name: "schedule and at", mut: func(_ *testStore, j *cron.Job) { j.Schedule = "@daily" }, wantErr: "not both"},
		{name: "min_interval does not apply", mut: func(ts *testStore, j *cron.Job) { j.At = cron.FormatAt(ts.now.Add(2 * time.Minute)) }},
		{name: "a duplicate of a done one-shot starts pending", mut: func(_ *testStore, j *cron.Job) {
			j.State, j.Result = cron.StateCompleted, cron.ResultFired
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestStore(t)
			j := ts.once("remind", ts.now.Add(24*time.Hour))
			tt.mut(ts, &j)
			got, err := ts.Create(j)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				if len(ts.history(t, "remind")) != 0 {
					t.Error("a rejected create was recorded")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.State != "" || !got.IsOnce() || got.Schedule != "" {
				t.Errorf("job = %+v", got)
			}
			if !ts.lastFire(t, "remind").Equal(*ts.now) {
				t.Error("create did not seed")
			}
			h := ts.history(t, "remind")
			if len(h) != 1 || h[0].Result != cron.ResultCreated || h[0].Def == nil || h[0].Def.At != got.At || h[0].Def.Prompt != "remind me" {
				t.Errorf("history = %+v", h)
			}
		})
	}
}

func TestEditOnce(t *testing.T) {
	ts := newTestStore(t)
	if _, err := ts.Create(ts.once("remind", ts.now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	ts.complete(t, "remind", cron.StateCompleted)

	// An edit that does not touch at keeps the outcome (a past at is fine).
	*ts.now = ts.now.Add(2 * time.Hour)
	stored, _ := ts.Get("remind")
	form := stored
	form.State, form.Result, form.CompletedAt = "", "", time.Time{} // the forms do not send it
	form.Prompt = "remind me twice"
	res, err := ts.Update("remind", form)
	if err != nil {
		t.Fatal(err)
	}
	if res.Job.State != cron.StateCompleted || res.Reseeded {
		t.Fatalf("prompt edit reset the one-shot: %+v", res)
	}

	// Resume of a done one-shot is refused.
	if _, err := ts.SetEnabled("remind", false); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.SetEnabled("remind", true); err == nil || !strings.Contains(err.Error(), "cron edit remind --at") {
		t.Fatalf("resume err = %v", err)
	}
	if _, err := ts.SetEnabled("remind", false); err != nil {
		t.Fatal(err)
	}

	// A new at in the past is refused; in the future it resets to pending
	// and re-seeds.
	_, err = ts.Modify("remind", func(j *cron.Job) error {
		j.At = cron.FormatAt(ts.now.Add(-30 * time.Minute))
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "in the past") {
		t.Fatalf("past at err = %v", err)
	}
	next := ts.now.Add(24 * time.Hour)
	res, err = ts.Modify("remind", func(j *cron.Job) error {
		j.At = cron.FormatAt(next)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Job.State != "" || res.Job.Result != "" || !res.Reseeded || !ts.lastFire(t, "remind").Equal(*ts.now) {
		t.Fatalf("at edit = %+v", res)
	}

	// Switching to a schedule drops the at.
	res, err = ts.Modify("remind", func(j *cron.Job) error {
		j.At, j.Schedule = "", "0 9 * * *"
		return nil
	})
	if err != nil || res.Job.IsOnce() || !res.Reseeded {
		t.Fatalf("to recurring = %+v, %v", res, err)
	}

	var results []string
	for _, e := range ts.history(t, "remind") {
		results = append(results, e.Result)
	}
	if want := "created edited paused edited edited"; strings.Join(results, " ") != want {
		t.Errorf("history = %q, want %q", strings.Join(results, " "), want)
	}
}

func TestDeleteOnceRecordsDefinition(t *testing.T) {
	ts := newTestStore(t)
	ts.Actor = func() cron.Actor { return cron.Actor{Kind: cron.ActorWindow, WindowID: "@12", SessionID: "sess"} }
	j := ts.once("remind", ts.now.Add(time.Hour))
	if _, err := ts.Create(j); err != nil {
		t.Fatal(err)
	}
	if err := ts.Delete("remind"); err != nil {
		t.Fatal(err)
	}
	h := ts.history(t, "remind")
	d := h[len(h)-1]
	if d.Result != cron.ResultDeleted || d.Def == nil || d.Def.At == "" || d.Def.Agent != "brandon" || d.Def.Dir != ts.projDir ||
		d.By == nil || d.By.WindowID != "@12" || d.By.SessionID != "sess" {
		t.Fatalf("deleted entry = %+v by %+v def %+v", d, d.By, d.Def)
	}
	if err := ts.Delete("remind"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete err = %v", err)
	}
	if len(ts.history(t, "remind")) != len(h) {
		t.Error("a failed delete was recorded")
	}
}

func TestOverviewIgnoresLifecycle(t *testing.T) {
	ts := newTestStore(t)
	if _, err := ts.Create(ts.job("triage")); err != nil {
		t.Fatal(err)
	}
	if err := cron.AppendRun(ts.stateDir, cron.Entry{Time: *ts.now, Job: "triage", Result: cron.Failed("launch")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.SetEnabled("triage", false); err != nil {
		t.Fatal(err)
	}
	ov, err := ts.Overview()
	if err != nil {
		t.Fatal(err)
	}
	st := ov.Jobs[0]
	if st.Last == nil || st.Last.Result != cron.Failed("launch") || !st.FailedLast || len(st.Recent) != 1 {
		t.Fatalf("status = %+v", st)
	}
	d, err := ts.Detail("triage")
	if err != nil || len(d.Runs) != 1 {
		t.Fatalf("detail runs = %+v, %v", d.Runs, err)
	}
}

func TestOnceStatusAndPreview(t *testing.T) {
	ts := newTestStore(t)
	at := time.Date(2026, 12, 1, 9, 0, 0, 0, time.Local)
	if _, err := ts.Create(ts.once("remind", at)); err != nil {
		t.Fatal(err)
	}
	d, err := ts.Detail("remind")
	if err != nil {
		t.Fatal(err)
	}
	if d.Words != "once, Tue 1 Dec 09:00" || d.Simple != nil || d.NextFire == nil || len(d.NextFiresText) != 1 || d.ScheduleError != "" || d.Invalid != "" {
		t.Fatalf("pending detail = %+v", d.Status)
	}
	ts.complete(t, "remind", cron.StateCompleted)
	d, _ = ts.Detail("remind")
	if !strings.HasPrefix(d.Words, "done, fired ") || d.NextFire != nil || len(d.NextFires) != 0 {
		t.Fatalf("done detail = %+v", d.Status)
	}
	ts.complete(t, "remind", cron.StateExpired)
	if d, _ = ts.Detail("remind"); d.Words != "expired, missed Tue 1 Dec 09:00" {
		t.Errorf("expired words = %q", d.Words)
	}

	p := ts.Preview(ts.once("other", at), "", nil)
	if !p.Valid || p.Words != "once, Tue 1 Dec 09:00" || len(p.NextFires) != 1 {
		t.Fatalf("preview = %+v", p)
	}
	p = ts.Preview(ts.once("other", ts.now.Add(-time.Hour)), "", nil)
	if p.Valid {
		t.Error("preview accepted a past at")
	}
}
