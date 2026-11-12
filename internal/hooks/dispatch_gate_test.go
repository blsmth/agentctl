package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// withFakeDispatchPopupRunner replaces dispatchPopupRunner for the duration
// of a test and restores the original afterward. The fake records whether it
// was invoked so tests asserting "no popup shown" can fail loudly instead of
// silently passing for the wrong reason.
func withFakeDispatchPopupRunner(t *testing.T, run func(popupW, popupH int, argsFilePath string) error) *bool {
	t.Helper()
	called := new(bool)
	orig := dispatchPopupRunner
	dispatchPopupRunner = func(_ context.Context, popupW, popupH int, argsFilePath string) error {
		*called = true
		return run(popupW, popupH, argsFilePath)
	}
	t.Cleanup(func() { dispatchPopupRunner = orig })
	return called
}

// failIfDispatchPopupRunnerCalled installs a dispatchPopupRunner that fails
// the test outright if invoked -- for asserting a popup must never appear.
func failIfDispatchPopupRunnerCalled(t *testing.T) {
	t.Helper()
	orig := dispatchPopupRunner
	dispatchPopupRunner = func(_ context.Context, popupW, popupH int, argsFilePath string) error {
		t.Fatalf("dispatchPopupRunner must not be called")
		return nil
	}
	t.Cleanup(func() { dispatchPopupRunner = orig })
}

// exitError1 returns a genuine *exec.ExitError with exit code 1, without
// shelling out to tmux -- exercising the same error type dispatchPreviewGate
// checks for from a real cmd.Run().
func exitError1(t *testing.T) error {
	t.Helper()
	return exitErrorCode(t, 1)
}

// exitErrorCode returns a genuine *exec.ExitError with the given exit code,
// without shelling out to tmux -- used to exercise the "unexpected exit
// code" branch (anything other than 0 or 1) in dispatchPreviewGate /
// bashPreviewGate, which today falls through to "passing through" rather
// than being treated as either an explicit approve or an explicit deny.
func exitErrorCode(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %T (%v)", err, err)
	}
	return err
}

// ---------------------------------------------------------------------------
// denyBashPopup — flat deny JSON schema
// ---------------------------------------------------------------------------

