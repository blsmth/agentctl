package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/launchd"
	"github.com/blsmth/agentctl/internal/prompttemplate"
	"github.com/blsmth/agentctl/internal/ratelimit"
)

// testCronEnv is a cronEnv over temp dirs whose deps never spawn anything.
func testCronEnv(t *testing.T, launches *[]cron.LaunchRequest) cronEnv {
	t.Helper()
	root := t.TempDir()
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.Local)
	return cronEnv{
		now:        func() time.Time { return now },
		jobsPath:   func() (string, error) { return filepath.Join(root, "config", cron.SchedulesFile), nil },
		stateDir:   func() (string, error) { return filepath.Join(root, "state", "cron"), nil },
		loadConfig: func() config.CronConfig { return config.CronConfig{} },
		validateAgent: func(name, _ string) error {
			return map[bool]error{true: nil, false: errors.New("unknown agent")}[name == "brandon"]
		},
		schedulerInstalled: func() bool { return true },
		deps: func(dir string, cfg config.CronConfig, jobs []cron.Job) cron.Deps {
			return cron.Deps{
				Dir: dir, Config: cfg, Jobs: jobs, Now: func() time.Time { return now },
				Windows: func([]cron.Job) ([]cron.Window, error) { return nil, nil },
				Usage:   func(string) (*ratelimit.PushedRateLimits, error) { return nil, nil },
				Launch: func(r cron.LaunchRequest) (cron.LaunchResult, error) {
					*launches = append(*launches, r)
					return cron.LaunchResult{WindowID: "@7", Session: "agentctl-p"}, nil
				},
			}
		},
	}
}

func TestCronAdd(t *testing.T) {
	projDir := t.TempDir()
	promptFile := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(promptFile, []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := CronAddOptions{Name: "triage", Schedule: "*/15 * * * *", Agent: "brandon", Dir: projDir, Prompt: "go"}
	tests := []struct {
		name    string
		mut     func(o *CronAddOptions)
		wantErr string
		check   func(t *testing.T, j cron.Job)
	}{
		{name: "every 15 minutes accepted", mut: func(*CronAddOptions) {}, check: func(t *testing.T, j cron.Job) {
			if j.Prompt != "go" || !j.UseWorktree() || !j.IsEnabled() || j.Harness != "claude" || j.Mode != "window" {
				t.Errorf("job = %+v", j)
			}
		}},
		{name: "every minute rejected by min_interval", mut: func(o *CronAddOptions) { o.Schedule = "* * * * *" }, wantErr: "min_interval"},
		{name: "prompt file copied", mut: func(o *CronAddOptions) { o.Prompt, o.PromptFile = "", promptFile }, check: func(t *testing.T, j cron.Job) {
			if j.Prompt != "from file" {
				t.Errorf("prompt = %q", j.Prompt)
			}
		}},
		{name: "two prompt sources", mut: func(o *CronAddOptions) { o.PromptKey = "k" }, wantErr: "exactly one"},
		{name: "no worktree", mut: func(o *CronAddOptions) { o.NoWorktree = true }, check: func(t *testing.T, j cron.Job) {
			if j.UseWorktree() {
				t.Error("worktree still on")
			}
		}},
		{name: "unknown agent", mut: func(o *CronAddOptions) { o.Agent = "nobody" }, wantErr: "unknown agent"},
		{name: "missing dir", mut: func(o *CronAddOptions) { o.Dir = filepath.Join(projDir, "nope") }, wantErr: "not a directory"},
		{name: "bad timeout", mut: func(o *CronAddOptions) { o.Timeout = "forever" }, wantErr: "timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var launches []cron.LaunchRequest
			e := testCronEnv(t, &launches)
			o := base
			tt.mut(&o)
			var out bytes.Buffer
			err := e.add(&out, o)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			path, _ := e.jobsPath()
			fi, err := os.Stat(path)
			if err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("jobs file: %v %v", fi, err)
			}
			jobs, err := cron.LoadJobs(path)
			if err != nil || len(jobs.Jobs) != 1 {
				t.Fatalf("jobs = %+v, %v", jobs, err)
			}
			tt.check(t, jobs.Jobs[0])
			dir, _ := e.stateDir()
			st, _ := cron.LoadState(dir)
			if !st.Jobs["triage"].LastFire.Equal(e.now()) {
				t.Errorf("add did not seed last_fire: %+v", st.Jobs)
			}
			if err := e.add(&out, o); err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Errorf("duplicate add err = %v", err)
			}
		})
	}
}

