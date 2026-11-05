package hooks

// handlers_test.go — unit tests for critical hook handlers:
// SessionStart, ValidateBash (end-to-end), CheckDone (via checkDoneReason),
// Notify, and StopFailure.
//
// All tests are hermetic:
//   - windowstate.SetDirForTest isolates window state files.
//   - hooks.SetDepsForTest injects stubs (no tmux, no git, no build tool).
//   - t.Setenv / t.Cleanup manage env var lifecycle.
//   - os.Stdin / os.Stdout / os.Stderr are replaced via pipe pairs.

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// stubDeps returns a minimal Deps suitable for handler tests.
// All commands return success / empty output by default so tests opt in to
// specific failure modes rather than having to silence the defaults.
func stubDeps(t *testing.T) *Deps {
	t.Helper()
	return &Deps{
		RunCommand: func(name string, args ...string) ([]byte, error) {
			return nil, nil
		},
		TmuxDisplay: func(target, format string) string {
			return ""
		},
		LoadConfig: func() *config.Config {
			return &config.Config{Agents: make(map[string]config.AgentConfig)}
		},
		LoadProjectConfig: func(dir string) *config.ProjectConfig {
			return nil
		},
		Notify: func(opts notify.Options) {},
		StatFile: func(name string) (os.FileInfo, error) {
			return nil, errors.New("not found")
		},
		EnqueueMailboxMessage: func(m mailbox.Message) (string, error) {
			return "stub-id", nil
		},
	}
}

// setupWindowstate creates a temp dir for window state and registers cleanup.
func setupWindowstate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	windowstate.SetDirForTest(dir)
	t.Cleanup(func() { windowstate.SetDirForTest("") })
	return dir
}

// ---------------------------------------------------------------------------
// SessionStart tests
// ---------------------------------------------------------------------------

// TestSessionStart_WritesEnvVars verifies that SessionStart writes
// AGENTCTL_WINDOW_ID (and AGENTCTL_WINDOW_INDEX) to CLAUDE_ENV_FILE.
func TestSessionStart_WritesEnvVars(t *testing.T) {
	setupWindowstate(t)

	d := stubDeps(t)
	// TmuxDisplay is only consulted when TMUX_PANE is set; we skip it here.
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	envFile := filepath.Join(t.TempDir(), "claude-env.sh")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Setenv("AGENTCTL_WINDOW_ID", "@5")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "")

	stdin := `{"session_id":"sid1","cwd":"/tmp"}`
	runHookCaptured(t, nil, stdin, SessionStart)

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, "export AGENTCTL_WINDOW_ID='@5'") {
		t.Errorf("env file missing AGENTCTL_WINDOW_ID='@5'\nfile:\n%s", got)
	}
	// Window index is empty when tmux is absent, but the export line must exist.
	if !strings.Contains(got, "AGENTCTL_WINDOW_INDEX") {
		t.Errorf("env file missing AGENTCTL_WINDOW_INDEX\nfile:\n%s", got)
	}
}

// TestSessionStart_LinksSessionToWindowstate verifies that SessionStart writes
// session_id and log_path into the windowstate file.
func TestSessionStart_LinksSessionToWindowstate(t *testing.T) {
	setupWindowstate(t)

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	envFile := filepath.Join(t.TempDir(), "claude-env.sh")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Setenv("AGENTCTL_WINDOW_ID", "@7")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "")

	stdin := `{"session_id":"test-session-id","cwd":"/home/user/myproject"}`
	runHookCaptured(t, nil, stdin, SessionStart)

	ws, err := windowstate.Read("@7")
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.SessionID != "test-session-id" {
		t.Errorf("windowstate.SessionID = %q, want %q", ws.SessionID, "test-session-id")
	}
	if ws.LogPath == "" {
		t.Errorf("windowstate.LogPath is empty, want a non-empty path")
	}
}

