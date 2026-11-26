package cron

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/ratelimit"
)

// onceJob is a pending one-shot at t.
func onceJob(name string, t time.Time) Job {
	j := Job{Name: name, At: FormatAt(t), Dir: "/tmp/p", Agent: "a", Prompt: "remind me"}
	j.Normalize()
	return j
}

// onceSetup writes jobs to a jobs file and seeds their last_fire to seed,
// as cronedit.Create does at add time.
func onceSetup(t *testing.T, seed time.Time, jobs ...Job) (dir, jobsPath string) {
	t.Helper()
	dir = t.TempDir()
	jobsPath = filepath.Join(t.TempDir(), SchedulesFile)
	if err := SaveJobs(jobsPath, &Jobs{Jobs: jobs}); err != nil {
		t.Fatal(err)
	}
	m := map[string]time.Time{}
	for _, j := range jobs {
		m[j.Name] = seed
	}
	seedState(t, dir, m)
	return dir, jobsPath
}

func loadJob(t *testing.T, path, name string) Job {
	t.Helper()
	f, err := LoadJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	i := f.Find(name)
	if i < 0 {
		t.Fatalf("job %s gone from %s", name, path)
	}
	return f.Jobs[i]
}

func TestParseAt(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	tests := []struct {
		name    string
		in      string
		want    time.Time
		wantErr string
	}{
		{name: "local minute", in: "2026-12-01 09:00", want: time.Date(2026, 12, 1, 9, 0, 0, 0, ny)},
		{name: "T separator", in: "2026-12-01T09:00", want: time.Date(2026, 12, 1, 9, 0, 0, 0, ny)},
		{name: "rfc3339 keeps its offset", in: "2026-12-01T09:00:00Z", want: time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)},
		{name: "spring-forward gap rejected", in: "2026-03-08 02:30", wantErr: "does not exist"},
		{name: "fall-back repeat is the first occurrence", in: "2026-11-01 01:30", want: time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)},
		{name: "garbage", in: "tomorrow", wantErr: "want YYYY-MM-DD HH:MM"},
		{name: "empty", in: " ", wantErr: "needs a time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAtIn(tt.in, ny)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || !got.Equal(tt.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestOnceValidate(t *testing.T) {
	base := onceJob("r", at(10, 0))
	tests := []struct {
		name    string
		mut     func(*Job)
		wantErr string
	}{
		{name: "valid", mut: func(*Job) {}},
		{name: "schedule and at", mut: func(j *Job) { j.Schedule = "@daily" }, wantErr: "not both"},
		{name: "bad at", mut: func(j *Job) { j.At = "2026-12-01 09:00" }, wantErr: "RFC3339"},
		{name: "done is valid", mut: func(j *Job) { j.State = StateCompleted }},
		{name: "unknown state", mut: func(j *Job) { j.State = "running" }, wantErr: "state"},
		{name: "state on recurring", mut: func(j *Job) { j.At, j.Schedule, j.State = "", "@daily", StateExpired }, wantErr: "only to a one-shot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := base
			tt.mut(&j)
			err := j.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestOnceSummary(t *testing.T) {
	j := onceJob("r", time.Date(2026, 12, 1, 9, 0, 0, 0, time.Local))
	if got := OnceSummary(j); got != "once, Tue 1 Dec 09:00" {
		t.Errorf("pending = %q", got)
	}
	done := j
	done.State, done.Result, done.CompletedAt = StateCompleted, ResultFired, time.Date(2026, 12, 1, 9, 1, 0, 0, time.Local)
	if got := OnceSummary(done); got != "done, fired Tue 1 Dec 09:01" {
		t.Errorf("completed = %q", got)
	}
	done.Result = Failed("launch")
	if got := OnceSummary(done); got != "done, failed Tue 1 Dec 09:01" {
		t.Errorf("failed = %q", got)
	}
	exp := j
	exp.State = StateExpired
	if got := OnceSummary(exp); got != "expired, missed Tue 1 Dec 09:00" {
		t.Errorf("expired = %q", got)
	}
	if got, err := JobSummary(j, at(9, 0)); err != nil || got != "once, Tue 1 Dec 09:00" {
		t.Errorf("JobSummary = %q, %v", got, err)
	}
	if Words(j) != OnceSummary(j) || ScheduleText(j) != OnceSummary(j) {
		t.Error("Words/ScheduleText of a one-shot are not its summary")
	}
	if NextOnce(done) != (time.Time{}) || !NextOnce(j).Equal(time.Date(2026, 12, 1, 9, 0, 0, 0, time.Local).Add(Jitter("r"))) {
		t.Error("NextOnce")
	}
}

func TestTickOnce(t *testing.T) {
	fireAt := at(10, 0)
	seed := at(8, 0)
	jit := Jitter("r")
	tests := []struct {
		name       string
		now        time.Time
		cfg        config.CronConfig
		setup      func(f *fakeEnv, j *Job)
		wantLaunch int
		wantState  string // "" = still pending
		wantResult string // last entry's result, "" = none recorded
	}{
		{name: "not yet due", now: fireAt.Add(jit - time.Second)},
		{name: "due fires and completes", now: fireAt.Add(jit), wantLaunch: 1, wantState: StateCompleted, wantResult: ResultFired},
		{name: "asleep within catchup_window still fires", now: fireAt.Add(3 * time.Hour), wantLaunch: 1, wantState: StateCompleted, wantResult: ResultFired},
		{name: "asleep past catchup_window expires", now: fireAt.Add(7 * time.Hour), wantState: StateExpired, wantResult: ResultSkippedCatchup},
		{name: "catchup none past the grace expires", now: fireAt.Add(time.Hour), setup: func(_ *fakeEnv, j *Job) { j.Catchup = CatchupNone },
			wantState: StateExpired, wantResult: ResultSkippedCatchup},
		{name: "launch failure completes", now: fireAt.Add(jit), setup: func(f *fakeEnv, _ *Job) { f.launchErr = errors.New("boom") },
			wantLaunch: 1, wantState: StateCompleted, wantResult: Failed("launch")},
		{name: "overlap skip stays pending", now: fireAt.Add(jit), setup: func(f *fakeEnv, _ *Job) {
			f.windows = []Window{{ID: "@1", Job: "r"}}
		}, wantResult: ResultSkippedOverlap},
		{name: "concurrency skip stays pending", now: fireAt.Add(jit), setup: func(f *fakeEnv, _ *Job) {
			f.windows = []Window{{ID: "@1", Job: "x"}, {ID: "@2", Job: "y"}}
		}, wantResult: ResultSkippedConcurrency},
		{name: "usage skip stays pending", now: fireAt.Add(jit), setup: func(f *fakeEnv, _ *Job) {
			f.usage = &ratelimit.PushedRateLimits{UpdatedAt: fireAt.Unix(), FiveHour: &ratelimit.PushedWindow{UsedPercentage: 95}}
		}, wantResult: ResultSkippedUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeEnv{}
			j := onceJob("r", fireAt)
			if tt.setup != nil {
				tt.setup(f, &j)
			}
			dir, path := onceSetup(t, seed, j)
			d := f.deps(dir, tt.now, tt.cfg, j)
			d.JobsPath = path
			res, err := Tick(d)
			if err != nil {
				t.Fatal(err)
			}
			if len(f.launches) != tt.wantLaunch {
				t.Fatalf("launches = %d, want %d", len(f.launches), tt.wantLaunch)
			}
			if tt.wantLaunch > 0 && (!f.launches[0].Scheduled.Equal(fireAt) || f.launches[0].Branch != "cron/r-20261117-1000") {
				t.Errorf("launch request = %+v", f.launches[0])
			}
			got := ""
			if n := len(res.Entries); n > 0 {
				got = res.Entries[n-1].Result
			}
			if got != tt.wantResult {
				t.Errorf("result = %q, want %q", got, tt.wantResult)
			}
			stored := loadJob(t, path, "r")
			if stored.State != tt.wantState {
				t.Errorf("jobs file state = %q, want %q", stored.State, tt.wantState)
			}
			if tt.wantState == StateCompleted && stored.Result != tt.wantResult {
				t.Errorf("jobs file result = %q", stored.Result)
			}
			if tt.wantResult == ResultFired && (stored.RunWindow != "@1" || stored.RunSessionID != "sess-1" || !stored.CompletedAt.Equal(tt.now)) {
				t.Errorf("completion not copied from the run entry: %+v", stored)
			}

			// A second tick never fires it again (done) and, when it was
			// skipped, does not record the same skip again.
			f.windows = nil
			d2 := f.deps(dir, tt.now.Add(time.Minute), tt.cfg, stored)
			d2.JobsPath = path
			f.usage = nil
			res2, _ := Tick(d2)
			if tt.wantState != "" && len(f.launches) != tt.wantLaunch {
				t.Errorf("a done one-shot fired again")
			}
			if tt.wantState != "" && len(res2.Entries) != 0 {
				t.Errorf("second tick recorded %+v", res2.Entries)
			}
		})
	}
}

func TestTickOnceRepeatedSkipRecordedOnce(t *testing.T) {
	fireAt := at(10, 0)
	f := &fakeEnv{windows: []Window{{ID: "@1", Job: "r"}}}
	j := onceJob("r", fireAt)
	dir, path := onceSetup(t, at(8, 0), j)
	var results []string
	for i := 0; i < 3; i++ {
		d := f.deps(dir, fireAt.Add(time.Duration(2+i)*time.Minute), config.CronConfig{}, j)
		d.JobsPath = path
		res, err := Tick(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range res.Entries {
			results = append(results, e.Result)
		}
	}
	if len(results) != 1 || results[0] != ResultSkippedOverlap {
		t.Fatalf("results = %v, want one skipped:overlap", results)
	}
	// The overlap clears: the next tick fires it.
	f.windows = nil
	d := f.deps(dir, fireAt.Add(10*time.Minute), config.CronConfig{}, j)
	d.JobsPath = path
	if _, err := Tick(d); err != nil {
		t.Fatal(err)
	}
	if len(f.launches) != 1 || loadJob(t, path, "r").State != StateCompleted {
		t.Fatalf("launches = %d, state %q", len(f.launches), loadJob(t, path, "r").State)
	}
}

func TestTickOncePausedPastItsTime(t *testing.T) {
	fireAt := at(10, 0)
	f := &fakeEnv{}
	j := onceJob("r", fireAt)
	// Resumed (re-seeded) after its time: not caught up.
	dir, path := onceSetup(t, fireAt.Add(30*time.Minute), j)
	d := f.deps(dir, fireAt.Add(31*time.Minute), config.CronConfig{}, j)
	d.JobsPath = path
	res, err := Tick(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.launches) != 0 || len(res.Entries) != 1 || res.Entries[0].Result != ResultSkippedCatchup {
		t.Fatalf("launches %d, entries %+v", len(f.launches), res.Entries)
	}
	if got := loadJob(t, path, "r").State; got != StateExpired {
		t.Errorf("state = %q", got)
	}
}

func TestTickOnceStateLostAfterItsTime(t *testing.T) {
	fireAt := at(10, 0)
	f := &fakeEnv{}
	j := onceJob("r", fireAt)
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), SchedulesFile)
	if err := SaveJobs(path, &Jobs{Jobs: []Job{j}}); err != nil {
		t.Fatal(err)
	}
	d := f.deps(dir, fireAt.Add(time.Hour), config.CronConfig{}, j)
	d.JobsPath = path
	if _, err := Tick(d); err != nil {
		t.Fatal(err)
	}
	if len(f.launches) != 0 || loadJob(t, path, "r").State != StateExpired {
		t.Fatalf("no state: launches %d, state %q", len(f.launches), loadJob(t, path, "r").State)
	}
}

// TestTickOnceJobsLockBusy: with an editor holding the jobs lock, the
// outcome stays in state.json (no refire) and reaches the jobs file on a
// later tick.
func TestTickOnceJobsLockBusy(t *testing.T) {
	fireAt := at(10, 0)
	f := &fakeEnv{}
	j := onceJob("r", fireAt)
	dir, path := onceSetup(t, at(8, 0), j)
	unlock, err := LockJobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	d := f.deps(dir, fireAt.Add(5*time.Minute), config.CronConfig{}, j)
	d.JobsPath = path
	if _, err := Tick(d); err != nil {
		t.Fatal(err)
	}
	if loadJob(t, path, "r").State != "" {
		t.Fatal("wrote the jobs file under a held lock")
	}
	d = f.deps(dir, fireAt.Add(6*time.Minute), config.CronConfig{}, j)
	d.JobsPath = path
	if _, err := Tick(d); err != nil {
		t.Fatal(err)
	}
	if len(f.launches) != 1 {
		t.Fatalf("launches = %d: fired again while the file was behind", len(f.launches))
	}
	unlock()
	if _, err := Tick(d); err != nil {
		t.Fatal(err)
	}
	if got := loadJob(t, path, "r"); got.State != StateCompleted || got.RunWindow != "@1" {
		t.Errorf("after the lock freed: %+v", got)
	}
}

func TestTickOnceInterruptedLaunch(t *testing.T) {
	fireAt := at(10, 0)
	f := &fakeEnv{}
	j := onceJob("r", fireAt)
	dir, path := onceSetup(t, at(8, 0), j)
	st, _ := LoadState(dir)
	js := st.Jobs["r"]
	js.OneShot = &OneShotOutcome{At: fireAt, State: OneShotLaunching}
	st.Jobs["r"] = js
	if err := SaveState(dir, st); err != nil {
		t.Fatal(err)
	}
	d := f.deps(dir, fireAt.Add(5*time.Minute), config.CronConfig{}, j)
	d.JobsPath = path
	if _, err := Tick(d); err != nil {
		t.Fatal(err)
	}
	if len(f.launches) != 0 {
		t.Fatal("refired an interrupted launch")
	}
	if got := loadJob(t, path, "r"); got.State != StateCompleted || got.Result != "unknown" {
		t.Errorf("job = %+v", got)
	}
}

func TestTickOncePersistsLaunchingBeforeLaunch(t *testing.T) {
	fireAt := at(10, 0)
	j := onceJob("r", fireAt)
	dir, path := onceSetup(t, at(8, 0), j)
	f := &fakeEnv{}
	var seen *OneShotOutcome
	f.onLaunch = func(LaunchRequest) {
		st, _ := LoadState(dir)
		seen = st.Jobs["r"].OneShot
	}
	d := f.deps(dir, fireAt.Add(5*time.Minute), config.CronConfig{}, j)
	d.JobsPath = path
	if _, err := Tick(d); err != nil {
		t.Fatal(err)
	}
	if seen == nil || seen.State != OneShotLaunching {
		t.Fatalf("state at launch = %+v", seen)
	}
}

func TestTickPrunesDoneOneShots(t *testing.T) {
	now := at(10, 0)
	old := onceJob("old", now.Add(-40*24*time.Hour))
	old.State, old.CompletedAt, old.Result = StateCompleted, now.Add(-31*24*time.Hour), ResultFired
	recent := onceJob("recent", now.Add(-2*24*time.Hour))
	recent.State, recent.CompletedAt = StateExpired, now.Add(-2*24*time.Hour)
	f := &fakeEnv{}
	dir, path := onceSetup(t, now.Add(-50*24*time.Hour), old, recent)
	d := f.deps(dir, now, config.CronConfig{}, old, recent)
	d.JobsPath = path
	if _, err := Tick(d); err != nil {
		t.Fatal(err)
	}
	jobs, _ := LoadJobs(path)
	if jobs.Find("old") >= 0 || jobs.Find("recent") < 0 {
		t.Fatalf("jobs after prune = %+v", jobs.Jobs)
	}
	runs, _ := ReadRuns(dir, "old")
	if len(runs) != 1 || runs[0].Result != ResultPruned || runs[0].Def == nil || runs[0].Def.State != StateCompleted ||
		runs[0].By == nil || runs[0].By.Kind != ActorTick {
		t.Fatalf("prune entry = %+v", runs)
	}
	if len(LastByJob(runs)) != 0 {
		t.Error("a pruned entry counts as a run")
	}
}

func TestRunNowOnce(t *testing.T) {
	fireAt := at(10, 0)
	j := onceJob("r", fireAt)
	dir, path := onceSetup(t, at(8, 0), j)
	f := &fakeEnv{}
	d := f.deps(dir, at(9, 0), config.CronConfig{}, j)
	d.JobsPath = path
	e, err := RunNow(d, "r", false)
	if err != nil || e.Result != ResultFired || !e.Manual {
		t.Fatalf("RunNow = %+v, %v", e, err)
	}
	stored := loadJob(t, path, "r")
	if stored.State != StateCompleted || stored.RunWindow != "@1" {
		t.Fatalf("manual run did not complete the one-shot: %+v", stored)
	}
	// The tick at its time does not fire it again.
	d = f.deps(dir, fireAt.Add(5*time.Minute), config.CronConfig{}, stored)
	d.JobsPath = path
	if _, err := Tick(d); err != nil || len(f.launches) != 1 {
		t.Fatalf("tick after manual run: launches %d, %v", len(f.launches), err)
	}
	// A second manual run is refused.
	if _, err := RunNow(d, "r", true); err == nil || !strings.Contains(err.Error(), "cron edit r --at") {
		t.Fatalf("second run err = %v", err)
	}
}

func TestRunNowOnceSkipLeavesPending(t *testing.T) {
	j := onceJob("r", at(10, 0))
	dir, path := onceSetup(t, at(8, 0), j)
	f := &fakeEnv{windows: []Window{{ID: "@1", Job: "r"}}}
	d := f.deps(dir, at(9, 0), config.CronConfig{}, j)
	d.JobsPath = path
	e, err := RunNow(d, "r", false)
	if err != nil || e.Result != ResultSkippedOverlap {
		t.Fatalf("RunNow = %+v, %v", e, err)
	}
	if loadJob(t, path, "r").State != "" {
		t.Error("a skipped manual run completed the one-shot")
	}
}

func TestLifecycleEntries(t *testing.T) {
	long := strings.Repeat("word ", 100)
	j := hourlyJob("j")
	j.Prompt = long
	e := LifecycleEntry(at(9, 0), ResultCreated, j, Actor{Kind: ActorWindow, WindowID: "@12", SessionID: "0123456789abcdef"}, "")
	if e.Def == nil || e.Def.PromptSource != FieldPrompt || len([]rune(e.Def.Prompt)) != promptExcerptRunes+1 || !strings.HasSuffix(e.Def.Prompt, "…") {
		t.Fatalf("snapshot = %+v", e.Def)
	}
	if got := e.By.String(); got != "window @12 (session 01234567)" {
		t.Errorf("actor = %q", got)
	}
	k := hourlyJob("k")
	k.Prompt, k.PromptKey = "", "the-key"
	if s := SnapshotOf(k); s.PromptSource != "prompt_key" || s.Prompt != "the-key" || s.Job().PromptKey != "the-key" {
		t.Errorf("key snapshot = %+v", s)
	}
	if !e.IsLifecycle() || e.IsAttempt() {
		t.Error("lifecycle entry classified as a run")
	}
	entries := []Entry{
		{Job: "j", Result: ResultFired},
		e,
		{Job: "j", Result: Failed("launch")},
		{Job: "j", Result: Failed("launch")},
		{Job: "j", Result: ResultDeleted, Def: &JobSnapshot{Name: "j"}},
	}
	if last := LastByJob(entries)["j"]; last.Result != Failed("launch") {
		t.Errorf("LastByJob = %+v", last)
	}
	if got := FailingJobs(entries, 2); len(got) != 1 {
		t.Errorf("FailingJobs = %v", got)
	}
	if got := LatestSnapshots(entries)["j"]; got.Result != ResultDeleted {
		t.Errorf("LatestSnapshots = %+v", got)
	}
	if got := RunsOnly(entries); len(got) != 3 {
		t.Errorf("RunsOnly = %+v", got)
	}
}

// TestOldFilesStillRead: a jobs file and a runs.ndjson written by a binary
// that predates one-shots and lifecycle entries load unchanged.
func TestOldFilesStillRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SchedulesFile)
	old := `{"jobs":[{"name":"n","schedule":"0 9 * * *","dir":"/tmp/p","agent":"a","prompt":"p","harness":"claude","mode":"window","overlap":"skip","catchup":"latest"}]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := LoadJobs(path)
	if err != nil || len(f.Jobs) != 1 || f.Jobs[0].IsOnce() || f.Jobs[0].IsDone() || f.Jobs[0].Validate() != nil {
		t.Fatalf("old jobs file: %+v, %v", f, err)
	}
	if err := SaveJobs(path, f); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var raw struct{ Jobs []map[string]any }
	_ = json.Unmarshal(data, &raw)
	for _, k := range []string{"at", "state", "completed_at", "result"} {
		if _, ok := raw.Jobs[0][k]; ok {
			t.Errorf("a recurring job gained %q on save", k)
		}
	}
	if runs, err := ReadRuns(t.TempDir(), ""); err != nil || runs != nil {
		t.Errorf("missing runs.ndjson = %v, %v", runs, err)
	}
	if err := os.WriteFile(filepath.Join(dir, runsFile), []byte(`{"ts":"2026-11-17T09:00:00Z","job":"n","result":"fired"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runs, _ := ReadRuns(dir, "")
	if len(runs) != 1 || len(LatestSnapshots(runs)) != 0 || LastByJob(runs)["n"].Result != ResultFired {
		t.Errorf("old runs = %+v", runs)
	}
}

// TestAppendRunTrimUnderLock: an oversized file is trimmed under runs.lock,
// and appends from several goroutines (standing in for the tick and an
// editor) all land.
func TestAppendRunTrimUnderLock(t *testing.T) {
	dir := t.TempDir()
	pad := strings.Repeat("x", 4096)
	for i := 0; i < runsMaxBytes/4096+10; i++ {
		if err := AppendRun(dir, Entry{Job: "j", Result: ResultFired, Note: pad}); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(filepath.Join(dir, runsFile))
	if err != nil || fi.Size() > runsMaxBytes {
		t.Fatalf("not trimmed: %v %v", fi.Size(), err)
	}
	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 20; i++ {
				_ = AppendRun(dir, Entry{Job: "k", Result: ResultCreated})
			}
		}()
	}
	for g := 0; g < 4; g++ {
		<-done
	}
	runs, _ := ReadRuns(dir, "k")
	if len(runs) != 80 {
		t.Errorf("appends lost: %d of 80", len(runs))
	}
}
