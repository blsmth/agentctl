package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// withFakeAttachedClient replaces hasAttachedClient for the duration of a
// test and restores the original afterward.
func withFakeAttachedClient(t *testing.T, attached bool) {
	t.Helper()
	orig := hasAttachedClient
	hasAttachedClient = func() bool { return attached }
	t.Cleanup(func() { hasAttachedClient = orig })
}

// withBashPreviewDeps installs a Deps whose LoadConfig returns cfg, cleaning
// up afterward.
func withBashPreviewDeps(t *testing.T, cfg *config.Config) {
	t.Helper()
	d := DefaultDeps()
	d.LoadConfig = func() *config.Config { return cfg }
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
}

// boolPtr returns a pointer to v -- shorthand for *bool config fields.
func boolPtr(v bool) *bool { return &v }

// runValidateBashCapturingExit is like runHookCaptured, but ALSO records
// every exitFunc invocation instead of stubbing it to a silent no-op --
// needed because runHookCaptured's own no-op stub is specifically there so
// callers that don't care about exit codes don't kill the test binary,
// which means it can't be used to assert exit(2) actually happened. See
// exitFunc's doc comment (validate.go).
func runValidateBashCapturingExit(t *testing.T, envVars map[string]string, stdinPayload string) (stdout string, exitCodes []int) {
	t.Helper()

	origExit := exitFunc
	exitFunc = func(code int) { exitCodes = append(exitCodes, code) }
	t.Cleanup(func() { exitFunc = origExit })

	for k, v := range envVars {
		t.Setenv(k, v)
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	if _, err := io.WriteString(stdinW, stdinPayload); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = stdinW.Close()
	oldStdin := os.Stdin
	os.Stdin = stdinR
	defer func() { os.Stdin = oldStdin; _ = stdinR.Close() }()

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	oldStdout := os.Stdout
	os.Stdout = stdoutW
	defer func() { os.Stdout = oldStdout }()

	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	oldStderr := os.Stderr
	os.Stderr = stderrW
	defer func() { os.Stderr = oldStderr }()

	ValidateBash()

	_ = stdoutW.Close()
	_ = stderrW.Close()

	var outBuf, errBuf bytes.Buffer
	_, _ = outBuf.ReadFrom(stdoutR)
	_, _ = errBuf.ReadFrom(stderrR)

	return outBuf.String(), exitCodes
}

// ---------------------------------------------------------------------------
// ValidateBash: table test over every decided/not-decided combination
// ---------------------------------------------------------------------------

// TestValidateBash_PreviewGate_OutputContract is the central regression test
// for fix 1 (approving the popup must actually emit an allow decision) and
// the safety rails added for fix 2 (a bash_preview command must never emit
// anything other than empty stdout unless a human actually decided). It
// captures the EXACT bytes ValidateBash writes to stdout for every
// decided/not-decided combination -- the security property under test
// (nothing written on every not-decided path) is invisible to an assertion
// that only inspects return values.
func TestValidateBash_PreviewGate_OutputContract(t *testing.T) {
	const agentName = "go-engineer"

	type wantKind int
	const (
		wantEmpty wantKind = iota
		wantAllow
		wantDeny
	)

	tests := []struct {
		name           string
		command        string
		bashPreview    bool // config: bash_preview for agentName
		tmux           bool // TMUX env var set
		attachedClient bool // hasAttachedClient() return value
		lockHeld       bool // pre-hold the gate lock to simulate contention
		deny           bool // popup runner reports an explicit deny (exit 1)
		launchFail     bool // popup runner reports a non-ExitError launch failure
		exitCode       int  // popup runner reports an ExitError with this code (0 = unused)
		want           wantKind
	}{
		{
			name:           "dispatch command approved",
			command:        "agentctl dispatch --issue 1 --agent go-engineer",
			tmux:           true,
			attachedClient: true,
			want:           wantAllow,
		},
		{
			name:           "dispatch command denied",
			command:        "agentctl dispatch --issue 2 --agent go-engineer",
			tmux:           true,
			attachedClient: true,
			deny:           true,
			want:           wantDeny,
		},
		{
			name:    "dispatch command not in tmux passes through silently",
			command: "agentctl dispatch --issue 3 --agent go-engineer",
			tmux:    false,
			want:    wantEmpty,
		},
		{
			name:           "dispatch command lock contention passes through silently",
			command:        "agentctl dispatch --issue 4 --agent go-engineer",
			tmux:           true,
			attachedClient: true,
			lockHeld:       true,
			want:           wantEmpty,
		},
		{
			name:        "plain bash command bash_preview off never gates",
			command:     "go test ./...",
			bashPreview: false,
			tmux:        true,
			want:        wantEmpty,
		},
		{
			name:           "plain bash command bash_preview on approved",
			command:        "go test ./... -run TestApproved",
			bashPreview:    true,
			tmux:           true,
			attachedClient: true,
			want:           wantAllow,
		},
		{
			name:           "plain bash command bash_preview on denied",
			command:        "go test ./... -run TestDenied",
			bashPreview:    true,
			tmux:           true,
			attachedClient: true,
			deny:           true,
			want:           wantDeny,
		},
		{
			name:        "plain bash command bash_preview on but not in tmux passes through silently",
			command:     "go test ./... -run TestNoTmux",
			bashPreview: true,
			tmux:        false,
			want:        wantEmpty,
		},
		{
			name:           "plain bash command bash_preview on but no attached client passes through silently",
			command:        "go test ./... -run TestNoClient",
			bashPreview:    true,
			tmux:           true,
			attachedClient: false,
			want:           wantEmpty,
		},
		{
			name:           "plain bash command bash_preview on lock contention passes through silently",
			command:        "go test ./... -run TestLockHeld",
			bashPreview:    true,
			tmux:           true,
			attachedClient: true,
			lockHeld:       true,
			want:           wantEmpty,
		},
		{
			name:           "plain bash command bash_preview on popup launch failure passes through silently",
			command:        "go test ./... -run TestLaunchFail",
			bashPreview:    true,
			tmux:           true,
			attachedClient: true,
			launchFail:     true,
			want:           wantEmpty,
		},
		{
			name:           "plain bash command bash_preview on unexpected popup exit code passes through silently",
			command:        "go test ./... -run TestUnexpectedExitCode",
			bashPreview:    true,
			tmux:           true,
			attachedClient: true,
			exitCode:       3,
			want:           wantEmpty,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Agents: map[string]config.AgentConfig{}}
			if tt.bashPreview {
				cfg.Agents[agentName] = config.AgentConfig{BashPreview: boolPtr(true)}
			}
			withBashPreviewDeps(t, cfg)

			if tt.tmux {
				t.Setenv("TMUX", "/tmp/fake-tmux-socket")
			} else {
				t.Setenv("TMUX", "")
			}
			sessionID := "sess-" + tt.name
			t.Setenv("AGENTCTL_SESSION_ID", sessionID)
			removeDispatchLockForTest(t, tt.command)

			withFakeAttachedClient(t, tt.attachedClient)

			if tt.lockHeld {
				path := gateLockPath(tt.command)
				if !createGateLockFile(path) {
					t.Fatalf("failed to pre-create lock file at %s", path)
				}
				t.Cleanup(func() { os.Remove(path) })
				failIfDispatchPopupRunnerCalled(t)
			} else {
				withFakeDispatchPopupRunner(t, func(popupW, popupH int, argsFilePath string) error {
					switch {
					case tt.deny:
						return exitError1(t)
					case tt.launchFail:
						return errors.New("tmux: not found")
					case tt.exitCode != 0:
						return exitErrorCode(t, tt.exitCode)
					default:
						return nil
					}
				})
			}

			stdin := `{"tool_name":"Bash","tool_input":{"command":"` + tt.command + `"}}`
			envVars := map[string]string{
				"AGENTCTL":            "1",
				"AGENTCTL_AGENT_NAME": agentName,
				"AGENTCTL_WINDOW_ID":  "w99",
				"AGENTCTL_SESSION_ID": sessionID,
				"TMUX_PANE":           "",
			}

			out, _ := runHookCaptured(t, envVars, stdin, ValidateBash)

			switch tt.want {
			case wantEmpty:
				if out != "" {
					t.Errorf("expected empty stdout, got %q", out)
				}
			case wantAllow:
				var got map[string]interface{}
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatalf("unmarshal output: %v\nraw: %s", err, out)
				}
				hso, ok := got["hookSpecificOutput"].(map[string]interface{})
				if !ok {
					t.Fatalf("hookSpecificOutput missing or wrong type in: %s", out)
				}
				if decision := hso["permissionDecision"]; decision != "allow" {
					t.Errorf("permissionDecision = %v, want %q", decision, "allow")
				}
				if v := hso["hookEventName"]; v != "PreToolUse" {
					t.Errorf("hookEventName = %v, want %q", v, "PreToolUse")
				}
			case wantDeny:
				var got map[string]interface{}
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatalf("unmarshal output: %v\nraw: %s", err, out)
				}
				hso, ok := got["hookSpecificOutput"].(map[string]interface{})
				if !ok {
					t.Fatalf("hookSpecificOutput missing or wrong type in: %s", out)
				}
				if decision := hso["permissionDecision"]; decision != "deny" {
					t.Errorf("permissionDecision = %v, want %q", decision, "deny")
				}
			}
		})
	}
}

