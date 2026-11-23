package cron

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/ratelimit"
)

// fakeEnv records every side effect a tick has; nothing is spawned.
type fakeEnv struct {
	mu            sync.Mutex
	launches      []LaunchRequest
	interrupts    []string
	notes         []string
	windows       []Window
	usage         *ratelimit.PushedRateLimits
	launchErr     error
	onLaunch      func(LaunchRequest)
	nextWindow    int
	windowsErr    error
	usageAccounts []string
	usageBy       map[string]*ratelimit.PushedRateLimits
}

func (f *fakeEnv) deps(dir string, now time.Time, cfg config.CronConfig, jobs ...Job) Deps {
	return Deps{
		Dir: dir, Config: cfg, Jobs: jobs,
		Now: func() time.Time { return now },
		Windows: func([]Job) ([]Window, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return append([]Window(nil), f.windows...), f.windowsErr
		},
		Usage: func(account string) (*ratelimit.PushedRateLimits, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.usageAccounts = append(f.usageAccounts, account)
			if f.usageBy != nil {
				return f.usageBy[account], nil
			}
			return f.usage, nil
		},
		Launch: func(r LaunchRequest) (LaunchResult, error) {
			if f.onLaunch != nil {
				f.onLaunch(r)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.launches = append(f.launches, r)
			if f.launchErr != nil {
				return LaunchResult{}, f.launchErr
			}
			f.nextWindow++
			return LaunchResult{WindowID: fmt.Sprintf("@%d", f.nextWindow), Session: "agentctl-proj"}, nil
		},
		Interrupt: func(w Window, _ Job, _ time.Duration) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.interrupts = append(f.interrupts, w.ID)
			return nil
		},
		Notify: func(title, subtitle, msg string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.notes = append(f.notes, title+" | "+subtitle+" | "+msg)
		},
	}
}

// hourlyJob fires at minute 0; its jitter is computed from its name.
func hourlyJob(name string) Job {
	j := Job{Name: name, Schedule: "0 * * * *", Dir: "/tmp/p", Agent: "a", Prompt: "p"}
	j.Normalize()
	return j
}

func seedState(t *testing.T, dir string, lastFire map[string]time.Time) {
	t.Helper()
	st := &State{Jobs: map[string]JobState{}}
	for k, v := range lastFire {
		st.Jobs[k] = JobState{LastFire: v}
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatal(err)
	}
}

// at returns a local time on 2026-11-17.
func at(h, m int) time.Time { return time.Date(2026, 11, 17, h, m, 0, 0, time.Local) }

func TestTickFirstRunSeeds(t *testing.T) {
	dir := t.TempDir()
	f := &fakeEnv{}
	now := at(9, 30)
	if _, err := Tick(f.deps(dir, now, config.CronConfig{}, hourlyJob("j"))); err != nil {
		t.Fatal(err)
	}
	if len(f.launches) != 0 {
		t.Fatalf("first sight launched %d", len(f.launches))
	}
	st, _ := LoadState(dir)
	if !st.Jobs["j"].LastFire.Equal(now) || !st.LastTick.Equal(now) {
		t.Errorf("state = %+v", st)
	}
}

