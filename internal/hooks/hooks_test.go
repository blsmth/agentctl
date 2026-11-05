package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/shellquote"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// hookNoopTest runs a hook function with AGENTCTL unset (simulating a session
// outside an agentctl-managed window) and verifies exit 0 + no stdout output.
// stdin is seeded with valid JSON so we can confirm it is drained cleanly.
func hookNoopTest(t *testing.T, name string, fn func(), stdinPayload string) {
	t.Helper()

	// Ensure AGENTCTL is NOT set to "1".
	orig, wasSet := os.LookupEnv("AGENTCTL")
	os.Unsetenv("AGENTCTL")
	defer func() {
		if wasSet {
			_ = os.Setenv("AGENTCTL", orig)
		}
	}()

	// Replace os.Stdin with a pipe seeded with the payload.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("%s: create stdin pipe: %v", name, err)
	}
	if _, err := io.WriteString(stdinW, stdinPayload); err != nil {
		t.Fatalf("%s: write stdin: %v", name, err)
	}
	_ = stdinW.Close()
	oldStdin := os.Stdin
	os.Stdin = stdinR
	defer func() { os.Stdin = oldStdin; _ = stdinR.Close() }()

	// Capture os.Stdout.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("%s: create stdout pipe: %v", name, err)
	}
	oldStdout := os.Stdout
	os.Stdout = stdoutW
	defer func() { os.Stdout = oldStdout }()

	// Capture os.Stderr.
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("%s: create stderr pipe: %v", name, err)
	}
	oldStderr := os.Stderr
	os.Stderr = stderrW
	defer func() { os.Stderr = oldStderr }()

	// Run the hook -- must not panic or call os.Exit.
	fn()

	_ = stdoutW.Close()
	_ = stderrW.Close()

	var outBuf, errBuf bytes.Buffer
	_, _ = outBuf.ReadFrom(stdoutR)
	_, _ = errBuf.ReadFrom(stderrR)

	if out := outBuf.String(); out != "" {
		t.Errorf("%s noop: unexpected stdout %q", name, out)
	}
	// Stderr may contain logging output from the logging package -- that's fine as
	// long as it doesn't contain JSON that the hook runner could misinterpret.
	// We only block explicit hook JSON payloads.
	if s := errBuf.String(); strings.Contains(s, `"hookSpecificOutput"`) || strings.Contains(s, `"permissionDecision"`) {
		t.Errorf("%s noop: unexpected hook JSON on stderr: %q", name, s)
	}
}