func TestDenyBashPopup_OutputSchema(t *testing.T) {
	tests := []struct {
		name      string
		agentName string
		command   string
	}{
		{
			name:      "typical dispatch command",
			agentName: "go-engineer",
			command:   "agentctl dispatch --issue 42 --agent go-engineer",
		},
		{
			name:      "empty agent name",
			agentName: "",
			command:   "agentctl dispatch --issue 1",
		},
		{
			name:      "long command",
			agentName: "python-engineer",
			command:   "agentctl dispatch --prompt-key spec-123 --agent python-engineer --issue 999",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _, _ := captureDenyEmitter(t, func() {
				denyBashPopup(tt.agentName, tt.command, true, denyAuditContext{})
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
				t.Errorf("permissionDecision = %v, want %q", decision, "deny")
			}

			reason, _ := hso["permissionDecisionReason"].(string)
			if reason == "" {
				t.Errorf("permissionDecisionReason must be non-empty")
			}

			// hookEventName must be present and set to "PreToolUse" --
			// required as of Claude Code 2.1.231, or Claude Code shows
			// "hook error" instead of the deny reason.
			if got := hso["hookEventName"]; got != "PreToolUse" {
				t.Errorf("hookEventName = %v, want %q", got, "PreToolUse")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// dispatchPreviewGate — approval / dedupe / stale-lock
// ---------------------------------------------------------------------------

func TestDispatchPreviewGate_NotInTmux_StaysSilent(t *testing.T) {
	t.Setenv("TMUX", "")
	failIfDispatchPopupRunnerCalled(t)

	approved, decided, _ := dispatchPreviewGate("go-engineer", "agentctl dispatch --issue 1")
	if decided {
		t.Errorf("decided = true, want false when not in tmux")
	}
	if approved {
		t.Errorf("approved = true, want false when not decided")
	}
}

func TestDispatchPreviewGate_PopupApproves(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-approve-1")
	command := "agentctl dispatch --issue 1 --agent go-engineer"

	// Ensure a clean lock for this session/command pair.
	removeDispatchLockForTest(t, command)

	called := withFakeDispatchPopupRunner(t, func(popupW, popupH int, argsFilePath string) error {
		return nil // exit 0 == approved
	})

	approved, decided, _ := dispatchPreviewGate("go-engineer", command)
	if !*called {
		t.Fatalf("expected dispatchPopupRunner to be called")
	}
	if !decided {
		t.Fatalf("decided = false, want true")
	}
	if !approved {
		t.Fatalf("approved = false, want true")
	}
}

func TestDispatchPreviewGate_PopupDenies(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-deny-1")
	command := "agentctl dispatch --issue 2 --agent go-engineer"

	removeDispatchLockForTest(t, command)

	withFakeDispatchPopupRunner(t, func(popupW, popupH int, argsFilePath string) error {
		return exitError1(t)
	})

	approved, decided, _ := dispatchPreviewGate("go-engineer", command)
	if !decided {
		t.Fatalf("decided = false, want true")
	}
	if approved {
		t.Fatalf("approved = true, want false (exit code 1 means deny)")
	}
}

// TestDispatchPreviewGate_LockAlreadyHeld_LoserStaysSilent verifies the
// dedupe path: with the lock already held, the gate must return
// decided=false and produce zero bytes of output, since a stray newline is
// still a parse target for Claude Code.
func TestDispatchPreviewGate_LockAlreadyHeld_LoserStaysSilent(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-locked-1")
	command := "agentctl dispatch --issue 3 --agent go-engineer"

	removeDispatchLockForTest(t, command)
	path := gateLockPath(command)
	if !createGateLockFile(path) {
		t.Fatalf("failed to pre-create lock file at %s", path)
	}
	t.Cleanup(func() { os.Remove(path) })

	failIfDispatchPopupRunnerCalled(t)

	raw := captureStdout(t, func() {
		approved, decided, _ := dispatchPreviewGate("go-engineer", command)
		if decided {
			t.Errorf("decided = true, want false when lock is already held")
		}
		if approved {
			t.Errorf("approved = true, want false when lock is already held")
		}
	})

	if len(raw) != 0 {
		t.Errorf("loser wrote %d bytes to stdout, want zero: %q", len(raw), raw)
	}
}

// TestDispatchPreviewGate_StaleLockIsReclaimed verifies a lock file with an
// mtime older than gateStaleAfter is treated as abandoned: it gets
// removed and the popup path is entered.
func TestDispatchPreviewGate_StaleLockIsReclaimed(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-stale-1")
	command := "agentctl dispatch --issue 4 --agent go-engineer"

	removeDispatchLockForTest(t, command)
	path := gateLockPath(command)
	if !createGateLockFile(path) {
		t.Fatalf("failed to pre-create lock file at %s", path)
	}
	t.Cleanup(func() { os.Remove(path) })

	stale := time.Now().Add(-20 * time.Minute)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	called := withFakeDispatchPopupRunner(t, func(popupW, popupH int, argsFilePath string) error {
		return nil
	})

	approved, decided, _ := dispatchPreviewGate("go-engineer", command)
	if !*called {
		t.Fatalf("expected the stale lock to be reclaimed and the popup path entered")
	}
	if !decided || !approved {
		t.Errorf("approved=%v decided=%v, want approved=true decided=true", approved, decided)
	}
}

// TestDispatchPreviewGate_FreshLockIsNotReclaimed is the inverse of the
// stale-lock test: a lock file younger than gateStaleAfter must NOT
// be reclaimed, even though it looks identical apart from mtime.
func TestDispatchPreviewGate_FreshLockIsNotReclaimed(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-fresh-1")
	command := "agentctl dispatch --issue 5 --agent go-engineer"

	removeDispatchLockForTest(t, command)
	path := gateLockPath(command)
	if !createGateLockFile(path) {
		t.Fatalf("failed to pre-create lock file at %s", path)
	}
	t.Cleanup(func() { os.Remove(path) })

	failIfDispatchPopupRunnerCalled(t)

	approved, decided, _ := dispatchPreviewGate("go-engineer", command)
	if decided || approved {
		t.Errorf("approved=%v decided=%v, want both false (fresh lock must not be reclaimed)", approved, decided)
	}
}

// removeDispatchLockForTest removes any leftover lock file for command under
// the current AGENTCTL_SESSION_ID, so tests start from a clean slate.
func removeDispatchLockForTest(t *testing.T, command string) {
	t.Helper()
	path := gateLockPath(command)
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })
}

// ---------------------------------------------------------------------------
// gateLockPath — hashing
// ---------------------------------------------------------------------------

func TestDispatchGateLockPath_DiffersByCommand(t *testing.T) {
	t.Setenv("AGENTCTL_SESSION_ID", "same-session")

	p1 := gateLockPath("agentctl dispatch --issue 1")
	p2 := gateLockPath("agentctl dispatch --issue 2")

	if p1 == p2 {
		t.Errorf("lock paths must differ for different commands in the same session, both = %q", p1)
	}
}

func TestDispatchGateLockPath_DiffersBySession(t *testing.T) {
	const command = "agentctl dispatch --issue 1"

	t.Setenv("AGENTCTL_SESSION_ID", "session-a")
	p1 := gateLockPath(command)

	t.Setenv("AGENTCTL_SESSION_ID", "session-b")
	p2 := gateLockPath(command)

	if p1 == p2 {
		t.Errorf("lock paths must differ for the same command across different session ids, both = %q", p1)
	}
}

func TestDispatchGateLockPath_StableForSameInputs(t *testing.T) {
	t.Setenv("AGENTCTL_SESSION_ID", "session-stable")
	const command = "agentctl dispatch --issue 1"

	p1 := gateLockPath(command)
	p2 := gateLockPath(command)

	if p1 != p2 {
		t.Errorf("lock path must be stable for identical inputs, got %q and %q", p1, p2)
	}

	// Sanity check: the path is derived from a sha256 hex digest, not from
	// the raw session id or command (which could contain path separators).
	dir := filepath.Dir(p1)
	if dir != filepath.Clean(os.TempDir()) {
		t.Errorf("lock path %q not under os.TempDir() %q", p1, os.TempDir())
	}
	sum := sha256.Sum256([]byte("session-stable" + "\x00" + command))
	wantSuffix := "agentctl-dispatch-gate-" + hexEncode(sum[:]) + ".lock"
	if filepath.Base(p1) != wantSuffix {
		t.Errorf("lock path base = %q, want %q", filepath.Base(p1), wantSuffix)
	}
}

// hexEncode mirrors the "%x" formatting used by gateLockPath.
func hexEncode(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexDigits[c>>4]
		out[i*2+1] = hexDigits[c&0x0f]
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// ValidateBash: deny-list checks run BEFORE the dispatch gate
// ---------------------------------------------------------------------------

// TestValidateBash_BlockedDispatchCommand_NeverShowsPopup verifies that a
// dispatch command blocked by a per-agent deny rule is denied via the normal
// PreToolUse deny path, and the dispatch preview gate (and therefore the
// popup) is never reached.
func TestValidateBash_BlockedDispatchCommand_NeverShowsPopup(t *testing.T) {
	const agentName = "go-engineer"
	const command = "agentctl dispatch --issue 1 --agent go-engineer"

	d := DefaultDeps()
	d.LoadConfig = func() *config.Config {
		return &config.Config{
			Agents: map[string]config.AgentConfig{
				agentName: {BlockedCommands: []string{"dispatch"}},
			},
		}
	}
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	// Even if we were in tmux, no popup must ever be launched -- the deny
	// check must short-circuit before dispatchPreviewGate runs.
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	failIfDispatchPopupRunnerCalled(t)

	stdin := `{"tool_name":"Bash","tool_input":{"command":"` + command + `"}}`
	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_AGENT_NAME": agentName,
		"AGENTCTL_WINDOW_ID":  "w99",
		"AGENTCTL_SESSION_ID": "",
		"TMUX_PANE":           "",
	}

	out, _ := runHookCaptured(t, envVars, stdin, ValidateBash)

	var got map[string]interface{}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal ValidateBash output: %v\nraw: %s", err, out)
	}
	hso, ok := got["hookSpecificOutput"].(map[string]interface{})
	if !ok {
		t.Fatalf("hookSpecificOutput missing or wrong type in: %s", out)
	}
	if decision := hso["permissionDecision"]; decision != "deny" {
		t.Errorf("permissionDecision = %v, want %q", decision, "deny")
	}
}

// TestValidateBash_ApprovedDispatchCommand_EmitsAllowDecision is the
// regression test for the bug this change fixes: approving the dispatch
// popup used to fall through to ValidateBash's silent "allow by omission"
// path, which is correct for the deny-list checks but WRONG here -- Claude
// Code has no way to distinguish "PreToolUse passed through" from "a human
// just approved this in a popup", so it went on to show its OWN permission
// prompt for the same command, and the human approved twice. ValidateBash
// must emit an explicit flat "allow" decision on this path, and ONLY this
// path -- see allowCommand's doc comment for the safety invariant.
func TestValidateBash_ApprovedDispatchCommand_EmitsAllowDecision(t *testing.T) {
	const agentName = "go-engineer"
	const command = "agentctl dispatch --issue 77 --agent go-engineer"

	d := DefaultDeps()
	d.LoadConfig = func() *config.Config { return &config.Config{} }
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	sessionID := "sess-validate-approve-1"
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", sessionID)
	removeDispatchLockForTest(t, command)

	withFakeDispatchPopupRunner(t, func(popupW, popupH int, argsFilePath string) error {
		return nil
	})

	stdin := `{"tool_name":"Bash","tool_input":{"command":"` + command + `"}}`
	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_AGENT_NAME": agentName,
		"AGENTCTL_WINDOW_ID":  "w99",
		"AGENTCTL_SESSION_ID": sessionID,
		"TMUX_PANE":           "",
	}

	out, _ := runHookCaptured(t, envVars, stdin, ValidateBash)

	var got map[string]interface{}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal ValidateBash output: %v\nraw: %s", err, out)
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
}

// TestValidateBash_DispatchGateLockContention_EmitsNoOutput exercises the
// dedupe path end-to-end through ValidateBash: with the lock already held
// (simulating the duplicate hook invocation race), ValidateBash must emit
// zero bytes -- a stray newline is still a parse target for Claude Code.
func TestValidateBash_DispatchGateLockContention_EmitsNoOutput(t *testing.T) {
	const agentName = "go-engineer"
	const command = "agentctl dispatch --issue 88 --agent go-engineer"

	d := DefaultDeps()
	d.LoadConfig = func() *config.Config { return &config.Config{} }
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	sessionID := "sess-validate-contention-1"
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", sessionID)
	removeDispatchLockForTest(t, command)

	// Pre-hold the lock, as if another invocation of the duplicate hook
	// registration got there first.
	path := gateLockPath(command)
	if !createGateLockFile(path) {
		t.Fatalf("failed to pre-create lock file at %s", path)
	}
	t.Cleanup(func() { os.Remove(path) })

	failIfDispatchPopupRunnerCalled(t)

	stdin := `{"tool_name":"Bash","tool_input":{"command":"` + command + `"}}`
	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_AGENT_NAME": agentName,
		"AGENTCTL_WINDOW_ID":  "w99",
		"AGENTCTL_SESSION_ID": sessionID,
		"TMUX_PANE":           "",
	}

	out, _ := runHookCaptured(t, envVars, stdin, ValidateBash)
	if len(out) != 0 {
		t.Errorf("expected zero bytes of stdout when the dispatch gate lock is contended, got %d bytes: %q", len(out), out)
	}
}
