package hooks

// mailbox_deliver_test.go — tests for deliverMailbox and its wiring into
// PostTool (PostToolUse) and UserPrompt (UserPromptSubmit). See
// docs/agent-messaging-design.md §3.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// setupMailboxDeliverTest isolates the mailbox store and windowstate dirs
// for the duration of a test.
func setupMailboxDeliverTest(t *testing.T) string {
	t.Helper()
	mailboxDir := t.TempDir()
	mailbox.SetDirForTest(mailboxDir)
	t.Cleanup(func() { mailbox.SetDirForTest("") })
	setupWindowstate(t)
	return mailboxDir
}

// hookSpecificOutputFrom decodes stdout produced by a hook, returning the
// hookEventName and additionalContext fields. Fails the test if stdout is
// not valid JSON shaped like the hookSpecificOutput contract.
func hookSpecificOutputFrom(t *testing.T, stdout string) (eventName, additionalContext string) {
	t.Helper()
	var decoded struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("stdout is not valid hookSpecificOutput JSON: %v\nstdout: %q", err, stdout)
	}
	return decoded.HookSpecificOutput.HookEventName, decoded.HookSpecificOutput.AdditionalContext
}

// ---------------------------------------------------------------------------
// PostTool (PostToolUse)
// ---------------------------------------------------------------------------

// TestPostTool_DeliversPendingMailbox verifies PostTool emits valid
// hookSpecificOutput JSON with hookEventName=PostToolUse when mail is
// pending for the window.
func TestPostTool_DeliversPendingMailbox(t *testing.T) {
	setupMailboxDeliverTest(t)

	const windowID = "@70"
	if err := windowstate.Write(windowID, windowstate.WindowState{SessionID: "s1"}); err != nil {
		t.Fatalf("windowstate.Write: %v", err)
	}
	if _, err := mailbox.Enqueue(mailbox.Message{
		From:      "@1",
		To:        windowID,
		ToSession: "s1",
		Kind:      mailbox.KindFYI,
		Body:      "hello from a sibling agent",
	}); err != nil {
		t.Fatalf("mailbox.Enqueue: %v", err)
	}

	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_WINDOW_ID":  windowID,
		"AGENTCTL_AGENT_NAME": "go-engineer",
		"TMUX_PANE":           "",
		"AGENTCTL_SESSION_ID": "",
	}
	stdout, _ := runHookCaptured(t, envVars, `{"tool_name":"Read"}`, PostTool)

	eventName, ctx := hookSpecificOutputFrom(t, stdout)
	if eventName != "PostToolUse" {
		t.Errorf("hookEventName = %q, want %q", eventName, "PostToolUse")
	}
	if !strings.Contains(ctx, "hello from a sibling agent") {
		t.Errorf("additionalContext %q missing delivered message body", ctx)
	}

	// Delivered messages must be claimed (moved to read/), not left pending.
	pending, err := mailbox.Pending(windowID)
	if err != nil {
		t.Fatalf("mailbox.Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("Pending after delivery = %d, want 0", len(pending))
	}
}

// TestPostTool_NoOutputWhenMailboxEmpty verifies PostTool emits nothing on
// stdout when there is no mail to deliver -- PostToolUse fires on every
// tool call, so emitting JSON unconditionally would be needless noise.
func TestPostTool_NoOutputWhenMailboxEmpty(t *testing.T) {
	setupMailboxDeliverTest(t)

	const windowID = "@71"
	if err := windowstate.Write(windowID, windowstate.WindowState{SessionID: "s1"}); err != nil {
		t.Fatalf("windowstate.Write: %v", err)
	}

	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_WINDOW_ID":  windowID,
		"AGENTCTL_AGENT_NAME": "go-engineer",
		"TMUX_PANE":           "",
		"AGENTCTL_SESSION_ID": "",
	}
	stdout, _ := runHookCaptured(t, envVars, `{"tool_name":"Read"}`, PostTool)

	if stdout != "" {
		t.Errorf("expected no stdout from PostTool with an empty mailbox, got: %q", stdout)
	}
}

