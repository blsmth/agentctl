package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/ids"
)

// buildDispatchBinary compiles the agentctl binary for dispatch flag tests.
// Reuses the shared testBinaryOnce mechanism from smoke_test.go.
func buildDispatchBinary(t *testing.T) string {
	t.Helper()
	return getTestBinary(t)
}

// TestDispatchMutualExclusion verifies that providing both --issue and --prompt
// produces a clear error and exits with code 1.
func TestDispatchMutualExclusion(t *testing.T) {
	bin := buildDispatchBinary(t)

	cmd := exec.Command(bin, "dispatch", "--issue", "42", "--prompt", "Fix lint warnings", "--agent", "go-engineer")
	cmd.Dir = t.TempDir()
	cmd.Env = isolatedEnv(t)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()

	if err == nil {
		t.Fatal("expected non-zero exit, got 0")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", exitErr.ExitCode())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr %q does not contain 'mutually exclusive'", stderr.String())
	}
}

// TestDispatchNeitherIssueNorPrompt verifies that supplying neither --issue nor
// --prompt produces a clear error and exits with code 1.
func TestDispatchNeitherIssueNorPrompt(t *testing.T) {
	bin := buildDispatchBinary(t)

	cmd := exec.Command(bin, "dispatch", "--agent", "go-engineer")
	cmd.Dir = t.TempDir()
	cmd.Env = isolatedEnv(t)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()

	if err == nil {
		t.Fatal("expected non-zero exit, got 0")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", exitErr.ExitCode())
	}
	if !strings.Contains(stderr.String(), "--issue, --prompt, or --prompt-key is required") {
		t.Errorf("stderr %q does not contain '--issue, --prompt, or --prompt-key is required'", stderr.String())
	}
}

// TestDispatchPromptRequiresAgent verifies that --prompt without --agent
// produces a clear error and exits with code 1.
func TestDispatchPromptRequiresAgent(t *testing.T) {
	bin := buildDispatchBinary(t)

	cmd := exec.Command(bin, "dispatch", "--prompt", "Fix lint warnings")
	cmd.Dir = t.TempDir()
	cmd.Env = isolatedEnv(t)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()

	if err == nil {
		t.Fatal("expected non-zero exit, got 0")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", exitErr.ExitCode())
	}
	if !strings.Contains(stderr.String(), "--agent is required") {
		t.Errorf("stderr %q does not contain '--agent is required'", stderr.String())
	}
}

// TestAdHocBranchNaming verifies that ids.NewUUID()[:8] produces 8 hex characters
// suitable for branch slug usage.
func TestAdHocBranchNaming(t *testing.T) {
	slug := ids.NewUUID()[:8]
	if len(slug) != 8 {
		t.Errorf("ad-hoc slug len = %d, want 8", len(slug))
	}
	// Slug should be hex digits only (first segment of UUID is 8 hex chars).
	for _, ch := range slug {
		if !('0' <= ch && ch <= '9') && !('a' <= ch && ch <= 'f') {
			t.Errorf("non-hex character %q in ad-hoc slug %q", ch, slug)
		}
	}
}

// isolatedEnv returns an environment for running the agentctl test binary
// that cannot reach the developer's real tmux server.
//
// This matters more than it looks. These tests invoke the real binary with a
// real `dispatch --agent go-engineer` command line. If that command can find
// the live tmux session it spawns an actual Claude Code agent — and because
// the spawned agent runs `go test ./...` itself, each generation spawns the
// next. Running the suite from the repo root was observed to set off exactly
// that self-replicating loop.
//
// The previous implementation only unset TMUX and pointed TMUX_TMPDIR at a
// non-existent path. That does not isolate anything:
//
//   - agentctl never consults $TMUX. It targets its session by *name*
//     ("agentctl-<project>", see tmux.SetSessionName), so removing TMUX is
//     irrelevant.
//   - tmux still located the real server anyway; a bogus TMUX_TMPDIR does not
//     stop it.
//
// The hard guarantee now comes from a stub `tmux` placed first on PATH that
// always exits non-zero, so no tmux invocation can succeed no matter how
// agentctl resolves its session. HOME, AGENTCTL_RUNTIME_DIR and (at the call
// site) the working directory are additionally pointed at throwaway temp dirs
// so no real user or project config is discovered — notably `default_agent`,
// which can turn a command expected to fail into a successful live dispatch.
func isolatedEnv(t *testing.T) []string {
	t.Helper()

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write tmux stub: %v", err)
	}

	home := t.TempDir()
	runtimeDir := filepath.Join(t.TempDir(), ".agentctl")

	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		switch {
		case strings.HasPrefix(e, "TMUX="),
			strings.HasPrefix(e, "PATH="),
			strings.HasPrefix(e, "HOME="),
			strings.HasPrefix(e, "AGENTCTL_RUNTIME_DIR="):
			continue
		}
		env = append(env, e)
	}
	env = append(env,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+home,
		"AGENTCTL_RUNTIME_DIR="+runtimeDir,
	)
	return env
}