func TestCronPauseResumeRemoveRun(t *testing.T) {
	var launches []cron.LaunchRequest
	e := testCronEnv(t, &launches)
	var out bytes.Buffer
	if err := e.add(&out, CronAddOptions{Name: "j", Schedule: "@hourly", Agent: "brandon", Dir: t.TempDir(), Prompt: "p", NoWorktree: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.setEnabled(&out, "j", false); err != nil {
		t.Fatal(err)
	}
	path, _ := e.jobsPath()
	jobs, _ := cron.LoadJobs(path)
	if jobs.Jobs[0].IsEnabled() {
		t.Error("pause did not disable")
	}
	if err := e.setEnabled(&out, "j", true); err != nil {
		t.Fatal(err)
	}
	if err := e.run(&out, "j", false, false); err != nil {
		t.Fatal(err)
	}
	if len(launches) != 1 || launches[0].Job.Name != "j" || launches[0].Branch != "" {
		t.Errorf("launches = %+v", launches)
	}
	if !strings.Contains(out.String(), "Fired j → window @7") {
		t.Errorf("output = %q", out.String())
	}
	if err := e.remove(&out, "j"); err != nil {
		t.Fatal(err)
	}
	if err := e.remove(&out, "j"); err == nil {
		t.Error("removing a missing job: no error")
	}
	if err := e.run(&out, "j", false, false); err == nil {
		t.Error("running a missing job: no error")
	}
}

func TestCronTickSkipsInvalidJobs(t *testing.T) {
	var launches []cron.LaunchRequest
	e := testCronEnv(t, &launches)
	path, _ := e.jobsPath()
	good := cron.Job{Name: "good", Schedule: "0 * * * *", Dir: "/tmp", Agent: "brandon", Prompt: "p"}
	bad := cron.Job{Name: "bad", Schedule: "0 * * * *", Dir: "/tmp", Agent: "brandon"} // no prompt
	if err := cron.SaveJobs(path, &cron.Jobs{Jobs: []cron.Job{good, bad}}); err != nil {
		t.Fatal(err)
	}
	dir, _ := e.stateDir()
	for _, name := range []string{"good", "bad"} {
		if err := cron.Seed(dir, name, e.now().Add(-2*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.tick(); err != nil {
		t.Fatal(err)
	}
	if len(launches) != 1 || launches[0].Job.Name != "good" {
		t.Errorf("launches = %+v", launches)
	}
}

func TestCronDispatchOptions(t *testing.T) {
	no := false
	job := cron.Job{Name: "j", Dir: "/p", Agent: "brandon", Prompt: "do", Model: "opus", Effort: "high",
		Backend: "none", Account: "personal", Worktree: &no}
	opts, err := cronDispatchOptions(cron.LaunchRequest{Job: job, Branch: ""})
	if err != nil {
		t.Fatal(err)
	}
	want := DispatchOptions{Prompt: "do", Agent: "brandon", Dir: "/p", Model: "opus", Effort: "high", Backend: "none",
		Account: "personal", NoWorktree: true, ScheduledBy: "j", Detached: true}
	if opts != want {
		t.Errorf("opts = %+v\nwant %+v", opts, want)
	}
}

func TestCronInstall(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "cron")
	var calls []string
	env := cronInstallEnv{
		exe: func() (string, error) { return "/Users/me/.local/bin/agentctl", nil },
		lookPath: func(name string) (string, error) {
			switch name {
			case "claude":
				return "/Users/me/.claude/local/claude", nil
			case "tmux":
				return "/opt/homebrew/bin/tmux", nil
			}
			return "", errors.New("not found")
		},
		stateDir: func() (string, error) { return stateDir, nil },
	}
	home := t.TempDir()
	env.agent = func() (launchd.Agent, error) {
		return launchd.Agent{Home: home, UID: 501, Sleep: func(time.Duration) {}, Run: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil, nil
		}}, nil
	}

	t.Run("darwin writes and bootstraps the plist", func(t *testing.T) {
		calls = nil
		env.goos = "darwin"
		var out bytes.Buffer
		if err := env.install(&out); err != nil {
			t.Fatal(err)
		}
		plist, err := os.ReadFile(launchd.PlistPath(home, CronLaunchdLabel))
		if err != nil {
			t.Fatal(err)
		}
		s := string(plist)
		for _, want := range []string{
			"<string>/Users/me/.local/bin/agentctl</string>", "<string>cron</string>", "<string>tick</string>",
			"<key>StartInterval</key>\n\t<integer>60</integer>", "<key>RunAtLoad</key>",
			"<string>/Users/me/.local/bin:/Users/me/.claude/local:/opt/homebrew/bin:/usr/local/bin:",
			"<key>LANG</key>\n\t\t<string>en_US.UTF-8</string>", "<key>LC_ALL</key>\n\t\t<string>en_US.UTF-8</string>",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("plist missing %q:\n%s", want, s)
			}
		}
		if len(calls) != 2 || !strings.HasPrefix(calls[1], "launchctl bootstrap gui/501 ") {
			t.Errorf("launchctl calls = %q", calls)
		}
		calls = nil
		if err := env.uninstall(&out); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || calls[0] != "launchctl bootout gui/501/"+CronLaunchdLabel {
			t.Errorf("uninstall calls = %q", calls)
		}
	})

	t.Run("linux prints a crontab line", func(t *testing.T) {
		calls = nil
		env.goos = "linux"
		var out bytes.Buffer
		if err := env.install(&out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "* * * * * cd ") || !strings.Contains(out.String(), "agentctl' cron tick >> ") ||
			!strings.Contains(out.String(), "LC_ALL=en_US.UTF-8") {
			t.Errorf("crontab output = %q", out.String())
		}
		if len(calls) != 0 {
			t.Errorf("launchctl called on linux: %q", calls)
		}
	})
}

// stubCronTemplates makes loadPromptTemplate resolve only tmpls.
func stubCronTemplates(t *testing.T, tmpls ...prompttemplate.Template) {
	t.Helper()
	prev := loadPromptTemplate
	t.Cleanup(func() { loadPromptTemplate = prev })
	loadPromptTemplate = func(name, _ string) (prompttemplate.Template, error) {
		for _, tm := range tmpls {
			if tm.Name == name {
				return tm, nil
			}
		}
		return prompttemplate.Template{}, fmt.Errorf("%w: %q", prompttemplate.ErrNotFound, name)
	}
}

func TestCronTemplateAddEdit(t *testing.T) {
	ptr := func(s string) *string { return &s }
	stubCronTemplates(t, prompttemplate.Template{Name: "triage", Agent: "brandon", Prompt: "triage it"})
	var launches []cron.LaunchRequest
	e := testCronEnv(t, &launches)
	projDir := t.TempDir()
	var out bytes.Buffer
	base := CronAddOptions{Name: "j", Schedule: "@hourly", Dir: projDir, Template: "triage"}

	for name, mut := range map[string]func(o *CronAddOptions){
		"template and prompt":   func(o *CronAddOptions) { o.Prompt = "x" },
		"template and key":      func(o *CronAddOptions) { o.PromptKey = "k" },
		"missing template":      func(o *CronAddOptions) { o.Template = "gone" },
		"no agent, no template": func(o *CronAddOptions) { o.Template, o.Prompt = "", "p" },
	} {
		o := base
		mut(&o)
		if err := e.add(&out, o); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if err := e.add(&out, base); err != nil {
		t.Fatal(err)
	}
	get := func() cron.Job {
		t.Helper()
		path, _ := e.jobsPath()
		jobs, err := cron.LoadJobs(path)
		if err != nil || len(jobs.Jobs) != 1 {
			t.Fatalf("jobs = %+v, %v", jobs, err)
		}
		return jobs.Jobs[0]
	}
	if j := get(); j.Template != "triage" || j.Agent != "" || j.Prompt != "" {
		t.Fatalf("added job = %+v", j)
	}

	// Template → prompt → template.
	if err := e.edit(&out, CronEditOptions{Name: "j", Prompt: ptr("inline"), Agent: ptr("brandon")}); err != nil {
		t.Fatal(err)
	}
	if j := get(); j.Template != "" || j.Prompt != "inline" {
		t.Errorf("to prompt: %+v", j)
	}
	if err := e.edit(&out, CronEditOptions{Name: "j", Template: ptr("triage")}); err != nil {
		t.Fatal(err)
	}
	if j := get(); j.Template != "triage" || j.Prompt != "" || j.PromptKey != "" {
		t.Errorf("back to template: %+v", j)
	}
	for name, opts := range map[string]CronEditOptions{
		"missing template":    {Name: "j", Template: ptr("gone")},
		"empty template":      {Name: "j", Template: ptr("")},
		"template and prompt": {Name: "j", Template: ptr("triage"), Prompt: ptr("x")},
	} {
		if err := e.edit(&out, opts); err == nil {
			t.Errorf("edit %s: no error", name)
		}
	}
}

func TestCronDispatchOptionsTemplate(t *testing.T) {
	stubCronTemplates(t, prompttemplate.Template{Name: "triage", Agent: "go-engineer", Model: "sonnet", Effort: "low",
		Dir: "/elsewhere", Prompt: "triage it"})
	job := cron.Job{Name: "j", Dir: "/p", Template: "triage"}
	opts, err := cronDispatchOptions(cron.LaunchRequest{Job: job})
	if err != nil {
		t.Fatal(err)
	}
	want := DispatchOptions{Prompt: "triage it", Template: "triage", Agent: "go-engineer", Dir: "/p", Model: "sonnet",
		Effort: "low", ScheduledBy: "j", Detached: true}
	if opts != want {
		t.Errorf("template defaults: opts = %+v\nwant %+v", opts, want)
	}
	job.Agent, job.Model, job.Effort = "brandon", "opus", "high"
	opts, err = cronDispatchOptions(cron.LaunchRequest{Job: job})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Agent != "brandon" || opts.Model != "opus" || opts.Effort != "high" || opts.Dir != "/p" {
		t.Errorf("job fields win: opts = %+v", opts)
	}
	job.Template = "gone"
	_, err = cronDispatchOptions(cron.LaunchRequest{Job: job})
	if !errors.Is(err, cron.ErrTemplateNotFound) || !strings.Contains(err.Error(), `template "gone" not found`) {
		t.Errorf("missing template err = %v", err)
	}
}

// TestCronRunMissingTemplate: a fire (here cron run --force) of a job whose
// template was deleted is recorded as failed:template with the reason, not
// skipped.
func TestCronRunMissingTemplate(t *testing.T) {
	stubCronTemplates(t, prompttemplate.Template{Name: "triage", Agent: "brandon", Prompt: "triage it"})
	var launches []cron.LaunchRequest
	e := testCronEnv(t, &launches)
	deps := e.deps
	e.deps = func(dir string, cfg config.CronConfig, jobs []cron.Job) cron.Deps {
		d := deps(dir, cfg, jobs)
		launch := d.Launch
		d.Launch = func(r cron.LaunchRequest) (cron.LaunchResult, error) {
			if _, err := cronDispatchOptions(r); err != nil {
				return cron.LaunchResult{}, err
			}
			return launch(r)
		}
		return d
	}
	var out bytes.Buffer
	if err := e.add(&out, CronAddOptions{Name: "j", Schedule: "@hourly", Dir: t.TempDir(), Template: "triage"}); err != nil {
		t.Fatal(err)
	}
	if err := e.run(&out, "j", true, false); err != nil || len(launches) != 1 {
		t.Fatalf("run with template: %v, launches %d", err, len(launches))
	}
	stubCronTemplates(t) // deleted
	err := e.run(&out, "j", true, false)
	if err == nil || !strings.Contains(err.Error(), `template "triage" not found`) || !strings.HasPrefix(err.Error(), "failed:template") {
		t.Errorf("run err = %v", err)
	}
	dir, _ := e.stateDir()
	runs, _ := cron.ReadRuns(dir, "j")
	last := runs[len(runs)-1]
	if last.Result != cron.Failed("template") || !strings.Contains(last.Note, `template "triage" not found`) {
		t.Errorf("last run = %+v", last)
	}
	out.Reset()
	if err := e.list(&out, false); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, `invalid: template "triage" not found`) || !strings.Contains(s, "template triage") ||
		!strings.Contains(s, "(template)") {
		t.Errorf("list:\n%s", s)
	}
}

func TestPromptColumn(t *testing.T) {
	for _, tt := range []struct {
		j    cron.Job
		want string
	}{
		{cron.Job{Template: "triage"}, "template triage"},
		{cron.Job{PromptKey: "spec"}, "key spec"},
		{cron.Job{Prompt: "short\nprompt"}, "short prompt"},
		{cron.Job{Prompt: strings.Repeat("x", 40)}, strings.Repeat("x", 31) + "…"},
	} {
		if got := promptColumn(tt.j, time.Now()); got != tt.want {
			t.Errorf("promptColumn(%+v) = %q, want %q", tt.j, got, tt.want)
		}
	}
}

func TestRenderCronList(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.Local)
	paused := false
	j1 := cron.Job{Name: "triage", Schedule: "30 7 * * 1-5", Dir: "/p", Agent: "brandon", Prompt: "p", Timeout: "45m"}
	j1.Normalize()
	j2 := cron.Job{Name: "deps", Schedule: "@daily", Dir: "/p", Agent: "brandon", Prompt: "p", Enabled: &paused}
	j2.Normalize()
	v := cronListView{
		now: now,
		ov: cronedit.BuildOverview(cronedit.OverviewInput{Now: now, Jobs: []cron.Job{j1, j2},
			Runs: []cron.Entry{{Time: now.Add(-25 * time.Hour), Job: "triage", Result: cron.ResultFired}}}),
		accounts: map[string]string{"triage": "ll", "deps": "personal"},
		usage: map[string]*ratelimit.PushedRateLimits{
			"ll":       {UpdatedAt: now.Add(-3 * time.Minute).Unix(), FiveHour: &ratelimit.PushedWindow{UsedPercentage: 42}},
			"personal": nil,
		},
	}
	var out bytes.Buffer
	renderCronList(&out, v)
	s := out.String()
	for _, want := range []string{
		"Scheduler: not installed", "min_interval 15m · max_concurrent 2 (0 active) · catchup_window 6h", "5h ≥ 80%, 7d ≥ 90%", "Usage (ll): now 5h 42%, 7d ?", "Usage (personal): no usage data", "ACCOUNT",
		"triage", "Wed 18 Nov 07:3", "fired 1 day ago", "timeout:45m", "deps", "paused",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("list missing %q:\n%s", want, s)
		}
	}
}