// TestHookNoopWithoutAgentctl verifies that every hook exits cleanly (no
// stdout, no hook JSON on stderr) when AGENTCTL != "1". This covers the
// scenario where Claude Code fires hooks during improvement sessions or manual
// `claude` invocations outside an agentctl-managed window.
func TestHookNoopWithoutAgentctl(t *testing.T) {
	tests := []struct {
		name    string
		fn      func()
		payload string
	}{
		{
			name:    "hook-pre-tool",
			fn:      PreTool,
			payload: `{"tool_name":"Glob"}`,
		},
		{
			name:    "hook-post-tool",
			fn:      PostTool,
			payload: `{"tool_name":"Read"}`,
		},
		{
			name:    "hook-user-prompt",
			fn:      UserPrompt,
			payload: `{"prompt":"hello"}`,
		},
		{
			name:    "hook-validate-bash",
			fn:      ValidateBash,
			payload: `{"tool_name":"Bash","tool_input":{"command":"ls"}}`,
		},
		{
			name:    "hook-post-tool-failure",
			fn:      PostToolFailure,
			payload: `{"tool_name":"Bash","error":"exit 1","is_interrupt":false}`,
		},
		{
			name:    "hook-notify",
			fn:      Notify,
			payload: `{"notification_type":"idle_prompt","message":"waiting"}`,
		},
		{
			name:    "hook-session-start",
			fn:      SessionStart,
			payload: `{"session_id":"abc123","cwd":"/tmp"}`,
		},
		{
			name:    "hook-session-end",
			fn:      SessionEnd,
			payload: `{"reason":"exit","session_id":"abc123"}`,
		},
		{
			name:    "hook-post-compact",
			fn:      PostCompact,
			payload: `{}`,
		},
		{
			name:    "hook-subagent-start",
			fn:      SubagentStart,
			payload: `{"agent_id":"x","agent_type":"subagent"}`,
		},
		{
			name:    "hook-subagent-stop",
			fn:      SubagentStop,
			payload: `{"agent_id":"x","agent_type":"subagent"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hookNoopTest(t, tt.name, tt.fn, tt.payload)
		})
	}
}

// ---------------------------------------------------------------------------
// hookNotify
// ---------------------------------------------------------------------------

func TestHookNotify_EmptyCmd_NoOp(t *testing.T) {
	// When notify_cmd is empty and platform is not darwin, hookNotify should no-op.
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")
	cfg := &config.Config{
		NotifyCmd: config.StringPtr(""),
		Agents:    map[string]config.AgentConfig{},
	}
	config.SetGlobalConfigPathForTest(cfgPath)
	config.SetConfigPathForTest(cfgPath)
	config.Save(cfg) //nolint:errcheck
	defer func() { config.SetGlobalConfigPathForTest(""); config.SetConfigPathForTest("") }()

	// hookNotify should not panic or produce errors with empty cmd on non-darwin.
	// On darwin, the default alerter cmd will be used, but we can't easily test that
	// without actually running alerter. The important behavior is that it doesn't panic.
	hookNotify(notify.Options{Title: "test", Message: "message"})
}

func TestHookNotify_TemplateSubstitution(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")
	outFile := filepath.Join(cfgDir, "notify-output.txt")
	cfg := &config.Config{
		// Use a command that writes the substituted values to a file.
		// Note: shellquote.Quote wraps {{title}} and {{message}} in single quotes, so
		// the template must NOT include its own surrounding quotes.
		NotifyCmd: config.StringPtr("printf '%s\\n%s' {{title}} {{message}} > " + outFile),
		Agents:    map[string]config.AgentConfig{},
	}
	config.SetGlobalConfigPathForTest(cfgPath)
	config.SetConfigPathForTest(cfgPath)
	config.Save(cfg) //nolint:errcheck
	defer func() { config.SetGlobalConfigPathForTest(""); config.SetConfigPathForTest("") }()

	hookNotify(notify.Options{Title: "my-agent", Message: "Needs permission"})

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read notify output file: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "my-agent") {
		t.Errorf("output %q does not contain title 'my-agent'", got)
	}
	if !strings.Contains(got, "Needs permission") {
		t.Errorf("output %q does not contain message 'Needs permission'", got)
	}
}

// TestHookNotify_QuotesValues verifies that multi-word title and message values
// are delivered intact to the notify command. This is the regression test for
// the shell-splitting bug: without quoting the shell treats each word in the
// title/message as a separate argument, causing the command to receive garbled
// input (e.g. alerter exits 64 on unexpected args).
func TestHookNotify_QuotesValues(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")
	outFile := filepath.Join(cfgDir, "notify-quoted.txt")

	// Template uses bare {{title}} and {{message}}; shellquote.Quote provides the quotes.
	cfg := &config.Config{
		NotifyCmd: config.StringPtr("printf '%s\\n%s' {{title}} {{message}} > " + outFile),
		Agents:    map[string]config.AgentConfig{},
	}
	config.SetGlobalConfigPathForTest(cfgPath)
	config.SetConfigPathForTest(cfgPath)
	config.Save(cfg) //nolint:errcheck
	defer func() { config.SetGlobalConfigPathForTest(""); config.SetConfigPathForTest("") }()

	const (
		wantTitle   = "Needs permission"
		wantMessage = "Idle — waiting for input"
	)
	hookNotify(notify.Options{Title: wantTitle, Message: wantMessage})

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read notify output file: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, wantTitle) {
		t.Errorf("output %q does not contain multi-word title %q", got, wantTitle)
	}
	if !strings.Contains(got, wantMessage) {
		t.Errorf("output %q does not contain multi-word message %q", got, wantMessage)
	}
}

// TestHookNotify_EnvVarsAvailable verifies that AGENTCTL_AGENT_NAME and
// AGENTCTL_NOTIFICATION_TYPE from the env map are available as shell variables
// inside the notify_cmd process.
func TestHookNotify_EnvVarsAvailable(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")
	outFile := filepath.Join(cfgDir, "env-notify-output.txt")

	cfg := &config.Config{
		NotifyCmd: config.StringPtr("printf '%s %s' $AGENTCTL_AGENT_NAME $AGENTCTL_NOTIFICATION_TYPE > " + outFile),
		Agents:    map[string]config.AgentConfig{},
	}
	config.SetGlobalConfigPathForTest(cfgPath)
	config.SetConfigPathForTest(cfgPath)
	config.Save(cfg) //nolint:errcheck
	defer func() { config.SetGlobalConfigPathForTest(""); config.SetConfigPathForTest("") }()

	hookNotify(notify.Options{
		Title:   "test-agent",
		Message: "some message",
		Env: map[string]string{
			"AGENTCTL_AGENT_NAME":        "test-agent",
			"AGENTCTL_NOTIFICATION_TYPE": "permission_prompt",
		},
	})

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read notify output file: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "test-agent") {
		t.Errorf("output %q does not contain AGENTCTL_AGENT_NAME 'test-agent'", got)
	}
	if !strings.Contains(got, "permission_prompt") {
		t.Errorf("output %q does not contain AGENTCTL_NOTIFICATION_TYPE 'permission_prompt'", got)
	}
}

// TestHookNotify_AgentTypePlaceholders verifies that {{agent}} and {{type}}
// placeholders are substituted with the corresponding env map values.
func TestHookNotify_AgentTypePlaceholders(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")
	outFile := filepath.Join(cfgDir, "placeholder-output.txt")

	cfg := &config.Config{
		NotifyCmd: config.StringPtr("printf '%s %s' {{agent}} {{type}} > " + outFile),
		Agents:    map[string]config.AgentConfig{},
	}
	config.SetGlobalConfigPathForTest(cfgPath)
	config.SetConfigPathForTest(cfgPath)
	config.Save(cfg) //nolint:errcheck
	defer func() { config.SetGlobalConfigPathForTest(""); config.SetConfigPathForTest("") }()

	hookNotify(notify.Options{
		Title:   "title-val",
		Message: "msg-val",
		Env: map[string]string{
			"AGENTCTL_AGENT_NAME":        "go-engineer",
			"AGENTCTL_NOTIFICATION_TYPE": "idle_prompt",
		},
	})

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read notify output file: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "go-engineer") {
		t.Errorf("output %q does not contain {{agent}} substitution 'go-engineer'", got)
	}
	if !strings.Contains(got, "idle_prompt") {
		t.Errorf("output %q does not contain {{type}} substitution 'idle_prompt'", got)
	}
}

// ---------------------------------------------------------------------------
// checkGlobalDenyList
// ---------------------------------------------------------------------------

// TestCheckGlobalDenyList exercises every pattern in globalDenyPatterns.
// Command strings are built programmatically so that this source file does not
// contain literal dangerous patterns that could be caught by hook scanners.
func TestCheckGlobalDenyList(t *testing.T) {
	slash := "/"
	tilde := "~"
	mainBranch := "main"

	tests := []struct {
		name     string
		command  string
		wantDeny bool
	}{
		// --- rm root ---
		{
			name:     "rm_root_bare",
			command:  "rm " + slash,
			wantDeny: true,
		},
		{
			name:     "rm_rf_root",
			command:  "rm -rf " + slash,
			wantDeny: true,
		},
		{
			name:     "rm_f_root",
			command:  "rm -f " + slash,
			wantDeny: true,
		},
		// Safe rm variants that happen to reference "/"
		{
			name:     "rm_tmp_file",
			command:  "rm /tmp/build/artifact",
			wantDeny: false,
		},
		{
			name:     "rm_rf_tmp_dir",
			command:  "rm -rf /tmp/build",
			wantDeny: false,
		},

		// --- rm home directory ---
		{
			name:     "rm_tilde_bare",
			command:  "rm " + tilde,
			wantDeny: true,
		},
		{
			name:     "rm_tilde_slash",
			command:  "rm " + tilde + slash,
			wantDeny: true,
		},
		{
			name:     "rm_rf_tilde",
			command:  "rm -rf " + tilde,
			wantDeny: true,
		},
		{
			name:     "rm_rf_tilde_slash",
			command:  "rm -rf " + tilde + slash,
			wantDeny: true,
		},
		// Safe: rm a file inside home
		{
			name:     "rm_home_file",
			command:  "rm ~/documents/report.pdf",
			wantDeny: false,
		},
		{
			name:     "rm_rf_home_subdir",
			command:  "rm -rf ~/tmp/scratch",
			wantDeny: false,
		},

		// --- git force push to main ---
		{
			name:     "git_force_push_main",
			command:  "git push --force origin " + mainBranch,
			wantDeny: true,
		},
		// Blocked: any push to main (use a PR instead)
		{
			name:     "git_push_main_no_force",
			command:  "git push origin " + mainBranch,
			wantDeny: true,
		},
		// Safe: force push to a different branch
		{
			name:     "git_force_push_feature",
			command:  "git push --force origin feature/my-branch",
			wantDeny: false,
		},
		// Blocked: force-with-lease to main is still a push to main
		{
			name:     "git_force_with_lease_main",
			command:  "git push --force-with-lease origin " + mainBranch,
			wantDeny: true,
		},

		// --- git push to main (non-force) ---
		{
			name:     "git_push_main",
			command:  "git push origin " + mainBranch,
			wantDeny: true,
		},
		// Safe: push to a feature branch
		{
			name:     "git_push_feature",
			command:  "git push origin feature/my-branch",
			wantDeny: false,
		},
		// Safe: push with -u to feature branch
		{
			name:     "git_push_u_feature",
			command:  "git push -u origin feature/my-branch",
			wantDeny: false,
		},

		// --- curl piped to interpreter ---
		{
			name:     "curl_pipe_sh",
			command:  "curl https://install.example.com | sh",
			wantDeny: true,
		},
		{
			name:     "curl_pipe_bash",
			command:  "curl https://install.example.com | bash",
			wantDeny: true,
		},
		{
			name:     "curl_pipe_bash_spaced",
			command:  "curl -fsSL https://install.example.com |  bash",
			wantDeny: true,
		},
		// Safe: curl piped to something else
		{
			name:     "curl_pipe_grep",
			command:  "curl https://api.example.com | grep status",
			wantDeny: false,
		},
		{
			name:     "curl_pipe_jq",
			command:  "curl -s https://api.example.com | jq .",
			wantDeny: false,
		},

		// --- agentctl via go run (use 'agentctl' from PATH instead) ---
		{
			name:     "go_run_dot_agentctl_dir",
			command:  "cd ~/src/agentctl && go run . status",
			wantDeny: true,
		},
		{
			name:     "go_run_main_agentctl_dir",
			command:  "cd ~/src/agentctl && go run main.go dispatch --issue 42",
			wantDeny: false, // pattern matches path containing "agentctl", not main.go
		},
		{
			name:     "go_run_agentctl_explicit",
			command:  "go run ~/src/agentctl status",
			wantDeny: true,
		},
		// Safe: go run for non-agentctl code
		{
			name:     "go_run_other_pkg",
			command:  "go run ./cmd/mytool",
			wantDeny: false,
		},
		{
			name:     "go_run_dot_other_dir",
			command:  "cd ~/src/myapp && go run .",
			wantDeny: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := checkGlobalDenyList(tt.command)
			if tt.wantDeny && reason == "" {
				t.Errorf("expected deny for command %q, got empty reason", tt.command)
			}
			if !tt.wantDeny && reason != "" {
				t.Errorf("expected allow for command %q, got reason %q", tt.command, reason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ShellPrefixScriptContent
// ---------------------------------------------------------------------------

func TestShellPrefixScript_UsesEnvVar(t *testing.T) {
	content := ShellPrefixScriptContent("")

	// The script must use the AGENTCTL_BIN env var instead of a baked-in path.
	if !strings.Contains(content, "${AGENTCTL_BIN:-agentctl}") {
		t.Errorf("script does not contain AGENTCTL_BIN env var fallback\nscript:\n%s", content)
	}

	// The script must use $_agentctl for invocations (e.g. by shell-hooks.d
	// scripts sourced into it).
	if !strings.Contains(content, "$_agentctl") {
		t.Errorf("script does not contain $_agentctl invocation\nscript:\n%s", content)
	}

	// Regression guard: the audit sidecar was removed (audit is now written
	// from the PostToolUse/PostToolUseFailure Go hook handlers via
	// internal/audit, not forked pre-eval from this script). See
	// internal/hooks/tools.go and internal/audit.
	if strings.Contains(content, "audit-cmd") {
		t.Errorf("script must not contain 'audit-cmd' -- the sidecar audit writer was removed\nscript:\n%s", content)
	}
}

func TestShellPrefixScript_NoExec(t *testing.T) {
	content := ShellPrefixScriptContent("")

	// The script must NOT use exec "$@" -- it needs to capture exit code for post-command signaling.
	if strings.Contains(content, `exec "$@"`) {
		t.Errorf("script contains 'exec \"$@\"' but should use plain \"$@\" with exit code capture\nscript:\n%s", content)
	}
}

func TestShellPrefixScript_ExitCodePropagation(t *testing.T) {
	content := ShellPrefixScriptContent("")

	// State transitions are now driven by PreToolUse/PostToolUse hooks in the
	// Claude Code settings file, not by shell-prefix signal calls. Verify the
	// dead signal blocks are absent.
	if strings.Contains(content, "signal start") {
		t.Errorf("script should not contain 'signal start' (state is driven by hooks now)\nscript:\n%s", content)
	}
	if strings.Contains(content, "signal state idle") {
		t.Errorf("script should not contain 'signal state idle' (state is driven by hooks now)\nscript:\n%s", content)
	}

	// Verify exit code is captured and propagated.
	if !strings.Contains(content, "_agentctl_rc=$?") {
		t.Errorf("script does not capture exit code into _agentctl_rc\nscript:\n%s", content)
	}
	if !strings.Contains(content, "exit $_agentctl_rc") {
		t.Errorf("script does not exit with captured exit code\nscript:\n%s", content)
	}
}

func TestShellPrefixScriptContent_IsValidBash(t *testing.T) {
	content := ShellPrefixScriptContent("")

	// Write to a temp file and validate with bash -n (syntax check only).
	tmpFile, err := os.CreateTemp("", "shell-prefix-*.sh")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	_ = tmpFile.Close()

	out, err := exec.Command("bash", "-n", tmpFile.Name()).CombinedOutput()
	if err != nil {
		t.Errorf("bash -n syntax check failed: %v\n%s", err, out)
	}
}

// ---------------------------------------------------------------------------
// Hook-sourced audit trail (internal/audit) — replaces the deleted AuditCmd
// shell sidecar. AuditCmd fired pre-eval from shell-prefix.sh, covered Bash
// only, and recorded no outcome; PostTool/PostToolFailure now append a
// record for EVERY tool call (Read, Edit, Write, WebFetch, Agent, ... not
// just Bash) with an actual success/failure outcome. See internal/audit.
// ---------------------------------------------------------------------------

// setupAuditRecordTest points paths at a fresh temp runtime dir and isolates
// windowstate, for the duration of a test that exercises PostTool/
// PostToolFailure's audit.Append side effect.
func setupAuditRecordTest(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	// AGENTCTL_RUNTIME_DIR is inherited from any agentctl-managed tmux window
	// (agentctl exports it into every window it creates) and paths.Init
	// prefers it over tmpDir, so it must be pinned here or this test silently
	// writes into the real project's runtime dir.
	paths.ResetForTest()
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(tmpDir)
	t.Cleanup(paths.ResetForTest)
	setupWindowstate(t)
}

// TestPostTool_WritesAuditRecord verifies that PostTool appends an audit
// record built from a real captured PostToolUse payload, with tool,
// tool_use_id, duration_ms, ok=true, and a non-empty summary.
func TestPostTool_WritesAuditRecord(t *testing.T) {
	setupAuditRecordTest(t)

	const sessionID = "e09aef10-test-session"
	envVars := map[string]string{
		"AGENTCTL":               "1",
		"AGENTCTL_WINDOW_ID":     "@9",
		"AGENTCTL_AGENT_NAME":    "go-engineer",
		"AGENTCTL_INSTANCE_NAME": "go-engineer",
		"TMUX_PANE":              "",
		"AGENTCTL_SESSION_ID":    "",
	}
	// Verbatim shape of a real captured PostToolUse payload (see spec).
	payload := `{"session_id":"` + sessionID + `","transcript_path":"/Users/x/.claude/projects/p/` + sessionID + `.jsonl","cwd":"/private/tmp/hookprobe","prompt_id":"ae0de94f-1","permission_mode":"default","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"echo one","description":"Echo one"},"tool_response":{"stdout":"one","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false},"tool_use_id":"toolu_01SCyRgMD9zxPqApvdUvgjXP","duration_ms":1173}`

	runHookCaptured(t, envVars, payload, PostTool)

	records, err := audit.Query(audit.Filter{Session: sessionID})
	if err != nil {
		t.Fatalf("audit.Query: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("audit.Query returned %d records, want 1", len(records))
	}
	r := records[0]
	if r.Event != audit.EventTool {
		t.Errorf("Event = %q, want %q", r.Event, audit.EventTool)
	}
	if r.Tool != "Bash" {
		t.Errorf("Tool = %q, want %q", r.Tool, "Bash")
	}
	if r.ToolUseID != "toolu_01SCyRgMD9zxPqApvdUvgjXP" {
		t.Errorf("ToolUseID = %q, want %q", r.ToolUseID, "toolu_01SCyRgMD9zxPqApvdUvgjXP")
	}
	if r.DurationMS != 1173 {
		t.Errorf("DurationMS = %d, want %d", r.DurationMS, 1173)
	}
	if !r.OK {
		t.Error("OK = false, want true")
	}
	if r.Summary == "" {
		t.Error("Summary is empty, want a non-empty summary")
	}
	if r.Window != "@9" {
		t.Errorf("Window = %q, want %q", r.Window, "@9")
	}
}

// TestPostToolFailure_WritesAuditRecord verifies that PostToolFailure
// appends an audit record with ok=false and the error populated.
func TestPostToolFailure_WritesAuditRecord(t *testing.T) {
	setupAuditRecordTest(t)

	const sessionID = "sess-fail-1"
	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_WINDOW_ID":  "@3",
		"AGENTCTL_AGENT_NAME": "go-engineer",
		"TMUX_PANE":           "",
		"AGENTCTL_SESSION_ID": "",
	}
	payload := `{"tool_name":"Bash","error":"exit status 1","is_interrupt":false,"session_id":"` + sessionID + `","cwd":"/tmp","permission_mode":"default"}`

	runHookCaptured(t, envVars, payload, PostToolFailure)

	records, err := audit.Query(audit.Filter{Session: sessionID})
	if err != nil {
		t.Fatalf("audit.Query: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("audit.Query returned %d records, want 1", len(records))
	}
	r := records[0]
	if r.Event != audit.EventToolFailure {
		t.Errorf("Event = %q, want %q", r.Event, audit.EventToolFailure)
	}
	if r.OK {
		t.Error("OK = true, want false")
	}
	if r.Error == "" {
		t.Error("Error is empty, want the failure's error populated")
	}
}

// TestPostTool_AuditUsesResolvedWindowNotPoisonedEnv verifies that the audit
// record's Window field comes from resolveHookWindowID() (session-ID ->
// windowstate lookup), not from a poisoned AGENTCTL_WINDOW_ID env var. tmux
// sets that env var session-globally (see internal/tmux), so every window in
// a session can inherit the PREVIOUSLY created window's ID; only the
// session-ID lookup is trustworthy.
func TestPostTool_AuditUsesResolvedWindowNotPoisonedEnv(t *testing.T) {
	setupAuditRecordTest(t)

	const sessionID = "sess-resolve-1"
	const correctWindow = "@42"
	const poisonedWindow = "@1" // stale value AGENTCTL_WINDOW_ID would carry

	if err := windowstate.Write(correctWindow, windowstate.WindowState{SessionID: sessionID}); err != nil {
		t.Fatalf("windowstate.Write: %v", err)
	}

	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_SESSION_ID": sessionID,
		"AGENTCTL_WINDOW_ID":  poisonedWindow, // must NOT end up in the record
		"TMUX_PANE":           "",             // tmux unavailable -- no TMUX_PANE fallback
		"AGENTCTL_AGENT_NAME": "go-engineer",
	}
	payload := `{"session_id":"` + sessionID + `","tool_name":"Read","tool_use_id":"toolu_x","duration_ms":5}`

	runHookCaptured(t, envVars, payload, PostTool)

	records, err := audit.Query(audit.Filter{Session: sessionID})
	if err != nil {
		t.Fatalf("audit.Query: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("audit.Query returned %d records, want 1", len(records))
	}
	if got := records[0].Window; got != correctWindow {
		t.Errorf("Window = %q, want resolved window %q (not poisoned env %q)", got, correctWindow, poisonedWindow)
	}
}

// ---------------------------------------------------------------------------
// Audit trail project attribution
// ---------------------------------------------------------------------------

// TestAuditProjectAttribution_PopulatedFromInitializedPaths verifies
// auditProjectAttribution resolves Project/RepoRoot from paths.DirName()/
// paths.ProjectDir() when paths has been initialized normally (no worktree
// involved).
func TestAuditProjectAttribution_PopulatedFromInitializedPaths(t *testing.T) {
	tmpDir := t.TempDir()
	paths.ResetForTest()
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(tmpDir)
	t.Cleanup(paths.ResetForTest)

	project, repoRoot := auditProjectAttribution()
	if want := filepath.Base(tmpDir); project != want {
		t.Errorf("project = %q, want %q", project, want)
	}
	if repoRoot != tmpDir {
		t.Errorf("repoRoot = %q, want %q", repoRoot, tmpDir)
	}
}

// TestAuditProjectAttribution_EmptyWhenPathsUninitialized verifies that both
// return values are the empty string -- never a placeholder -- when paths.Init
// has not been called. audit.Record's omitempty tags depend on this.
func TestAuditProjectAttribution_EmptyWhenPathsUninitialized(t *testing.T) {
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)

	project, repoRoot := auditProjectAttribution()
	if project != "" {
		t.Errorf("project = %q, want empty", project)
	}
	if repoRoot != "" {
		t.Errorf("repoRoot = %q, want empty", repoRoot)
	}
}

// TestPostTool_AuditRecordsProjectAttribution verifies a real PostTool audit
// record carries Project/RepoRoot resolved from paths, in the plain
// (non-worktree) case.
func TestPostTool_AuditRecordsProjectAttribution(t *testing.T) {
	setupAuditRecordTest(t)

	const sessionID = "sess-project-attr"
	envVars := map[string]string{
		"AGENTCTL":               "1",
		"AGENTCTL_WINDOW_ID":     "@60",
		"AGENTCTL_AGENT_NAME":    "go-engineer",
		"AGENTCTL_INSTANCE_NAME": "go-engineer",
		"TMUX_PANE":              "",
		"AGENTCTL_SESSION_ID":    "",
	}
	payload := `{"session_id":"` + sessionID + `","tool_name":"Bash","tool_response":{"stdout":"ok"},"tool_use_id":"toolu_pa1","duration_ms":5}`

	runHookCaptured(t, envVars, payload, PostTool)

	records, err := audit.Query(audit.Filter{Session: sessionID})
	if err != nil {
		t.Fatalf("audit.Query: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("audit.Query returned %d records, want 1", len(records))
	}
	r := records[0]
	wantProject := filepath.Base(paths.ProjectDir())
	if r.Project != wantProject {
		t.Errorf("Project = %q, want %q", r.Project, wantProject)
	}
	if r.RepoRoot != paths.ProjectDir() {
		t.Errorf("RepoRoot = %q, want %q", r.RepoRoot, paths.ProjectDir())
	}
}

// TestAppendAuditRecord_WorktreeDispatchAttributesToParentProject is the
// regression guard for the crux of project attribution: a tool call made from a
// worktree-dispatched agent must attribute to the PARENT project, not the
// worktree it's physically running in. A dispatched worktree agent's
// AGENTCTL_RUNTIME_DIR is pinned to the parent project's .agentctl (see
// internal/dispatch.buildDispatchEnv; paths.Init honors it verbatim), while its process
// cwd -- and the "cwd" field on every hook payload -- is the worktree. This
// test deliberately asserts the payload's cwd IS the worktree while the
// resulting record's Project/RepoRoot are the PARENT project, so it fails if
// project attribution is ever reimplemented off cwd instead of paths.
func TestAppendAuditRecord_WorktreeDispatchAttributesToParentProject(t *testing.T) {
	setupWindowstate(t)

	root := t.TempDir()
	projectRoot := filepath.Join(root, "myproject")
	rtDir := filepath.Join(projectRoot, ".agentctl")
	worktree := filepath.Join(projectRoot, ".worktrees", "agent-go-engineer-issue-42")
	for _, d := range []string{rtDir, worktree} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}

	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	// A worktree-dispatched agent inherits AGENTCTL_RUNTIME_DIR pointing at
	// the PARENT project's runtime dir; its own baseDir (cwd) is the
	// worktree. paths.Init calls initRuntimeDir with requireOwnership=false,
	// so a set AGENTCTL_RUNTIME_DIR is taken verbatim and ownsDir is never
	// reached; this test guards that verbatim-env mechanism, not ownsDir.
	t.Setenv("AGENTCTL_RUNTIME_DIR", rtDir)
	paths.Init(worktree)
	if got := paths.ProjectDir(); got != projectRoot {
		t.Fatalf("test setup broken: paths.ProjectDir() = %q, want %q", got, projectRoot)
	}

	const sessionID = "sess-worktree-dispatch"
	envVars := map[string]string{
		"AGENTCTL":               "1",
		"AGENTCTL_WINDOW_ID":     "@77",
		"AGENTCTL_AGENT_NAME":    "go-engineer",
		"AGENTCTL_INSTANCE_NAME": "go-engineer",
		"TMUX_PANE":              "",
		"AGENTCTL_SESSION_ID":    "",
	}
	// The "cwd" field is exactly what a real PostToolUse payload reports for
	// a worktree-dispatched agent's tool call: the worktree, not the parent.
	payload := `{"session_id":"` + sessionID + `","cwd":"` + worktree + `","tool_name":"Bash","tool_response":{"stdout":"ok"},"tool_use_id":"toolu_wt1","duration_ms":10}`

	runHookCaptured(t, envVars, payload, PostTool)

	records, err := audit.Query(audit.Filter{Session: sessionID})
	if err != nil {
		t.Fatalf("audit.Query: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("audit.Query returned %d records, want 1", len(records))
	}
	r := records[0]
	if r.Cwd != worktree {
		t.Fatalf("test setup broken: record Cwd = %q, want worktree %q", r.Cwd, worktree)
	}
	if r.Project != "myproject" {
		t.Errorf("Project = %q, want %q (the PARENT project, not the worktree)", r.Project, "myproject")
	}
	if r.RepoRoot != projectRoot {
		t.Errorf("RepoRoot = %q, want %q (the PARENT project root, not the worktree)", r.RepoRoot, projectRoot)
	}
}

// ---------------------------------------------------------------------------
// checkAgentDenyList
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// checkProjectDenyList
// ---------------------------------------------------------------------------

func TestCheckProjectDenyList_BlockedCommand(t *testing.T) {
	projectCfg := &config.ProjectConfig{
		BlockedCommands: []string{"kubectl delete"},
	}
	reason := checkProjectDenyList("kubectl delete pod my-pod --namespace production", "any-agent", projectCfg)
	if reason == "" {
		t.Error("expected deny for command containing project blocked substring, got empty reason")
	}
	if !strings.Contains(reason, "project config") {
		t.Errorf("reason %q should mention project config", reason)
	}
}

func TestCheckProjectDenyList_BlockedPattern(t *testing.T) {
	projectCfg := &config.ProjectConfig{
		BlockedPatterns: []string{`git\s+push\s+.*--force`},
	}
	reason := checkProjectDenyList("git push --force origin my-branch", "any-agent", projectCfg)
	if reason == "" {
		t.Error("expected deny for command matching project blocked pattern, got empty reason")
	}
}

func TestCheckProjectDenyList_ProtectedBranch(t *testing.T) {
	projectCfg := &config.ProjectConfig{
		ProtectedBranches: []string{"production"},
	}
	reason := checkProjectDenyList("git push origin production", "any-agent", projectCfg)
	if reason == "" {
		t.Error("expected deny for push to protected branch, got empty reason")
	}
}

func TestCheckProjectDenyList_ProtectedBranch_SafePush(t *testing.T) {
	projectCfg := &config.ProjectConfig{
		ProtectedBranches: []string{"production"},
	}
	reason := checkProjectDenyList("git push origin feature/my-branch", "any-agent", projectCfg)
	if reason != "" {
		t.Errorf("expected allow for push to non-protected branch, got reason %q", reason)
	}
}

func TestCheckProjectDenyList_AllowedCommand(t *testing.T) {
	projectCfg := &config.ProjectConfig{
		BlockedCommands: []string{"kubectl delete"},
		BlockedPatterns: []string{`git\s+push\s+--force`},
	}
	reason := checkProjectDenyList("go test ./...", "any-agent", projectCfg)
	if reason != "" {
		t.Errorf("expected allow for safe command, got reason %q", reason)
	}
}

func TestCheckProjectDenyList_PerAgentConfig(t *testing.T) {
	projectCfg := &config.ProjectConfig{
		Agents: map[string]config.AgentConfig{
			"go-engineer": {
				BlockedCommands: []string{"go install"},
			},
		},
	}

	t.Run("blocked for matching agent", func(t *testing.T) {
		reason := checkProjectDenyList("go install some/tool", "go-engineer", projectCfg)
		if reason == "" {
			t.Error("expected deny for agent-specific blocked command, got empty reason")
		}
	})

	t.Run("allowed for different agent", func(t *testing.T) {
		reason := checkProjectDenyList("go install some/tool", "other-agent", projectCfg)
		if reason != "" {
			t.Errorf("expected allow for different agent, got reason %q", reason)
		}
	})

	t.Run("allowed for empty agent name", func(t *testing.T) {
		reason := checkProjectDenyList("go install some/tool", "", projectCfg)
		if reason != "" {
			t.Errorf("expected allow when agent name is empty, got reason %q", reason)
		}
	})
}

func TestCheckProjectDenyList_NilProjectConfig(t *testing.T) {
	// Verify the hook flow is safe when no project config is present.
	// This tests that ValidateBash nil-checks before calling.
	// checkProjectDenyList itself is not called with nil -- this test documents
	// the expected usage: only call if projectCfg != nil.
	// So we just verify an empty ProjectConfig allows everything.
	projectCfg := &config.ProjectConfig{}
	reason := checkProjectDenyList("git push origin main", "go-engineer", projectCfg)
	if reason != "" {
		t.Errorf("empty project config should allow all commands, got reason %q", reason)
	}
}

func TestCheckProjectDenyList_BadPattern(t *testing.T) {
	projectCfg := &config.ProjectConfig{
		// Invalid regex -- opening bracket is unmatched.
		BlockedPatterns: []string{"[invalid-regex"},
	}
	// Must not panic; bad pattern should be skipped.
	reason := checkProjectDenyList("anything", "any-agent", projectCfg)
	if reason != "" {
		t.Errorf("bad regex pattern should be skipped, got reason %q", reason)
	}
}

// TestCheckProjectDenyList_UserAllowsProjectBlocks verifies that project config
// is checked independently -- a command not blocked by user config is still
// blocked by project config. (Project config is additive; the deny is in
// addition to user-level checks, not instead of them.)
func TestCheckProjectDenyList_UserAllowsProjectBlocks(t *testing.T) {
	// User-level config: empty (allows everything)
	userAC := config.AgentConfig{}
	userReason := checkAgentDenyList("go install some/tool", userAC)
	if userReason != "" {
		t.Fatalf("user config should allow command, got %q", userReason)
	}

	// Project config: blocks "go install"
	projectCfg := &config.ProjectConfig{
		BlockedCommands: []string{"go install"},
	}
	projectReason := checkProjectDenyList("go install some/tool", "go-engineer", projectCfg)
	if projectReason == "" {
		t.Error("project config should block command even though user config allows it")
	}
}

func TestCheckAgentDenyList_BlockedCommand(t *testing.T) {
	ac := config.AgentConfig{
		BlockedCommands: []string{"kubectl delete"},
	}
	reason := checkAgentDenyList("kubectl delete pod my-pod --namespace production", ac)
	if reason == "" {
		t.Error("expected deny for command containing blocked substring, got empty reason")
	}
}

func TestCheckAgentDenyList_BlockedPattern(t *testing.T) {
	ac := config.AgentConfig{
		// Regex that matches any git push with --force flag.
		BlockedPatterns: []string{`git\s+push\s+.*--force`},
	}
	reason := checkAgentDenyList("git push --force origin my-branch", ac)
	if reason == "" {
		t.Error("expected deny for command matching blocked pattern, got empty reason")
	}
}

func TestCheckAgentDenyList_AllowedCommand(t *testing.T) {
	ac := config.AgentConfig{
		BlockedCommands: []string{"kubectl delete"},
		BlockedPatterns: []string{`git\s+push\s+--force`},
	}
	reason := checkAgentDenyList("go test ./...", ac)
	if reason != "" {
		t.Errorf("expected allow for safe command, got reason %q", reason)
	}
}

func TestCheckAgentDenyList_BadPattern(t *testing.T) {
	ac := config.AgentConfig{
		// This is not a valid regex -- the opening bracket is unmatched.
		BlockedPatterns: []string{"[invalid-regex"},
	}
	// Must not panic; the bad pattern should be silently skipped.
	reason := checkAgentDenyList("anything goes here", ac)
	if reason != "" {
		t.Errorf("bad regex pattern should be skipped, got reason %q", reason)
	}
}

// ---------------------------------------------------------------------------
// Output format tests
// ---------------------------------------------------------------------------

// captureStdout runs fn with os.Stdout replaced by a pipe and returns whatever
// was written to stdout during the call.
func captureStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("captureStdout: create pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	_ = w.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("captureStdout: read pipe: %v", err)
	}
	return buf.Bytes()
}

// captureDenyEmitter runs fn (expected to be denyCommand, denyBashPopup,
// or something that calls into ValidateBash's deny path) with os.Stdout and
// os.Stderr replaced by pipes, and exitFunc stubbed to a recorder instead of
// the real os.Exit -- otherwise a production deny call would kill the test
// binary. It returns the captured stdout, the captured stderr, and every
// exit code exitFunc was invoked with (recording all calls, not just
// whether one happened, so a caller that fires exitFunc more than once is
// caught too).
func captureDenyEmitter(t *testing.T, fn func()) (stdout, stderr []byte, exitCodes []int) {
	t.Helper()

	origExit := exitFunc
	exitFunc = func(code int) { exitCodes = append(exitCodes, code) }
	t.Cleanup(func() { exitFunc = origExit })

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("captureDenyEmitter: create stdout pipe: %v", err)
	}
	oldOut := os.Stdout
	os.Stdout = outW
	defer func() { os.Stdout = oldOut }()

	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("captureDenyEmitter: create stderr pipe: %v", err)
	}
	oldErr := os.Stderr
	os.Stderr = errW
	defer func() { os.Stderr = oldErr }()

	fn()

	_ = outW.Close()
	_ = errW.Close()

	var outBuf, errBuf bytes.Buffer
	if _, err := outBuf.ReadFrom(outR); err != nil {
		t.Fatalf("captureDenyEmitter: read stdout pipe: %v", err)
	}
	if _, err := errBuf.ReadFrom(errR); err != nil {
		t.Fatalf("captureDenyEmitter: read stderr pipe: %v", err)
	}
	return outBuf.Bytes(), errBuf.Bytes(), exitCodes
}

// TestAutoAcceptOutput verifies the JSON Claude Code expects for an auto-accept
// permission decision: hookSpecificOutput.permissionDecision == "allow".
func TestAutoAcceptOutput(t *testing.T) {
	raw := captureStdout(t, func() {
		output := map[string]interface{}{
			"hookSpecificOutput": map[string]string{
				"permissionDecision": "allow",
			},
		}
		json.NewEncoder(os.Stdout).Encode(output) //nolint:errcheck
	})

	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal auto-accept output: %v\nraw: %s", err, raw)
	}

	hso, ok := got["hookSpecificOutput"].(map[string]interface{})
	if !ok {
		t.Fatalf("hookSpecificOutput missing or wrong type in: %s", raw)
	}
	if got := hso["permissionDecision"]; got != "allow" {
		t.Errorf("permissionDecision = %q, want %q", got, "allow")
	}
}

// TestDenyOutput verifies the JSON Claude Code expects for a deny decision:
// hookSpecificOutput.permissionDecision == "deny" with a non-empty reason that
// includes the original reason and the blocked command. Also verifies that
// hookEventName is present and set to "PreToolUse" -- required as of Claude
// Code 2.1.231, or Claude Code shows "hook error" instead of the deny reason.
func TestDenyOutput(t *testing.T) {
	const (
		baseReason = "\U0001F6D1 BLOCKED: recursive delete of root -- are you trying to nuke the machine?"
		command    = "rm -rf /"
	)

	raw, _, _ := captureDenyEmitter(t, func() {
		denyCommand("test-agent", command, baseReason, denyAuditContext{})
	})

	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal deny output: %v\nraw: %s", err, raw)
	}

	hso, ok := got["hookSpecificOutput"].(map[string]interface{})
	if !ok {
		t.Fatalf("hookSpecificOutput missing or wrong type in: %s", raw)
	}
	if decision := hso["permissionDecision"]; decision != "deny" {
		t.Errorf("permissionDecision = %q, want %q", decision, "deny")
	}
	reason, _ := hso["permissionDecisionReason"].(string)
	if !strings.Contains(reason, baseReason) {
		t.Errorf("permissionDecisionReason %q does not contain base reason %q", reason, baseReason)
	}
	if !strings.Contains(reason, command) {
		t.Errorf("permissionDecisionReason %q does not contain blocked command %q", reason, command)
	}
	// hookEventName must be present and set to "PreToolUse" -- required as of
	// Claude Code 2.1.231, or Claude Code shows "hook error" instead of the
	// deny reason.
	if got := hso["hookEventName"]; got != "PreToolUse" {
		t.Errorf("hookEventName = %v, want %q", got, "PreToolUse")
	}
}

// denyEmitterCase is the shared table-test fixture for both PreToolUse deny
// emitters -- used by TestDenyEmitters_HookSpecificOutputSchema and
// TestDenyEmitters_ExitEnforcedRegardlessOfStdoutSchema below.
type denyEmitterCase struct {
	name       string
	emit       func()
	wantStderr string // substring the plain-text stderr reason must contain
}

func denyEmitterCases() []denyEmitterCase {
	return []denyEmitterCase{
		{
			name:       "denyCommand",
			emit:       func() { denyCommand("test-agent", "rm -rf /", "blocked", denyAuditContext{}) },
			wantStderr: "blocked",
		},
		{
			name:       "denyBashPopup_dispatch",
			emit:       func() { denyBashPopup("test-agent", "agentctl dispatch --issue 1", true, denyAuditContext{}) },
			wantStderr: "Dispatch denied in preview popup",
		},
		{
			name:       "denyBashPopup_plain",
			emit:       func() { denyBashPopup("test-agent", "rm -rf /tmp/scratch", false, denyAuditContext{}) },
			wantStderr: "Command denied in preview popup",
		},
	}
}

// TestDenyEmitters_HookSpecificOutputSchema is a regression guard: it exercises
// both PreToolUse deny emitters (denyCommand and denyBashPopup) and asserts
// the full hookSpecificOutput schema Claude Code 2.1.231+ requires -- all three
// keys present with the right values, and the emitted bytes decode as exactly
// one valid JSON object. This must catch any future regression that drops
// hookEventName or otherwise breaks the shape of the deny payload.
//
// It also asserts the enforcement side of the contract added alongside this
// test: exit(2) must be requested exactly once, stderr must be non-empty and
// contain the plain-text reason, and stderr must never carry JSON (Claude
// Code uses stderr verbatim as the blocking reason -- a JSON blob there would
// just be noise, and the two deny emitters must not regress into printing the
// same hookSpecificOutput payload to both streams).
func TestDenyEmitters_HookSpecificOutputSchema(t *testing.T) {
	for _, tt := range denyEmitterCases() {
		t.Run(tt.name, func(t *testing.T) {
			raw, errRaw, exitCodes := captureDenyEmitter(t, tt.emit)

			// Must decode as exactly one valid JSON object -- a stray extra
			// token (e.g. leftover debug output) would break Claude Code's
			// parser just as badly as a missing field.
			decoder := json.NewDecoder(bytes.NewReader(raw))
			var got map[string]interface{}
			if err := decoder.Decode(&got); err != nil {
				t.Fatalf("decode: %v\nraw: %s", err, raw)
			}
			if decoder.More() {
				t.Fatalf("output contains more than one JSON value: %s", raw)
			}

			hso, ok := got["hookSpecificOutput"].(map[string]interface{})
			if !ok {
				t.Fatalf("hookSpecificOutput missing or wrong type in: %s", raw)
			}

			if v := hso["hookEventName"]; v != "PreToolUse" {
				t.Errorf("hookEventName = %v, want %q", v, "PreToolUse")
			}
			if v := hso["permissionDecision"]; v != "deny" {
				t.Errorf("permissionDecision = %v, want %q", v, "deny")
			}
			if v, _ := hso["permissionDecisionReason"].(string); v == "" {
				t.Errorf("permissionDecisionReason must be non-empty")
			}

			// Enforcement: exit(2) requested exactly once.
			if len(exitCodes) != 1 || exitCodes[0] != 2 {
				t.Errorf("exitCodes = %v, want exactly one call with code 2", exitCodes)
			}

			// Enforcement: stderr carries the plain-text reason, never JSON.
			if len(errRaw) == 0 {
				t.Fatalf("stderr is empty, want the plain-text deny reason")
			}
			if !bytes.Contains(errRaw, []byte(tt.wantStderr)) {
				t.Errorf("stderr %q does not contain reason %q", errRaw, tt.wantStderr)
			}
			if bytes.Contains(errRaw, []byte(`"hookSpecificOutput"`)) || bytes.Contains(errRaw, []byte(`"permissionDecision"`)) {
				t.Errorf("stderr must never contain hook JSON, got: %q", errRaw)
			}
		})
	}
}

// TestDenyEmitters_ExitEnforcedRegardlessOfStdoutSchema is the schema-drift
// regression guard that was missing: it proves the exit(2) enforcement does
// not depend on the stdout JSON being valid (or even present) at all. A
// future reviewer who breaks the hookSpecificOutput shape again -- exactly
// the earlier bug, a missing hookEventName causing Claude Code to reject the
// schema and silently fail the deny open -- should see from this test that
// the block still happens, because it never depended on stdout parsing to
// begin with.
func TestDenyEmitters_ExitEnforcedRegardlessOfStdoutSchema(t *testing.T) {
	for _, tt := range denyEmitterCases() {
		t.Run(tt.name, func(t *testing.T) {
			raw, _, exitCodes := captureDenyEmitter(t, tt.emit)

			// Simulate Claude Code's schema validator rejecting the stdout
			// JSON (e.g. a future missing/renamed field, same shape as the
			// earlier missing-hookEventName bug this change responds to). Mangling `raw`
			// here has no bearing on the assertions below -- that's the
			// point: exitFunc was already invoked, independent of whatever
			// stdout ends up containing or how Claude Code parses it.
			raw = append([]byte("not valid JSON at all, schema validation would reject this"), raw...)
			_ = raw

			if len(exitCodes) != 1 || exitCodes[0] != 2 {
				t.Fatalf("exitCodes = %v, want exactly one call with code 2 -- enforcement must not depend on stdout schema validity", exitCodes)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// runHookCaptured helper + integration-style hook tests (from smoke tests PR)
// ---------------------------------------------------------------------------

// runHookCaptured is a test helper that:
//   - sets the provided env vars (restoring originals after the test)
//   - seeds os.Stdin with stdinPayload
//   - captures os.Stdout and os.Stderr
//   - stubs exitFunc to a no-op so a deny path reached via fn() (e.g.
//     ValidateBash calling denyCommand/denyBashPopup) doesn't kill the
//     test binary with a real os.Exit(2)
//   - calls fn()
//   - returns captured stdout and stderr strings
//
// Callers that need to assert exit(2) was actually requested should stub
// exitFunc themselves (with their own recorder) before invoking fn directly,
// rather than through this helper -- see
// TestValidateBash_GlobalDeny_EnforcesExitRegardlessOfStdoutSchema.
func runHookCaptured(t *testing.T, envVars map[string]string, stdinPayload string, fn func()) (stdout, stderr string) {
	t.Helper()

	origExit := exitFunc
	exitFunc = func(int) {}
	defer func() { exitFunc = origExit }()

	// Set env vars and queue restores.
	for k, v := range envVars {
		orig, wasSet := os.LookupEnv(k)
		os.Setenv(k, v) //nolint:errcheck
		defer func(key, original string, had bool) {
			if had {
				os.Setenv(key, original) //nolint:errcheck
			} else {
				os.Unsetenv(key) //nolint:errcheck
			}
		}(k, orig, wasSet)
	}

	// Seed stdin.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("runHookCaptured: create stdin pipe: %v", err)
	}
	if _, err := io.WriteString(stdinW, stdinPayload); err != nil {
		t.Fatalf("runHookCaptured: write stdin: %v", err)
	}
	_ = stdinW.Close()
	oldStdin := os.Stdin
	os.Stdin = stdinR
	defer func() { os.Stdin = oldStdin; _ = stdinR.Close() }()

	// Capture stdout.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("runHookCaptured: create stdout pipe: %v", err)
	}
	oldStdout := os.Stdout
	os.Stdout = stdoutW
	defer func() { os.Stdout = oldStdout }()

	// Capture stderr.
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("runHookCaptured: create stderr pipe: %v", err)
	}
	oldStderr := os.Stderr
	os.Stderr = stderrW
	defer func() { os.Stderr = oldStderr }()

	fn()

	stdoutW.Close()
	stderrW.Close()

	var outBuf, errBuf bytes.Buffer
	outBuf.ReadFrom(stdoutR) //nolint:errcheck
	errBuf.ReadFrom(stderrR) //nolint:errcheck

	return outBuf.String(), errBuf.String()
}

// TestHookPreTool_AutoAcceptNoJSON verifies that PreTool does NOT emit
// any JSON on stdout -- auto-accept is now handled via permissions.allow in the
// --settings file, not via hookSpecificOutput (which broke in Claude Code v2.1.86).
func TestHookPreTool_AutoAcceptNoJSON(t *testing.T) {
	const agentName = "test-auto-accept-worker"

	// Write a config file with AutoAccept=true for our test agent.
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")

	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			agentName: {AutoAccept: true},
		},
	}
	config.SetGlobalConfigPathForTest(cfgPath)
	config.SetConfigPathForTest(cfgPath)
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save test config: %v", err)
	}
	// Restore config path after test.
	t.Cleanup(func() { config.SetConfigPathForTest("") })

	stdin := `{"tool_name":"Glob"}`
	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_AGENT_NAME": agentName,
		"AGENTCTL_WINDOW_ID":  "w99", // fake; bypasses tmux lookup
		"AGENTCTL_SESSION_ID": "",    // ensure session-id path is skipped
		"TMUX_PANE":           "",    // ensure pane path is skipped
	}

	out, _ := runHookCaptured(t, envVars, stdin, PreTool)

	if out != "" {
		t.Errorf("expected no stdout from hook-pre-tool (auto-accept moved to --settings), got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// hook-session-start: CLAUDE_ENV_FILE propagation
// ---------------------------------------------------------------------------

// TestHookSessionStart_EnvFilePropagation verifies that SessionStart
// writes AGENTCTL_TICKET, AGENTCTL_TICKET_TITLE, and AGENTCTL_MODE to
// CLAUDE_ENV_FILE when those vars are set, and omits them when they are not.
func TestHookSessionStart_EnvFilePropagation(t *testing.T) {
	tests := []struct {
		name        string
		ticket      string
		ticketTitle string
		mode        string
		wantTicket  bool
		wantTitle   bool
		wantMode    bool
	}{
		{
			name:        "all_set",
			ticket:      "42",
			ticketTitle: "Fix the thing",
			mode:        "autonomous",
			wantTicket:  true,
			wantTitle:   true,
			wantMode:    true,
		},
		{
			name:        "none_set",
			ticket:      "",
			ticketTitle: "",
			mode:        "",
			wantTicket:  false,
			wantTitle:   false,
			wantMode:    false,
		},
		{
			name:        "only_ticket_set",
			ticket:      "7",
			ticketTitle: "",
			mode:        "",
			wantTicket:  true,
			wantTitle:   false,
			wantMode:    false,
		},
		{
			name:        "ticket_and_mode_set",
			ticket:      "99",
			ticketTitle: "",
			mode:        "triage",
			wantTicket:  true,
			wantTitle:   false,
			wantMode:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a temp file to serve as CLAUDE_ENV_FILE.
			envFile, err := os.CreateTemp(t.TempDir(), "claude-env-*.sh")
			if err != nil {
				t.Fatalf("create temp env file: %v", err)
			}
			envFile.Close()

			envVars := map[string]string{
				"AGENTCTL":        "1",
				"CLAUDE_ENV_FILE": envFile.Name(),
				// Skip tmux resolution paths so the hook bails early on windowID.
				// We need to supply a fake window ID via AGENTCTL_WINDOW_ID so
				// the hook proceeds to write the env file. Use the fallback path:
				// clear TMUX_PANE and AGENTCTL_SESSION_ID so resolveHookWindowID
				// falls through to AGENTCTL_WINDOW_ID.
				"AGENTCTL_WINDOW_ID":  "@42", // fake; used as fallback
				"AGENTCTL_SESSION_ID": "",
				"TMUX_PANE":           "",
				// Ticket/mode vars under test.
				"AGENTCTL_TICKET":       tt.ticket,
				"AGENTCTL_TICKET_TITLE": tt.ticketTitle,
				"AGENTCTL_MODE":         tt.mode,
			}

			stdin := `{"session_id":"test-session","cwd":"/tmp"}`
			runHookCaptured(t, envVars, stdin, SessionStart)

			contents, err := os.ReadFile(envFile.Name())
			if err != nil {
				t.Fatalf("read env file: %v", err)
			}
			got := string(contents)

			// WINDOW_ID and WINDOW_INDEX must always be present (WINDOW_INDEX may
			// be empty when tmux is absent, but the export line is still written).
			if !strings.Contains(got, "AGENTCTL_WINDOW_ID") {
				t.Errorf("env file missing AGENTCTL_WINDOW_ID\nfile:\n%s", got)
			}

			checkPresence := func(key, value string, want bool) {
				t.Helper()
				exportLine := "export " + key + "=" + shellquote.Quote(value)
				has := strings.Contains(got, exportLine)
				if want && !has {
					t.Errorf("expected %q in env file, not found\nfile:\n%s", exportLine, got)
				}
				if !want && strings.Contains(got, key) {
					t.Errorf("unexpected %q in env file\nfile:\n%s", key, got)
				}
			}

			checkPresence("AGENTCTL_TICKET", tt.ticket, tt.wantTicket)
			checkPresence("AGENTCTL_TICKET_TITLE", tt.ticketTitle, tt.wantTitle)
			checkPresence("AGENTCTL_MODE", tt.mode, tt.wantMode)
		})
	}
}

// ---------------------------------------------------------------------------
// HooksConfig
// ---------------------------------------------------------------------------

// TestHooksConfigComplete verifies that HooksConfig() contains entries for all
// known Claude Code hook events. Any new event added to Claude Code must be
// registered here (even as a no-op) and in HooksConfig().
func TestHooksConfigComplete(t *testing.T) {
	knownEvents := []string{
		"SessionStart",
		"SessionEnd",
		"Notification",
		"PreToolUse",
		"PostToolUse",
		"PostToolUseFailure",
		"Stop",
		"StopFailure",
		"UserPromptSubmit",
		"SubagentStart",
		"SubagentStop",
		"PostCompact",
		"PreCompact",
		"InstructionsLoaded",
		"ConfigChange",
		"CwdChanged",
		"FileChanged",
		"PermissionRequest",
		"WorktreeCreate",
		"WorktreeRemove",
		"Elicitation",
		"ElicitationResult",
		"TaskCreated",
		"TaskCompleted",
		"TeammateIdle",
		"Setup",
	}

	cfg := HooksConfig()

	for _, event := range knownEvents {
		t.Run(event, func(t *testing.T) {
			val, ok := cfg[event]
			if !ok {
				t.Errorf("HooksConfig() missing event %q", event)
				return
			}

			// Each event value must be a non-empty slice.
			slice, ok := val.([]interface{})
			if !ok {
				t.Errorf("HooksConfig()[%q] is %T, want []interface{}", event, val)
				return
			}
			if len(slice) == 0 {
				t.Errorf("HooksConfig()[%q] is empty slice", event)
				return
			}

			// Each entry must be a map containing a "hooks" key.
			for i, entry := range slice {
				m, ok := entry.(map[string]interface{})
				if !ok {
					t.Errorf("HooksConfig()[%q][%d] is %T, want map[string]interface{}", event, i, entry)
					continue
				}
				hooks, ok := m["hooks"]
				if !ok {
					t.Errorf("HooksConfig()[%q][%d] missing \"hooks\" key", event, i)
					continue
				}
				hooksSlice, ok := hooks.([]interface{})
				if !ok {
					t.Errorf("HooksConfig()[%q][%d][\"hooks\"] is %T, want []interface{}", event, i, hooks)
					continue
				}
				if len(hooksSlice) == 0 {
					t.Errorf("HooksConfig()[%q][%d][\"hooks\"] is empty", event, i)
					continue
				}
				// Each hook must have type "command" and a non-empty "command" string.
				for j, h := range hooksSlice {
					hm, ok := h.(map[string]interface{})
					if !ok {
						t.Errorf("HooksConfig()[%q][%d][\"hooks\"][%d] is %T, want map", event, i, j, h)
						continue
					}
					if typ, _ := hm["type"].(string); typ != "command" {
						t.Errorf("HooksConfig()[%q][%d][\"hooks\"][%d][\"type\"] = %q, want \"command\"", event, i, j, typ)
					}
					if cmd, _ := hm["command"].(string); cmd == "" {
						t.Errorf("HooksConfig()[%q][%d][\"hooks\"][%d][\"command\"] is empty", event, i, j)
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// runUserHooks
// ---------------------------------------------------------------------------

// TestRunUserHooks verifies that runUserHooks fires global hooks followed by
// agent-specific hooks, and returns without blocking (fire-and-forget).
func TestRunUserHooks(t *testing.T) {
	cfgDir := t.TempDir()
	outFile := filepath.Join(cfgDir, "hook-output.txt")

	// Config: one global hook + one agent hook, both append to outFile.
	cfg := &config.Config{
		Hooks: config.HooksConfig{
			"session_start": {
				"printf 'global\\n' >> " + outFile,
			},
		},
		Agents: map[string]config.AgentConfig{
			"test-agent": {
				Hooks: config.HooksConfig{
					"session_start": {
						"printf 'agent\\n' >> " + outFile,
					},
				},
			},
		},
	}
	config.SetConfigPathForTest(filepath.Join(cfgDir, "config.json"))
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	t.Cleanup(func() { config.SetConfigPathForTest("") })

	// Set AGENTCTL_AGENT_NAME so runUserHooks picks up per-agent hooks.
	orig, wasSet := os.LookupEnv("AGENTCTL_AGENT_NAME")
	os.Setenv("AGENTCTL_AGENT_NAME", "test-agent") //nolint:errcheck
	defer func() {
		if wasSet {
			os.Setenv("AGENTCTL_AGENT_NAME", orig) //nolint:errcheck
		} else {
			os.Unsetenv("AGENTCTL_AGENT_NAME") //nolint:errcheck
		}
	}()

	runUserHooks("session_start", map[string]string{"AGENTCTL_WINDOW_ID": "@1"})

	// Wait for the async goroutine to finish writing.
	deadline := time.Now().Add(3 * time.Second)
	var content string
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(outFile)
		content = string(data)
		if strings.Contains(content, "global") && strings.Contains(content, "agent") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !strings.Contains(content, "global") {
		t.Errorf("global hook output not found in %q", content)
	}
	if !strings.Contains(content, "agent") {
		t.Errorf("agent hook output not found in %q", content)
	}

	// Verify ordering: global line appears before agent line.
	globalIdx := strings.Index(content, "global")
	agentIdx := strings.Index(content, "agent")
	if globalIdx >= agentIdx {
		t.Errorf("global hook should appear before agent hook, got: %q", content)
	}
}

// TestRunUserHooks_EnvVarsInjected verifies that extra env vars passed to
// runUserHooks are available to the shell command.
func TestRunUserHooks_EnvVarsInjected(t *testing.T) {
	cfgDir := t.TempDir()
	outFile := filepath.Join(cfgDir, "env-output.txt")

	cfg := &config.Config{
		Hooks: config.HooksConfig{
			"session_start": {
				"printf '%s' \"$AGENTCTL_WINDOW_ID\" > " + outFile,
			},
		},
		Agents: map[string]config.AgentConfig{},
	}
	config.SetConfigPathForTest(filepath.Join(cfgDir, "config.json"))
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	t.Cleanup(func() { config.SetConfigPathForTest("") })

	// Unset agent name so we don't try to load a non-existent agent.
	origAgent, agentSet := os.LookupEnv("AGENTCTL_AGENT_NAME")
	os.Unsetenv("AGENTCTL_AGENT_NAME") //nolint:errcheck
	defer func() {
		if agentSet {
			os.Setenv("AGENTCTL_AGENT_NAME", origAgent) //nolint:errcheck
		}
	}()

	const wantWindowID = "@42"
	runUserHooks("session_start", map[string]string{"AGENTCTL_WINDOW_ID": wantWindowID})

	// Wait for the async goroutine.
	deadline := time.Now().Add(3 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(outFile)
		got = string(data)
		if got != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got != wantWindowID {
		t.Errorf("AGENTCTL_WINDOW_ID = %q, want %q", got, wantWindowID)
	}
}

// TestRunUserHooks_NoOp verifies that runUserHooks returns immediately when
// no hooks are configured for the event.
func TestRunUserHooks_NoOp(t *testing.T) {
	cfgDir := t.TempDir()
	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{},
	}
	config.SetConfigPathForTest(filepath.Join(cfgDir, "config.json"))
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	t.Cleanup(func() { config.SetConfigPathForTest("") })

	// Must not panic and must return promptly.
	done := make(chan struct{})
	go func() {
		runUserHooks("session_start", nil)
		close(done)
	}()
	select {
	case <-done:
		// Good.
	case <-time.After(500 * time.Millisecond):
		t.Error("runUserHooks blocked when no hooks configured")
	}
}

// TestRunUserHooksTimeout verifies that runSingleUserHook respects the timeout
// and kills a slow command.
func TestRunUserHooksTimeout(t *testing.T) {
	// Override the global timeout to something short so the test is fast.
	orig := userHookTimeout
	userHookTimeout = 100 * time.Millisecond
	defer func() { userHookTimeout = orig }()

	// Build env with just os.Environ() -- no extras needed.
	env := os.Environ()

	start := time.Now()
	// "sleep 10" would normally block; with a 100ms timeout it should be killed quickly.
	runSingleUserHook("test_event", "sleep 10", env)
	elapsed := time.Since(start)

	// The timeout is 100ms; allow up to 2s for process setup/teardown overhead.
	if elapsed > 2*time.Second {
		t.Errorf("runSingleUserHook did not respect timeout: elapsed=%v", elapsed)
	}
}

// ---------------------------------------------------------------------------
// writeJournalEntry
// ---------------------------------------------------------------------------

// TestWriteJournalEntry_CreatesFile verifies that writeJournalEntry creates the
// journal file when it doesn't exist and writes a properly formatted entry.
func TestWriteJournalEntry_CreatesFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	summary := &sessions.Summary{}

	if err := writeJournalEntry(home, "go-engineer", "abc123", "", "", summary, "- Implemented feature X\n- Fixed bug Y"); err != nil {
		t.Fatalf("writeJournalEntry: %v", err)
	}

	journalPath := filepath.Join(home, ".local", "share", "agentctl", "journal", "go-engineer.md")
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, "session abc123") {
		t.Errorf("journal missing session ID, got:\n%s", got)
	}
	if !strings.Contains(got, "Implemented feature X") {
		t.Errorf("journal missing AI summary, got:\n%s", got)
	}
	if !strings.Contains(got, "---") {
		t.Errorf("journal missing separator, got:\n%s", got)
	}
}

// TestWriteJournalEntry_Appends verifies that subsequent calls append rather
// than overwrite, and that both entries are present in the file.
func TestWriteJournalEntry_Appends(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	summary := &sessions.Summary{}

	if err := writeJournalEntry(home, "go-engineer", "sess1", "", "", summary, "- First session"); err != nil {
		t.Fatalf("first writeJournalEntry: %v", err)
	}
	if err := writeJournalEntry(home, "go-engineer", "sess2", "", "", summary, "- Second session"); err != nil {
		t.Fatalf("second writeJournalEntry: %v", err)
	}

	journalPath := filepath.Join(home, ".local", "share", "agentctl", "journal", "go-engineer.md")
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, "session sess1") {
		t.Errorf("first entry missing, got:\n%s", got)
	}
	if !strings.Contains(got, "session sess2") {
		t.Errorf("second entry missing, got:\n%s", got)
	}
	if !strings.Contains(got, "First session") {
		t.Errorf("first summary missing, got:\n%s", got)
	}
	if !strings.Contains(got, "Second session") {
		t.Errorf("second summary missing, got:\n%s", got)
	}
}

// TestWriteJournalEntry_EmptySummary verifies that an empty session summary
// still produces a valid journal entry (no tool activity is informative).
func TestWriteJournalEntry_EmptySummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	summary := &sessions.Summary{}

	if err := writeJournalEntry(home, "go-engineer", "abc123", "", "", summary, "No tool activity"); err != nil {
		t.Fatalf("writeJournalEntry: %v", err)
	}

	journalPath := filepath.Join(home, ".local", "share", "agentctl", "journal", "go-engineer.md")
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, "No tool activity") {
		t.Errorf("journal missing empty-summary text, got:\n%s", got)
	}
}

// TestWriteJournalEntry_WithDuration verifies that a session with start/end
// times includes a duration in the header.
func TestWriteJournalEntry_WithDuration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	now := time.Now()
	sess := sessions.Session{
		StartTime: now.Add(-45 * time.Minute),
		EndTime:   now,
	}
	summary := &sessions.Summary{Session: sess}

	if err := writeJournalEntry(home, "go-engineer", "abc123", "", "", summary, "- Did stuff"); err != nil {
		t.Fatalf("writeJournalEntry: %v", err)
	}

	journalPath := filepath.Join(home, ".local", "share", "agentctl", "journal", "go-engineer.md")
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, "45m") {
		t.Errorf("journal missing duration, got:\n%s", got)
	}
}

// TestBuildJournalPrompt_TruncatesLongContent verifies that buildJournalPrompt
// caps output at ~4000 chars even with many files and tool calls.
func TestBuildJournalPrompt_TruncatesLongContent(t *testing.T) {
	// Build a summary with many files to ensure truncation.
	filesEdited := make([]string, 50)
	for i := range filesEdited {
		filesEdited[i] = fmt.Sprintf("/very/long/path/to/some/file/edited_%d.go", i)
	}
	filesRead := make([]string, 50)
	for i := range filesRead {
		filesRead[i] = fmt.Sprintf("/very/long/path/to/some/file/read_%d.go", i)
	}
	toolCalls := make([]sessions.ToolCall, 100)
	for i := range toolCalls {
		toolCalls[i] = sessions.ToolCall{Name: "Read"}
	}

	summary := &sessions.Summary{
		FilesEdited: filesEdited,
		FilesRead:   filesRead,
		ToolCalls:   toolCalls,
		TurnCount:   200,
	}

	prompt := buildJournalPrompt("go-engineer", "abc123", summary, "", "")
	if len(prompt) > 4000 {
		t.Errorf("prompt too long: %d chars (max 4000)", len(prompt))
	}
}

// TestBuildJournalPrompt_IncludesTicket verifies that ticket info is included
// in the prompt when present.
func TestBuildJournalPrompt_IncludesTicket(t *testing.T) {
	summary := &sessions.Summary{}
	prompt := buildJournalPrompt("go-engineer", "abc123", summary, "577", "feat(hooks): auto-generate work journal")
	if !strings.Contains(prompt, "#577") {
		t.Errorf("prompt missing ticket number, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "auto-generate work journal") {
		t.Errorf("prompt missing ticket title, got:\n%s", prompt)
	}
}

// TestWriteJournalEntry_DifferentAgents verifies that different agents get
// separate journal files.
func TestWriteJournalEntry_DifferentAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	summary := &sessions.Summary{}

	if err := writeJournalEntry(home, "go-engineer", "sess1", "", "", summary, "Go work"); err != nil {
		t.Fatalf("writeJournalEntry go-engineer: %v", err)
	}
	if err := writeJournalEntry(home, "python-engineer", "sess2", "", "", summary, "Python work"); err != nil {
		t.Fatalf("writeJournalEntry python-engineer: %v", err)
	}

	goJournal, err := os.ReadFile(filepath.Join(home, ".local", "share", "agentctl", "journal", "go-engineer.md"))
	if err != nil {
		t.Fatalf("read go journal: %v", err)
	}
	pyJournal, err := os.ReadFile(filepath.Join(home, ".local", "share", "agentctl", "journal", "python-engineer.md"))
	if err != nil {
		t.Fatalf("read python journal: %v", err)
	}

	if !strings.Contains(string(goJournal), "Go work") {
		t.Errorf("go journal missing entry: %s", goJournal)
	}
	if !strings.Contains(string(pyJournal), "Python work") {
		t.Errorf("python journal missing entry: %s", pyJournal)
	}
	// Cross-contamination check.
	if strings.Contains(string(goJournal), "Python work") {
		t.Errorf("go journal should not contain python entry")
	}
}

// TestRunUserHooks_ErrorContinues verifies that a failing command does not
// prevent subsequent commands from running.
func TestRunUserHooks_ErrorContinues(t *testing.T) {
	cfgDir := t.TempDir()
	outFile := filepath.Join(cfgDir, "continue-output.txt")

	cfg := &config.Config{
		Hooks: config.HooksConfig{
			"session_start": {
				"exit 1",                              // fails
				"printf 'after-failure' > " + outFile, // must still run
			},
		},
		Agents: map[string]config.AgentConfig{},
	}
	config.SetConfigPathForTest(filepath.Join(cfgDir, "config.json"))
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	t.Cleanup(func() { config.SetConfigPathForTest("") })

	origAgent, agentSet := os.LookupEnv("AGENTCTL_AGENT_NAME")
	os.Unsetenv("AGENTCTL_AGENT_NAME") //nolint:errcheck
	defer func() {
		if agentSet {
			os.Setenv("AGENTCTL_AGENT_NAME", origAgent) //nolint:errcheck
		}
	}()

	runUserHooks("session_start", nil)

	deadline := time.Now().Add(3 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(outFile)
		got = string(data)
		if got != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got != "after-failure" {
		t.Errorf("second hook not executed after first failed; outFile=%q", got)
	}
}

// ---------------------------------------------------------------------------
// Dispatch smoke test
// ---------------------------------------------------------------------------

// TestDispatchCoversAllHooks verifies that every hook subcommand referenced in
// HooksConfig() is handled by Dispatch(). This catches registration drift: if
// someone adds a hook to HooksConfig() without adding a Dispatch() case (or
// vice versa), this test will fail.
func TestDispatchCoversAllHooks(t *testing.T) {
	cmdPrefix := commandPrefix() + " "

	cfg := HooksConfig()

	// Ensure AGENTCTL is not set so hooks no-op quickly.
	orig, wasSet := os.LookupEnv("AGENTCTL")
	os.Unsetenv("AGENTCTL")
	defer func() {
		if wasSet {
			_ = os.Setenv("AGENTCTL", orig)
		}
	}()

	for eventName, val := range cfg {
		entries, ok := val.([]interface{})
		if !ok {
			t.Errorf("HooksConfig()[%q] is %T, want []interface{}", eventName, val)
			continue
		}
		for i, entry := range entries {
			m, ok := entry.(map[string]interface{})
			if !ok {
				continue
			}
			hooksVal, ok := m["hooks"]
			if !ok {
				continue
			}
			hooksSlice, ok := hooksVal.([]interface{})
			if !ok {
				continue
			}
			for j, h := range hooksSlice {
				hm, ok := h.(map[string]interface{})
				if !ok {
					continue
				}
				cmdStr, _ := hm["command"].(string)
				if !strings.HasPrefix(cmdStr, cmdPrefix) {
					t.Errorf("HooksConfig()[%q][%d][hooks][%d] command %q does not start with expected prefix", eventName, i, j, cmdStr)
					continue
				}
				subcmd := strings.TrimPrefix(cmdStr, cmdPrefix)
				// subcmd may have extra args (e.g. none in practice, but be safe)
				subcmd = strings.Fields(subcmd)[0]

				t.Run(eventName+"/"+subcmd, func(t *testing.T) {
					// hook-worktree-create deliberately does NOT respect the
					// AGENTCTL guard (see WorktreeCreate's doc comment: it
					// must work regardless of agentctl window state, since it
					// replaces Claude Code's own git worktree creation). That
					// means it would perform a real "git worktree add" against
					// this test process's actual working directory and then
					// os.Exit -- both unacceptable in this generic coverage
					// loop. Its own behavior is covered by worktree_test.go
					// (worktreeCreate/worktreeRemove, the injectable cores);
					// here we only need the registration-drift check, done
					// statically since the case is easy to eyeball in
					// Dispatch's switch.
					if subcmd == "hook-worktree-create" {
						t.Skip("hook-worktree-create bypasses the AGENTCTL guard by design and calls os.Exit; covered by worktree_test.go instead")
					}

					// Seed stdin so hooks that read it don't block.
					stdinR, stdinW, err := os.Pipe()
					if err != nil {
						t.Fatalf("create stdin pipe: %v", err)
					}
					io.WriteString(stdinW, `{}`) //nolint:errcheck
					stdinW.Close()
					oldStdin := os.Stdin
					os.Stdin = stdinR
					defer func() { os.Stdin = oldStdin; stdinR.Close() }()

					handled := Dispatch(subcmd)
					if !handled {
						t.Errorf("Dispatch(%q) = false, want true — HooksConfig() registers this subcommand but Dispatch() has no case for it", subcmd)
					}
				})
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// generateWorkJournal env stripping
// ---------------------------------------------------------------------------

// TestGenerateWorkJournal_SkipsWhenNoLogPath verifies that generateWorkJournal
// returns nil (without calling Claude) when no log path is available for the
// window. This also implicitly confirms the function does not panic when
// windowstate has no data.
func TestGenerateWorkJournal_SkipsWhenNoLogPath(t *testing.T) {
	// windowID "" means no window state — logPath will be empty and the
	// function returns nil early.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	err := generateWorkJournal("test-agent", "session-abc", "")
	if err != nil {
		t.Errorf("expected nil when no log path; got %v", err)
	}
}

// TestDispatch_HandlesHookSubcommands verifies that Dispatch returns true for
// all known hook subcommands and false for non-hook subcommands.
func TestDispatch_HandlesHookSubcommands(t *testing.T) {
	hookSubcmds := []string{
		"hook-session-start",
		"hook-notify",
		"hook-pre-tool",
		"hook-post-tool",
		"hook-stop-failure",
		"hook-user-prompt",
		"hook-subagent-start",
		"hook-subagent-stop",
		"hook-post-compact",
		"hook-post-tool-failure",
		"hook-validate-bash",
		"hook-check-done",
		"hook-session-end",
		"hook-pre-compact",
		"hook-instructions-loaded",
		"hook-config-change",
		"hook-cwd-changed",
		"hook-file-changed",
		"hook-permission-request",
		"hook-worktree-create",
		"hook-worktree-remove",
		"hook-elicitation",
		"hook-elicitation-result",
		"hook-task-created",
		"hook-task-completed",
		"hook-teammate-idle",
		"hook-setup",
	}

	// Ensure AGENTCTL is not set so hooks no-op quickly.
	orig, wasSet := os.LookupEnv("AGENTCTL")
	os.Unsetenv("AGENTCTL")
	defer func() {
		if wasSet {
			_ = os.Setenv("AGENTCTL", orig)
		}
	}()

	for _, subcmd := range hookSubcmds {
		t.Run(subcmd, func(t *testing.T) {
			// hook-worktree-create deliberately bypasses the AGENTCTL guard
			// (it must work regardless of agentctl window state -- see
			// WorktreeCreate's doc comment) and calls os.Exit, so it would
			// perform a real "git worktree add" against this test process's
			// actual working directory and then crash the test. Its
			// registration is covered by TestDispatchRoutesToWorktreeCreate
			// (subprocess) and its behavior by worktree_test.go.
			if subcmd == "hook-worktree-create" {
				t.Skip("hook-worktree-create bypasses the AGENTCTL guard by design and calls os.Exit; covered by worktree_test.go instead")
			}

			// Seed stdin so hooks that read it don't block.
			stdinR, stdinW, _ := os.Pipe()
			io.WriteString(stdinW, `{}`)
			stdinW.Close()
			oldStdin := os.Stdin
			os.Stdin = stdinR
			defer func() { os.Stdin = oldStdin; stdinR.Close() }()

			handled := Dispatch(subcmd)
			if !handled {
				t.Errorf("Dispatch(%q) = false, want true", subcmd)
			}
		})
	}

	// Non-hook subcommands should return false. "audit-cmd" is included as a
	// regression guard: it was a hook-adjacent subcommand (the AuditCmd shell
	// sidecar writer) that has been removed -- Dispatch must no longer route
	// to it. See internal/audit for its hook-sourced replacement.
	nonHook := []string{"version", "monitor", "launch", "menu", "hooks-install", "unknown", "audit-cmd"}
	for _, subcmd := range nonHook {
		t.Run("non-hook/"+subcmd, func(t *testing.T) {
			handled := Dispatch(subcmd)
			if handled {
				t.Errorf("Dispatch(%q) = true, want false", subcmd)
			}
		})
	}
}
