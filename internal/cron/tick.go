package cron

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/ratelimit"
)

// ErrTmuxUnavailable is returned by a Launcher when tmux cannot be run; the
// fire is recorded as failed:tmux-unavailable and a notification is sent.
var ErrTmuxUnavailable = errors.New("tmux is not available")

// ErrTemplateNotFound is returned (wrapped) by a Launcher when a job's
// prompt template no longer resolves; the fire is recorded as
// failed:template, with the reason in the note, and notified.
var ErrTemplateNotFound = errors.New("template not found")

// TemplateError is a Launcher error for a template that cannot be read,
// with msg as its whole text; errors.Is matches ErrTemplateNotFound.
func TemplateError(msg string) error { return &templateError{msg: msg} }

type templateError struct{ msg string }

func (e *templateError) Error() string        { return e.msg }
func (e *templateError) Is(target error) bool { return target == ErrTemplateNotFound }

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
	// SessionID is the agent session the launch pre-assigned ("" when the
	// harness assigns it later). Window IDs are reused, so this is what
	// identifies the run once its window is gone.
	SessionID string
}

// Deps are a tick's inputs and side effects. Every exec, tmux call and
// launch goes through here so tests never spawn anything.
type Deps struct {
	// Dir is <StateDir>/cron (lock, state, runs).
	Dir string
	// JobsPath is schedules.json, which the tick writes only to record a
	// one-shot's outcome and to prune done one-shots (under TryLockJobs).
	// "" leaves the jobs file alone (state.json still stops a refire).
	JobsPath string
	Config   config.CronConfig
	Jobs     []Job
	Now      func() time.Time
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
	// Notify sends a desktop notification: the job name as the title, the
	// event ("Cron run failed · project") as the subtitle, the cause as the
	// message.
	Notify func(title, subtitle, message string)
	// Logf records diagnostics.
	Logf func(format string, args ...any)
}

func (d *Deps) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

func (d *Deps) notify(title, subtitle, msg string) {
	if d.Notify != nil {
		d.Notify(title, subtitle, msg)
	}
}

// notifyFailed announces a job that did not run: titled by the job, with the
// project (the job directory's name) after the event.
func (d *Deps) notifyFailed(job Job, event string, err error) {
	subtitle := event
	if job.Dir != "" {
		subtitle += " · " + filepath.Base(job.Dir)
	}
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if msg == "" {
		msg = event
	}
	d.notify(job.Name, subtitle, msg)
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
	// sync holds one-shot outcomes to mirror into the jobs file.
	sync map[string]OneShotOutcome
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
			// A paused one-shot that already fired still gets its outcome
			// into the jobs file, so a resume sees it done.
			if o := t.st.Jobs[job.Name].OneShot; job.IsOnce() && o.matches(job) && o.State != OneShotLaunching {
				t.queueSync(job, *o)
			}
			continue
		}
		if err := t.tickJob(job); err != nil {
			return err
		}
	}
	if err := SaveState(t.d.Dir, t.st); err != nil {
		return err
	}
	t.writeJobsFile(t.donePrunable())
	return nil
}

// donePrunable lists the done one-shots whose outcome is older than
// PruneDoneAfter.
func (t *ticker) donePrunable() []string {
	var out []string
	for _, j := range t.d.Jobs {
		if prunable(j, t.now) {
			out = append(out, j.Name)
		}
	}
	return out
}

func prunable(j Job, now time.Time) bool {
	return j.IsOnce() && j.IsDone() && !j.CompletedAt.IsZero() && now.Sub(j.CompletedAt) >= PruneDoneAfter
}

// queueSync schedules o to be written onto the job in the jobs file, unless
// the file already says so.
func (t *ticker) queueSync(job Job, o OneShotOutcome) {
	if job.State == o.State && job.CompletedAt.Equal(o.CompletedAt) && job.Result == o.Result {
		return
	}
	if t.sync == nil {
		t.sync = map[string]OneShotOutcome{}
	}
	t.sync[job.Name] = o
}

