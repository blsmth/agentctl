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
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/launchd"
	"github.com/blsmth/agentctl/internal/ratelimit"
	"github.com/blsmth/agentctl/internal/textutil"
)

// relTime renders t relative to now: "3 minutes ago", "2 hours from now".
func relTime(t, now time.Time) string {
	return humanize.RelTime(t, now, "ago", "from now")
}

// cronListView is everything `cron list` renders, gathered up front so the
// rendering is a pure function. The per-job facts (next fire, last result,
// active count) come from the shared cronedit.BuildOverview.
type cronListView struct {
	now time.Time
	cfg config.CronConfig
	ov  cronedit.Overview
	// accounts maps each job to its resolved account ("" when no accounts
	// are configured); usage holds each account's pushed rate limits.
	accounts map[string]string
	usage    map[string]*ratelimit.PushedRateLimits
	// deleted are jobs gone from schedules.json (--all): each one's newest
	// lifecycle entry, oldest name first; last holds their last runs.
	deleted []cron.Entry
	last    map[string]cron.Entry
}

// RunCronList prints the jobs with their next fire, last result and
// throttles; with all, also the deleted jobs runs.ndjson remembers.
func RunCronList(w io.Writer, all bool) error { return defaultCronEnv.list(w, all) }

func (e cronEnv) list(w io.Writer, all bool) error {
	dir, cfg, jobs, err := e.loadAll()
	if err != nil {
		return err
	}
	if _, err := cron.LoadState(dir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	runs, _ := cron.ReadRuns(dir, "")
	in := cronedit.OverviewInput{Now: e.now(), Config: cfg, Jobs: jobs.Jobs, Runs: runs, SchedulerInstalled: true, Check: e.store().Check}
	if e.schedulerInstalled != nil {
		in.SchedulerInstalled = e.schedulerInstalled()
	}
	d := e.deps(dir, cfg, jobs.Jobs)
	if d.Windows != nil {
		in.Windows, _ = d.Windows(jobs.Jobs)
	}
	v := cronListView{now: in.Now, cfg: cfg, ov: cronedit.BuildOverview(in)}
	if all {
		v.deleted, v.last = deletedJobs(runs, jobs), cron.LastByJob(runs)
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
	case !v.ov.Enabled:
		fmt.Fprintln(w, "Scheduler: OFF (cron.enabled is false; tick does nothing)")
	case !v.ov.SchedulerInstalled:
		fmt.Fprintln(w, "Scheduler: not installed (run agentctl cron install)")
	}
	th := v.cfg.EffectiveUsageThresholds()
	fmt.Fprintf(w, "Throttles: min_interval %s · max_concurrent %d (%d active) · catchup_window %s · usage gate 5h ≥ %.0f%%, 7d ≥ %.0f%%\n",
		v.ov.Throttles.MinInterval, v.ov.Throttles.MaxConcurrent, v.ov.Active, v.ov.Throttles.CatchupWindow,
		th.FiveHour, th.SevenDay)
	for _, acct := range sortedKeys(v.usage) {
		label := "Usage"
		if acct != "" {
			label = "Usage (" + acct + ")"
		}
		fmt.Fprintf(w, "%s: %s\n", label, usageSummary(v.usage[acct], v.now))
	}
	if len(v.ov.Jobs) == 0 && len(v.deleted) == 0 {
		fmt.Fprintln(w, "\nNo cron jobs. Add one with agentctl cron add.")
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSCHEDULE\tNEXT\tLAST\tENABLED\tAGENT\tACCOUNT\tPROMPT\tTHROTTLE")
	for _, st := range v.ov.Jobs {
		j := st.Job
		acct := v.accounts[j.Name]
		if acct == "" {
			acct = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			j.Name, cron.ScheduleText(j), nextColumn(st, v.now), lastColumn(st.Last, v.now),
			enabledColumn(st), agentColumn(j), acct, promptColumn(j, v.now), throttleColumn(j, th))
	}
	for _, e := range v.deleted {
		j := e.Def.Job()
		var last *cron.Entry
		if l, ok := v.last[e.Job]; ok {
			last = &l
		}
		acct := j.Account
		if acct == "" {
			acct = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			j.Name, cron.ScheduleText(j), "-", lastColumn(last, v.now),
			deletedColumn(e, v.now), agentColumn(j), acct, promptColumn(j, v.now), "-")
	}
	_ = tw.Flush()
	if lines := unpinnedLines(v.ov.Jobs); len(lines) > 0 {
		fmt.Fprintln(w)
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
	}
}

// unpinnedLines warns about legacy prompt_key jobs, one line each naming
// the gated command that pins it.
func unpinnedLines(jobs []cronedit.Status) []string {
	var out []string
	for _, st := range jobs {
		if st.Job.Unpinned() {
			out = append(out, unpinnedWarning(st.Job))
		}
	}
	return out
}

// unpinnedWarning is the `cron list` and `health` line for an unpinned job.
func unpinnedWarning(j cron.Job) string {
	return fmt.Sprintf("UNPINNED %s: its fires read context key %s, which can change without approval. Pin it with %s (approve the text in the popup).",
		j.Name, j.PromptKey, j.PinCommand())
}

// deletedJobs returns the newest lifecycle entry of each job runs.ndjson
// knows that schedules.json no longer has, by name.
func deletedJobs(runs []cron.Entry, jobs *cron.Jobs) []cron.Entry {
	snaps := cron.LatestSnapshots(runs)
	var out []cron.Entry
	for _, name := range sortedKeys(snaps) {
		if jobs.Find(name) < 0 {
			out = append(out, snaps[name])
		}
	}
	return out
}

// deletedColumn is the ENABLED column of a gone job: "deleted" (or
// "pruned") with when, and by whom when it was not the tick.
func deletedColumn(e cron.Entry, now time.Time) string {
	what := "deleted"
	switch e.Result {
	case cron.ResultPruned:
		what = "pruned"
	case cron.ResultDeleted:
	default:
		// The newest entry is not a removal (runs.ndjson was trimmed, or the
		// file was edited by hand): the job is still gone.
		return "deleted (not recorded)"
	}
	return what + " " + relTime(e.Time, now)
}

func nextColumn(st cronedit.Status, now time.Time) string {
	switch {
	case st.Job.IsDone():
		return "-"
	case !st.Enabled:
		return "paused"
	case st.ScheduleError != "":
		return "invalid schedule"
	case st.NextFire == nil:
		return "never"
	}
	return fmt.Sprintf("%s (%s)", relTime(*st.NextFire, now), st.NextFire.Format("Mon 2 Jan 15:04"))
}

// agentColumn is the job's agent, or "(template)" when its template names
// it.
func agentColumn(j cron.Job) string {
	if j.IsImprove() {
		return j.ImproveAgentName()
	}
	if j.Agent == "" && j.Template != "" {
		return "(template)"
	}
	return j.Agent
}

// promptPreviewWidth is how much of an inline prompt `cron list` shows.
const promptPreviewWidth = 32

// promptColumn says what a fire runs: the template by name (read at every
// fire), a legacy context key by name and UNPINNED (read at every fire), a
// prompt pinned from a key by its key, else the start of the inline prompt.
func promptColumn(j cron.Job, now time.Time) string {
	if j.IsImprove() {
		s := "fleet improve, last " + cron.ShortDuration(j.ImproveSince(now))
		if j.Template != "" {
			s += " + template " + j.Template
		}
		return s
	}
	switch {
	case j.Template != "":
		return "template " + j.Template
	case j.Unpinned():
		return "key " + j.PromptKey + " (UNPINNED)"
	case j.PromptSourceKey != "":
		return "key " + j.PromptSourceKey + " (pinned)"
	}
	return textutil.Truncate(strings.Join(strings.Fields(j.Prompt), " "), promptPreviewWidth, "…")
}

func lastColumn(e *cron.Entry, now time.Time) string {
	if e == nil || e.Result == "" {
		return "-"
	}
	return fmt.Sprintf("%s %s", e.Result, relTime(e.Time, now))
}

func enabledColumn(st cronedit.Status) string {
	if st.Invalid != "" {
		return "invalid: " + st.Invalid
	}
	if st.Enabled {
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
	if !j.UseWorktree() && !j.IsImprove() {
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
		if e.IsLifecycle() {
			note = lifecycleNote(e, now)
		}
		if e.Missed > 0 {
			note = strings.TrimPrefix(fmt.Sprintf("%s; %d older fires coalesced", note, e.Missed), "; ")
		}
		fmt.Fprintf(tw, "%s (%s)\t%s\t%s\t%s\t%s\t%s\n",
			e.Time.Format("Mon 2 Jan 15:04"), relTime(e.Time, now), e.Job, sched, e.Result, win, note)
	}
	_ = tw.Flush()
}

// lifecycleNoteWidth bounds the definition part of a lifecycle note.
const lifecycleNoteWidth = 120

// lifecycleNote is the NOTE of a lifecycle entry: who, the entry's own note,
// and a short definition ("by window @12 (session 1a2b3c4d); changed at;
// once, Tue 1 Dec 09:00 · agent brandon · ~/p · prompt: check the …").
func lifecycleNote(e cron.Entry, now time.Time) string {
	parts := []string{"by " + cronActorText(e.By)}
	if e.Note != "" {
		parts = append(parts, e.Note)
	}
	if e.Def != nil {
		parts = append(parts, textutil.Truncate(snapshotText(*e.Def, now), lifecycleNoteWidth, "…"))
	}
	return strings.Join(parts, "; ")
}

func cronActorText(a *cron.Actor) string {
	if a == nil {
		return cron.ActorCLI
	}
	return a.String()
}

// snapshotText is a one-line job definition.
func snapshotText(s cron.JobSnapshot, now time.Time) string {
	j := s.Job()
	parts := []string{cron.ScheduleText(j), "agent " + agentColumn(j), "prompt: " + promptColumn(j, now)}
	if s.Account != "" {
		parts = append(parts, "account "+s.Account)
	}
	if s.Model != "" {
		parts = append(parts, "model "+s.Model)
	}
	if !s.Enabled {
		parts = append(parts, "paused")
	}
	if s.Dir != "" {
		parts = append(parts, tildify(s.Dir))
	}
	return strings.Join(parts, " · ")
}

// tildify shortens a home-relative path to ~/...
func tildify(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
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
	// unpinned are the legacy prompt_key jobs (fires read the key).
	unpinned []cron.Job
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
		if j.IsEnabled() && !j.IsDone() { // a done one-shot never fires again
			h.enabled++
		}
		if j.Unpinned() {
			h.unpinned = append(h.unpinned, j)
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
	for _, j := range h.unpinned {
		lines = append(lines, "  "+unpinnedWarning(j))
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

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
