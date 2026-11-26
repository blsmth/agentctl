package cronedit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
)

// testStore is a Store over temp dirs with an adjustable clock. Only the
// agent "brandon" exists.
type testStore struct {
	Store
	now      *time.Time
	stateDir string
	projDir  string
}

func newTestStore(t *testing.T) *testStore {
	t.Helper()
	root := t.TempDir()
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.Local)
	ts := &testStore{now: &now, stateDir: filepath.Join(root, "state", "cron"), projDir: t.TempDir()}
	ts.Store = Store{
		JobsPath: func() (string, error) { return filepath.Join(root, "config", cron.SchedulesFile), nil },
		StateDir: func() (string, error) { return ts.stateDir, nil },
		Now:      func() time.Time { return *ts.now },
		Config:   func() config.CronConfig { return config.CronConfig{} },
		ValidateAgent: func(name, _ string) error {
			if name != "brandon" {
				return fmt.Errorf("unknown agent %q", name)
			}
			return nil
		},
		ResolvePromptKey:   func(key string) (string, error) { return "text of " + key, nil },
		SchedulerInstalled: func() bool { return true },
	}
	return ts
}

func (ts *testStore) job(name string) cron.Job {
	return cron.Job{Name: name, Schedule: "30 7 * * 1-5", Dir: ts.projDir, Agent: "brandon", Prompt: "triage"}
}

func (ts *testStore) lastFire(t *testing.T, name string) time.Time {
	t.Helper()
	st, err := cron.LoadState(ts.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return st.Jobs[name].LastFire
}

func (ts *testStore) setLastFire(t *testing.T, name string, at time.Time) {
	t.Helper()
	if err := cron.Seed(ts.stateDir, name, at); err != nil {
		t.Fatal(err)
	}
}

func fields(errs []cron.FieldError) []string {
	out := make([]string, len(errs))
	for i, e := range errs {
		out[i] = e.Field
	}
	return out
}

func TestCreate(t *testing.T) {
	ts := newTestStore(t)
	job, err := ts.Create(ts.job("triage"))
	if err != nil {
		t.Fatal(err)
	}
	if !job.IsEnabled() || !job.UseWorktree() || job.Enabled == nil || job.Worktree == nil || job.Harness != "claude" {
		t.Errorf("job = %+v, want explicit enabled/worktree and defaults", job)
	}
	if got := ts.lastFire(t, "triage"); !got.Equal(*ts.now) {
		t.Errorf("last_fire = %v, want seeded to now", got)
	}
	path, _ := ts.JobsPath()
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("jobs file: %v %v", fi, err)
	}
	_, err = ts.Create(ts.job("triage"))
	if !errors.Is(err, ErrExists) || !slices.Equal(fields(FieldsOf(err)), []string{"name"}) {
		t.Errorf("duplicate create err = %v", err)
	}
}