func TestTickDecisions(t *testing.T) {
	type tc struct {
		name       string
		job        Job
		lastFire   time.Time
		now        time.Time
		cfg        config.CronConfig
		windows    []Window
		usage      *ratelimit.PushedRateLimits
		launchErr  error
		wantResult string // "" = no entry
		wantLaunch bool
		wantNotify bool
	}
	job := hourlyJob("j")
	jit := Jitter("j")
	onTime := at(10, 0).Add(jit + 30*time.Second)
	noCatch := job
	noCatch.Catchup = CatchupNone
	allow := job
	allow.Overlap = OverlapAllow
	fresh := func(five, seven float64) *ratelimit.PushedRateLimits {
		return &ratelimit.PushedRateLimits{
			UpdatedAt: onTime.Add(-10 * time.Minute).Unix(),
			FiveHour:  &ratelimit.PushedWindow{UsedPercentage: five, ResetsAt: onTime.Add(time.Hour).Unix()},
			SevenDay:  &ratelimit.PushedWindow{UsedPercentage: seven, ResetsAt: onTime.Add(48 * time.Hour).Unix()},
		}
	}
	tests := []tc{
		{name: "not due", job: job, lastFire: at(10, 0), now: at(10, 30)},
		{name: "jitter not yet elapsed", job: job, lastFire: at(9, 0), now: at(10, 0).Add(jit - time.Second)},
		{name: "due fires", job: job, lastFire: at(9, 0), now: onTime, wantResult: ResultFired, wantLaunch: true},
		{name: "catchup latest within window", job: job, lastFire: at(5, 0), now: at(10, 0).Add(jit + 50*time.Minute), wantResult: ResultFired, wantLaunch: true},
		{name: "sleep gap past catchup window", job: job, lastFire: at(1, 0), now: at(10, 0).Add(jit + 59*time.Minute),
			cfg: config.CronConfig{CatchupWindow: "30m"}, wantResult: ResultSkippedCatchup},
		{name: "catchup none on time", job: noCatch, lastFire: at(9, 0), now: onTime, wantResult: ResultFired, wantLaunch: true},
		{name: "catchup none late", job: noCatch, lastFire: at(9, 0), now: at(10, 0).Add(jit + 20*time.Minute), wantResult: ResultSkippedCatchup},
		{name: "overlap: previous running", job: job, lastFire: at(9, 0), now: onTime,
			windows: []Window{{ID: "@1", Job: "j"}}, wantResult: ResultSkippedOverlap},
		{name: "overlap: previous done", job: job, lastFire: at(9, 0), now: onTime,
			windows: []Window{{ID: "@1", Job: "j", Done: true}}, wantResult: ResultFired, wantLaunch: true},
		{name: "overlap: previous window gone", job: job, lastFire: at(9, 0), now: onTime, wantResult: ResultFired, wantLaunch: true},
		{name: "overlap allow", job: allow, lastFire: at(9, 0), now: onTime,
			windows: []Window{{ID: "@1", Job: "j"}}, wantResult: ResultFired, wantLaunch: true},
		{name: "max concurrent reached", job: job, lastFire: at(9, 0), now: onTime,
			windows: []Window{{ID: "@1", Job: "x"}, {ID: "@2", Job: "y"}}, wantResult: ResultSkippedConcurrency},
		{name: "done windows do not count toward concurrency", job: job, lastFire: at(9, 0), now: onTime,
			windows: []Window{{ID: "@1", Job: "x"}, {ID: "@2", Job: "y", Done: true}}, wantResult: ResultFired, wantLaunch: true},
		{name: "usage fresh over five_hour", job: job, lastFire: at(9, 0), now: onTime, usage: fresh(85, 10), wantResult: ResultSkippedUsage},
		{name: "usage fresh over seven_day", job: job, lastFire: at(9, 0), now: onTime, usage: fresh(10, 95), wantResult: ResultSkippedUsage},
		{name: "usage fresh under", job: job, lastFire: at(9, 0), now: onTime, usage: fresh(50, 50), wantResult: ResultFired, wantLaunch: true},
		{name: "tmux unavailable", job: job, lastFire: at(9, 0), now: onTime, launchErr: fmt.Errorf("x: %w", ErrTmuxUnavailable),
			wantResult: Failed("tmux-unavailable"), wantLaunch: true, wantNotify: true},
		{name: "template missing", job: job, lastFire: at(9, 0), now: onTime, launchErr: TemplateError(`template "x" not found`),
			wantResult: Failed("template"), wantLaunch: true, wantNotify: true},
		{name: "launch error", job: job, lastFire: at(9, 0), now: onTime, launchErr: errors.New("boom"),
			wantResult: Failed("launch"), wantLaunch: true, wantNotify: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			seedState(t, dir, map[string]time.Time{"j": tt.lastFire})
			f := &fakeEnv{windows: tt.windows, usage: tt.usage, launchErr: tt.launchErr}
			res, err := Tick(f.deps(dir, tt.now, tt.cfg, tt.job))
			if err != nil {
				t.Fatal(err)
			}
			if got := len(f.launches) > 0; got != tt.wantLaunch {
				t.Errorf("launched = %v, want %v", got, tt.wantLaunch)
			}
			if got := len(f.notes) > 0; got != tt.wantNotify {
				t.Errorf("notified = %v (%v), want %v", got, f.notes, tt.wantNotify)
			}
			var got string
			if len(res.Entries) > 0 {
				got = res.Entries[0].Result
			}
			if got != tt.wantResult {
				t.Errorf("result = %q (%+v), want %q", got, res.Entries, tt.wantResult)
			}
			runs, _ := ReadRuns(dir, "")
			if len(runs) != len(res.Entries) {
				t.Errorf("runs.ndjson has %d entries, tick reported %d", len(runs), len(res.Entries))
			}
			if tt.wantResult != "" {
				st, _ := LoadState(dir)
				if !st.Jobs["j"].LastFire.After(tt.lastFire) {
					t.Errorf("last_fire not advanced on a decision: %v", st.Jobs["j"].LastFire)
				}
				// A second tick at the same time decides nothing new.
				f2 := &fakeEnv{windows: tt.windows, usage: tt.usage}
				res2, _ := Tick(f2.deps(dir, tt.now, tt.cfg, tt.job))
				if len(res2.Entries) != 0 || len(f2.launches) != 0 {
					t.Errorf("second tick repeated the decision: %+v", res2.Entries)
				}
			}
		})
	}
}