// TestSessionStart_LogPathSessionIDConsistency verifies that SessionStart
// keeps log_path and session_id atomically consistent -- specifically, that
// a stale log_path from a previous session (or the tmux LaunchAgent
// bootstrap write) is cleared rather than left in place when a fresh
// log_path cannot be computed for the new session_id.
//
// Regression test for a live bug: window @93 had session_id
// 00000000-0000-4000-8000-000000000001 but a stale log_path pointing at an
// entirely different repo's session file, causing the monitor to
// mis-attribute tokens/cost/context% to the wrong project.
func TestSessionStart_LogPathSessionIDConsistency(t *testing.T) {
	tests := []struct {
		name          string
		cwd           string
		seedLogPath   string
		sessionID     string
		wantLogSuffix string // if empty, LogPath must be cleared to ""
	}{
		{
			name:          "cwd empty and stale log_path present -> cleared",
			cwd:           "",
			seedLogPath:   "/Users/alice/.claude/projects/-Users-alice-src-acme-repo-a--worktrees-agent-go-engineer-adhoc-04abce4a/00000000-0000-4000-8000-000000000002.jsonl",
			sessionID:     "00000000-0000-4000-8000-000000000001",
			wantLogSuffix: "",
		},
		{
			name:          "cwd set -> both fields updated and consistent",
			cwd:           "/home/user/myproject",
			seedLogPath:   "/some/stale/other-session.jsonl",
			sessionID:     "new-session-id",
			wantLogSuffix: "new-session-id.jsonl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupWindowstate(t)

			d := stubDeps(t)
			SetDepsForTest(d)
			t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

			const windowID = "@93"

			// Seed a stale windowstate record, mimicking the tmux LaunchAgent
			// bootstrap write or a prior session on a reused tmux window.
			if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
				ws.LogPath = tt.seedLogPath
				ws.SessionID = "old-session-id"
			}); err != nil {
				t.Fatalf("seed windowstate: %v", err)
			}

			envFile := filepath.Join(t.TempDir(), "claude-env.sh")
			t.Setenv("AGENTCTL", "1")
			t.Setenv("CLAUDE_ENV_FILE", envFile)
			t.Setenv("AGENTCTL_WINDOW_ID", windowID)
			t.Setenv("TMUX_PANE", "")
			t.Setenv("AGENTCTL_SESSION_ID", "")
			t.Setenv("AGENTCTL_AGENT_NAME", "")

			stdin := `{"session_id":"` + tt.sessionID + `","cwd":"` + tt.cwd + `"}`
			runHookCaptured(t, nil, stdin, SessionStart)

			ws, err := windowstate.Read(windowID)
			if err != nil {
				t.Fatalf("windowstate.Read: %v", err)
			}
			if ws.SessionID != tt.sessionID {
				t.Errorf("SessionID = %q, want %q", ws.SessionID, tt.sessionID)
			}
			if tt.wantLogSuffix == "" {
				if ws.LogPath != "" {
					t.Errorf("LogPath = %q, want cleared (empty) -- stale value must not persist", ws.LogPath)
				}
			} else {
				if !strings.HasSuffix(ws.LogPath, tt.wantLogSuffix) {
					t.Errorf("LogPath = %q, want suffix %q", ws.LogPath, tt.wantLogSuffix)
				}
				if filepath.Base(ws.LogPath) != tt.sessionID+".jsonl" {
					t.Errorf("LogPath basename %q does not match session_id %q -- invariant violated", filepath.Base(ws.LogPath), tt.sessionID)
				}
			}
		})
	}
}

// TestSessionStart_EmitsAdditionalContext verifies that SessionStart writes a
// JSON object with an "additionalContext" key to stdout when AGENTCTL_AGENT_NAME
// is set.
func TestSessionStart_EmitsAdditionalContext(t *testing.T) {
	setupWindowstate(t)

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	envFile := filepath.Join(t.TempDir(), "claude-env.sh")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Setenv("AGENTCTL_WINDOW_ID", "@9")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_NAME", "go-engineer")

	stdin := `{"session_id":"ctx-session","cwd":"/tmp"}`
	stdout, _ := runHookCaptured(t, nil, stdin, SessionStart)

	if stdout == "" {
		t.Fatal("expected JSON on stdout, got empty string")
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("unmarshal stdout JSON: %v\nraw: %s", err, stdout)
	}
	ctx, ok := payload["additionalContext"]
	if !ok {
		t.Fatalf("stdout JSON missing 'additionalContext' key\npayload: %+v", payload)
	}
	if !strings.Contains(ctx, "@9") {
		t.Errorf("additionalContext %q does not mention window ID '@9'", ctx)
	}
}

