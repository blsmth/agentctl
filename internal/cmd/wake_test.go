package cmd

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// setupWakeTest isolates both the mailbox and windowstate directories to a
// fresh temp dir for the duration of the test, and stubs the tmux/reaper
// seams so no real tmux session or process table is ever touched.
func setupWakeTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	mailbox.SetDirForTest(filepath.Join(dir, "inbox"))
	windowstate.SetDirForTest(filepath.Join(dir, "windows"))
	t.Cleanup(func() {
		mailbox.SetDirForTest("")
		windowstate.SetDirForTest("")
	})

	origPID, origClaude, origInject, origCheck := wakeMainPanePID, wakeHasClaude, wakeInjectPrompt, wakeCheckInject
	wakeCheckInject = func(string) error { return nil }
	wakeMainPanePID = func(string) (int, bool) { return 123, true }
	wakeHasClaude = func(int) bool { return true }
	wakeInjectPrompt = func(string, string) error { return nil }
	t.Cleanup(func() {
		wakeMainPanePID, wakeHasClaude, wakeInjectPrompt, wakeCheckInject = origPID, origClaude, origInject, origCheck
	})
}

func writeState(t *testing.T, windowID, state, sessionID string) {
	t.Helper()
	if err := windowstate.Write(windowID, windowstate.WindowState{
		State:     state,
		SessionID: sessionID,
	}); err != nil {
		t.Fatalf("windowstate.Write: %v", err)
	}
}

func enqueueMail(t *testing.T, to, toSession, body string) string {
	t.Helper()
	id, err := mailbox.Enqueue(mailbox.Message{
		From:      "@1",
		To:        to,
		ToSession: toSession,
		Kind:      mailbox.KindFYI,
		Body:      body,
	})
	if err != nil {
		t.Fatalf("mailbox.Enqueue: %v", err)
	}
	return id
}

func TestDoWake_DeliversAndReportsCount(t *testing.T) {
	setupWakeTest(t)

	writeState(t, "@50", "idle", "session-1")
	enqueueMail(t, "@50", "session-1", "hello from @1")
	enqueueMail(t, "@50", "session-1", "second message")

	msg, err := doWake("@50")
	if err != nil {
		t.Fatalf("doWake: unexpected error: %v", err)
	}
	if !strings.Contains(msg, "2") || !strings.Contains(msg, "@50") {
		t.Errorf("doWake message = %q, want it to mention 2 messages and @50", msg)
	}

	pending, err := mailbox.Pending("@50")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("Pending after doWake = %d, want 0 (delivered mail must be claimed)", len(pending))
	}
	read, err := mailbox.Read("@50")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read) != 2 {
		t.Errorf("Read after doWake = %d, want 2", len(read))
	}
}

func TestDoWake_NoPendingMailReportsPlainly(t *testing.T) {
	setupWakeTest(t)

	writeState(t, "@51", "idle", "session-1")

	msg, err := doWake("@51")
	if err != nil {
		t.Fatalf("doWake: unexpected error: %v", err)
	}
	if !strings.Contains(msg, "No pending mail") {
		t.Errorf("doWake message = %q, want it to say there's nothing to deliver", msg)
	}
}

func TestDoWake_RefusesWhenWorking(t *testing.T) {
	setupWakeTest(t)

	writeState(t, "@52", "working", "session-1")
	enqueueMail(t, "@52", "session-1", "hello")

	injectCalled := false
	wakeInjectPrompt = func(string, string) error { injectCalled = true; return nil }

	_, err := doWake("@52")
	if err == nil {
		t.Fatal("doWake: expected an error refusing to wake a working agent")
	}
	if !strings.Contains(err.Error(), "working") {
		t.Errorf("error %q should explain the window is still working", err.Error())
	}
	if injectCalled {
		t.Error("injection must never be attempted for a working agent")
	}

	// The refusal must happen before any drain, so the mail stays pending
	// (not silently claimed and dropped by a refusal that runs too late).
	pending, err := mailbox.Pending("@52")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("Pending after refused wake = %d, want 1 (mail must not be drained)", len(pending))
	}
}

