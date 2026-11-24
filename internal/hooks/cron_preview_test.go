package hooks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/remoteapproval"
)

const cronAddCmd = `agentctl cron add nightly --schedule "30 7 * * 1-5" --agent go-engineer --dir /src/app --prompt "Triage new issues."`

func TestIsAgentctlCronGated(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"cron add", cronAddCmd, true},
		{"absolute path", `/Users/me/.local/bin/agentctl cron add nightly --schedule @daily --agent a --dir . -p hi`, true},
		{"chained after &&", `cd /src/app && agentctl cron add nightly --schedule @daily --agent a --dir . -p hi`, true},
		{"after a read-only cron call", `agentctl cron list && agentctl cron add x --schedule @daily --agent a --dir . -p hi`, true},
		{"add --help", `agentctl cron add --help`, false},
		{"add -h", `agentctl cron add -h`, false},
		{"cron --help", `agentctl cron --help`, false},
		{"help-looking prompt value is not help", `agentctl cron add x --schedule @daily --agent a --dir . --prompt -h`, true},
		{"quoted help-looking word is not help", `agentctl cron add x --schedule @daily --agent a --dir . -p "--help"`, true},
		{"list", `agentctl cron list`, false},
		{"runs", `agentctl cron runs nightly -n 5`, false},
		{"pause", `agentctl cron pause x`, false},
		{"rm", `agentctl cron rm x`, false},
		{"install", `agentctl cron install`, false},
		{"uninstall", `agentctl cron uninstall`, false},
		{"resume", `agentctl cron resume x`, true},
		{"edit", `agentctl cron edit x --prompt hi`, true},
		{"edit with no flags", `agentctl cron edit x`, true},
		{"edit --help", `agentctl cron edit --help`, false},
		{"run --json is not forced", `agentctl cron run x --json`, false},
		{"resume after pause", `agentctl cron pause x; agentctl cron resume x`, true},
		{"resume with attached terminator", `agentctl cron resume;`, true},
		{"resume --help", `agentctl cron resume x --help`, false},
		{"plain run", `agentctl cron run x`, false},
		{"run --force", `agentctl cron run x --force`, true},
		{"run --force before name", `agentctl cron run --force x`, true},
		{"run --force=true", `agentctl cron run x --force=true`, true},
		{"force on a later command does not count", `agentctl cron run x && git push --force`, false},
		// Like dispatch: words inside one quoted string are one token.
		{"double-quoted string", `echo "agentctl cron add x"`, false},
		{"single-quoted commit message", `git commit -m 'agentctl cron add now gated'`, false},
		// Like dispatch: unquoted words match even as echo arguments (fail safe).
		{"unquoted echo args", `echo agentctl cron add x`, true},
		{"not agentctl", `crontab -l && cron add`, false},
		// A go run of the agentctl root package is agentctl.
		{"go run dot cron add", `go run . cron add x --schedule @daily --agent a --dir . -p hi`, true},
		{"go run flags cron resume", `go run -race ./ -- cron resume x`, true},
		{"go run module cron run --force", `go run github.com/blsmth/agentctl@latest cron run x --force`, true},
		{"timeout go run cron add", `timeout 9 go run . cron add x --schedule @daily --agent a --dir . -p hi`, true},
		{"go -C cron add", `go -C ../x run . cron add x --schedule @daily --agent a --dir . -p hi`, true},
		{"subshell cd go run cron add", `(cd /tmp) && go run . cron add x --schedule @daily --agent a --dir . -p hi`, true},
		{"skipped cd go run cron add", `false && cd /tmp; go run . cron add x --schedule @daily --agent a --dir . -p hi`, true},
		{"substitution go run cron add", `go run $(pwd) cron add x --schedule @daily --agent a --dir . -p hi`, true},
		{"go run cron list", `go run . cron list`, false},
		{"go run subpackage cron add", `go run ./internal/foo cron add x --schedule @daily`, false},
		{"go run other module cron add", `go run example.com/tool cron add x`, false},
		{"quoted go run cron add", `git commit -m "go run . cron add x"`, false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAgentctlCronGated(tt.command); got != tt.want {
				t.Errorf("isAgentctlCronGated(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

// TestCronDetection_MatchesDispatchQuoting pins that cron detection treats
// quoting exactly as dispatch detection does.
func TestCronDetection_MatchesDispatchQuoting(t *testing.T) {
	for _, form := range []string{`echo "agentctl %s"`, `echo 'agentctl %s'`, `echo agentctl %s`} {
		d := isAgentctlDispatch(strings.Replace(form, "%s", "dispatch --issue 1", 1))
		c := isAgentctlCronGated(strings.Replace(form, "%s", "cron add x", 1))
		if d != c {
			t.Errorf("%s: dispatch gated=%v, cron gated=%v, want equal", form, d, c)
		}
	}
}

func TestCronSegmentParse(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		wantName   string
		wantValues map[string]string
		noWorktree bool
	}{
		{
			name:     "long flags",
			command:  cronAddCmd,
			wantName: "nightly",
			wantValues: map[string]string{
				"schedule": "30 7 * * 1-5", "agent": "go-engineer", "dir": "/src/app", "prompt": "Triage new issues.",
			},
		},
		{
			name:       "short and equals forms",
			command:    `agentctl cron add -a go-engineer --schedule=@daily --dir=~/src -k spec-1 --no-worktree --timeout 45m job1`,
			wantName:   "job1",
			wantValues: map[string]string{"agent": "go-engineer", "schedule": "@daily", "dir": "~/src", "prompt-key": "spec-1", "timeout": "45m"},
			noWorktree: true,
		},
		{
			name:       "prompt file and account",
			command:    `agentctl cron add weekly --prompt-file ~/prompts/weekly.md --account work --schedule "0 9 * * 1" -a ops --dir /x`,
			wantName:   "weekly",
			wantValues: map[string]string{"prompt-file": "~/prompts/weekly.md", "account": "work", "schedule": "0 9 * * 1", "agent": "ops"},
		},
		{
			name:       "trailing redirect and chained command dropped",
			command:    `agentctl cron add j --schedule @daily -a a --dir /x -p "go" 2>&1 && echo done`,
			wantName:   "j",
			wantValues: map[string]string{"prompt": "go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segs := gatedCronSegments(tt.command)
			if len(segs) != 1 {
				t.Fatalf("got %d gated segments, want 1", len(segs))
			}
			a := segs[0].parse()
			if len(a.positional) == 0 || a.positional[0].text != tt.wantName {
				t.Errorf("name = %+v, want %q", a.positional, tt.wantName)
			}
			for k, want := range tt.wantValues {
				if got := a.value(k); got != want {
					t.Errorf("--%s = %q, want %q", k, got, want)
				}
			}
			if a.noWorktree != tt.noWorktree {
				t.Errorf("noWorktree = %v, want %v", a.noWorktree, tt.noWorktree)
			}
		})
	}
}

// withCronJobs makes cronJobsLoader return jobs.
func withCronJobs(t *testing.T, jobs ...cron.Job) {
	t.Helper()
	orig := cronJobsLoader
	cronJobsLoader = func() (*cron.Jobs, error) {
		for i := range jobs {
			jobs[i].Normalize()
		}
		return &cron.Jobs{Jobs: jobs}, nil
	}
	t.Cleanup(func() { cronJobsLoader = orig })
}

// withCronNow pins the next-fire clock.
func withCronNow(t *testing.T) {
	t.Helper()
	orig := cronNow
	cronNow = func() time.Time { return time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local) }
	t.Cleanup(func() { cronNow = orig })
}

func TestBuildCronPreview(t *testing.T) {
	isolateContextStore(t)
	withCronNow(t)
	off := false
	withCronJobs(t,
		cron.Job{Name: "paused", Schedule: "0 9 * * *", Agent: "ops", Dir: "/srv", Prompt: "stored prompt text", Enabled: &off, Timeout: "30m"},
		cron.Job{Name: "keyed", Schedule: "0 9 * * *", Agent: "ops", Dir: "/srv", PromptKey: "gone-key"},
	)
	promptFile := filepath.Join(t.TempDir(), "weekly.md")
	if err := os.WriteFile(promptFile, []byte("# Weekly\n\nSummarize the week."), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		command      string
		wantDeny     string
		wantAction   string
		wantJob      string
		wantAgent    string
		wantSchedule string
		wantBody     []string
	}{
		{
			name:         "inline prompt",
			command:      cronAddCmd,
			wantAction:   cronActionAdd,
			wantJob:      "nightly",
			wantAgent:    "go-engineer",
			wantSchedule: "30 7 * * 1-5 (weekdays 07:30), next: Wed 18 Nov 07:",
			wantBody: []string{"**Job:** nightly", "**Agent:** go-engineer", "**Dir:** /src/app", "**Harness:** claude",
				"**Mode:** window", "**Worktree:** on", "**Timeout:** none", "Triage new issues."},
		},
		{
			name:       "prompt file content shown",
			command:    `agentctl cron add weekly --schedule "0 9 * * 1" -a ops --dir /x --no-worktree --timeout 45m --account work --prompt-file ` + promptFile,
			wantAction: cronActionAdd,
			wantJob:    "weekly",
			wantAgent:  "ops",
			wantBody:   []string{"Summarize the week.", "copied into the job now", "**Worktree:** off", "**Timeout:** 45m", "**Account:** work"},
		},
		{
			name:       "prompt key from an earlier literal context set",
			command:    `agentctl context set spec-9 'Review open PRs.' && agentctl cron add prs --schedule @daily -a ops --dir /x -k spec-9`,
			wantAction: cronActionAdd,
			wantJob:    "prs",
			wantBody:   []string{"Review open PRs.", "context key spec-9 (read at every fire"},
		},
		{name: "command substitution prompt", command: `agentctl cron add j --schedule @daily -a a --dir /x --prompt "$(cat spec.md)"`, wantDeny: "--prompt uses shell expansion"},
		{name: "variable prompt", command: `agentctl cron add j --schedule @daily -a a --dir /x --prompt="do $TASK"`, wantDeny: "--prompt uses shell expansion"},
		{name: "backtick prompt", command: "agentctl cron add j --schedule @daily -a a --dir /x -p \"`cat x`\"", wantDeny: "--prompt uses shell expansion"},
		{name: "expanding dir", command: `agentctl cron add j --schedule @daily -a a --dir "$(pwd)" -p hi`, wantDeny: "--dir uses shell expansion"},
		{name: "expanding name", command: `agentctl cron add "$NAME" --schedule @daily -a a --dir /x -p hi`, wantDeny: "the job name uses shell expansion"},
		{name: "unknown prompt key", command: `agentctl cron add j --schedule @daily -a a --dir /x --prompt-key nope`, wantDeny: "key not found"},
		{name: "missing prompt file", command: `agentctl cron add j --schedule @daily -a a --dir /x --prompt-file /no/such/file.md`, wantDeny: "cannot read --prompt-file"},
		{name: "invalid schedule", command: `agentctl cron add j --schedule "61 * * * *" -a a --dir /x -p hi`, wantDeny: "invalid schedule"},
		{name: "missing schedule", command: `agentctl cron add j -a a --dir /x -p hi`, wantDeny: "schedule is required"},
		{name: "two adds in one call", command: cronAddCmd + " && " + strings.Replace(cronAddCmd, "nightly", "other", 1), wantDeny: "one cron add/edit/resume/run --force per Bash call"},
		{
			name:         "resume shows the stored job",
			command:      `agentctl cron resume paused`,
			wantAction:   cronActionResume,
			wantJob:      "paused",
			wantAgent:    "ops",
			wantSchedule: "0 9 * * * (daily 09:00), next: Wed 18 Nov 09:",
			wantBody:     []string{"Resuming re-enables scheduled fires", "stored prompt text", "**Dir:** /srv", "**Timeout:** 30m"},
		},
		{
			name:       "run --force shows the stored job",
			command:    `agentctl cron run paused --force`,
			wantAction: cronActionForce,
			wantJob:    "paused",
			wantBody:   []string{"Force run", "ignoring overlap, max_concurrent and the usage gate", "stored prompt text"},
		},
		{
			name:       "resume with an unset prompt key still previews",
			command:    `agentctl cron resume keyed`,
			wantAction: cronActionResume,
			wantJob:    "keyed",
			wantBody:   []string{"context key gone-key is not set"},
		},
		{
			name:         "edit shows the job after the edit and what changes",
			command:      `agentctl cron edit paused --schedule "30 8 * * 1-5" --prompt "New prompt." --no-worktree --model ""`,
			wantAction:   cronActionEdit,
			wantJob:      "paused",
			wantAgent:    "ops",
			wantSchedule: "30 8 * * 1-5 (weekdays 08:30), next: Wed 18 Nov 08:",
			wantBody: []string{"Edit changes: prompt, schedule, worktree.", "schedule restarts from now", "New prompt.",
				"**Worktree:** off", "**Dir:** /srv", "**Timeout:** 30m", "--prompt"},
		},
		{
			name:       "edit without a prompt flag shows the stored prompt",
			command:    `agentctl cron edit paused --timeout 1h`,
			wantAction: cronActionEdit,
			wantJob:    "paused",
			wantBody:   []string{"Edit changes: timeout.", "stored prompt text", "stored prompt (unchanged)", "**Timeout:** 1h"},
		},
		{
			name:       "edit switching to a prompt key",
			command:    `agentctl context set spec-3 'Keyed prompt.' && agentctl cron edit paused -k spec-3`,
			wantAction: cronActionEdit,
			wantJob:    "paused",
			wantBody:   []string{"Edit changes: prompt, prompt_key.", "Keyed prompt.", "context key spec-3"},
		},
		{name: "edit attached short prompt", command: `agentctl cron edit paused -pEVIL`, wantDeny: `cannot show "-pEVIL" for approval`},
		{name: "edit attached short prompt with =", command: `agentctl cron edit paused -p=EVIL`, wantDeny: `cannot show "-p=EVIL"`},
		{name: "edit attached short agent", command: `agentctl cron edit paused -aother`, wantDeny: `cannot show "-aother"`},
		{name: "edit attached short key", command: `agentctl cron edit paused -kKEY`, wantDeny: `cannot show "-kKEY"`},
		{name: "edit quoted attached short prompt", command: `agentctl cron edit paused "-pEVIL"`, wantDeny: `cannot show "-pEVIL"`},
		{name: "edit no-worktree with a value", command: `agentctl cron edit paused --no-worktree=true`, wantDeny: `cannot show "--no-worktree=true"`},
		{name: "edit worktree with a value", command: `agentctl cron edit paused --worktree=false`, wantDeny: `cannot show "--worktree=false"`},
		{name: "edit unknown flag", command: `agentctl cron edit paused --bogus x`, wantDeny: `cannot show "--bogus"`},
		{name: "add attached short prompt", command: `agentctl cron add j --schedule @daily -a ops --dir /x -pEVIL`, wantDeny: `cron add: cannot show "-pEVIL"`},
		{name: "add attached short agent", command: `agentctl cron add j --schedule @daily -aops --dir /x -p hi`, wantDeny: `cannot show "-aops"`},
		{name: "run --force with an extra flag", command: `agentctl cron run paused --force --json`, wantDeny: `cannot show "--json"`},
		{name: "edit unknown job", command: `agentctl cron edit nope -p hi`, wantDeny: `no cron job named "nope"`},
		{name: "edit without a name", command: `agentctl cron edit`, wantDeny: "no job name"},
		{name: "edit expanding prompt", command: `agentctl cron edit paused -p "$(cat x)"`, wantDeny: "cron edit: --prompt uses shell expansion"},
		{name: "edit bad schedule", command: `agentctl cron edit paused --schedule nope`, wantDeny: "cron edit: needs 5 fields, has 1"},
		{name: "edit unknown prompt key", command: `agentctl cron edit paused -k nope`, wantDeny: "cron edit --prompt-key nope: key not found"},
		{name: "resume unknown job", command: `agentctl cron resume nope`, wantDeny: `no cron job named "nope"`},
		{name: "resume without a name", command: `agentctl cron resume`, wantDeny: "no job name"},
		{name: "resume expanding name", command: `agentctl cron resume "$JOB"`, wantDeny: "the job name uses shell expansion"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, deny := buildCronPreview(tt.command)
			if tt.wantDeny != "" {
				if !strings.Contains(deny, tt.wantDeny) {
					t.Fatalf("deny = %q, want containing %q", deny, tt.wantDeny)
				}
				return
			}
			if deny != "" {
				t.Fatalf("unexpected deny: %s", deny)
			}
			if p.action != tt.wantAction || p.job != tt.wantJob {
				t.Errorf("action/job = %q/%q, want %q/%q", p.action, p.job, tt.wantAction, tt.wantJob)
			}
			if tt.wantAgent != "" && p.agent != tt.wantAgent {
				t.Errorf("agent = %q, want %q", p.agent, tt.wantAgent)
			}
			if tt.wantSchedule != "" && !strings.HasPrefix(p.schedule, tt.wantSchedule) {
				t.Errorf("schedule = %q, want prefix %q", p.schedule, tt.wantSchedule)
			}
			for _, want := range tt.wantBody {
				if !strings.Contains(p.body, want) {
					t.Errorf("body missing %q:\n%s", want, p.body)
				}
			}
		})
	}
}