// TestSessionStart_SkipsWhenNotAgentctl verifies that SessionStart is a no-op
// when AGENTCTL != "1".
func TestSessionStart_SkipsWhenNotAgentctl(t *testing.T) {
	setupWindowstate(t)

	envFile := filepath.Join(t.TempDir(), "claude-env.sh")

	// Explicitly unset AGENTCTL (hookNoopTest does the same, but we want to
	// check the env file was NOT created as a stronger assertion).
	t.Setenv("AGENTCTL", "")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Setenv("AGENTCTL_WINDOW_ID", "@5")

	stdin := `{"session_id":"s1","cwd":"/tmp"}`
	stdout, _ := runHookCaptured(t, nil, stdin, SessionStart)

	if stdout != "" {
		t.Errorf("expected no stdout, got: %q", stdout)
	}
	// Env file must not have been written.
	if _, err := os.Stat(envFile); err == nil {
		data, _ := os.ReadFile(envFile)
		if len(data) > 0 {
			t.Errorf("env file was written when AGENTCTL!=1:\n%s", data)
		}
	}
}

// TestSessionStart_MissingClaudeEnvFile verifies that SessionStart logs a
// warning and returns cleanly when CLAUDE_ENV_FILE is not set.
func TestSessionStart_MissingClaudeEnvFile(t *testing.T) {
	setupWindowstate(t)

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "@5")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "")

	stdin := `{"session_id":"s2","cwd":"/tmp"}`
	// Must not panic.
	stdout, _ := runHookCaptured(t, nil, stdin, SessionStart)

	// No JSON output is expected (agent name is not set).
	if stdout != "" {
		t.Errorf("unexpected stdout: %q", stdout)
	}
}

// TestSessionStart_PropagatesDispatchMetadata verifies that AGENTCTL_TICKET and
// AGENTCTL_MODE are written to CLAUDE_ENV_FILE when set.
func TestSessionStart_PropagatesDispatchMetadata(t *testing.T) {
	setupWindowstate(t)

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	envFile := filepath.Join(t.TempDir(), "claude-env.sh")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Setenv("AGENTCTL_WINDOW_ID", "@5")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "")
	t.Setenv("AGENTCTL_TICKET", "42")
	t.Setenv("AGENTCTL_MODE", "dispatch")

	stdin := `{"session_id":"s3","cwd":"/tmp"}`
	runHookCaptured(t, nil, stdin, SessionStart)

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, "export AGENTCTL_TICKET='42'") {
		t.Errorf("env file missing AGENTCTL_TICKET='42'\nfile:\n%s", got)
	}
	if !strings.Contains(got, "export AGENTCTL_MODE='dispatch'") {
		t.Errorf("env file missing AGENTCTL_MODE='dispatch'\nfile:\n%s", got)
	}
}

