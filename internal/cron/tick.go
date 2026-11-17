package cron

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/ratelimit"
)

// ErrTmuxUnavailable is returned by a Launcher when tmux cannot be run; the
// fire is recorded as failed:tmux-unavailable and a notification is sent.
var ErrTmuxUnavailable = errors.New("tmux is not available")

// onTimeGrace is how late a fire may start and still count as on time for
// catchup: none. A 60s StartInterval plus up to 90s of jitter and launchd
// scheduling slop fit comfortably inside it.
const onTimeGrace = 5 * time.Minute

// usageStaleAfter is the age past which pushed rate-limit data is treated as
// unknown: the gate lets the run through and says so.
const usageStaleAfter = 6 * time.Hour

// Window is a live tmux window a cron job launched (windowstate
// scheduled_by), as listed by Deps.Windows.
type Window struct {
	ID          string
	Job         string
	RuntimeDir  string
	Session     string
	MainPane    string
	SessionID   string
	Done        bool
	ScheduledAt time.Time
}

// LaunchRequest is one fire handed to Deps.Launch.
type LaunchRequest struct {
	Job       Job
	Scheduled time.Time
	// Branch is the worktree branch (cron/<job>-<YYYYMMDD-HHMM>, which
	// worktree.Create turns into the slug cron-<job>-<YYYYMMDD-HHMM>), or
	// "" when the job sets worktree: false.
	Branch string
}

// LaunchResult reports where a fire landed.
type LaunchResult struct {
	WindowID string
	Session  string
}

// Deps are a tick's inputs and side effects. Every exec, tmux call and
// launch goes through here so tests never spawn anything.
type Deps struct {
	// Dir is <StateDir>/cron (lock, state, runs).
	Dir    string
	Config config.CronConfig
	Jobs   []Job
	Now    func() time.Time
	// Windows lists the live windows launched by any of jobs.
	Windows func(jobs []Job) ([]Window, error)
	// Account resolves the Claude account a job runs under (its flag, the
	// agent's account, account_rules on the repo root, default_account), the
	// same way dispatch does. "" means no accounts are configured. nil uses
	// the job's own account field.
	Account func(job Job) (string, error)
	// Usage returns account's pushed 5h/7d rate limits (nil when none).
	Usage func(account string) (*ratelimit.PushedRateLimits, error)
	// Launch starts one fire.
	Launch func(LaunchRequest) (LaunchResult, error)
	// Interrupt sends C-c to a timed-out window and marks it blocked.
	Interrupt func(w Window, job Job, timeout time.Duration) error
	// Notify sends a desktop notification.
	Notify func(title, message string)
	// Logf records diagnostics.
	Logf func(format string, args ...any)
}

func (d *Deps) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

func (d *Deps) notify(title, msg string) {
	if d.Notify != nil {
		d.Notify(title, msg)
	}
}

// BranchFor returns the worktree branch for a fire of job at t.
func BranchFor(job Job, t time.Time) string {
	if !job.UseWorktree() {
		return ""
	}
	return fmt.Sprintf("cron/%s-%s", job.Name, t.Format("20060102-1504"))
}

// TickResult summarizes one tick, for callers and tests.
type TickResult struct {
	// Locked is false when another tick held the lock and this one did
	// nothing.
	Locked   bool
	Disabled bool
	Entries  []Entry
}

// Tick runs one scheduler pass: timeouts, then for every enabled job the due
// fire (if any) through catch-up, overlap, max_concurrent and the usage
// gate. It returns at once when another tick holds the lock.
func Tick(d Deps) (TickResult, error) {
	unlock, ok, err := Lock(d.Dir, false)
	if err != nil {
		return TickResult{}, err
	}
	if !ok {
		return TickResult{}, nil
	}
	defer unlock()
	t := &ticker{d: d, res: TickResult{Locked: true}}
	err = t.run()
	return t.res, err
}

type ticker struct {
	d       Deps
	st      *State
	now     time.Time
	windows []Window
	running int
	res     TickResult
}

func (t *ticker) run() error {
	st, err := LoadState(t.d.Dir)
	if err != nil {
		t.d.logf("cron: %v (starting from empty state)", err)
	}
	t.st = st
	t.now = t.d.Now()
	t.st.LastTick = t.now
	t.pruneState()
	if !t.d.Config.EnabledOrDefault() {
		t.res.Disabled = true
		return SaveState(t.d.Dir, t.st)
	}
	t.loadWindows()
	t.checkTimeouts()

	jobs := slices.Clone(t.d.Jobs)
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
	for _, job := range jobs {
		if !job.IsEnabled() {
			continue
		}
		if err := t.tickJob(job); err != nil {
			return err
		}
	}
	return SaveState(t.d.Dir, t.st)
}

// pruneState drops state for jobs that no longer exist.
func (t *ticker) pruneState() {
	for name := range t.st.Jobs {
		if !slices.ContainsFunc(t.d.Jobs, func(j Job) bool { return j.Name == name }) {
			delete(t.st.Jobs, name)
		}
	}
}