func TestBuildCronPreview_JobsUnreadableDenies(t *testing.T) {
	orig := cronJobsLoader
	cronJobsLoader = func() (*cron.Jobs, error) { return nil, errors.New("corrupt") }
	t.Cleanup(func() { cronJobsLoader = orig })
	if _, deny := buildCronPreview(`agentctl cron resume x`); !strings.Contains(deny, "corrupt") {
		t.Errorf("deny = %q, want the load error", deny)
	}
}

func TestCronPreviewGate_NotInTmux_StaysSilent(t *testing.T) {
	t.Setenv("TMUX", "")
	failIfDispatchPopupRunnerCalled(t)
	approved, decided, deny := cronPreviewGate("orch", cronAddCmd, "")
	if approved || decided || deny != "" {
		t.Errorf("got (%v, %v, %q), want silent pass-through", approved, decided, deny)
	}
}

func TestCronPreviewGate_DenyReasonShowsNoPopup(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-cron-deny-reason")
	command := `agentctl cron add j --schedule @daily -a a --dir /x --prompt "$(cat spec.md)"`
	removeDispatchLockForTest(t, command)
	failIfDispatchPopupRunnerCalled(t)
	approved, decided, deny := cronPreviewGate("orch", command, "")
	if approved || decided || !strings.Contains(deny, "shell expansion") {
		t.Errorf("got (%v, %v, %q), want a shell-expansion deny", approved, decided, deny)
	}
}