// TestPostTool_CorruptMessageNeverFailsHook verifies that a corrupt/
// unreadable message file sitting alongside a valid one never prevents
// delivery of the valid message and never panics the hook. mailbox.Pending
// already skips unparseable files (see listMessages); this asserts that
// guarantee holds through the whole PostTool call path.
func TestPostTool_CorruptMessageNeverFailsHook(t *testing.T) {
	mailboxDir := setupMailboxDeliverTest(t)

	const windowID = "@72"
	if err := windowstate.Write(windowID, windowstate.WindowState{SessionID: "s1"}); err != nil {
		t.Fatalf("windowstate.Write: %v", err)
	}
	if _, err := mailbox.Enqueue(mailbox.Message{
		From:      "@1",
		To:        windowID,
		ToSession: "s1",
		Kind:      mailbox.KindFYI,
		Body:      "the valid one",
	}); err != nil {
		t.Fatalf("mailbox.Enqueue: %v", err)
	}

	// Plant a corrupt (unparseable) message file directly into the window's
	// pending inbox, bypassing Enqueue.
	inboxDir := filepath.Join(mailboxDir, "@72")
	if err := os.WriteFile(filepath.Join(inboxDir, "zzz-corrupt.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("write corrupt message: %v", err)
	}

	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_WINDOW_ID":  windowID,
		"AGENTCTL_AGENT_NAME": "go-engineer",
		"TMUX_PANE":           "",
		"AGENTCTL_SESSION_ID": "",
	}

	// Must not panic despite the corrupt file.
	stdout, _ := runHookCaptured(t, envVars, `{"tool_name":"Read"}`, PostTool)

	_, ctx := hookSpecificOutputFrom(t, stdout)
	if !strings.Contains(ctx, "the valid one") {
		t.Errorf("additionalContext %q missing the valid message despite a corrupt sibling file", ctx)
	}
}

// ---------------------------------------------------------------------------
// UserPrompt (UserPromptSubmit)
// ---------------------------------------------------------------------------

// TestUserPrompt_DeliversPendingMailbox verifies UserPrompt emits valid
// hookSpecificOutput JSON with hookEventName=UserPromptSubmit when mail is
// pending -- this is the path that covers interactive/bare-mode windows the
// old tmux delivery design excluded entirely.
func TestUserPrompt_DeliversPendingMailbox(t *testing.T) {
	setupMailboxDeliverTest(t)

	const windowID = "@80"
	if err := windowstate.Write(windowID, windowstate.WindowState{SessionID: "s1", Mode: "bare"}); err != nil {
		t.Fatalf("windowstate.Write: %v", err)
	}
	if _, err := mailbox.Enqueue(mailbox.Message{
		From:      "@1",
		To:        windowID,
		ToSession: "s1",
		Kind:      mailbox.KindFYI,
		Body:      "waiting on you",
	}); err != nil {
		t.Fatalf("mailbox.Enqueue: %v", err)
	}

	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_WINDOW_ID":  windowID,
		"AGENTCTL_AGENT_NAME": "go-engineer",
		"TMUX_PANE":           "",
		"AGENTCTL_SESSION_ID": "",
	}
	stdout, _ := runHookCaptured(t, envVars, `{"prompt":"hi"}`, UserPrompt)

	eventName, ctx := hookSpecificOutputFrom(t, stdout)
	if eventName != "UserPromptSubmit" {
		t.Errorf("hookEventName = %q, want %q", eventName, "UserPromptSubmit")
	}
	if !strings.Contains(ctx, "waiting on you") {
		t.Errorf("additionalContext %q missing delivered message body", ctx)
	}
}

// TestUserPrompt_NoOutputWhenMailboxEmpty verifies UserPrompt emits nothing
// on stdout when there is no mail to deliver.
func TestUserPrompt_NoOutputWhenMailboxEmpty(t *testing.T) {
	setupMailboxDeliverTest(t)

	const windowID = "@81"
	if err := windowstate.Write(windowID, windowstate.WindowState{SessionID: "s1"}); err != nil {
		t.Fatalf("windowstate.Write: %v", err)
	}

	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_WINDOW_ID":  windowID,
		"AGENTCTL_AGENT_NAME": "go-engineer",
		"TMUX_PANE":           "",
		"AGENTCTL_SESSION_ID": "",
	}
	stdout, _ := runHookCaptured(t, envVars, `{"prompt":"hi"}`, UserPrompt)

	if stdout != "" {
		t.Errorf("expected no stdout from UserPrompt with an empty mailbox, got: %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// deliverMailbox directly
// ---------------------------------------------------------------------------

// TestDeliverMailbox_EmptyWindowIDNoOps verifies deliverMailbox is a no-op
// (no panic, no stdout) when windowID is empty -- the hook could not
// resolve its own window.
func TestDeliverMailbox_EmptyWindowIDNoOps(t *testing.T) {
	setupMailboxDeliverTest(t)

	stdout, _ := runHookCaptured(t, nil, "", func() {
		deliverMailbox("PostToolUse", "", "s1")
	})
	if stdout != "" {
		t.Errorf("expected no stdout for empty windowID, got: %q", stdout)
	}
}