func TestRenderCronRuns(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.Local)
	entries := []cron.Entry{
		{Time: now.Add(-2 * time.Hour), Job: "a", Scheduled: now.Add(-2 * time.Hour), Result: cron.ResultSkippedUsage, Note: "five_hour usage 85% >= 80%"},
		{Time: now.Add(-time.Hour), Job: "a", Scheduled: now.Add(-time.Hour), Result: cron.ResultFired, WindowID: "@3", Session: "agentctl-p", Missed: 2},
		{Time: now, Job: "a", Manual: true, Result: cron.Failed("tmux-unavailable")},
	}
	var out bytes.Buffer
	renderCronRuns(&out, entries, 2, now)
	s := out.String()
	if strings.Contains(s, "skipped:usage") {
		t.Error("limit not applied")
	}
	for _, want := range []string{"agentctl-p:@3", "2 older fires coalesced", "manual", "failed:tmux-unavailable"} {
		if !strings.Contains(s, want) {
			t.Errorf("runs missing %q:\n%s", want, s)
		}
	}
	out.Reset()
	renderCronRuns(&out, nil, 0, now)
	if !strings.Contains(out.String(), "No runs") {
		t.Errorf("empty = %q", out.String())
	}
}

func TestCronHealthLines(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.Local)
	tests := []struct {
		name string
		h    cronHealth
		want []string
	}{
		{name: "healthy", h: cronHealth{jobs: 2, enabled: 2, darwin: true, installed: true, loaded: true, lastTick: now.Add(-40 * time.Second)},
			want: []string{"2 jobs (2 enabled)", "plist loaded", "last tick 40 seconds ago"}},
		{name: "not loaded and stale", h: cronHealth{jobs: 1, enabled: 1, darwin: true, installed: true, lastTick: now.Add(-time.Hour)},
			want: []string{"NOT loaded", "(STALE)"}},
		{name: "not installed, never ticked", h: cronHealth{jobs: 1, enabled: 1, darwin: true},
			want: []string{"plist not installed", "never ticked"}},
		{name: "kill switch and failing jobs", h: cronHealth{jobs: 1, darwin: true, installed: true, loaded: true, killSwitch: true, failing: []string{"nightly"}},
			want: []string{"DISABLED by cron.enabled=false", "nightly: last 3 runs failed"}},
		{name: "linux", h: cronHealth{jobs: 1, enabled: 1}, want: []string{"crontab (not checked)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := strings.Join(cronHealthLines(tt.h, now), "\n")
			for _, w := range tt.want {
				if !strings.Contains(s, w) {
					t.Errorf("missing %q in %q", w, s)
				}
			}
		})
	}
}

