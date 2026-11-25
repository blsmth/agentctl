package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
)

// cliCase describes a single CLI smoke-test scenario.
type cliCase struct {
	name string
	args []string
	// wantExit is the expected exit code. Use -1 for "any non-zero".
	wantExit int
	wantOut  string // substring match on stdout
	wantErr  string // substring match on stderr (may be empty)
}

// cliTests exercises the CLI surface without running commands that require tmux.
// Kong's exit code for usage errors (missing required arg, unknown command) is 80.
// Our own validation errors in Run() methods exit with 1.
var cliTests = []cliCase{
	// Help exits cleanly
	{"help flag", []string{"--help"}, 0, "Usage:", ""},
	{"-h flag", []string{"-h"}, 0, "Usage:", ""},

	// Version exits cleanly and includes "("
	{"version", []string{"version"}, 0, "(", ""},
	{"--version", []string{"--version"}, 0, "(", ""},
	{"-v flag", []string{"-v"}, 0, "(", ""},

	// Subcommand help exits cleanly and includes expected flag/arg names
	{"dispatch --help", []string{"dispatch", "--help"}, 0, "issue", ""},
	{"launch --help", []string{"launch", "--help"}, 0, "name", ""},
	{"race --help shows wpm", []string{"race", "--help"}, 0, "--read-wpm", ""},
	{"race --help shows max-gap", []string{"race", "--help"}, 0, "--max-gap", ""},
	{"race --json on a missing file", []string{"race", "/nonexistent/x.jsonl", "--json"}, 1, "", "no transcript at"},
	{"launch --help shows no-worktree", []string{"launch", "--help"}, 0, "no-worktree", ""},
	{"cleanup --help", []string{"cleanup", "--help"}, 0, "dry-run", ""},
	{"sync --help", []string{"sync", "--help"}, 0, "dry-run", ""},
	{"new --help", []string{"new", "--help"}, 0, "name", ""},
	{"install-templates --help", []string{"install-templates", "--help"}, 0, "force", ""},
	{"install-plugin prints marketplace commands", []string{"install-plugin"}, 0, "/plugin install agentctl@agentctl", ""},
	{"sessions summary --help", []string{"sessions", "summary", "--help"}, 0, "date", ""},
	{"status --help lists subcommands", []string{"status", "--help"}, 0, "peek", ""},
	{"status list --help shows filters", []string{"status", "list", "--help"}, 0, "blocked", ""},
	{"status list --help shows verify", []string{"status", "list", "--help"}, 0, "verify", ""},
	{"status list --help shows closed", []string{"status", "list", "--help"}, 0, "--closed", ""},
	{"status peek --help", []string{"status", "peek", "--help"}, 0, "lines", ""},
	{"status whoami --help", []string{"status", "whoami", "--help"}, 0, "json", ""},
	{"resume --help", []string{"resume", "--help"}, 0, "handle", ""},
	// --state is an enum; an unknown value must be a usage error rather than
	// silently matching nothing and printing an empty, reassuring table.
	{"status rejects an unknown --state", []string{"status", "--state", "nonsense"}, -1, "", ""},
	{"ticket create --help", []string{"ticket", "create", "--help"}, 0, "title", ""},
	{"audit --help", []string{"audit", "--help"}, 0, "session", ""},
	{"audit --help lists report", []string{"audit", "--help"}, 0, "report", ""},
	{"audit log --help", []string{"audit", "log", "--help"}, 0, "--denied", ""},
	{"audit --help lists verdicts", []string{"audit", "--help"}, 0, "verdicts", ""},
	{"audit verdicts --help", []string{"audit", "verdicts", "--help"}, 0, "since", ""},
	{"audit report --help", []string{"audit", "report", "--help"}, 0, "--severity", ""},
	{"audit report --help lists narrate", []string{"audit", "report", "--help"}, 0, "--narrate", ""},
	{"audit report on a missing file", []string{"audit", "report", "/nonexistent/x.jsonl"}, 1, "", "no transcript at"},
	{"audit report rejects an unknown --severity", []string{"audit", "report", "--severity", "urgent"}, -1, "", ""},

	// `tickets` is a Kong alias of `ticket`: same default browse flags and
	// the same create subcommand.
	{"tickets alias --help", []string{"tickets", "--help"}, 0, "create", ""},
	{"tickets alias --help lists browse", []string{"tickets", "--help"}, 0, "browse", ""},
	{"tickets alias create --help", []string{"tickets", "create", "--help"}, 0, "title", ""},
	{"tickets alias accepts browse flags", []string{"tickets", "browse", "--help"}, 0, "tui", ""},

	// Unknown command exits non-zero (Kong exit code 80 = usage error)
	{"unknown command", []string{"nonexistent-garbage"}, -1, "", ""},

	// Commands that require a positional arg fail gracefully without it.
	// Kong uses exit code 80 for missing required args.
	{"kill no arg", []string{"kill"}, -1, "", ""},
	{"launch no arg", []string{"launch"}, -1, "", ""},
	{"logs no arg", []string{"logs"}, -1, "", ""},
	{"hud no arg", []string{"hud"}, -1, "", ""},
	{"toggle no arg", []string{"toggle"}, -1, "", ""},

	// Dispatch validation: our own errors use exit code 1
	{"dispatch --issue and --prompt mutually exclusive", []string{"dispatch", "--issue", "1", "--prompt", "x", "--agent", "go-engineer"}, 1, "", "mutually exclusive"},
	{"dispatch no issue or prompt", []string{"dispatch"}, 1, "", "--review-pr, --prompt, --prompt-key, or --template is required"},
	{"dispatch --issue and --review-pr mutually exclusive", []string{"dispatch", "--issue", "1", "--review-pr", "2"}, 1, "", "--issue and --review-pr are mutually exclusive"},
	{"dispatch --author without --review-pr", []string{"dispatch", "--prompt", "x", "--agent", "go-engineer", "--author", "@3"}, 1, "", "--author requires --review-pr"},
	{"dispatch --help lists --review-pr", []string{"dispatch", "--help"}, 0, "review-pr", ""},
	{"dispatch --prompt without --agent", []string{"dispatch", "--prompt", "fix it"}, 1, "", "--agent is required"},
}