func (t *ticker) loadWindows() {
	if t.d.Windows == nil {
		return
	}
	ws, err := t.d.Windows(t.d.Jobs)
	if err != nil {
		t.d.logf("cron: listing scheduled windows: %v", err)
	}
	t.windows = ws
	t.running = countRunning(ws)
}

func countRunning(ws []Window) int {
	n := 0
	for _, w := range ws {
		if !w.Done {
			n++
		}
	}
	return n
}

// checkTimeouts interrupts each live, not-done window whose job timeout has
// passed, once per window.
func (t *ticker) checkTimeouts() {
	for _, job := range t.d.Jobs {
		js := t.st.Jobs[job.Name]
		js.TimedOut = slices.DeleteFunc(js.TimedOut, func(id string) bool {
			return !slices.ContainsFunc(t.windows, func(w Window) bool { return w.ID == id && w.Job == job.Name })
		})
		timeout := job.TimeoutDuration()
		for _, w := range t.windows {
			if w.Job != job.Name || timeout == 0 || w.Done || w.ScheduledAt.IsZero() ||
				t.now.Sub(w.ScheduledAt) < timeout || slices.Contains(js.TimedOut, w.ID) {
				continue
			}
			e := Entry{Time: t.now, Job: job.Name, Result: ResultTimeout, WindowID: w.ID, Session: w.Session,
				Note: fmt.Sprintf("running %s, over its %s timeout; sent C-c", t.now.Sub(w.ScheduledAt).Round(time.Minute), timeout)}
			if t.d.Interrupt != nil {
				if err := t.d.Interrupt(w, job, timeout); err != nil {
					e.Note = "interrupt failed: " + err.Error()
				}
			}
			js.TimedOut = append(js.TimedOut, w.ID)
			t.record(e)
		}
		if len(js.TimedOut) == 0 {
			js.TimedOut = nil
		}
		if _, ok := t.st.Jobs[job.Name]; ok || js.TimedOut != nil {
			t.st.Jobs[job.Name] = js
		}
	}
}

func (t *ticker) record(e Entry) {
	t.res.Entries = append(t.res.Entries, e)
	if err := AppendRun(t.d.Dir, e); err != nil {
		t.d.logf("cron: recording run: %v", err)
	}
}

// tickJob decides and, if due and allowed, launches one job.
func (t *ticker) tickJob(job Job) error {
	sched, err := ParseSchedule(job.Schedule)
	if err != nil {
		t.d.logf("cron: job %s: %v", job.Name, err)
		return nil
	}
	js := t.st.Jobs[job.Name]
	if js.LastFire.IsZero() {
		// First sight of the job: seed, never back-fill.
		js.LastFire = t.now
		t.st.Jobs[job.Name] = js
		return nil
	}
	jitter := Jitter(job.Name)
	due, ok := DueFires(sched, js.LastFire, t.now, jitter)
	if !ok {
		return nil
	}
	js.LastFire = due.Latest
	t.st.Jobs[job.Name] = js
	base := Entry{Time: t.now, Job: job.Name, Scheduled: due.Latest, Missed: due.Missed, Account: job.Account}

	late := t.now.Sub(due.Latest.Add(jitter))
	if win := t.d.Config.EffectiveCatchupWindow(); late > win {
		base.Result, base.Note = ResultSkippedCatchup, fmt.Sprintf("%s late, past catchup_window %s", late.Round(time.Minute), win)
		t.record(base)
		return nil
	}
	if job.Catchup == CatchupNone && late > onTimeGrace {
		base.Result, base.Note = ResultSkippedCatchup, fmt.Sprintf("%s late and catchup is none", late.Round(time.Minute))
		t.record(base)
		return nil
	}
	account, ok := t.resolveAccount(job, &base)
	if !ok {
		return nil
	}
	res, note, skip := t.gate(job, account, false)
	base.Note = note
	if skip {
		base.Result = res
		t.record(base)
		return nil
	}
	// Persist last_fire before launching: a crash mid-launch must not fire
	// the same scheduled time again.
	if err := SaveState(t.d.Dir, t.st); err != nil {
		base.Result, base.Note = Failed("state"), err.Error()
		t.record(base)
		return err
	}
	t.launch(job, due.Latest, base)
	return nil
}

// gate applies overlap, max_concurrent and the usage gate. It returns the
// skip result and a note, or skip=false (note may still say the usage data
// was unknown).
func (t *ticker) gate(job Job, account string, force bool) (result, note string, skip bool) {
	if force {
		return "", "forced", false
	}
	if job.Overlap != OverlapAllow {
		for _, w := range t.windows {
			if w.Job == job.Name && !w.Done {
				return ResultSkippedOverlap, "previous run still active in window " + w.ID, true
			}
		}
	}
	if max := t.d.Config.EffectiveMaxConcurrent(); t.running >= max {
		return ResultSkippedConcurrency, fmt.Sprintf("%d scheduled runs active (max_concurrent %d)", t.running, max), true
	}
	var pr *ratelimit.PushedRateLimits
	var err error
	if t.d.Usage != nil {
		pr, err = t.d.Usage(account)
	}
	if err != nil {
		t.d.logf("cron: reading rate limits: %v", err)
		pr = nil
	}
	over, why := UsageGate(pr, t.now, job.UsageThresholds(t.d.Config.EffectiveUsageThresholds()))
	if over {
		if account != "" {
			why += " (account " + account + ")"
		}
		return ResultSkippedUsage, why, true
	}
	return "", why, false
}