// TestSessionStart_PropagatesSpawnedByMetadata verifies that
// AGENTCTL_SPAWNED_BY and AGENTCTL_SPAWNED_BY_SESSION are written to
// CLAUDE_ENV_FILE when set, so the dispatcher's reply-to address survives a
// session restart/resume the same way Ticket and Mode do.
func TestSessionStart_PropagatesSpawnedByMetadata(t *testing.T) {
	setupWindowstate(t)

	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	envFile := filepath.Join(t.TempDir(), "claude-env.sh")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Setenv("AGENTCTL_WINDOW_ID", "@5")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "")
	t.Setenv("AGENTCTL_SPAWNED_BY", "@36")
	t.Setenv("AGENTCTL_SPAWNED_BY_SESSION", "00000000-0000-4000-8000-000000000001")

	stdin := `{"session_id":"s3","cwd":"/tmp"}`
	runHookCaptured(t, nil, stdin, SessionStart)

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, "export AGENTCTL_SPAWNED_BY='@36'") {
		t.Errorf("env file missing AGENTCTL_SPAWNED_BY='@36'\nfile:\n%s", got)
	}
	if !strings.Contains(got, "export AGENTCTL_SPAWNED_BY_SESSION='00000000-0000-4000-8000-000000000001'") {
		t.Errorf("env file missing AGENTCTL_SPAWNED_BY_SESSION\nfile:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// ValidateBash end-to-end tests
// ---------------------------------------------------------------------------

// TestValidateBash_DeniesGloballyBlockedCommand verifies that a command in the
// global deny list is rejected.
func TestValidateBash_DeniesGloballyBlockedCommand(t *testing.T) {
	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "@1")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	// "git push origin main" is in the global deny list.
	stdin := `{"tool_name":"Bash","tool_input":{"command":"git push origin main"}}`
	stdout, _ := runHookCaptured(t, nil, stdin, ValidateBash)

	if !strings.Contains(stdout, "permissionDecision") {
		t.Fatalf("expected deny JSON on stdout, got: %q", stdout)
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("unmarshal deny output: %v\nraw: %s", err, stdout)
	}
	hso, _ := got["hookSpecificOutput"].(map[string]interface{})
	if decision, _ := hso["permissionDecision"].(string); decision != "deny" {
		t.Errorf("permissionDecision = %q, want deny", decision)
	}
}

// TestValidateBash_GlobalDeny_EnforcesExitCode2 is the highest-value
// regression guard for the fail-open bug fixed alongside this test: a
// globally denied command must actually result in exit(2), not merely in
// JSON on stdout that Claude Code might reject as invalid (as happened
// during an earlier regression, when a missing hookEventName silently downgraded
// every deny -- including "rm -rf /" -- into an allow). Unlike
// TestValidateBash_DeniesGloballyBlockedCommand, this test stubs exitFunc
// itself (rather than going through runHookCaptured's automatic no-op stub)
// so it can assert on exactly what exitFunc was called with.
func TestValidateBash_GlobalDeny_EnforcesExitCode2(t *testing.T) {
	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "@1")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	origExit := exitFunc
	var exitCodes []int
	exitFunc = func(code int) { exitCodes = append(exitCodes, code) }
	t.Cleanup(func() { exitFunc = origExit })

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	if _, err := io.WriteString(stdinW, `{"tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = stdinW.Close()
	oldStdin := os.Stdin
	os.Stdin = stdinR
	t.Cleanup(func() { os.Stdin = oldStdin })

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	oldStdout := os.Stdout
	os.Stdout = outW
	t.Cleanup(func() { os.Stdout = oldStdout })

	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	oldStderr := os.Stderr
	os.Stderr = errW
	t.Cleanup(func() { os.Stderr = oldStderr })

	ValidateBash()

	_ = outW.Close()
	_ = errW.Close()
	var outBuf, errBuf strings.Builder
	if _, err := io.Copy(&outBuf, outR); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if _, err := io.Copy(&errBuf, errR); err != nil {
		t.Fatalf("read stderr: %v", err)
	}

	if len(exitCodes) != 1 || exitCodes[0] != 2 {
		t.Fatalf("exitCodes = %v, want exactly one call with code 2 -- a globally denied command must not silently pass", exitCodes)
	}
	if !strings.Contains(outBuf.String(), `"deny"`) {
		t.Errorf("expected deny JSON on stdout, got: %q", outBuf.String())
	}
	if errBuf.String() == "" {
		t.Errorf("expected a non-empty plain-text reason on stderr, got empty")
	}
}

// TestValidateBash_DeniesPerAgentBlockedCommand verifies that a command blocked
// in the per-agent config is denied.
func TestValidateBash_DeniesPerAgentBlockedCommand(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")
	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"test-agent": {
				BlockedCommands: []string{"kubectl delete"},
			},
		},
	}
	config.SetConfigPathForTest(cfgPath)
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	t.Cleanup(func() { config.SetConfigPathForTest("") })

	d := stubDeps(t)
	d.LoadConfig = config.Load
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "test-agent")
	t.Setenv("AGENTCTL_WINDOW_ID", "@1")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"tool_name":"Bash","tool_input":{"command":"kubectl delete pod mypod"}}`
	stdout, _ := runHookCaptured(t, nil, stdin, ValidateBash)

	if !strings.Contains(stdout, `"deny"`) {
		t.Errorf("expected deny JSON on stdout, got: %q", stdout)
	}
}

// TestValidateBash_DeniesProjectLevelBlockedCommand verifies that a command
// blocked by the project config is denied.
func TestValidateBash_DeniesProjectLevelBlockedCommand(t *testing.T) {
	d := stubDeps(t)
	d.LoadProjectConfig = func(dir string) *config.ProjectConfig {
		return &config.ProjectConfig{
			BlockedCommands: []string{"terraform destroy"},
		}
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "@1")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"tool_name":"Bash","tool_input":{"command":"terraform destroy -auto-approve"}}`
	stdout, _ := runHookCaptured(t, nil, stdin, ValidateBash)

	if !strings.Contains(stdout, `"deny"`) {
		t.Errorf("expected deny JSON on stdout, got: %q", stdout)
	}
}

// TestValidateBash_AllowsCleanCommand verifies that a benign command produces
// no stdout output (exit 0 path).
func TestValidateBash_AllowsCleanCommand(t *testing.T) {
	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "@1")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"tool_name":"Bash","tool_input":{"command":"ls -la"}}`
	stdout, _ := runHookCaptured(t, nil, stdin, ValidateBash)

	if stdout != "" {
		t.Errorf("expected no stdout for allowed command, got: %q", stdout)
	}
}