// TestCLISmoke builds the binary once and runs all cliTests.
func TestCLISmoke(t *testing.T) {
	bin := getTestBinary(t)

	for _, tc := range cliTests {
		tc := tc // capture loop var
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			runCLICase(t, bin, tc)
		})
	}
}

// hermeticCmd builds an exec.Cmd for the test binary that is isolated from
// the developer's real environment: it runs from a fresh temp working
// directory (so no project-local .agentctl/config.json is discovered) with
// $HOME pointed at a fresh temp directory (so no global
// ~/.config/agentctl/config.json is discovered either), and with
// AGENTCTL_RUNTIME_DIR explicitly stripped from the inherited environment
// (a stray value there — e.g. because the test itself is running inside an
// agentctl-managed session — would otherwise redirect runtime-dir discovery
// straight back to a real, non-hermetic location). This makes CLI smoke
// tests produce the same result regardless of the CWD or ambient env the
// test suite happens to run from.
func hermeticCmd(t *testing.T, bin string, args ...string) *exec.Cmd {
	t.Helper()

	home := t.TempDir()
	workDir := t.TempDir()

	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "AGENTCTL_RUNTIME_DIR=") {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "HOME="+home)

	cmd := exec.Command(bin, args...)
	cmd.Dir = workDir
	cmd.Env = env
	return cmd
}