func TestCronPreviewGate_PopupArgsAndVerdict(t *testing.T) {
	withCronNow(t)
	tests := []struct {
		name         string
		verdict      func(t *testing.T) error
		wantApproved bool
	}{
		{"approve", func(*testing.T) error { return nil }, true},
		{"deny", exitError1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TMUX", "/tmp/fake-tmux-socket")
			t.Setenv("AGENTCTL_SESSION_ID", "sess-cron-"+tt.name)
			removeDispatchLockForTest(t, cronAddCmd)

			var args DispatchPreviewArgs
			called := withFakeDispatchPopupRunner(t, func(_, _ int, argsFile string) error {
				data, err := os.ReadFile(argsFile)
				if err != nil {
					t.Fatalf("reading args: %v", err)
				}
				if err := json.Unmarshal(data, &args); err != nil {
					t.Fatalf("decoding args: %v", err)
				}
				return tt.verdict(t)
			})

			approved, decided, deny := cronPreviewGate("orch", cronAddCmd, "")
			if !*called || !decided || deny != "" || approved != tt.wantApproved {
				t.Fatalf("called=%v got (%v, %v, %q), want decided approved=%v", *called, approved, decided, deny, tt.wantApproved)
			}
			if args.Kind != PreviewKindCron || args.CronAction != cronActionAdd || args.CronJob != "nightly" ||
				args.TargetAgent != "go-engineer" || args.Command != cronAddCmd {
				t.Errorf("args = %+v", args)
			}
			if !strings.Contains(args.CronSchedule, "weekdays 07:30") || !strings.Contains(args.Prompt, "Triage new issues.") {
				t.Errorf("schedule %q / prompt %q missing content", args.CronSchedule, args.Prompt)
			}
		})
	}
}