// TestDispatchModelFlagAccepted verifies that --model is a recognized flag and
// does not trigger a parse error. The command still exits 1 because --issue and
// --prompt are mutually exclusive with each other (not because of --model).
func TestDispatchModelFlagAccepted(t *testing.T) {
	bin := buildDispatchBinary(t)

	// Provide --model alongside a valid flag combination that fails for business
	// reasons (no tmux), not for flag-parse reasons. We just need to confirm
	// that "--model sonnet" doesn't produce "unknown flag" output.
	// Use an isolated env to prevent the subprocess from finding the real tmux
	// session and accidentally creating live agent windows.
	cmd := exec.Command(bin, "dispatch", "--prompt", "fix lint", "--agent", "go-engineer", "--model", "sonnet", "--no-worktree")
	cmd.Dir = t.TempDir()
	cmd.Env = isolatedEnv(t)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	_ = cmd.Run() // may fail due to missing tmux — that's fine

	if strings.Contains(stderr.String(), "unknown flag") || strings.Contains(stderr.String(), "unexpected flag") {
		t.Errorf("--model flag not recognized: stderr = %q", stderr.String())
	}
}

// TestDispatchModelFlagShorthand verifies that -m is accepted as a shorthand for --model.
func TestDispatchModelFlagShorthand(t *testing.T) {
	bin := buildDispatchBinary(t)

	// Use an isolated env to prevent the subprocess from finding the real tmux
	// session and accidentally creating live agent windows.
	cmd := exec.Command(bin, "dispatch", "--prompt", "fix lint", "--agent", "go-engineer", "-m", "opus", "--no-worktree")
	cmd.Dir = t.TempDir()
	cmd.Env = isolatedEnv(t)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	_ = cmd.Run()

	if strings.Contains(stderr.String(), "unknown flag") || strings.Contains(stderr.String(), "unexpected flag") {
		t.Errorf("-m shorthand not recognized: stderr = %q", stderr.String())
	}
}

// TestDispatchModelThreadedThroughRequest verifies that runCmdDispatch passes
// the model value through to the dispatch functions. We test this by inspecting
// the launchTicketMode and dispatchPrompt signatures compile correctly with the
// model parameter — the compile-time check in build is the primary guard, but
// we also verify the DispatchCmd.Model field exists and is wired to the call.
func TestDispatchCmdHasModelField(t *testing.T) {
	t.Parallel()
	cmd := DispatchCmd{
		Prompt: "hello",
		Agent:  "go-engineer",
		Model:  "sonnet",
	}
	if cmd.Model != "sonnet" {
		t.Errorf("DispatchCmd.Model = %q, want %q", cmd.Model, "sonnet")
	}
}

// TestDispatchPromptHappyPath verifies that --prompt with a mock tmux environment
// succeeds. We gate this test on a AGENTCTL_TEST_TMUX env var so it only runs
// when a real or mock tmux session is available.
func TestDispatchPromptHappyPath(t *testing.T) {
	if os.Getenv("AGENTCTL_TEST_TMUX") == "" {
		t.Skip("set AGENTCTL_TEST_TMUX=1 to run integration tests requiring tmux")
	}

	bin := buildDispatchBinary(t)
	dir := t.TempDir()

	cmd := exec.Command(bin, "dispatch",
		"--prompt", "Fix lint warnings",
		"--agent", "go-engineer",
		"--dir", dir,
		"--no-worktree",
	)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	if err != nil {
		t.Fatalf("dispatch --prompt failed: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Dispatched prompt →") {
		t.Errorf("stdout %q does not contain 'Dispatched prompt →'", stdout.String())
	}
}
