package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
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
	{"launch --help shows no-worktree", []string{"launch", "--help"}, 0, "no-worktree", ""},
	{"cleanup --help", []string{"cleanup", "--help"}, 0, "dry-run", ""},
	{"sync --help", []string{"sync", "--help"}, 0, "dry-run", ""},
	{"new --help", []string{"new", "--help"}, 0, "name", ""},
	{"install-templates --help", []string{"install-templates", "--help"}, 0, "force", ""},
	{"install-plugin --help", []string{"install-plugin", "--help"}, 0, "force", ""},
	{"sessions summary --help", []string{"sessions", "summary", "--help"}, 0, "date", ""},
	{"status --help lists subcommands", []string{"status", "--help"}, 0, "peek", ""},
	{"status list --help shows filters", []string{"status", "list", "--help"}, 0, "blocked", ""},
	{"status list --help shows verify", []string{"status", "list", "--help"}, 0, "verify", ""},
	{"status peek --help", []string{"status", "peek", "--help"}, 0, "lines", ""},
	{"status whoami --help", []string{"status", "whoami", "--help"}, 0, "json", ""},
	// --state is an enum; an unknown value must be a usage error rather than
	// silently matching nothing and printing an empty, reassuring table.
	{"status rejects an unknown --state", []string{"status", "--state", "nonsense"}, -1, "", ""},
	{"ticket create --help", []string{"ticket", "create", "--help"}, 0, "title", ""},
	{"audit --help", []string{"audit", "--help"}, 0, "session", ""},

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
	{"dispatch no issue or prompt", []string{"dispatch"}, 1, "", "--issue, --prompt, or --prompt-key is required"},
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
			wantErr: "--issue, --prompt, or --prompt-key is required",
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
		{[]string{"agentctl", "dispatch", "--issue", "1"}, false},
		{[]string{"agentctl", "send", "@1", "hi"}, false},
	}
	for _, tt := range tests {
		if got := isReadOnlyInvocation(tt.args); got != tt.want {
			t.Errorf("isReadOnlyInvocation(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}