func TestDoWake_RefusesWhenNoClaudeDescendant(t *testing.T) {
	setupWakeTest(t)
	wakeHasClaude = func(int) bool { return false }

	writeState(t, "@53", "idle", "session-1")
	enqueueMail(t, "@53", "session-1", "hello")

	injectCalled := false
	wakeInjectPrompt = func(string, string) error { injectCalled = true; return nil }

	_, err := doWake("@53")
	if err == nil {
		t.Fatal("doWake: expected an error refusing to wake a window with no live claude process")
	}
	if !strings.Contains(err.Error(), "shell") {
		t.Errorf("error %q should explain the agent looks like it exited to a shell", err.Error())
	}
	if injectCalled {
		t.Error("injection must never be attempted with no live claude descendant")
	}

	pending, err := mailbox.Pending("@53")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("Pending after refused wake = %d, want 1 (mail must not be drained)", len(pending))
	}
}

func TestDoWake_RefusesWhenMainPaneUnresolvable(t *testing.T) {
	setupWakeTest(t)
	wakeMainPanePID = func(string) (int, bool) { return 0, false }

	writeState(t, "@54", "idle", "session-1")

	_, err := doWake("@54")
	if err == nil {
		t.Fatal("doWake: expected an error when the main pane cannot be resolved")
	}
}

// TestDoWake_InjectionFailureLeavesNoRecourseButLogsLoudly verifies that a
// failed injection surfaces as an error (RunWake exits nonzero on it) even
// though the mail was already claimed by DrainForIDs before injection was
// attempted -- see the comment in doWake on this tradeoff.
func TestDoWake_InjectionFailureReturnsError(t *testing.T) {
	setupWakeTest(t)
	wakeInjectPrompt = func(string, string) error { return errors.New("boom") }

	writeState(t, "@55", "idle", "session-1")
	enqueueMail(t, "@55", "session-1", "hello")

	_, err := doWake("@55")
	if err == nil {
		t.Fatal("doWake: expected the injection failure to surface as an error")
	}
	if !strings.Contains(err.Error(), "@55") {
		t.Errorf("error %q should mention the window", err.Error())
	}

	// The mail is already claimed -- this is the documented tradeoff, not a
	// bug: re-attempting delivery on every injection failure would
	// re-deliver duplicates on every subsequent wake once the underlying
	// tmux problem is fixed.
	pending, err := mailbox.Pending("@55")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("Pending after failed injection = %d, want 0 (already claimed before injection was attempted)", len(pending))
	}
}

// TestDoWake_UnknownAddrErrorsFromResolveSendAddress verifies wake does not
// reimplement address resolution -- unknown/ambiguous addresses must
// surface exactly the errors resolveSendAddress already produces (see the
// exhaustive coverage in TestResolveSendAddress, send_test.go).
func TestDoWake_UnknownAddrErrorsFromResolveSendAddress(t *testing.T) {
	setupWakeTest(t)

	_, err := doWake("no-such-agent")
	if err == nil {
		t.Fatal("doWake: expected an error for an unknown agent name")
	}
	if !strings.Contains(err.Error(), "no-such-agent") {
		t.Errorf("error %q should name the unresolved address", err.Error())
	}
}

func TestDoWake_UnwrittenWindowStateIsNotTreatedAsWorking(t *testing.T) {
	setupWakeTest(t)

	// A bare "@N" address always resolves without consulting running
	// agents (see resolveSendAddress), and windowstate.Read on a window
	// that was never written returns a zero-value WindowState and a nil
	// error. This documents that doWake treats a zero-value state
	// (State == "") as not "working" and proceeds through the liveness
	// check to the drain, rather than erroring outright -- there is
	// simply nothing pending for a window nothing has ever addressed.
	msg, err := doWake("@does-not-exist")
	if err != nil {
		t.Fatalf("doWake on an unwritten window: unexpected error: %v", err)
	}
	if !strings.Contains(msg, "No pending mail") {
		t.Errorf("doWake message = %q, want it to say there's nothing to deliver", msg)
	}
}

// Regression: a dialog in the pane must stop delivery before mail
// is claimed, so the message is neither injected nor lost.
func TestDoWake_RefusesWhenDialogShowing(t *testing.T) {
	setupWakeTest(t)

	writeState(t, "@53", "idle", "session-1")
	enqueueMail(t, "@53", "session-1", "hello")

	wakeCheckInject = func(string) error { return tmux.ErrDialogBlocking }
	injectCalled := false
	wakeInjectPrompt = func(string, string) error { injectCalled = true; return nil }

	_, err := doWake("@53")
	if !errors.Is(err, tmux.ErrDialogBlocking) {
		t.Fatalf("err = %v, want ErrDialogBlocking", err)
	}
	if injectCalled {
		t.Error("must not inject while a dialog is showing")
	}
	pending, err := mailbox.Pending("@53")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("Pending = %d, want 1 (mail must stay unclaimed)", len(pending))
	}
}