// TestValidateBash_SkipsWhenNotAgentctl verifies that ValidateBash produces no
// output when AGENTCTL != "1".
func TestValidateBash_SkipsWhenNotAgentctl(t *testing.T) {
	t.Setenv("AGENTCTL", "")

	stdin := `{"tool_name":"Bash","tool_input":{"command":"git push origin main"}}`
	stdout, _ := runHookCaptured(t, nil, stdin, ValidateBash)

	if stdout != "" {
		t.Errorf("expected no stdout when AGENTCTL!=1, got: %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// checkDoneReason tests
// ---------------------------------------------------------------------------

// TestCheckDoneReason_BlocksOnDirtyGitStatus verifies that uncommitted changes
// produce a non-empty reason containing "uncommitted".
func TestCheckDoneReason_BlocksOnDirtyGitStatus(t *testing.T) {
	d := stubDeps(t)
	d.RunCommand = func(name string, args ...string) ([]byte, error) {
		if name == "git" {
			return []byte("M  internal/foo.go\n"), nil
		}
		return nil, nil
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	reason := checkDoneReason("test-agent")
	if reason == "" {
		t.Fatal("expected non-empty reason for dirty git status, got empty")
	}
	if !strings.Contains(strings.ToLower(reason), "uncommitted") {
		t.Errorf("reason %q does not mention uncommitted", reason)
	}
}

// TestCheckDoneReason_BlocksOnBuildFailure verifies that a build failure
// produces a non-empty reason containing "build".
func TestCheckDoneReason_BlocksOnBuildFailure(t *testing.T) {
	d := stubDeps(t)
	d.RunCommand = func(name string, args ...string) ([]byte, error) {
		if name == "git" {
			// Clean git status.
			return []byte(""), nil
		}
		if name == "go" {
			// Simulate build failure.
			return []byte("./main.go:5:2: undefined: Foo"), errors.New("exit status 1")
		}
		return nil, nil
	}
	d.StatFile = func(name string) (os.FileInfo, error) {
		if name == "go.mod" {
			// Pretend go.mod exists.
			return nil, nil
		}
		return nil, os.ErrNotExist
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	reason := checkDoneReason("test-agent")
	if reason == "" {
		t.Fatal("expected non-empty reason for build failure, got empty")
	}
	if !strings.Contains(strings.ToLower(reason), "build") {
		t.Errorf("reason %q does not mention build", reason)
	}
}

// TestCheckDoneReason_AllowsWhenClean verifies that clean git + passing build
// returns an empty reason.
func TestCheckDoneReason_AllowsWhenClean(t *testing.T) {
	setupWindowstate(t)

	d := stubDeps(t)
	d.RunCommand = func(name string, args ...string) ([]byte, error) {
		// Both git status and go build succeed with no output.
		return []byte(""), nil
	}
	d.StatFile = func(name string) (os.FileInfo, error) {
		if name == "go.mod" {
			return nil, nil // go.mod exists
		}
		return nil, os.ErrNotExist
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	reason := checkDoneReason("test-agent")
	if reason != "" {
		t.Errorf("expected empty reason for clean state, got %q", reason)
	}
}

// TestCheckDone_RespectsStopHookActive verifies that CheckDone returns
// immediately when stop_hook_active=true without running git/build checks.
func TestCheckDone_RespectsStopHookActive(t *testing.T) {
	setupWindowstate(t)

	gitCalled := false
	d := stubDeps(t)
	d.RunCommand = func(name string, args ...string) ([]byte, error) {
		if name == "git" {
			gitCalled = true
			return []byte("M  file.go"), nil // dirty — would normally block
		}
		return nil, nil
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", "@3")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"stop_hook_active":true}`
	// CheckDone must not call os.Exit here — if stop_hook_active is true, all
	// checks are skipped.
	runHookCaptured(t, nil, stdin, CheckDone)

	if gitCalled {
		t.Error("git status should not be called when stop_hook_active=true")
	}
}

// TestCheckDone_SetsIdleOnSuccess verifies that windowstate is set to "idle"
// when all checks pass.
func TestCheckDone_SetsIdleOnSuccess(t *testing.T) {
	setupWindowstate(t)

	d := stubDeps(t)
	d.RunCommand = func(name string, args ...string) ([]byte, error) {
		return []byte(""), nil
	}
	// No go.mod → skip build check.
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", "@4")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"stop_hook_active":false}`
	runHookCaptured(t, nil, stdin, CheckDone)

	ws, err := windowstate.Read("@4")
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.State != "idle" {
		t.Errorf("windowstate.State = %q, want idle", ws.State)
	}
}

// TestCheckDoneReason_SkipsBuildCheckWhenNoGoMod verifies that go build is not
// invoked when go.mod is absent.
func TestCheckDoneReason_SkipsBuildCheckWhenNoGoMod(t *testing.T) {
	buildCalled := false
	d := stubDeps(t)
	d.RunCommand = func(name string, args ...string) ([]byte, error) {
		if name == "go" {
			buildCalled = true
		}
		return []byte(""), nil
	}
	d.StatFile = func(name string) (os.FileInfo, error) {
		// go.mod does not exist.
		return nil, os.ErrNotExist
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	reason := checkDoneReason("test-agent")
	if reason != "" {
		t.Errorf("expected empty reason, got %q", reason)
	}
	if buildCalled {
		t.Error("go build should not be called when go.mod is absent")
	}
}

// ---------------------------------------------------------------------------
// Notify tests
// ---------------------------------------------------------------------------

// TestNotify_PermissionPromptSetsBlocked verifies that a permission_prompt
// notification sets windowstate.State = "blocked" and calls Notify.
func TestNotify_PermissionPromptSetsBlocked(t *testing.T) {
	setupWindowstate(t)

	var notifyTitle, notifyMsg string
	d := stubDeps(t)
	d.Notify = func(opts notify.Options) {
		notifyTitle = opts.Title
		notifyMsg = opts.Message
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_ID", "@10")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"notification_type":"permission_prompt","message":"needs approval"}`
	runHookCaptured(t, nil, stdin, Notify)

	ws, err := windowstate.Read("@10")
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.State != "blocked" {
		t.Errorf("windowstate.State = %q, want blocked", ws.State)
	}
	if notifyTitle == "" {
		t.Error("Notify was not called")
	}
	if notifyMsg == "" {
		t.Error("Notify message is empty")
	}
}

// TestNotify_IdlePromptSetsIdle verifies that an idle_prompt notification sets
// windowstate.State = "idle" and calls Notify.
func TestNotify_IdlePromptSetsIdle(t *testing.T) {
	setupWindowstate(t)

	notifyCalled := false
	d := stubDeps(t)
	d.Notify = func(opts notify.Options) {
		notifyCalled = true
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_ID", "@11")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"notification_type":"idle_prompt","message":"waiting"}`
	runHookCaptured(t, nil, stdin, Notify)

	ws, err := windowstate.Read("@11")
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.State != "idle" {
		t.Errorf("windowstate.State = %q, want idle", ws.State)
	}
	if !notifyCalled {
		t.Error("Notify was not called for idle_prompt")
	}
}

// TestNotify_UnknownTypeIsNoOp verifies that an unknown notification type does
// not update windowstate or call Notify.
func TestNotify_UnknownTypeIsNoOp(t *testing.T) {
	setupWindowstate(t)

	notifyCalled := false
	d := stubDeps(t)
	d.Notify = func(opts notify.Options) {
		notifyCalled = true
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_ID", "@12")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"notification_type":"auth_success","message":"logged in"}`
	runHookCaptured(t, nil, stdin, Notify)

	// windowstate file may not exist at all (never written).
	ws, _ := windowstate.Read("@12")
	if ws.State != "" {
		t.Errorf("windowstate.State = %q, want empty (no-op)", ws.State)
	}
	if notifyCalled {
		t.Error("Notify should not be called for auth_success")
	}
}

// ---------------------------------------------------------------------------
// StopFailure tests
// ---------------------------------------------------------------------------

// TestStopFailure_SetsIdleWithErrorType verifies that StopFailure sets
// windowstate.State = "idle" and windowstate.Error = the error type.
func TestStopFailure_SetsIdleWithErrorType(t *testing.T) {
	setupWindowstate(t)

	d := stubDeps(t)
	d.Notify = func(opts notify.Options) {}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_ID", "@20")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"error_type":"rate_limit","error_message":"quota exceeded"}`
	runHookCaptured(t, nil, stdin, StopFailure)

	ws, err := windowstate.Read("@20")
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.State != "idle" {
		t.Errorf("windowstate.State = %q, want idle", ws.State)
	}
	if ws.Error != "rate_limit" {
		t.Errorf("windowstate.Error = %q, want rate_limit", ws.Error)
	}
}

// TestStopFailure_MapsErrorTypesToMessages verifies that each known error type
// maps to the expected human-friendly notification message.
func TestStopFailure_MapsErrorTypesToMessages(t *testing.T) {
	cases := []struct {
		errorType   string
		wantMessage string
	}{
		{"rate_limit", "Rate limited"},
		{"authentication_failed", "Authentication failed"},
		{"billing_error", "Billing error"},
		{"server_error", "Server error"},
		{"max_output_tokens", "Hit max output tokens"},
		{"invalid_request", "Invalid request"},
	}

	for _, tc := range cases {
		t.Run(tc.errorType, func(t *testing.T) {
			setupWindowstate(t)

			var gotMessage string
			d := stubDeps(t)
			d.Notify = func(opts notify.Options) {
				gotMessage = opts.Message
			}
			SetDepsForTest(d)
			t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

			t.Setenv("AGENTCTL", "1")
			t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
			t.Setenv("AGENTCTL_WINDOW_ID", "@21")
			t.Setenv("TMUX_PANE", "")
			t.Setenv("AGENTCTL_SESSION_ID", "")

			stdin := `{"error_type":"` + tc.errorType + `"}`
			runHookCaptured(t, nil, stdin, StopFailure)

			if gotMessage != tc.wantMessage {
				t.Errorf("error_type=%q: Notify message = %q, want %q",
					tc.errorType, gotMessage, tc.wantMessage)
			}
		})
	}
}

// TestStopFailure_NotifyCalled verifies that Notify is always called, even for
// unknown error types.
func TestStopFailure_NotifyCalled(t *testing.T) {
	setupWindowstate(t)

	notifyCalled := false
	d := stubDeps(t)
	d.Notify = func(opts notify.Options) {
		notifyCalled = true
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
	t.Setenv("AGENTCTL_WINDOW_ID", "@22")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdin := `{"error_type":"some_unknown_error"}`
	runHookCaptured(t, nil, stdin, StopFailure)

	if !notifyCalled {
		t.Error("Notify was not called for StopFailure")
	}
}
