package cmd

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/launchd"
	"github.com/blsmth/agentctl/internal/ratelimit"
)

// relTime renders t relative to now: "3 minutes ago", "2 hours from now".
func relTime(t, now time.Time) string {
	return humanize.RelTime(t, now, "ago", "from now")
}

// cronListView is everything `cron list` renders, gathered up front so the
// rendering is a pure function.
type cronListView struct {
	now    time.Time
	cfg    config.CronConfig
	jobs   []cron.Job
	state  *cron.State
	last   map[string]cron.Entry
	active int
	// accounts maps each job to its resolved account ("" when no accounts
	// are configured); usage holds each account's pushed rate limits.
	accounts       map[string]string
	usage          map[string]*ratelimit.PushedRateLimits
	schedInstalled bool // scheduler installed
}

// RunCronList prints the jobs with their next fire, last result and
// throttles.
func RunCronList(w io.Writer) error { return defaultCronEnv.list(w) }

func (e cronEnv) list(w io.Writer) error {
	dir, cfg, jobs, err := e.loadAll()
	if err != nil {
		return err
	}
	st, err := cron.LoadState(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	runs, _ := cron.ReadRuns(dir, "")
	v := cronListView{now: e.now(), cfg: cfg, jobs: jobs.Jobs, state: st, last: cron.LastByJob(runs), schedInstalled: true}
	if e.schedulerInstalled != nil {
		v.schedInstalled = e.schedulerInstalled()
	}
	d := e.deps(dir, cfg, jobs.Jobs)
	if d.Windows != nil {
		if ws, err := d.Windows(jobs.Jobs); err == nil {
			for _, w := range ws {
				if !w.Done {
					v.active++
				}
			}
		}
	}
	v.accounts, v.usage = map[string]string{}, map[string]*ratelimit.PushedRateLimits{}
	for _, j := range jobs.Jobs {
		acct := j.Account
		if d.Account != nil {
			if a, err := d.Account(j); err == nil {
				acct = a
			}
		}
		v.accounts[j.Name] = acct
		if _, seen := v.usage[acct]; !seen && d.Usage != nil {
			v.usage[acct], _ = d.Usage(acct)
		}
	}
	renderCronList(w, v)
	return nil
}

func renderCronList(w io.Writer, v cronListView) {
	switch {
	case !v.cfg.EnabledOrDefault():
		fmt.Fprintln(w, "Scheduler: OFF (cron.enabled is false; tick does nothing)")
	case !v.schedInstalled:
		fmt.Fprintln(w, "Scheduler: not installed (run agentctl cron install)")
	}
	th := v.cfg.EffectiveUsageThresholds()
	fmt.Fprintf(w, "Throttles: min_interval %s · max_concurrent %d (%d active) · catchup_window %s · usage gate 5h ≥ %.0f%%, 7d ≥ %.0f%%\n",
		shortDuration(v.cfg.EffectiveMinInterval()), v.cfg.EffectiveMaxConcurrent(), v.active, shortDuration(v.cfg.EffectiveCatchupWindow()),
		th.FiveHour, th.SevenDay)
	for _, acct := range sortedKeys(v.usage) {
		label := "Usage"
		if acct != "" {
			label = "Usage (" + acct + ")"
		}
		fmt.Fprintf(w, "%s: %s\n", label, usageSummary(v.usage[acct], v.now))
	}
	if len(v.jobs) == 0 {
		fmt.Fprintln(w, "\nNo cron jobs. Add one with agentctl cron add.")
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSCHEDULE\tNEXT\tLAST\tENABLED\tAGENT\tACCOUNT\tTHROTTLE")
	for _, j := range v.jobs {
		acct := v.accounts[j.Name]
		if acct == "" {
			acct = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			j.Name, j.Schedule, nextColumn(j, v.now), lastColumn(v.last[j.Name], v.now),
			enabledColumn(j), j.Agent, acct, throttleColumn(j, th))
	}
	_ = tw.Flush()
}

func nextColumn(j cron.Job, now time.Time) string {
	if !j.IsEnabled() {
		return "paused"
	}
	sched, err := cron.ParseSchedule(j.Schedule)
	if err != nil {
		return "invalid schedule"
	}
	next := cron.NextFire(sched, now, cron.Jitter(j.Name))
	if next.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%s (%s)", relTime(next, now), next.Format("Mon 2 Jan 15:04"))
}

func lastColumn(e cron.Entry, now time.Time) string {
	if e.Result == "" {
		return "-"
	}
	return fmt.Sprintf("%s %s", e.Result, relTime(e.Time, now))
}

func enabledColumn(j cron.Job) string {
	if err := j.Validate(); err != nil {
		return "invalid: " + err.Error()
	}
	if j.IsEnabled() {
		return "yes"
	}
	return "paused"
}

func throttleColumn(j cron.Job, global config.CronUsageThresholds) string {
	parts := []string{"overlap:" + j.Overlap, "catchup:" + j.Catchup}
	if t := j.UsageThresholds(global); t != global {
		parts = append(parts, fmt.Sprintf("5h≥%.0f%% 7d≥%.0f%%", t.FiveHour, t.SevenDay))
	}
	if j.Timeout != "" {
		parts = append(parts, "timeout:"+j.Timeout)
	}
	if !j.UseWorktree() {
		parts = append(parts, "no-worktree")
	}
	return strings.Join(parts, " ")
}

// usageSummary describes the pushed rate limits the gate would read.
func usageSummary(pr *ratelimit.PushedRateLimits, now time.Time) string {
	if pr == nil {
		return "no usage data: runs go ahead"
	}
	updated := time.Unix(pr.UpdatedAt, 0)
	pct := func(w *ratelimit.PushedWindow) string {
		if w == nil {
			return "?"
		}
		return fmt.Sprintf("%.0f%%", w.UsedPercentage)
	}
	s := fmt.Sprintf("now 5h %s, 7d %s, updated %s", pct(pr.FiveHour), pct(pr.SevenDay), relTime(updated, now))
	if now.Sub(updated) > 6*time.Hour {
		s += "; stale, runs go ahead"
	}
	return s
}

// RunCronRuns prints the run history, newest last.
func RunCronRuns(w io.Writer, name string, limit int) error {
	return defaultCronEnv.runs(w, name, limit)
}

func (e cronEnv) runs(w io.Writer, name string, limit int) error {
	dir, err := e.stateDir()
	if err != nil {
		return err
	}
	entries, err := cron.ReadRuns(dir, name)
	if err != nil {
		return err
	}
	renderCronRuns(w, entries, limit, e.now())
	return nil
}

func renderCronRuns(w io.Writer, entries []cron.Entry, limit int, now time.Time) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "No runs recorded.")
		return
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tJOB\tSCHEDULED\tRESULT\tWINDOW\tNOTE")
	for _, e := range entries {
		sched := "-"
		if !e.Scheduled.IsZero() {
			sched = e.Scheduled.Format("Mon 2 Jan 15:04")
		}
		if e.Manual {
			sched = "manual"
		}
		win := e.WindowID
		if win == "" {
			win = "-"
		} else if e.Session != "" {
			win = e.Session + ":" + win
		}
		note := e.Note
		if e.Missed > 0 {
			note = strings.TrimPrefix(fmt.Sprintf("%s; %d older fires coalesced", note, e.Missed), "; ")
		}
		fmt.Fprintf(tw, "%s (%s)\t%s\t%s\t%s\t%s\t%s\n",
			e.Time.Format("Mon 2 Jan 15:04"), relTime(e.Time, now), e.Job, sched, e.Result, win, note)
	}
	_ = tw.Flush()
}