func TestCronPopupNotice(t *testing.T) {
	p := cronPreview{action: cronActionForce, job: "nightly", agent: "go-engineer", schedule: "@daily", body: "job body"}
	n := cronPopupNotice("orch", "agentctl cron run nightly --force", p)
	if n.Tool != "cron" || n.Action != "force-run cron job nightly" || !strings.Contains(n.Detail, "nightly (go-engineer)") {
		t.Errorf("notice = %+v", n)
	}
	r := n.Remote
	if r == nil || r.Kind != remoteapproval.KindCron || r.TargetAgent != "go-engineer" || r.Prompt != "job body" ||
		r.AskingAgent != "orch" || r.Command != "agentctl cron run nightly --force" {
		t.Errorf("remote = %+v", r)
	}
}

// runValidateBash runs ValidateBash on command with an empty config.
func runValidateBash(t *testing.T, sessionID, command string) string {
	t.Helper()
	d := DefaultDeps()
	d.LoadConfig = func() *config.Config { return &config.Config{} }
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
	in, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]string{"command": command}})
	out, _ := runHookCaptured(t, map[string]string{
		"AGENTCTL": "1", "AGENTCTL_AGENT_NAME": "orch", "AGENTCTL_WINDOW_ID": "w99",
		"AGENTCTL_SESSION_ID": sessionID, "TMUX_PANE": "",
	}, string(in), ValidateBash)
	return out
}