func TestValidatePerFieldErrors(t *testing.T) {
	ts := newTestStore(t)
	bad := cron.Job{Name: "../x", Schedule: "* * * * *", Dir: filepath.Join(ts.projDir, "missing"), Agent: "nobody",
		Timeout: "soon", Overlap: "queue"}
	_, errs := ts.Validate(bad, "")
	want := []string{"name", "dir", "prompt", "schedule", "timeout", "overlap"}
	if got := fields(errs); !slices.Equal(got, want) {
		t.Errorf("fields = %v, want %v (%+v)", got, want, errs)
	}
	for _, e := range errs {
		if e.Field == "schedule" && !strings.Contains(e.Message, "min_interval") {
			t.Errorf("schedule error = %q, want the min_interval rejection", e.Message)
		}
	}
	// The agent is checked once the dir is good.
	good := ts.job("ok")
	good.Agent = "nobody"
	if _, errs := ts.Validate(good, ""); !slices.Equal(fields(errs), []string{"agent"}) {
		t.Errorf("unknown agent: %+v", errs)
	}
	// ~ expands, and a regular file is not a directory.
	file := filepath.Join(ts.projDir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	good.Agent, good.Dir = "brandon", file
	if _, errs := ts.Validate(good, ""); len(errs) != 1 || !strings.Contains(errs[0].Message, "not a directory") {
		t.Errorf("file dir: %+v", errs)
	}
	if _, err := ts.Create(cron.Job{}); len(FieldsOf(err)) == 0 {
		t.Errorf("empty job: err = %v, want field errors", err)
	}
}

func TestUpdateFireState(t *testing.T) {
	earlier := time.Date(2026, 11, 16, 7, 30, 0, 0, time.Local)
	tests := []struct {
		name       string
		edit       func(j *cron.Job)
		wantReseed bool
		wantChange []string
	}{
		{name: "prompt only keeps last_fire", edit: func(j *cron.Job) { j.Prompt = "new" }, wantChange: []string{"prompt"}},
		{name: "agent and timeout keep last_fire", edit: func(j *cron.Job) { j.Timeout = "45m" }, wantChange: []string{"timeout"}},
		{name: "schedule change reseeds", edit: func(j *cron.Job) { j.Schedule = "0 * * * *" }, wantReseed: true, wantChange: []string{"schedule"}},
		{name: "whitespace in the schedule is not a change", edit: func(j *cron.Job) { j.Schedule = " 30 7 * * 1-5 " }},
		{name: "nothing", edit: func(*cron.Job) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestStore(t)
			if _, err := ts.Create(ts.job("triage")); err != nil {
				t.Fatal(err)
			}
			ts.setLastFire(t, "triage", earlier)
			*ts.now = ts.now.Add(time.Hour)
			res, err := ts.Modify("triage", func(j *cron.Job) error { tt.edit(j); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if res.Reseeded != tt.wantReseed || !slices.Equal(res.Changed, tt.wantChange) {
				t.Errorf("res = %+v, want reseed %v changed %v", res, tt.wantReseed, tt.wantChange)
			}
			want := earlier
			if tt.wantReseed {
				want = *ts.now
			}
			if got := ts.lastFire(t, "triage"); !got.Equal(want) {
				t.Errorf("last_fire = %v, want %v", got, want)
			}
		})
	}
}

// TestScheduleEditNeverFiresEarlier: after a schedule edit, a tick finds
// nothing due for a time before the edit, though the old last_fire would
// have made the new schedule's earlier slots due.
func TestScheduleEditNeverFiresEarlier(t *testing.T) {
	ts := newTestStore(t)
	if _, err := ts.Create(ts.job("triage")); err != nil {
		t.Fatal(err)
	}
	ts.setLastFire(t, "triage", ts.now.Add(-3*time.Hour)) // 06:00
	*ts.now = time.Date(2026, 11, 17, 9, 50, 0, 0, time.Local)
	res, err := ts.Modify("triage", func(j *cron.Job) error { j.Schedule = "0 * * * *"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	var launched []string
	jobs, _ := ts.Load()
	d := cron.Deps{Dir: ts.stateDir, Jobs: jobs.Jobs, Now: func() time.Time { return *ts.now },
		Launch: func(r cron.LaunchRequest) (cron.LaunchResult, error) {
			launched = append(launched, r.Job.Name)
			return cron.LaunchResult{WindowID: "@1"}, nil
		}}
	if _, err := cron.Tick(d); err != nil {
		t.Fatal(err)
	}
	if !res.Reseeded || len(launched) != 0 {
		t.Errorf("reseeded %v, launched %v: an edit fired a pre-edit slot", res.Reseeded, launched)
	}
}

func TestUpdateRules(t *testing.T) {
	ts := newTestStore(t)
	if _, err := ts.Create(ts.job("triage")); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.SetEnabled("triage", false); err != nil {
		t.Fatal(err)
	}

	t.Run("name is immutable", func(t *testing.T) {
		j := ts.job("renamed")
		_, err := ts.Update("triage", j)
		if !slices.Equal(fields(FieldsOf(err)), []string{"name"}) || !strings.Contains(err.Error(), "cannot be changed") {
			t.Errorf("rename err = %v", err)
		}
		if _, err := ts.Get("renamed"); !errors.Is(err, ErrNotFound) {
			t.Errorf("renamed job exists: %v", err)
		}
	})
	t.Run("empty name keeps it and nil enabled keeps paused", func(t *testing.T) {
		j := ts.job("")
		j.Prompt = "changed"
		res, err := ts.Update("triage", j)
		if err != nil {
			t.Fatal(err)
		}
		if res.Job.Name != "triage" || res.Job.IsEnabled() || res.Reseeded {
			t.Errorf("res = %+v, want the paused job kept", res)
		}
	})
	t.Run("enabling through update reseeds", func(t *testing.T) {
		ts.setLastFire(t, "triage", ts.now.Add(-48*time.Hour))
		j := ts.job("triage")
		on := true
		j.Enabled = &on
		res, err := ts.Update("triage", j)
		if err != nil || !res.Reseeded || !slices.Contains(res.Changed, "enabled") {
			t.Fatalf("res = %+v, %v", res, err)
		}
		if got := ts.lastFire(t, "triage"); !got.Equal(*ts.now) {
			t.Errorf("last_fire = %v, want now", got)
		}
	})
	t.Run("invalid edit leaves the job alone", func(t *testing.T) {
		before, _ := ts.Get("triage")
		_, err := ts.Modify("triage", func(j *cron.Job) error { j.Schedule = "nope"; return nil })
		if !slices.Equal(fields(FieldsOf(err)), []string{"schedule"}) {
			t.Errorf("err = %v", err)
		}
		after, _ := ts.Get("triage")
		if after.Schedule != before.Schedule {
			t.Errorf("schedule changed to %q", after.Schedule)
		}
	})
	t.Run("missing job", func(t *testing.T) {
		if _, err := ts.Update("nope", ts.job("nope")); !errors.Is(err, ErrNotFound) || err.Error() != `no cron job named "nope"` {
			t.Errorf("err = %v", err)
		}
		if err := ts.Delete("nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("delete err = %v", err)
		}
		if _, err := ts.SetEnabled("nope", true); !errors.Is(err, ErrNotFound) {
			t.Errorf("set enabled err = %v", err)
		}
	})
}

func TestSetEnabledAndDelete(t *testing.T) {
	ts := newTestStore(t)
	if _, err := ts.Create(ts.job("triage")); err != nil {
		t.Fatal(err)
	}
	old := ts.now.Add(-time.Hour)
	ts.setLastFire(t, "triage", old)
	if _, err := ts.SetEnabled("triage", false); err != nil {
		t.Fatal(err)
	}
	if got := ts.lastFire(t, "triage"); !got.Equal(old) {
		t.Errorf("pause changed last_fire to %v", got)
	}
	if _, err := ts.SetEnabled("triage", true); err != nil {
		t.Fatal(err)
	}
	if got := ts.lastFire(t, "triage"); !got.Equal(*ts.now) {
		t.Errorf("resume did not reseed: %v", got)
	}
	if err := cron.AppendRun(ts.stateDir, cron.Entry{Time: *ts.now, Job: "triage", Result: cron.ResultFired}); err != nil {
		t.Fatal(err)
	}
	if err := ts.Delete("triage"); err != nil {
		t.Fatal(err)
	}
	runs, _ := cron.ReadRuns(ts.stateDir, "triage")
	if got := cron.RunsOnly(runs); len(got) != 1 || got[0].Result != cron.ResultFired {
		t.Errorf("delete touched the run history: %v", runs)
	}
	var results []string
	for _, e := range runs {
		results = append(results, e.Result)
	}
	if want := "created paused resumed fired deleted"; strings.Join(results, " ") != want {
		t.Errorf("history = %q, want %q", strings.Join(results, " "), want)
	}
	if d := runs[len(runs)-1]; d.Def == nil || d.Def.Name != "triage" || d.By == nil || d.By.Kind != cron.ActorCLI {
		t.Errorf("deleted entry = %+v", d)
	}
	jobs, _ := ts.Load()
	if len(jobs.Jobs) != 0 {
		t.Errorf("jobs = %+v", jobs.Jobs)
	}
}

// TestConcurrentWritesSerialized: concurrent creates and edits from several
// writers never lose one another's change (each holds the jobs lock across
// load+modify+save).
func TestConcurrentWritesSerialized(t *testing.T) {
	ts := newTestStore(t)
	if _, err := ts.Create(ts.job("base")); err != nil {
		t.Fatal(err)
	}
	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := range n {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := ts.Create(ts.job(fmt.Sprintf("job-%d", i)))
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := ts.Modify("base", func(j *cron.Job) error { j.Prompt += "x"; return nil })
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	jobs, _ := ts.Load()
	if len(jobs.Jobs) != n+1 {
		t.Errorf("jobs = %d, want %d (a write was lost)", len(jobs.Jobs), n+1)
	}
	base, _ := ts.Get("base")
	if base.Prompt != "triage"+strings.Repeat("x", n) {
		t.Errorf("base prompt = %q, want %d appended edits", base.Prompt, n)
	}
}

func TestMissingFileAndZeroJobs(t *testing.T) {
	ts := newTestStore(t)
	ov, err := ts.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if ov.Jobs == nil || len(ov.Jobs) != 0 || !ov.Enabled || ov.Throttles.MinInterval != "15m" || ov.Throttles.MaxConcurrent != 2 {
		t.Errorf("overview = %+v", ov)
	}
	if _, err := ts.Get("x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get on a missing file: %v", err)
	}
	p := ts.Preview(ts.job("first"), "", nil)
	if !p.Valid {
		t.Errorf("preview on a missing file: %+v", p.Errors)
	}
}

func TestChangedFields(t *testing.T) {
	a := cron.Job{Name: "j", Schedule: "0 * * * *", Dir: "/p", Agent: "a", Prompt: "p"}
	b := a
	on := true
	b.Worktree, b.Enabled = &on, &on // explicit defaults are no change
	if got := ChangedFields(a, b); len(got) != 0 {
		t.Errorf("defaults changed: %v", got)
	}
	b.Prompt, b.PromptKey, b.Agent = "", "k", "z"
	if got := ChangedFields(a, b); !slices.Equal(got, []string{"agent", "prompt", "prompt_key"}) {
		t.Errorf("changed = %v", got)
	}
}