// runCLICase runs one CLI test case and checks exit code + output substrings.
func runCLICase(t *testing.T, bin string, tc cliCase) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	cmd := hermeticCmd(t, bin, tc.args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Use a short timeout so tests don't hang if a command tries to start tmux.
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(5 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill() //nolint:errcheck
		}
		t.Fatalf("command timed out after 5s: %v", tc.args)
	}

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("unexpected error type: %T: %v", runErr, runErr)
		}
	}

	// Check exit code: -1 means "any non-zero".
	if tc.wantExit == -1 {
		if exitCode == 0 {
			t.Errorf("expected non-zero exit, got 0\nstdout: %s\nstderr: %s",
				stdout.String(), stderr.String())
		}
	} else if exitCode != tc.wantExit {
		t.Errorf("exit code = %d, want %d\nstdout: %s\nstderr: %s",
			exitCode, tc.wantExit, stdout.String(), stderr.String())
	}

	// Check stdout substring.
	if tc.wantOut != "" {
		combined := stdout.String() + stderr.String() // kong sometimes writes to stderr
		if !strings.Contains(combined, tc.wantOut) {
			t.Errorf("output does not contain %q\nstdout: %s\nstderr: %s",
				tc.wantOut, stdout.String(), stderr.String())
		}
	}

	// Check stderr substring.
	if tc.wantErr != "" {
		if !strings.Contains(stderr.String(), tc.wantErr) {
			t.Errorf("stderr does not contain %q\nstderr: %s", tc.wantErr, stderr.String())
		}
	}
}

// TestDispatchFlagParsing spot-checks that Kong correctly parses all DispatchCmd flags.
// We test the flag-parsing layer by checking error messages and exit codes.
func TestDispatchFlagParsing(t *testing.T) {
	bin := getTestBinary(t)

	cases := []struct {
		name    string
		args    []string
		wantErr string
		wantOut string
	}{
		{
			name:    "no flags — validation error",
			args:    []string{"dispatch"},
			wantErr: "--review-pr, --prompt, --prompt-key, or --template is required",
		},
		{
			name:    "both --issue and --prompt — mutual exclusion",
			args:    []string{"dispatch", "--issue", "42", "--prompt", "fix", "--agent", "go-engineer"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "--prompt without --agent — agent required",
			args:    []string{"dispatch", "--prompt", "fix lint"},
			wantErr: "--agent is required",
		},
		{
			name:    "dispatch --help shows all flags",
			args:    []string{"dispatch", "--help"},
			wantOut: "no-worktree",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			var stdout, stderr bytes.Buffer
			cmd := hermeticCmd(t, bin, tc.args...)
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			_ = cmd.Run() // intentionally ignore exit code — we check output below

			combined := stdout.String() + stderr.String()
			if tc.wantErr != "" && !strings.Contains(combined, tc.wantErr) {
				t.Errorf("output does not contain %q\nstdout: %s\nstderr: %s",
					tc.wantErr, stdout.String(), stderr.String())
			}
			if tc.wantOut != "" && !strings.Contains(combined, tc.wantOut) {
				t.Errorf("output does not contain %q\nstdout: %s\nstderr: %s",
					tc.wantOut, stdout.String(), stderr.String())
			}
		})
	}
}