// writeJobsFile mirrors the queued one-shot outcomes into the jobs file and
// removes the prune jobs, in one write, then records a pruned entry for
// each. It never waits for the jobs lock (see TryLockJobs): when an editor
// holds it, the next tick retries, and state.json keeps the outcome
// meanwhile. The file is re-read under the lock, so a concurrent edit is
// kept; an outcome is applied only while the job still has the at time it
// is for.
func (t *ticker) writeJobsFile(prune []string) {
	if t.d.JobsPath == "" || (len(t.sync) == 0 && len(prune) == 0) {
		return
	}
	unlock, ok, err := TryLockJobs(t.d.Dir)
	if err != nil {
		t.d.logf("cron: locking jobs file: %v", err)
		return
	}
	if !ok {
		t.d.logf("cron: jobs file busy; one-shot outcomes and prunes wait for the next tick")
		return
	}
	defer unlock()
	f, err := LoadJobs(t.d.JobsPath)
	if err != nil {
		t.d.logf("cron: %v", err)
		return
	}
	changed := false
	for name, o := range t.sync {
		i := f.Find(name)
		if i < 0 || !(&o).matches(f.Jobs[i]) {
			continue
		}
		o.Apply(&f.Jobs[i])
		changed = true
	}
	var pruned []Job
	for _, name := range prune {
		i := f.Find(name)
		if i < 0 || !prunable(f.Jobs[i], t.now) {
			continue
		}
		pruned = append(pruned, f.Jobs[i])
		f.Jobs = slices.Delete(f.Jobs, i, i+1)
		changed = true
	}
	if !changed {
		return
	}
	if err := SaveJobs(t.d.JobsPath, f); err != nil {
		t.d.logf("cron: %v", err)
		return
	}
	t.sync = nil
	for _, j := range pruned {
		note := fmt.Sprintf("%s %s ago; one-shots are pruned after %s", j.State, ShortDuration(t.now.Sub(j.CompletedAt).Truncate(time.Hour)), ShortDuration(PruneDoneAfter))
		t.record(LifecycleEntry(t.now, ResultPruned, j, Actor{Kind: ActorTick}, note))
	}
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
	if job.IsOnce() {
		return t.tickOnce(job)
	}
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

// tickOnce handles a one-shot: fire once when its at time (plus jitter) is
// due, through the same catch-up and gate rules as a scheduled fire. A gate
// skip leaves it pending for a later tick; a launch attempt (fired or
// failed) completes it; past catchup_window (or a catchup none job past
// the grace) it expires. min_interval does not apply.
func (t *ticker) tickOnce(job Job) error {
	at, err := job.AtTime()
	if err != nil {
		t.d.logf("cron: job %s: %v", job.Name, err)
		return nil
	}
	js := t.st.Jobs[job.Name]
	if o := js.OneShot; o.matches(job) {
		if o.State == OneShotLaunching {
			// The process died between persisting the launch and recording
			// its result: never retry (it may have started).
			o.State, o.CompletedAt, o.Result = StateCompleted, t.now, "unknown"
			t.d.logf("cron: job %s: launch was interrupted; marking the one-shot completed", job.Name)
		}
		t.queueSync(job, *o)
		return nil
	}
	if job.IsDone() {
		return nil
	}
	if js.LastFire.IsZero() {
		// First sight (state lost, or the file was edited by hand).
		js.LastFire = t.now
		t.st.Jobs[job.Name] = js
	}
	fireAt := at.Add(Jitter(job.Name))
	if t.now.Before(fireAt) {
		return nil
	}
	base := Entry{Time: t.now, Job: job.Name, Scheduled: at, Account: job.Account}
	late := t.now.Sub(fireAt)
	switch win := t.d.Config.EffectiveCatchupWindow(); {
	case js.LastFire.After(fireAt):
		t.expireOnce(job, at, base, "its time passed while it was paused (or before the scheduler saw it); not caught up")
		return nil
	case late > win:
		t.expireOnce(job, at, base, fmt.Sprintf("%s late, past catchup_window %s", late.Round(time.Minute), win))
		return nil
	case job.Catchup == CatchupNone && late > onTimeGrace:
		t.expireOnce(job, at, base, fmt.Sprintf("%s late and catchup is none", late.Round(time.Minute)))
		return nil
	}
	account, ok := t.resolveAccount(job, &base)
	if !ok {
		t.finishOnce(job, at, t.res.Entries[len(t.res.Entries)-1])
		return nil
	}
	res, note, skip := t.gate(job, account, false)
	base.Note = note
	if skip {
		base.Result = res
		if js.OnceSkip != res {
			// Recorded once per reason, not every tick it stays skipped.
			js.OnceSkip = res
			t.st.Jobs[job.Name] = js
			t.record(base)
		}
		return nil
	}
	if err := t.markLaunching(job.Name, at); err != nil {
		base.Result, base.Note = Failed("state"), err.Error()
		t.record(base)
		return err
	}
	t.finishOnce(job, at, t.launch(job, at, base))
	return nil
}

// markLaunching persists a one-shot's launching marker before its launch,
// so a crash mid-launch cannot fire it again.
func (t *ticker) markLaunching(name string, at time.Time) error {
	js := t.st.Jobs[name]
	prev := js
	js.OneShot, js.OnceSkip = &OneShotOutcome{At: at, State: OneShotLaunching}, ""
	t.st.Jobs[name] = js
	if err := SaveState(t.d.Dir, t.st); err != nil {
		t.st.Jobs[name] = prev
		return err
	}
	return nil
}

// expireOnce records a missed one-shot as skipped:catchup and expires it.
func (t *ticker) expireOnce(job Job, at time.Time, e Entry, note string) {
	e.Result, e.Note = ResultSkippedCatchup, note
	t.record(e)
	t.setOutcome(job, OneShotOutcome{At: at, State: StateExpired, CompletedAt: t.now, Result: e.Result})
}

// finishOnce completes a one-shot from its launch attempt's entry.
func (t *ticker) finishOnce(job Job, at time.Time, e Entry) {
	t.setOutcome(job, OneShotOutcome{
		At: at, State: StateCompleted, CompletedAt: e.Time, Result: e.Result,
		WindowID: e.WindowID, Session: e.Session, SessionID: e.SessionID,
	})
}

func (t *ticker) setOutcome(job Job, o OneShotOutcome) {
	js := t.st.Jobs[job.Name]
	js.OneShot, js.OnceSkip = &o, ""
	t.st.Jobs[job.Name] = js
	t.queueSync(job, o)
}

// OnceDoneError is the refusal to fire or resume a done one-shot.
func OnceDoneError(job Job) error {
	what := "already ran"
	if job.State == StateExpired {
		what = "expired (its time was missed)"
	}
	return fmt.Errorf("cron job %s is a one-shot that %s; schedule it again with agentctl cron edit %s --at 'YYYY-MM-DD HH:MM'", job.Name, what, job.Name)
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

func (t *ticker) launch(job Job, scheduled time.Time, e Entry) Entry {
	res, err := t.d.Launch(LaunchRequest{Job: job, Scheduled: scheduled, Branch: BranchFor(job, scheduled)})
	if err != nil {
		reason := "launch"
		switch {
		case errors.Is(err, ErrTmuxUnavailable):
			reason = "tmux-unavailable"
		case errors.Is(err, ErrTemplateNotFound):
			reason = "template"
		}
		e.Result = Failed(reason)
		e.Note = joinNote(e.Note, err.Error())
		t.record(e)
		t.d.notifyFailed(job, "Cron run failed", err)
		return e
	}
	t.running++
	t.windows = append(t.windows, Window{ID: res.WindowID, Job: job.Name, Session: res.Session, SessionID: res.SessionID, ScheduledAt: t.now})
	e.Result, e.WindowID, e.Session, e.SessionID = ResultFired, res.WindowID, res.Session, res.SessionID
	t.record(e)
	return e
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
// leaves last_fire alone so the schedule is unaffected. A pending one-shot
// is completed by the launch attempt, as if its time had come; a done one
// is refused (OnceDoneError).
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
	var at time.Time
	if job.IsOnce() {
		if at, err = job.AtTime(); err != nil {
			return Entry{}, err
		}
		t.st, err = LoadState(d.Dir)
		if err != nil {
			d.logf("cron: %v (starting from empty state)", err)
		}
		if o := t.st.Jobs[job.Name].OneShot; job.IsDone() || o.matches(job) {
			if o.matches(job) && o.State != OneShotLaunching {
				o.Apply(&job)
			}
			return Entry{}, OnceDoneError(job)
		}
	}
	t.loadWindows()
	e := Entry{Time: t.now, Job: job.Name, Scheduled: t.now, Manual: true, Account: job.Account}
	account, ok := t.resolveAccount(job, &e)
	if !ok {
		if job.IsOnce() {
			// failed:account is a launch attempt: it completes the one-shot,
			// as at a scheduled fire.
			t.finishOnce(job, at, e)
			if err := SaveState(d.Dir, t.st); err != nil {
				return e, err
			}
			t.writeJobsFile(nil)
		}
		return e, nil
	}
	res, note, skip := t.gate(job, account, force)
	e.Note = note
	if skip {
		e.Result = res
		t.record(e)
		return e, nil
	}
	if !job.IsOnce() {
		return t.launch(job, t.now, e), nil
	}
	if err := t.markLaunching(job.Name, at); err != nil {
		return Entry{}, err
	}
	out := t.launch(job, t.now, e)
	t.finishOnce(job, at, out)
	if err := SaveState(d.Dir, t.st); err != nil {
		return out, err
	}
	t.writeJobsFile(nil)
	return out, nil
}

// Seed records now as job's last_fire so a newly added job starts from the
// next scheduled time instead of back-filling. It takes the tick lock.
func Seed(dir, job string, now time.Time) error {
	unlock, _, err := Lock(dir, true)
	if err != nil {
		return err
	}
	defer unlock()
	return SeedLocked(dir, job, now)
}

// SeedLocked is Seed for a caller that already holds the tick lock. A
// one-shot's recorded outcome is kept: a resume must not forget that it
// already fired (the jobs file may not show it yet). SeedFreshLocked also
// drops it.
func SeedLocked(dir, job string, now time.Time) error {
	return seedLocked(dir, job, now, false)
}

// SeedFreshLocked is SeedLocked for a new job or a new one-shot time: it
// also drops the one-shot outcome, so the job starts pending.
func SeedFreshLocked(dir, job string, now time.Time) error {
	return seedLocked(dir, job, now, true)
}

func seedLocked(dir, job string, now time.Time, fresh bool) error {
	st, err := LoadState(dir)
	if err != nil && st == nil {
		return err
	}
	js := st.Jobs[job]
	js.LastFire = now
	js.TimedOut = nil
	js.OnceSkip = ""
	if fresh {
		js.OneShot = nil
	}
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
		t.d.notifyFailed(job, "Cron run failed", err)
		return "", false
	}
	e.Account = account
	return account, true
}