// TestValidateBash_PreviewGate_DenyExitsProcess verifies the deny path -- for
// both the always-on dispatch gate and the opt-in bash_preview gate -- still
// requests exit(2) via the exitFunc seam, independent of runHookCaptured's
// stubbing (which exists purely so the test binary survives the call). See
// exitFunc's doc comment (validate.go) for why this must be checked with its
// own recorder rather than through runHookCaptured's no-op stub.
func TestValidateBash_PreviewGate_DenyExitsProcess(t *testing.T) {
	const agentName = "go-engineer"

	tests := []struct {
		name        string
		command     string
		bashPreview bool
	}{
		{
			name:    "dispatch popup deny",
			command: "agentctl dispatch --issue 55 --agent go-engineer",
		},
		{
			name:        "bash_preview popup deny",
			command:     "rm -rf /tmp/some-scratch-dir",
			bashPreview: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Agents: map[string]config.AgentConfig{}}
			if tt.bashPreview {
				cfg.Agents[agentName] = config.AgentConfig{BashPreview: boolPtr(true)}
			}
			withBashPreviewDeps(t, cfg)

			sessionID := "sess-deny-exit-" + tt.name
			withFakeAttachedClient(t, true)
			t.Setenv("AGENTCTL_SESSION_ID", sessionID)
			removeDispatchLockForTest(t, tt.command)
			withFakeDispatchPopupRunner(t, func(popupW, popupH int, argsFilePath string) error {
				return exitError1(t)
			})

			stdin := `{"tool_name":"Bash","tool_input":{"command":"` + tt.command + `"}}`
			envVars := map[string]string{
				"AGENTCTL":            "1",
				"AGENTCTL_AGENT_NAME": agentName,
				"AGENTCTL_WINDOW_ID":  "w99",
				"AGENTCTL_SESSION_ID": sessionID,
				"TMUX_PANE":           "",
				"TMUX":                "/tmp/fake-tmux-socket",
			}

			_, exitCodes := runValidateBashCapturingExit(t, envVars, stdin)

			if len(exitCodes) != 1 || exitCodes[0] != 2 {
				t.Errorf("exitCodes = %v, want exactly one call with code 2", exitCodes)
			}
		})
	}
}