func TestTickLaunchRequest(t *testing.T) {
	dir := t.TempDir()
	job := hourlyJob("nightly")
	no := false
	flat := hourlyJob("flat")
	flat.Worktree = &no
	seedState(t, dir, map[string]time.Time{"nightly": at(9, 0), "flat": at(9, 0)})
	f := &fakeEnv{}
	if _, err := Tick(f.deps(dir, at(10, 2), config.CronConfig{}, job, flat)); err != nil {
		t.Fatal(err)
	}
	if len(f.launches) != 2 {
		t.Fatalf("launches = %d", len(f.launches))
	}
	byName := map[string]LaunchRequest{}
	for _, l := range f.launches {
		byName[l.Job.Name] = l
	}
	if got := byName["nightly"].Branch; got != "cron/nightly-20261117-1000" {
		t.Errorf("branch = %q", got)
	}
	if got := byName["flat"].Branch; got != "" {
		t.Errorf("worktree:false branch = %q", got)
	}
}

func TestTickPersistsLastFireBeforeLaunch(t *testing.T) {
	dir := t.TempDir()
	seedState(t, dir, map[string]time.Time{"j": at(9, 0)})
	f := &fakeEnv{}
	var seen time.Time
	f.onLaunch = func(LaunchRequest) {
		st, err := LoadState(dir)
		if err != nil {
			t.Error(err)
		}
		seen = st.Jobs["j"].LastFire
	}
	if _, err := Tick(f.deps(dir, at(10, 2), config.CronConfig{}, hourlyJob("j"))); err != nil {
		t.Fatal(err)
	}
	if !seen.Equal(at(10, 0)) {
		t.Errorf("last_fire on disk during launch = %v, want %v", seen, at(10, 0))
	}
}

func TestConcurrentTicksLaunchOnce(t *testing.T) {
	dir := t.TempDir()
	seedState(t, dir, map[string]time.Time{"j": at(9, 0)})
	f := &fakeEnv{}
	inLaunch, release := make(chan struct{}), make(chan struct{})
	f.onLaunch = func(LaunchRequest) {
		close(inLaunch)
		<-release
	}
	d := f.deps(dir, at(10, 2), config.CronConfig{}, hourlyJob("j"))
	done := make(chan TickResult)
	go func() {
		r, _ := Tick(d)
		done <- r
	}()
	<-inLaunch
	// The first tick holds the lock mid-launch: a second one backs off.
	r2, err := Tick(d)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Locked {
		t.Error("second tick ran while the first held the lock")
	}
	close(release)
	if r1 := <-done; !r1.Locked {
		t.Error("first tick did not run")
	}
	// And once the first is done, a third sees last_fire and does nothing.
	f.onLaunch = nil
	if _, err := Tick(d); err != nil {
		t.Fatal(err)
	}
	if len(f.launches) != 1 {
		t.Errorf("launches = %d, want 1", len(f.launches))
	}
}