func TestCronEdit(t *testing.T) {
	ptr := func(s string) *string { return &s }
	var launches []cron.LaunchRequest
	e := testCronEnv(t, &launches)
	var out bytes.Buffer
	projDir := t.TempDir()
	if err := e.add(&out, CronAddOptions{Name: "j", Schedule: "30 7 * * 1-5", Agent: "brandon", Dir: projDir, Prompt: "p", Model: "opus"}); err != nil {
		t.Fatal(err)
	}
	dir, _ := e.stateDir()
	earlier := e.now().Add(-time.Hour)
	if err := cron.Seed(dir, "j", earlier); err != nil {
		t.Fatal(err)
	}
	get := func() cron.Job {
		t.Helper()
		path, _ := e.jobsPath()
		jobs, err := cron.LoadJobs(path)
		if err != nil || len(jobs.Jobs) != 1 {
			t.Fatalf("jobs = %+v, %v", jobs, err)
		}
		return jobs.Jobs[0]
	}
	lastFire := func() time.Time {
		st, _ := cron.LoadState(dir)
		return st.Jobs["j"].LastFire
	}

	out.Reset()
	if err := e.edit(&out, CronEditOptions{Name: "j", Prompt: ptr("new prompt"), Model: ptr(""), NoWorktree: true}); err != nil {
		t.Fatal(err)
	}
	j := get()
	if j.Prompt != "new prompt" || j.Model != "" || j.UseWorktree() || j.Schedule != "30 7 * * 1-5" || j.Agent != "brandon" {
		t.Errorf("job = %+v", j)
	}
	if !strings.Contains(out.String(), "Updated j: prompt, model, worktree") || !lastFire().Equal(earlier) {
		t.Errorf("output %q, last_fire %v (want kept %v)", out.String(), lastFire(), earlier)
	}

	out.Reset()
	if err := e.edit(&out, CronEditOptions{Name: "j", Schedule: ptr("0 * * * *"), PromptKey: ptr("spec")}); err != nil {
		t.Fatal(err)
	}
	if j := get(); j.Prompt != "" || j.PromptKey != "spec" || j.Schedule != "0 * * * *" {
		t.Errorf("job = %+v", j)
	}
	if !lastFire().Equal(e.now()) || !strings.Contains(out.String(), "Schedule changed; next fire") {
		t.Errorf("schedule edit: output %q, last_fire %v", out.String(), lastFire())
	}

	for name, opts := range map[string]CronEditOptions{
		"unknown agent":  {Name: "j", Agent: ptr("nobody")},
		"bad schedule":   {Name: "j", Schedule: ptr("* * * * *")},
		"two prompts":    {Name: "j", Prompt: ptr("a"), PromptKey: ptr("k")},
		"missing job":    {Name: "nope", Prompt: ptr("a")},
		"both worktrees": {Name: "j", Worktree: true, NoWorktree: true},
	} {
		if err := e.edit(&out, opts); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	out.Reset()
	if err := e.edit(&out, CronEditOptions{Name: "j"}); err != nil || !strings.Contains(out.String(), "No changes to j") {
		t.Errorf("no-op edit: %v %q", err, out.String())
	}
}

func TestCronRunJSON(t *testing.T) {
	var launches []cron.LaunchRequest
	e := testCronEnv(t, &launches)
	var out bytes.Buffer
	if err := e.add(&out, CronAddOptions{Name: "j", Schedule: "@hourly", Agent: "brandon", Dir: t.TempDir(), Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := e.run(&out, "j", false, true); err != nil {
		t.Fatal(err)
	}
	entry, err := cronedit.ParseRunOutput("j", out.Bytes(), nil, nil)
	if err != nil || entry.Result != cron.ResultFired || entry.WindowID != "@7" {
		t.Errorf("entry = %+v, %v (output %q)", entry, err, out.String())
	}
}