// ---------------------------------------------------------------------------
// health
// ---------------------------------------------------------------------------

// cronHealth is what `agentctl health` reports about cron.
type cronHealth struct {
	jobs, enabled int
	killSwitch    bool
	installed     bool // plist on disk (macOS)
	loaded        bool // launchctl knows the job
	darwin        bool
	lastTick      time.Time
	failing       []string
}

// cronFailingRuns is how many consecutive failed launches health flags.
const cronFailingRuns = 3

// gatherCronHealth reads the cron files and asks launchd whether the ticker
// is loaded. loaded is a seam (it runs launchctl).
func gatherCronHealth(loaded func() bool) (cronHealth, bool) {
	h := cronHealth{darwin: runtime.GOOS == "darwin"}
	path, err := cron.JobsPath()
	if err != nil {
		return h, false
	}
	jobs, err := cron.LoadJobs(path)
	if err != nil {
		return h, false
	}
	h.jobs = len(jobs.Jobs)
	for _, j := range jobs.Jobs {
		if j.IsEnabled() {
			h.enabled++
		}
	}
	h.killSwitch = !config.Load().Cron.EnabledOrDefault()
	h.installed = cronPlistExists()
	if h.darwin && h.installed {
		h.loaded = loaded()
	}
	if dir, err := cron.Dir(); err == nil {
		if st, err := cron.LoadState(dir); err == nil {
			h.lastTick = st.LastTick
		}
		if runs, err := cron.ReadRuns(dir, ""); err == nil {
			h.failing = cron.FailingJobs(runs, cronFailingRuns)
		}
	}
	return h, h.jobs > 0 || (h.darwin && h.installed)
}

// cronHealthLines renders the health section.
func cronHealthLines(h cronHealth, now time.Time) []string {
	var sched string
	switch {
	case !h.darwin:
		sched = "crontab (not checked)"
	case !h.installed:
		sched = "plist not installed (agentctl cron install)"
	case h.loaded:
		sched = "plist loaded"
	default:
		sched = "plist installed but NOT loaded (agentctl cron install)"
	}
	tick := "never ticked"
	if !h.lastTick.IsZero() {
		tick = "last tick " + relTime(h.lastTick, now)
		if now.Sub(h.lastTick) > 5*time.Minute && h.enabled > 0 {
			tick += " (STALE)"
		}
	}
	line := fmt.Sprintf("%d jobs (%d enabled), %s, %s", h.jobs, h.enabled, sched, tick)
	if h.killSwitch {
		line += ", DISABLED by cron.enabled=false"
	}
	lines := []string{line}
	for _, j := range h.failing {
		lines = append(lines, fmt.Sprintf("  %s: last %d runs failed (agentctl cron runs %s)", j, cronFailingRuns, j))
	}
	return lines
}

// printCronHealth prints the cron section of `agentctl health`, if cron is
// in use.
func printCronHealth(now time.Time) {
	h, show := gatherCronHealth(func() bool {
		a, err := launchd.NewAgent()
		return err == nil && a.Loaded(CronLaunchdLabel)
	})
	if !show {
		return
	}
	for i, l := range cronHealthLines(h, now) {
		if i == 0 {
			fmt.Printf("  Cron:    %s\n", l)
		} else {
			fmt.Printf("           %s\n", l)
		}
	}
}

// shortDuration drops Duration.String's zero tails: 15m, 6h, 1h30m.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