func TestIsReadOnlyInvocation(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"agentctl"}, false},
		{[]string{"agentctl", "version"}, true},
		{[]string{"agentctl", "models"}, true},
		{[]string{"agentctl", "status", "--json"}, true},
		{[]string{"agentctl", "--help"}, true},
		{[]string{"agentctl", "dispatch", "-h"}, true},
		{[]string{"agentctl", "-v"}, true},
		{[]string{"agentctl", "serve"}, true},
		{[]string{"agentctl", "serve", "pair", "--name", "phone"}, true},
		{[]string{"agentctl", "dispatch", "--issue", "1"}, false},
		{[]string{"agentctl", "context", "set", "k", "v"}, false},
		{[]string{"agentctl", "cron", "list"}, true},
		{[]string{"agentctl", "cron", "add", "x", "--schedule", "@daily"}, true},
		{[]string{"agentctl", "cron", "tick"}, true},
		{[]string{"agentctl", "cron", "run", "x"}, true},
		{[]string{"agentctl", "race", "@3"}, true},
	}
	for _, tt := range tests {
		if got := isReadOnlyInvocation(tt.args); got != tt.want {
			t.Errorf("isReadOnlyInvocation(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

// TestAuditDefaultsToLog: `audit` became a command group, and bare
// `agentctl audit <flags>` must still run the trail query (`audit log`).
func TestAuditDefaultsToLog(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"audit", "--tool", "Bash", "--denied"}, "audit log"},
		{[]string{"audit"}, "audit log"},
		{[]string{"audit", "log", "--tool", "Bash"}, "audit log"},
		{[]string{"audit", "report", "@3", "--json"}, "audit report <target>"},
		{[]string{"audit", "report", "--all", "--since", "2d"}, "audit report"},
	}
	for _, tt := range tests {
		var cli CLI
		k, err := kong.New(&cli, kong.Name("agentctl"))
		if err != nil {
			t.Fatal(err)
		}
		ctx, err := k.Parse(tt.args)
		if err != nil {
			t.Fatalf("Parse(%v): %v", tt.args, err)
		}
		if got := ctx.Command(); got != tt.want {
			t.Errorf("Parse(%v) command = %q, want %q", tt.args, got, tt.want)
		}
		if tt.args[len(tt.args)-1] == "--denied" && (cli.Audit.Log.Tool != "Bash" || !cli.Audit.Log.Denied) {
			t.Errorf("flags not on audit log: %+v", cli.Audit.Log)
		}
	}
}