// TestValidateBash_PreviewGate_ApprovePath_NeverExits is the symmetric
// counterpart to TestValidateBash_PreviewGate_DenyExitsProcess: an approved
// popup must emit an "allow" decision (see
// TestValidateBash_ApprovedDispatchCommand_EmitsAllowDecision) and must
// NEVER call exitFunc. Only the deny path had this guard before; nothing
// pinned that the approve path calls exitFunc exactly zero times.
func TestValidateBash_PreviewGate_ApprovePath_NeverExits(t *testing.T) {
	const agentName = "go-engineer"

	tests := []struct {
		name        string
		command     string
		bashPreview bool
	}{
		{
			name:    "dispatch popup approve",
			command: "agentctl dispatch --issue 66 --agent go-engineer",
		},
		{
			name:        "bash_preview popup approve",
			command:     "go build ./... -approve-never-exits",
			bashPreview: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Agents: map[string]config.AgentConfig{}}
			if tt.bashPreview {
				cfg.Agents[agentName] = config.AgentConfig{BashPreview: boolPtr(true)}
			}
			withBashPreviewDeps(t, cfg)

			sessionID := "sess-approve-never-exits-" + tt.name
			withFakeAttachedClient(t, true)
			t.Setenv("AGENTCTL_SESSION_ID", sessionID)
			removeDispatchLockForTest(t, tt.command)
			withFakeDispatchPopupRunner(t, func(popupW, popupH int, argsFilePath string) error {
				return nil // exit 0 == approved
			})

			stdin := `{"tool_name":"Bash","tool_input":{"command":"` + tt.command + `"}}`
			envVars := map[string]string{
				"AGENTCTL":            "1",
				"AGENTCTL_AGENT_NAME": agentName,
				"AGENTCTL_WINDOW_ID":  "w99",
				"AGENTCTL_SESSION_ID": sessionID,
				"TMUX_PANE":           "",
				"TMUX":                "/tmp/fake-tmux-socket",
			}

			out, exitCodes := runValidateBashCapturingExit(t, envVars, stdin)

			if len(exitCodes) != 0 {
				t.Errorf("exitCodes = %v, want zero calls on the approve path", exitCodes)
			}

			var got map[string]interface{}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("unmarshal output: %v\nraw: %s", err, out)
			}
			hso, ok := got["hookSpecificOutput"].(map[string]interface{})
			if !ok {
				t.Fatalf("hookSpecificOutput missing or wrong type in: %s", out)
			}
			if decision := hso["permissionDecision"]; decision != "allow" {
				t.Errorf("permissionDecision = %v, want %q", decision, "allow")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// bashPreviewGate: gate selection and hasAttachedClient wiring
// ---------------------------------------------------------------------------

// TestBashPreviewGate_NoAttachedClient_StaysSilent verifies the fix-2 safety
// rail directly: hasAttachedClient() == false must pass through without ever
// invoking the popup runner, even though TMUX is set (an unattended
// dispatched agent runs inside a tmux pane with nobody watching).
func TestBashPreviewGate_NoAttachedClient_StaysSilent(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-no-client")
	withFakeAttachedClient(t, false)
	failIfDispatchPopupRunnerCalled(t)

	const command = "go build ./..."
	removeDispatchLockForTest(t, command)

	approved, decided, _ := bashPreviewGate("go-engineer", command)
	if decided {
		t.Errorf("decided = true, want false when no client is attached")
	}
	if approved {
		t.Errorf("approved = true, want false when not decided")
	}
}

// TestBashPreviewGate_NotInTmux_StaysSilent mirrors
// TestDispatchPreviewGate_NotInTmux_StaysSilent for the general bash gate.
func TestBashPreviewGate_NotInTmux_StaysSilent(t *testing.T) {
	t.Setenv("TMUX", "")
	failIfDispatchPopupRunnerCalled(t)

	approved, decided, _ := bashPreviewGate("go-engineer", "go build ./...")
	if decided {
		t.Errorf("decided = true, want false when not in tmux")
	}
	if approved {
		t.Errorf("approved = true, want false when not decided")
	}
}

// TestBashPreviewGate_Approves verifies the happy path end-to-end through
// bashPreviewGate with an attached client and a fake popup runner.
func TestBashPreviewGate_Approves(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-bash-approve")
	withFakeAttachedClient(t, true)

	const command = "go build ./... -bash-approve"
	removeDispatchLockForTest(t, command)

	called := withFakeDispatchPopupRunner(t, func(popupW, popupH int, argsFilePath string) error {
		return nil
	})

	approved, decided, _ := bashPreviewGate("go-engineer", command)
	if !*called {
		t.Fatalf("expected dispatchPopupRunner to be called")
	}
	if !decided || !approved {
		t.Errorf("approved=%v decided=%v, want both true", approved, decided)
	}
}

// ---------------------------------------------------------------------------
// ValidateBash: gate selection (dispatch vs. plain bash) per the
// bash_preview setting
// ---------------------------------------------------------------------------

// TestValidateBash_GateSelection verifies the exact selection rule from the
// spec: a dispatch command always gates regardless of bash_preview, a plain
// command never gates with bash_preview off, and does gate with it on.
func TestValidateBash_GateSelection(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		bashPreview bool
		wantGated   bool
	}{
		{
			name:      "dispatch command gates with setting off",
			command:   "agentctl dispatch --issue 1 --agent go-engineer",
			wantGated: true,
		},
		{
			name:      "non-dispatch command does not gate with setting off",
			command:   "echo hello",
			wantGated: false,
		},
		{
			name:        "non-dispatch command gates with setting on",
			command:     "echo hello",
			bashPreview: true,
			wantGated:   true,
		},
	}

	const agentName = "go-engineer"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Agents: map[string]config.AgentConfig{}}
			if tt.bashPreview {
				cfg.Agents[agentName] = config.AgentConfig{BashPreview: boolPtr(true)}
			}
			withBashPreviewDeps(t, cfg)

			t.Setenv("TMUX", "/tmp/fake-tmux-socket")
			sessionID := "sess-gate-selection-" + tt.name
			t.Setenv("AGENTCTL_SESSION_ID", sessionID)
			removeDispatchLockForTest(t, tt.command)
			withFakeAttachedClient(t, true)

			called := withFakeDispatchPopupRunner(t, func(popupW, popupH int, argsFilePath string) error {
				return nil
			})

			stdin := `{"tool_name":"Bash","tool_input":{"command":"` + tt.command + `"}}`
			envVars := map[string]string{
				"AGENTCTL":            "1",
				"AGENTCTL_AGENT_NAME": agentName,
				"AGENTCTL_WINDOW_ID":  "w99",
				"AGENTCTL_SESSION_ID": sessionID,
				"TMUX_PANE":           "",
			}
			runHookCaptured(t, envVars, stdin, ValidateBash)

			if *called != tt.wantGated {
				t.Errorf("popup called = %v, want %v", *called, tt.wantGated)
			}
		})
	}
}