func decisionOf(t *testing.T, out string) (decision, reason string) {
	t.Helper()
	var got struct {
		HSO struct {
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	return got.HSO.Decision, got.HSO.Reason
}

func TestValidateBash_CronGate(t *testing.T) {
	withCronNow(t)
	tests := []struct {
		name       string
		command    string
		verdict    func(t *testing.T) error // nil: popup must not open
		wantResult string                   // "allow", "deny" or "" (silent)
		wantReason string
	}{
		{name: "approved add", command: cronAddCmd, verdict: func(*testing.T) error { return nil }, wantResult: "allow"},
		{name: "denied add", command: cronAddCmd, verdict: exitError1, wantResult: "deny", wantReason: "Cron job denied in preview popup"},
		{name: "unpreviewable add", command: `agentctl cron add j --schedule @daily -a a --dir /x -p "$(cat x)"`, wantResult: "deny", wantReason: "shell expansion"},
		{name: "dispatch and cron together", command: "agentctl dispatch --issue 1 && " + cronAddCmd, wantResult: "deny", wantReason: "separate Bash calls"},
		{name: "read-only cron passes silently", command: `agentctl cron list`, wantResult: ""},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID := "sess-validate-cron-" + string(rune('a'+i))
			t.Setenv("TMUX", "/tmp/fake-tmux-socket")
			t.Setenv("AGENTCTL_SESSION_ID", sessionID)
			removeDispatchLockForTest(t, tt.command)
			if tt.verdict == nil {
				failIfDispatchPopupRunnerCalled(t)
			} else {
				withFakeDispatchPopupRunner(t, func(_, _ int, _ string) error { return tt.verdict(t) })
			}

			out := runValidateBash(t, sessionID, tt.command)
			if tt.wantResult == "" {
				if out != "" {
					t.Fatalf("want no output, got %q", out)
				}
				return
			}
			decision, reason := decisionOf(t, out)
			if decision != tt.wantResult || !strings.Contains(reason, tt.wantReason) {
				t.Errorf("decision %q reason %q, want %q containing %q", decision, reason, tt.wantResult, tt.wantReason)
			}
		})
	}
}