func TestTickKillSwitch(t *testing.T) {
	dir := t.TempDir()
	seedState(t, dir, map[string]time.Time{"j": at(9, 0)})
	f := &fakeEnv{}
	off := false
	res, err := Tick(f.deps(dir, at(10, 5), config.CronConfig{Enabled: &off}, hourlyJob("j")))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Disabled || len(f.launches) != 0 {
		t.Errorf("kill switch ignored: %+v, %d launches", res, len(f.launches))
	}
}

func TestTickPausedJob(t *testing.T) {
	dir := t.TempDir()
	seedState(t, dir, map[string]time.Time{"j": at(9, 0)})
	f := &fakeEnv{}
	j := hourlyJob("j")
	off := false
	j.Enabled = &off
	if _, err := Tick(f.deps(dir, at(10, 5), config.CronConfig{}, j)); err != nil {
		t.Fatal(err)
	}
	if len(f.launches) != 0 {
		t.Error("paused job launched")
	}
}

func TestTickTimeout(t *testing.T) {
	dir := t.TempDir()
	job := hourlyJob("j")
	job.Timeout = "30m"
	seedState(t, dir, map[string]time.Time{"j": at(10, 0)})
	f := &fakeEnv{windows: []Window{
		{ID: "@1", Job: "j", ScheduledAt: at(9, 0)},             // over: interrupt
		{ID: "@2", Job: "j", ScheduledAt: at(10, 0)},            // under
		{ID: "@3", Job: "j", ScheduledAt: at(8, 0), Done: true}, // done: leave
		{ID: "@4", Job: "other", ScheduledAt: at(8, 0)},         // not this job
	}}
	d := f.deps(dir, at(10, 10), config.CronConfig{}, job)
	if _, err := Tick(d); err != nil {
		t.Fatal(err)
	}
	if len(f.interrupts) != 1 || f.interrupts[0] != "@1" {
		t.Fatalf("interrupts = %v", f.interrupts)
	}
	// Once per window.
	if _, err := Tick(d); err != nil {
		t.Fatal(err)
	}
	if len(f.interrupts) != 1 {
		t.Errorf("interrupted twice: %v", f.interrupts)
	}
	runs, _ := ReadRuns(dir, "j")
	if len(runs) != 1 || runs[0].Result != ResultTimeout || runs[0].WindowID != "@1" {
		t.Errorf("runs = %+v", runs)
	}
}

func TestUsageGate(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	th := config.CronUsageThresholds{FiveHour: 80, SevenDay: 90}
	win := func(pct float64, resets time.Time) *ratelimit.PushedWindow {
		return &ratelimit.PushedWindow{UsedPercentage: pct, ResetsAt: resets.Unix()}
	}
	future := now.Add(time.Hour)
	tests := []struct {
		name     string
		pr       *ratelimit.PushedRateLimits
		wantOver bool
		wantNote string
	}{
		{name: "missing", pr: nil, wantNote: "usage unknown (no rate-limit data)"},
		{name: "stale", pr: &ratelimit.PushedRateLimits{UpdatedAt: now.Add(-7 * time.Hour).Unix(), FiveHour: win(99, future)},
			wantNote: "usage unknown (rate-limit data 7h0m0s old)"},
		{name: "fresh over five_hour", pr: &ratelimit.PushedRateLimits{UpdatedAt: now.Unix(), FiveHour: win(80, future)},
			wantOver: true, wantNote: "five_hour usage 80% >= 80%"},
		{name: "fresh over seven_day", pr: &ratelimit.PushedRateLimits{UpdatedAt: now.Unix(), FiveHour: win(10, future), SevenDay: win(91, future)},
			wantOver: true, wantNote: "seven_day usage 91% >= 90%"},
		{name: "fresh under", pr: &ratelimit.PushedRateLimits{UpdatedAt: now.Unix(), FiveHour: win(79, future), SevenDay: win(89, future)}},
		{name: "window already reset", pr: &ratelimit.PushedRateLimits{UpdatedAt: now.Add(-2 * time.Hour).Unix(), FiveHour: win(99, now.Add(-time.Minute))}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			over, note := UsageGate(tt.pr, now, th)
			if over != tt.wantOver || note != tt.wantNote {
				t.Errorf("UsageGate = (%v, %q), want (%v, %q)", over, note, tt.wantOver, tt.wantNote)
			}
		})
	}
}

