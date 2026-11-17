package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/ratelimit"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/winname"
	"github.com/blsmth/agentctl/internal/worktree"
)

// CronAddOptions are the inputs of `agentctl cron add`.
type CronAddOptions struct {
	Name       string
	Schedule   string
	Agent      string
	Dir        string
	Prompt     string
	PromptKey  string
	PromptFile string
	NoWorktree bool
	Model      string
	Effort     string
	Backend    string
	Account    string
	Timeout    string
	Overlap    string
	Catchup    string
}

// cronEnv is every outside-world touch point of the cron commands, so tests
// never exec, spawn or talk to tmux or launchctl.
type cronEnv struct {
	now           func() time.Time
	jobsPath      func() (string, error)
	stateDir      func() (string, error)
	loadConfig    func() config.CronConfig
	validateAgent func(name, dir string) error
	deps          func(dir string, cfg config.CronConfig, jobs []cron.Job) cron.Deps
	// schedulerInstalled reports whether the launchd plist exists (true
	// when it cannot tell).
	schedulerInstalled func() bool
}

// defaultCronEnv is the real environment.
var defaultCronEnv = cronEnv{
	now:                time.Now,
	jobsPath:           cron.JobsPath,
	stateDir:           cron.Dir,
	loadConfig:         func() config.CronConfig { return config.Load().Cron },
	validateAgent:      func(name, dir string) error { return validateAgentName(name, dir) },
	deps:               realCronDeps,
	schedulerInstalled: cronPlistExists,
}

// RunCronAdd validates and saves a new job, then seeds its state so it starts
// from the next scheduled time.
func RunCronAdd(w io.Writer, opts CronAddOptions) error {
	return defaultCronEnv.add(w, opts)
}

func (e cronEnv) add(w io.Writer, opts CronAddOptions) error {
	job, err := e.buildJob(opts)
	if err != nil {
		return err
	}
	path, err := e.jobsPath()
	if err != nil {
		return err
	}
	jobs, err := cron.LoadJobs(path)
	if err != nil {
		return err
	}
	if jobs.Find(job.Name) >= 0 {
		return fmt.Errorf("a cron job named %q already exists (agentctl cron rm %s first)", job.Name, job.Name)
	}
	jobs.Jobs = append(jobs.Jobs, job)
	if err := cron.SaveJobs(path, jobs); err != nil {
		return err
	}
	dir, err := e.stateDir()
	if err != nil {
		return err
	}
	now := e.now()
	if err := cron.Seed(dir, job.Name, now); err != nil {
		return fmt.Errorf("seeding state for %s: %w", job.Name, err)
	}
	sched, _ := cron.ParseSchedule(job.Schedule)
	next := cron.NextFire(sched, now, cron.Jitter(job.Name))
	fmt.Fprintf(w, "Added %s (%s), next fire %s\n", job.Name, job.Schedule, next.Format("Mon 2 Jan 15:04"))
	if e.schedulerInstalled != nil && !e.schedulerInstalled() {
		fmt.Fprintln(w, "The scheduler is not installed yet: run agentctl cron install")
	}
	return nil
}