func (t *ticker) launch(job Job, scheduled time.Time, e Entry) {
	res, err := t.d.Launch(LaunchRequest{Job: job, Scheduled: scheduled, Branch: BranchFor(job, scheduled)})
	if err != nil {
		reason := "launch"
		if errors.Is(err, ErrTmuxUnavailable) {
			reason = "tmux-unavailable"
		}
		e.Result = Failed(reason)
		e.Note = joinNote(e.Note, err.Error())
		t.record(e)
		t.d.notify("agentctl cron: "+job.Name+" failed", err.Error())
		return
	}
	t.running++
	t.windows = append(t.windows, Window{ID: res.WindowID, Job: job.Name, Session: res.Session, ScheduledAt: t.now})
	e.Result, e.WindowID, e.Session = ResultFired, res.WindowID, res.Session
	t.record(e)
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// UsageGate reports whether pushed rate limits are at or over a threshold.
// Missing or stale (over 6h) data is unknown: the run goes ahead and the
// note says so. A window whose reset time has passed no longer counts.
func UsageGate(pr *ratelimit.PushedRateLimits, now time.Time, th config.CronUsageThresholds) (over bool, note string) {
	if pr == nil {
		return false, "usage unknown (no rate-limit data)"
	}
	if age := now.Sub(time.Unix(pr.UpdatedAt, 0)); age > usageStaleAfter {
		return false, fmt.Sprintf("usage unknown (rate-limit data %s old)", age.Round(time.Minute))
	}
	check := func(label string, w *ratelimit.PushedWindow, limit float64) (bool, string) {
		if w == nil || limit <= 0 {
			return false, ""
		}
		if w.ResetsAt > 0 && !now.Before(time.Unix(w.ResetsAt, 0)) {
			return false, ""
		}
		if w.UsedPercentage >= limit {
			return true, fmt.Sprintf("%s usage %.0f%% >= %.0f%%", label, w.UsedPercentage, limit)
		}
		return false, ""
	}
	if o, n := check("five_hour", pr.FiveHour, th.FiveHour); o {
		return true, n
	}
	if o, n := check("seven_day", pr.SevenDay, th.SevenDay); o {
		return true, n
	}
	return false, ""
}

// RunNow fires job immediately (`agentctl cron run`). It waits for a running
// tick, honors overlap, max_concurrent and the usage gate unless force, and
// leaves last_fire alone so the schedule is unaffected.
func RunNow(d Deps, name string, force bool) (Entry, error) {
	unlock, _, err := Lock(d.Dir, true)
	if err != nil {
		return Entry{}, err
	}
	defer unlock()
	i := slices.IndexFunc(d.Jobs, func(j Job) bool { return j.Name == name })
	if i < 0 {
		return Entry{}, fmt.Errorf("no cron job named %q", name)
	}
	job := d.Jobs[i]
	t := &ticker{d: d, now: d.Now()}
	t.loadWindows()
	e := Entry{Time: t.now, Job: job.Name, Scheduled: t.now, Manual: true, Account: job.Account}
	account, ok := t.resolveAccount(job, &e)
	if !ok {
		return e, nil
	}
	res, note, skip := t.gate(job, account, force)
	e.Note = note
	if skip {
		e.Result = res
		t.record(e)
		return e, nil
	}
	t.launch(job, t.now, e)
	return t.res.Entries[len(t.res.Entries)-1], nil
}

// Seed records now as job's last_fire so a newly added job starts from the
// next scheduled time instead of back-filling. It takes the tick lock.
func Seed(dir, job string, now time.Time) error {
	unlock, _, err := Lock(dir, true)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := LoadState(dir)
	if err != nil && st == nil {
		return err
	}
	js := st.Jobs[job]
	js.LastFire = now
	js.TimedOut = nil
	st.Jobs[job] = js
	return SaveState(dir, st)
}

// resolveAccount returns the account job runs under and stamps it on e. A
// resolution error (an account that is not configured) is recorded as
// failed:account and notified; there is no fallback to another account.
func (t *ticker) resolveAccount(job Job, e *Entry) (string, bool) {
	if t.d.Account == nil {
		e.Account = job.Account
		return job.Account, true
	}
	account, err := t.d.Account(job)
	if err != nil {
		e.Result, e.Note = Failed("account"), err.Error()
		t.record(*e)
		t.d.notify("agentctl cron: "+job.Name+" failed", err.Error())
		return "", false
	}
	e.Account = account
	return account, true
}