func TestRunNow(t *testing.T) {
	tests := []struct {
		name       string
		force      bool
		windows    []Window
		wantResult string
	}{
		{name: "fires", wantResult: ResultFired},
		{name: "honors overlap", windows: []Window{{ID: "@1", Job: "j"}}, wantResult: ResultSkippedOverlap},
		{name: "force bypasses overlap", force: true, windows: []Window{{ID: "@1", Job: "j"}}, wantResult: ResultFired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			seedState(t, dir, map[string]time.Time{"j": at(9, 0)})
			f := &fakeEnv{windows: tt.windows}
			e, err := RunNow(f.deps(dir, at(9, 20), config.CronConfig{}, hourlyJob("j")), "j", tt.force)
			if err != nil {
				t.Fatal(err)
			}
			if e.Result != tt.wantResult || !e.Manual {
				t.Errorf("entry = %+v", e)
			}
			st, _ := LoadState(dir)
			if !st.Jobs["j"].LastFire.Equal(at(9, 0)) {
				t.Error("cron run moved last_fire")
			}
		})
	}
	if _, err := RunNow((&fakeEnv{}).deps(t.TempDir(), at(9, 0), config.CronConfig{}), "nope", false); err == nil {
		t.Error("unknown job: no error")
	}
}

func TestSeedAndPrune(t *testing.T) {
	dir := t.TempDir()
	if err := Seed(dir, "gone", at(8, 0)); err != nil {
		t.Fatal(err)
	}
	if err := Seed(dir, "j", at(9, 0)); err != nil {
		t.Fatal(err)
	}
	f := &fakeEnv{}
	if _, err := Tick(f.deps(dir, at(9, 30), config.CronConfig{}, hourlyJob("j"))); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(dir)
	if _, ok := st.Jobs["gone"]; ok {
		t.Error("state of a removed job kept")
	}
	if !st.Jobs["j"].LastFire.Equal(at(9, 0)) {
		t.Errorf("seeded last_fire = %v", st.Jobs["j"].LastFire)
	}
	if fi, err := os.Stat(filepath.Join(dir, stateFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("state file mode: %v %v", fi, err)
	}
}

func TestFailingJobs(t *testing.T) {
	e := func(job, result string) Entry { return Entry{Job: job, Result: result} }
	entries := []Entry{
		e("a", Failed("launch")), e("a", ResultSkippedUsage), e("a", Failed("launch")), e("a", Failed("tmux-unavailable")),
		e("b", Failed("launch")), e("b", ResultFired), e("b", Failed("launch")), e("b", Failed("launch")),
		e("c", Failed("launch")), e("c", Failed("launch")), e("c", Failed("launch")), e("c", ResultFired),
	}
	got := FailingJobs(entries, 3)
	if len(got) != 1 || got[0] != "a" {
		t.Errorf("FailingJobs = %v, want [a]", got)
	}
}

// The usage gate reads the resolved account's own 5h/7d: with one account
// hot and one cool, only the hot account's job skips. A job with no explicit
// account is gated on whatever the resolver picks (e.g. an account_rules
// match).
func TestTickUsageGatePerAccount(t *testing.T) {
	now := at(10, 2)
	hot := &ratelimit.PushedRateLimits{UpdatedAt: now.Unix(), FiveHour: &ratelimit.PushedWindow{UsedPercentage: 95, ResetsAt: now.Add(time.Hour).Unix()}}
	cool := &ratelimit.PushedRateLimits{UpdatedAt: now.Unix(), FiveHour: &ratelimit.PushedWindow{UsedPercentage: 10, ResetsAt: now.Add(time.Hour).Unix()}}
	tests := []struct {
		name        string
		job         Job
		resolved    map[string]string // job name -> resolved account
		resolveErr  error
		wantResult  string
		wantAccount string
	}{
		{name: "explicit hot account skips", job: withAccount(hourlyJob("work"), "ll"),
			resolved: map[string]string{"work": "ll"}, wantResult: ResultSkippedUsage, wantAccount: "ll"},
		{name: "explicit cool account fires", job: withAccount(hourlyJob("home"), "personal"),
			resolved: map[string]string{"home": "personal"}, wantResult: ResultFired, wantAccount: "personal"},
		{name: "rule-resolved hot account skips", job: hourlyJob("ruled"),
			resolved: map[string]string{"ruled": "ll"}, wantResult: ResultSkippedUsage, wantAccount: "ll"},
		{name: "rule-resolved cool account fires", job: hourlyJob("ruled2"),
			resolved: map[string]string{"ruled2": "personal"}, wantResult: ResultFired, wantAccount: "personal"},
		{name: "unknown account fails without fallback", job: withAccount(hourlyJob("bad"), "nope"),
			resolveErr: errors.New(`flag account: account "nope" is not configured`), wantResult: Failed("account"), wantAccount: "nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			seedState(t, dir, map[string]time.Time{tt.job.Name: at(9, 0)})
			f := &fakeEnv{usageBy: map[string]*ratelimit.PushedRateLimits{"ll": hot, "personal": cool}}
			d := f.deps(dir, now, config.CronConfig{}, tt.job)
			d.Account = func(j Job) (string, error) {
				if tt.resolveErr != nil {
					return "", tt.resolveErr
				}
				return tt.resolved[j.Name], nil
			}
			res, err := Tick(d)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Entries) != 1 {
				t.Fatalf("entries = %+v", res.Entries)
			}
			e := res.Entries[0]
			if e.Result != tt.wantResult || e.Account != tt.wantAccount {
				t.Errorf("entry = %+v, want %s on %q", e, tt.wantResult, tt.wantAccount)
			}
			if tt.resolveErr == nil && (len(f.usageAccounts) != 1 || f.usageAccounts[0] != tt.wantAccount) {
				t.Errorf("usage read for %v, want [%s]", f.usageAccounts, tt.wantAccount)
			}
			if (tt.wantResult == ResultFired) != (len(f.launches) == 1) {
				t.Errorf("launches = %d", len(f.launches))
			}
		})
	}
}

func withAccount(j Job, account string) Job {
	j.Account = account
	return j
}

func TestNotifyFailedContent(t *testing.T) {
	tests := []struct {
		name string
		job  Job
		err  error
		want string
	}{
		{"job dir names the project", Job{Name: "nightly", Dir: "/code/acme"}, errors.New("tmux is not available"),
			"nightly | Cron run failed · acme | tmux is not available"},
		{"no dir, multi-line error is one line", Job{Name: "nightly"}, errors.New("boom\n  second line"),
			"nightly | Cron run failed | boom second line"},
		{"empty error still has a message", Job{Name: "nightly"}, errors.New(""),
			"nightly | Cron run failed | Cron run failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			d := Deps{Notify: func(title, subtitle, msg string) { got = title + " | " + subtitle + " | " + msg }}
			d.notifyFailed(tt.job, "Cron run failed", tt.err)
			if got != tt.want {
				t.Errorf("notification = %q, want %q", got, tt.want)
			}
		})
	}
}