// buildJob turns add options into a validated job. --prompt-file is read
// now, so later edits to the file do not change what runs.
func (e cronEnv) buildJob(opts CronAddOptions) (cron.Job, error) {
	n := 0
	for _, s := range []string{opts.Prompt, opts.PromptKey, opts.PromptFile} {
		if s != "" {
			n++
		}
	}
	if n != 1 {
		return cron.Job{}, errors.New("exactly one of --prompt, --prompt-key or --prompt-file is required")
	}
	prompt := opts.Prompt
	if opts.PromptFile != "" {
		data, err := os.ReadFile(paths.ExpandHome(opts.PromptFile))
		if err != nil {
			return cron.Job{}, fmt.Errorf("reading --prompt-file: %w", err)
		}
		prompt = string(data)
		if strings.TrimSpace(prompt) == "" {
			return cron.Job{}, fmt.Errorf("--prompt-file %s is empty", opts.PromptFile)
		}
	}
	dir, err := filepath.Abs(paths.ExpandHome(opts.Dir))
	if err != nil {
		return cron.Job{}, fmt.Errorf("resolving --dir: %w", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return cron.Job{}, fmt.Errorf("--dir %s is not a directory", dir)
	}
	job := cron.Job{
		Name: opts.Name, Schedule: opts.Schedule, Dir: dir, Agent: opts.Agent,
		Prompt: prompt, PromptKey: opts.PromptKey,
		Model: opts.Model, Effort: opts.Effort, Backend: opts.Backend, Account: opts.Account,
		Timeout: opts.Timeout, Overlap: opts.Overlap, Catchup: opts.Catchup,
	}
	wt, enabled := !opts.NoWorktree, true
	job.Worktree, job.Enabled = &wt, &enabled
	job.Normalize()
	if err := job.Validate(); err != nil {
		return cron.Job{}, err
	}
	sched, _ := cron.ParseSchedule(job.Schedule)
	if err := cron.CheckMinInterval(sched, e.now(), e.loadConfig().EffectiveMinInterval()); err != nil {
		return cron.Job{}, err
	}
	if err := e.validateAgent(job.Agent, dir); err != nil {
		return cron.Job{}, err
	}
	return job, nil
}

// RunCronSetEnabled pauses or resumes a job. Resuming re-seeds last_fire, so
// fires missed while paused are not caught up.
func RunCronSetEnabled(w io.Writer, name string, enabled bool) error {
	return defaultCronEnv.setEnabled(w, name, enabled)
}

func (e cronEnv) setEnabled(w io.Writer, name string, enabled bool) error {
	path, jobs, i, err := e.findJob(name)
	if err != nil {
		return err
	}
	jobs.Jobs[i].Enabled = &enabled
	if err := cron.SaveJobs(path, jobs); err != nil {
		return err
	}
	verb := "Paused"
	if enabled {
		verb = "Resumed"
		dir, err := e.stateDir()
		if err != nil {
			return err
		}
		if err := cron.Seed(dir, name, e.now()); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "%s %s\n", verb, name)
	return nil
}

// RunCronRemove deletes a job. Its state is dropped by the next tick; its
// run history stays.
func RunCronRemove(w io.Writer, name string) error {
	return defaultCronEnv.remove(w, name)
}

func (e cronEnv) remove(w io.Writer, name string) error {
	path, jobs, i, err := e.findJob(name)
	if err != nil {
		return err
	}
	jobs.Jobs = append(jobs.Jobs[:i], jobs.Jobs[i+1:]...)
	if err := cron.SaveJobs(path, jobs); err != nil {
		return err
	}
	fmt.Fprintf(w, "Removed %s\n", name)
	return nil
}

func (e cronEnv) findJob(name string) (string, *cron.Jobs, int, error) {
	path, err := e.jobsPath()
	if err != nil {
		return "", nil, 0, err
	}
	jobs, err := cron.LoadJobs(path)
	if err != nil {
		return "", nil, 0, err
	}
	i := jobs.Find(name)
	if i < 0 {
		return "", nil, 0, fmt.Errorf("no cron job named %q", name)
	}
	return path, jobs, i, nil
}

// loadAll reads the jobs, config and state dir a tick or run needs.
func (e cronEnv) loadAll() (dir string, cfg config.CronConfig, jobs *cron.Jobs, err error) {
	path, err := e.jobsPath()
	if err != nil {
		return "", cfg, nil, err
	}
	if jobs, err = cron.LoadJobs(path); err != nil {
		return "", cfg, nil, err
	}
	if dir, err = e.stateDir(); err != nil {
		return "", cfg, nil, err
	}
	return dir, e.loadConfig(), jobs, nil
}

// RunCronTick is the body of the hidden `agentctl cron tick` that launchd
// runs every minute.
func RunCronTick() error {
	initCronProcess()
	return defaultCronEnv.tick()
}

// initCronProcess prepares a process that may launch agents: a UTF-8 locale
// for tmux, and the log at <StateDir>/cron/cron.log, opened before any job
// binds a project runtime dir (logging initializes once).
func initCronProcess() {
	tmux.EnsureUTF8Locale()
	if dir, err := cron.Dir(); err == nil && os.MkdirAll(dir, 0o700) == nil {
		logging.InitFile(filepath.Join(dir, "cron.log"))
	}
}

func (e cronEnv) tick() error {
	dir, cfg, jobs, err := e.loadAll()
	if err != nil {
		return err
	}
	var valid []cron.Job
	for _, j := range jobs.Jobs {
		if err := j.Validate(); err != nil {
			logging.Warn("cron: skipping invalid job %q: %v", j.Name, err)
			continue
		}
		valid = append(valid, j)
	}
	_, err = cron.Tick(e.deps(dir, cfg, valid))
	return err
}

// RunCronRun fires a job now.
func RunCronRun(w io.Writer, name string, force bool) error {
	initCronProcess()
	return defaultCronEnv.run(w, name, force)
}

func (e cronEnv) run(w io.Writer, name string, force bool) error {
	dir, cfg, jobs, err := e.loadAll()
	if err != nil {
		return err
	}
	i := jobs.Find(name)
	if i < 0 {
		return fmt.Errorf("no cron job named %q", name)
	}
	if err := jobs.Jobs[i].Validate(); err != nil {
		return fmt.Errorf("job %s is invalid: %w", name, err)
	}
	entry, err := cron.RunNow(e.deps(dir, cfg, jobs.Jobs), name, force)
	if err != nil {
		return err
	}
	switch {
	case entry.Result == cron.ResultFired:
		fmt.Fprintf(w, "Fired %s → window %s (%s)\n", name, entry.WindowID, entry.Session)
	case cron.IsFailed(entry.Result):
		return fmt.Errorf("%s: %s", entry.Result, entry.Note)
	default:
		fmt.Fprintf(w, "Not fired: %s (%s). Use --force to override.\n", entry.Result, entry.Note)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Real dependencies
// ---------------------------------------------------------------------------

// cronProject is where a job's windows live: the main repo root of its dir,
// that project's runtime dir and its tmux session.
type cronProject struct {
	root, runtimeDir, session string
}

// projectFor resolves a job's project. worktree.MainRepoRoot shells out to
// git, so this lives only in the real dependencies.
func projectFor(job cron.Job) cronProject {
	root := worktree.MainRepoRoot(job.Dir)
	return cronProject{
		root:       root,
		runtimeDir: filepath.Join(root, ".agentctl"),
		session:    tmux.SessionNameForProjectRoot(root),
	}
}

func realCronDeps(dir string, cfg config.CronConfig, jobs []cron.Job) cron.Deps {
	return cron.Deps{
		Dir:       dir,
		Config:    cfg,
		Jobs:      jobs,
		Now:       time.Now,
		Windows:   cronWindows,
		Account:   cronAccount,
		Usage:     cronUsage,
		Launch:    cronLaunch,
		Interrupt: cronInterrupt,
		Notify: func(title, msg string) {
			notify.NotifyWithOptions(notify.Options{Title: title, Message: msg, Env: map[string]string{"AGENTCTL_NOTIFICATION_TYPE": "cron"}})
		},
		Logf: logging.Log,
	}
}

// cronWindows lists every live window a cron job launched, across the jobs'
// projects: windowstate records with scheduled_by, joined with the
// project session's live tmux windows. A missing session means none.
func cronWindows(jobs []cron.Job) ([]cron.Window, error) {
	seen := map[string]bool{}
	var out []cron.Window
	for _, j := range jobs {
		p := projectFor(j)
		if seen[p.runtimeDir] {
			continue
		}
		seen[p.runtimeDir] = true
		records := windowstate.AllFrom(windowstate.WindowsDirFor(p.runtimeDir))
		if !hasScheduled(records) {
			continue
		}
		live, err := tmux.ListWindowsInErr(p.session)
		if err != nil {
			continue // session not running: no live windows
		}
		alive := map[string]bool{}
		for _, w := range live {
			alive[w.ID] = true
		}
		for id, ws := range records {
			if ws.ScheduledBy == "" || !alive[id] {
				continue
			}
			out = append(out, cron.Window{
				ID: id, Job: ws.ScheduledBy, RuntimeDir: p.runtimeDir, Session: p.session,
				MainPane: ws.MainPane, SessionID: ws.SessionID, Done: ws.Done, ScheduledAt: ws.ScheduledAt,
			})
		}
	}
	return out, nil
}

func hasScheduled(records map[string]windowstate.WindowState) bool {
	for _, ws := range records {
		if ws.ScheduledBy != "" {
			return true
		}
	}
	return false
}

// bindProject points the process-global session state (paths runtime dir,
// windowstate, config project layer, tmux.SessionName) at p for the duration
// of fn, the way agentctl serve's withSession does. A tick is single
// threaded, so no lock is needed.
func bindProject(p cronProject, fn func() error) error {
	if err := paths.CheckRuntimeDirAt(p.runtimeDir, true); err != nil {
		return err
	}
	prevSession := tmux.SessionName
	restore := paths.Bind(p.runtimeDir)
	tmux.SessionName = p.session
	windowstate.Init()
	cfgErr := config.Init()
	defer func() {
		restore()
		tmux.SessionName = prevSession
		windowstate.Init()
		_ = config.Init()
	}()
	if cfgErr != nil {
		return cfgErr
	}
	paths.EnsureRuntimeDir()
	return fn()
}

// cronLookTmux is the tmux availability check (a seam for tests).
var cronLookTmux = func() error {
	_, err := exec.LookPath("tmux")
	return err
}

// cronLaunch fires one job through the dispatch path: it binds the job's
// project, starts its tmux session detached when it is not running, and
// dispatches the prompt Detached (no parent window, no orchestrator, no
// focus switch, nothing that waits on a human).
func cronLaunch(req cron.LaunchRequest) (cron.LaunchResult, error) {
	if err := cronLookTmux(); err != nil {
		return cron.LaunchResult{}, fmt.Errorf("%w: %v", cron.ErrTmuxUnavailable, err)
	}
	job := req.Job
	p := projectFor(job)
	var res cron.LaunchResult
	err := bindProject(p, func() error {
		if !tmux.SessionExists() {
			logging.Log("cron: %s: starting session %s detached", job.Name, p.session)
			if err := tmux.StartSessionDetached(config.Load().DefaultAgent); err != nil {
				return fmt.Errorf("%w: starting session %s: %v", cron.ErrTmuxUnavailable, p.session, err)
			}
		}
		opts, err := cronDispatchOptions(req)
		if err != nil {
			return err
		}
		if err := validateAgentName(job.Agent, job.Dir); err != nil {
			return err
		}
		wid, _, err := dispatchPromptOptions(opts)
		if err != nil {
			return err
		}
		res = cron.LaunchResult{WindowID: wid, Session: p.session}
		return nil
	})
	return res, err
}

// cronDispatchOptions maps a fire onto the dispatch options, resolving a
// prompt_key from the context store.
func cronDispatchOptions(req cron.LaunchRequest) (DispatchOptions, error) {
	job := req.Job
	prompt := job.Prompt
	if job.PromptKey != "" {
		resolved, err := resolvePromptKey(job.PromptKey)
		if err != nil {
			return DispatchOptions{}, err
		}
		prompt = resolved
	}
	return DispatchOptions{
		Prompt:      prompt,
		PromptKey:   job.PromptKey,
		Agent:       job.Agent,
		Dir:         job.Dir,
		Model:       job.Model,
		Effort:      job.Effort,
		Backend:     job.Backend,
		Account:     job.Account,
		NoWorktree:  !job.UseWorktree(),
		Branch:      req.Branch,
		ScheduledBy: job.Name,
		Detached:    true,
	}, nil
}

// cronInterrupt handles a timed-out window: C-c to its main pane, the window
// marked blocked, a mailbox notice for the agent and a desktop notification.
// The window is never killed, so a human can look.
func cronInterrupt(w cron.Window, job cron.Job, timeout time.Duration) error {
	p := cronProject{root: filepath.Dir(w.RuntimeDir), runtimeDir: w.RuntimeDir, session: w.Session}
	return bindProject(p, func() error {
		var err error
		if tmux.IsPaneID(w.MainPane) {
			err = tmux.SendKeyToPane(w.MainPane, "C-c")
		} else {
			err = tmux.SendKeyToWindow(w.Session, w.ID, "C-c")
		}
		if err != nil {
			return fmt.Errorf("sending C-c: %w", err)
		}
		detail := fmt.Sprintf("cron job %s exceeded its %s timeout", job.Name, timeout)
		now := time.Now()
		if uerr := winname.Update(w.ID, func(ws *windowstate.WindowState) {
			ws.State = "blocked"
			ws.PendingTool = "cron-timeout"
			ws.PendingToolDetail = detail
			ws.PendingToolAt = now
			ws.BlockedSince = now
		}); uerr != nil {
			logging.Warn("cron: marking %s blocked: %v", w.ID, uerr)
		}
		if _, merr := mailbox.Enqueue(mailbox.Message{
			To: w.ID, ToSession: w.SessionID, Kind: mailbox.KindBlocked,
			Subject: "cron timeout",
			Body:    fmt.Sprintf("agentctl: this scheduled run (%s) passed its %s timeout and was interrupted. Wrap up, report where you got to, and stop.", job.Name, timeout),
		}); merr != nil {
			logging.Warn("cron: mailbox notice for %s: %v", w.ID, merr)
		}
		notify.NotifyWithOptions(notify.Options{
			Title: "agentctl cron: " + job.Name, Message: detail + "; interrupted in " + w.Session,
			Env: map[string]string{"AGENTCTL_NOTIFICATION_TYPE": "cron"},
		})
		return nil
	})
}

// cronAccount resolves the account a job runs under exactly as dispatch
// does (claudehome.ForLaunch: the job's account, agents.<name>.account,
// account_rules on the repo root, default_account). A cron fire has no
// parent window. "" means no accounts are configured.
func cronAccount(job cron.Job) (string, error) {
	res, err := claudehome.ForLaunch(config.Load(), job.Account, job.Agent, job.Dir, "")
	if err != nil {
		return "", err
	}
	return res.Name, nil
}

// cronUsage reads account's pushed rate limits (the pre-accounts single
// file for "" and, as a fallback, for the ~/.claude account).
func cronUsage(account string) (*ratelimit.PushedRateLimits, error) {
	return ratelimit.LoadPushedFor(account, account == claudehome.HomeAccount())
}