// TestCronTemplateFlagsParse: `cron add --template` needs no --agent (the
// template may name it) and `cron edit --template` is a pointer like the
// other edit flags.
func TestCronTemplateFlagsParse(t *testing.T) {
	var cli CLI
	k, err := kong.New(&cli, kong.Name("agentctl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Parse([]string{"cron", "add", "nightly", "--schedule", "@daily", "--dir", "/tmp", "--template", "triage"}); err != nil {
		t.Fatal(err)
	}
	if a := cli.Cron.Add; a.Template != "triage" || a.Agent != "" {
		t.Errorf("cron add parsed = %+v", a)
	}
	var cli2 CLI
	k2, _ := kong.New(&cli2, kong.Name("agentctl"))
	if _, err := k2.Parse([]string{"cron", "edit", "nightly", "--template", "triage"}); err != nil {
		t.Fatal(err)
	}
	if e := cli2.Cron.Edit; e.Template == nil || *e.Template != "triage" || e.Prompt != nil {
		t.Errorf("cron edit parsed = %+v", e)
	}
}

// TestCronEditFlagsParse: `cron edit` tells an absent flag (nil, unchanged)
// from an empty one (clears the field), and --worktree/--no-worktree are
// exclusive.
func TestCronEditFlagsParse(t *testing.T) {
	var cli CLI
	k, err := kong.New(&cli, kong.Name("agentctl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Parse([]string{"cron", "edit", "nightly", "--model", "", "--timeout", "30m", "--no-worktree"}); err != nil {
		t.Fatal(err)
	}
	c := cli.Cron.Edit
	if c.Name != "nightly" || c.Model == nil || *c.Model != "" || c.Timeout == nil || *c.Timeout != "30m" || !c.NoWorktree {
		t.Errorf("parsed = %+v", c)
	}
	if c.Schedule != nil || c.Prompt != nil || c.Agent != nil || c.Dir != nil || c.Worktree {
		t.Errorf("absent flags set: %+v", c)
	}
	var cli2 CLI
	k2, _ := kong.New(&cli2, kong.Name("agentctl"))
	if _, err := k2.Parse([]string{"cron", "edit", "x", "--worktree", "--no-worktree"}); err == nil {
		t.Error("--worktree with --no-worktree accepted")
	}
	var cli3 CLI
	k3, _ := kong.New(&cli3, kong.Name("agentctl"))
	if _, err := k3.Parse([]string{"cron", "run", "--json", "--", "nightly"}); err != nil || !cli3.Cron.Run.JSON || cli3.Cron.Run.Name != "nightly" || cli3.Cron.Run.Force {
		t.Errorf("cron run --json: %v %+v", err, cli3.Cron.Run)
	}
}

// TestArtifactFlagsParse: `artifact add -` reads stdin (a lone dash is a
// positional, not a flag), and the flag set matches the documented one.
func TestArtifactFlagsParse(t *testing.T) {
	var cli CLI
	k, err := kong.New(&cli, kong.Name("agentctl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Parse([]string{"artifact", "add", "-", "--entry", "report.html", "--title", "T", "--pin", "--no-notify", "--json"}); err != nil {
		t.Fatal(err)
	}
	a := cli.Artifact.Add
	if a.Path != "-" || a.Entry != "report.html" || a.Title != "T" || !a.Pin || !a.NoNotify || !a.JSON || a.Kind != "" {
		t.Errorf("add parsed = %+v", a)
	}
	var cli2 CLI
	k2, _ := kong.New(&cli2, kong.Name("agentctl"))
	if _, err := k2.Parse([]string{"artifact", "update", "abcdefghij", "-", "--note", "v2"}); err != nil {
		t.Fatal(err)
	}
	if u := cli2.Artifact.Update; u.ID != "abcdefghij" || u.Path != "-" || u.Note != "v2" {
		t.Errorf("update parsed = %+v", u)
	}
	var cli3 CLI
	k3, _ := kong.New(&cli3, kong.Name("agentctl"))
	if _, err := k3.Parse([]string{"artifact", "prune", "--dry-run"}); err != nil || cli3.Artifact.Prune.OlderThan != "30d" || !cli3.Artifact.Prune.DryRun {
		t.Errorf("prune parsed = %+v err=%v", cli3.Artifact.Prune, err)
	}
	var cli5 CLI
	k5, _ := kong.New(&cli5, kong.Name("agentctl"))
	if _, err := k5.Parse([]string{"artifact", "comments", "abcdefghij", "--json"}); err != nil || cli5.Artifact.Comments.ID != "abcdefghij" || !cli5.Artifact.Comments.JSON {
		t.Errorf("comments parsed = %+v err=%v", cli5.Artifact.Comments, err)
	}
	var cli4 CLI
	k4, _ := kong.New(&cli4, kong.Name("agentctl"))
	if _, err := k4.Parse([]string{"artifact", "add", "x.html", "--kind", "pdf"}); err == nil {
		t.Error("unknown --kind accepted")
	}
	var cli6 CLI
	k6, _ := kong.New(&cli6, kong.Name("agentctl"))
	if _, err := k6.Parse([]string{"artifact", "render", "spec.json", "--title", "T", "--description", "D", "--update", "abcdefghij",
		"--note", "n", "--no-notify", "--pin", "--json"}); err != nil {
		t.Fatal(err)
	}
	if r := cli6.Artifact.Render; r.Spec != "spec.json" || r.Title != "T" || r.Description != "D" || r.Update != "abcdefghij" ||
		r.Note != "n" || !r.NoNotify || !r.Pin || !r.JSON || r.Out != "" {
		t.Errorf("render parsed = %+v", r)
	}
	var cli8 CLI
	k8, _ := kong.New(&cli8, kong.Name("agentctl"))
	if _, err := k8.Parse([]string{"artifact", "pdf", "abcdefghij", "--rev", "2", "--out", "-", "--force"}); err != nil {
		t.Fatal(err)
	}
	if p := cli8.Artifact.PDF; p.ID != "abcdefghij" || p.Rev != 2 || p.Out != "-" || !p.Force {
		t.Errorf("pdf parsed = %+v", p)
	}
	var cli7 CLI
	k7, _ := kong.New(&cli7, kong.Name("agentctl"))
	if _, err := k7.Parse([]string{"artifact", "render", "-", "--out", "page.html"}); err != nil || cli7.Artifact.Render.Spec != "-" || cli7.Artifact.Render.Out != "page.html" {
		t.Errorf("render --out parsed = %+v err=%v", cli7.Artifact.Render, err)
	}
}

// TestLaunchWatchParses: the hidden launch watcher takes one window ID.
func TestLaunchWatchParses(t *testing.T) {
	var cli CLI
	k, err := kong.New(&cli, kong.Name("agentctl"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := k.Parse([]string{"launch-watch", "@1774"})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Command() != "launch-watch <window-id>" || cli.LaunchWatch.WindowID != "@1774" {
		t.Errorf("parsed %q window %q", ctx.Command(), cli.LaunchWatch.WindowID)
	}
}

// TestAgentsGroupRouting pins that the list stays the default child of
// `agents` and the history subcommands select their own commands.
func TestAgentsGroupRouting(t *testing.T) {
	parse := func(args ...string) (CLI, string) {
		t.Helper()
		var cli CLI
		k, err := kong.New(&cli, kong.Name("agentctl"))
		if err != nil {
			t.Fatal(err)
		}
		ctx, err := k.Parse(args)
		if err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
		return cli, ctx.Command()
	}
	for _, tc := range []struct {
		args []string
		cmd  string
		want AgentsListCmd
	}{
		{[]string{"agents"}, "agents list", AgentsListCmd{}},
		{[]string{"agents", "--json"}, "agents list", AgentsListCmd{JSON: true}},
		{[]string{"agents", "--tui"}, "agents list", AgentsListCmd{TUI: true}},
		{[]string{"agents", "reviewer"}, "agents list <name>", AgentsListCmd{Name: "reviewer"}},
	} {
		cli, got := parse(tc.args...)
		if got != tc.cmd || cli.Agents.List != tc.want {
			t.Errorf("%v: command %q list %+v, want %q %+v", tc.args, got, cli.Agents.List, tc.cmd, tc.want)
		}
	}
	if cli, got := parse("agents", "history", "a"); got != "agents history <name>" || cli.Agents.History.Name != "a" {
		t.Errorf("history: %q %+v", got, cli.Agents.History)
	}
	if cli, got := parse("agents", "revert", "x", "abc"); got != "agents revert <name> <rev>" || cli.Agents.Revert.Name != "x" || cli.Agents.Revert.Rev != "abc" {
		t.Errorf("revert: %q %+v", got, cli.Agents.Revert)
	}
}

func TestImproveFlagsParse(t *testing.T) {
	var cli CLI
	k, err := kong.New(&cli, kong.Name("agentctl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Parse([]string{"improve", "go-engineer", "--since", "24h", "--theme", "t", "--model", "sonnet", "--session", "abc", "--session", "def", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	c := cli.Improve.Start
	if c.Agent != "go-engineer" || c.Since != "24h" || c.Theme != "t" || c.Model != "sonnet" || !c.DryRun || c.Fleet || len(c.Session) != 2 {
		t.Errorf("improve = %+v", c)
	}
	var cli2 CLI
	k2, _ := kong.New(&cli2, kong.Name("agentctl"))
	if _, err := k2.Parse([]string{"improve", "--fleet"}); err != nil {
		t.Fatal(err)
	}
	if i := cli2.Improve.Start; !i.Fleet || i.Agent != "" || i.Since != "7d" || i.Focus {
		t.Errorf("improve --fleet = %+v", i)
	}
	var cli3 CLI
	k3, _ := kong.New(&cli3, kong.Name("agentctl"))
	if _, err := k3.Parse([]string{"improve", "runs", "--since", "24h"}); err != nil {
		t.Fatal(err)
	}
	if cli3.Improve.Runs.Since != "24h" {
		t.Errorf("improve runs = %+v", cli3.Improve.Runs)
	}
	var cli4 CLI
	k4, _ := kong.New(&cli4, kong.Name("agentctl"))
	if _, err := k4.Parse([]string{"agents", "history", "--store", "config", "foo"}); err != nil || cli4.Agents.History.Store != "config" {
		t.Errorf("agents history --store: %v %+v", err, cli4.Agents.History)
	}
}
