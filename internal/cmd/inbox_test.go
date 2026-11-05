package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/mailbox"
)

// isolateInboxEnv points the mailbox store at a fresh temp dir and forces
// signal.ResolveWindowID() to a deterministic value via AGENTCTL_WINDOW_ID,
// bypassing the real tmux/session lookups that would otherwise fire in this
// (tmux-managed) test environment.
func isolateInboxEnv(t *testing.T, windowID string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)

	dir := t.TempDir()
	mailbox.SetDirForTest(dir)
	t.Cleanup(func() { mailbox.SetDirForTest("") })
}

func TestRunInboxJSONEmptyMailboxIsEmptyArray(t *testing.T) {
	isolateInboxEnv(t, "@99")

	out := captureStdout(t, func() {
		if err := RunInbox(false, true); err != nil {
			t.Fatalf("RunInbox: %v", err)
		}
	})

	trimmed := strings.TrimSpace(out)
	if trimmed != "[]" {
		t.Errorf("RunInbox(false, true) on empty mailbox = %q, want %q", trimmed, "[]")
	}

	var rows []inboxJSONRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if len(rows) != 0 {
		t.Errorf("expected zero rows, got %d", len(rows))
	}
}

func TestRunInboxJSONNoWindowIDIsEmptyArray(t *testing.T) {
	isolateInboxEnv(t, "")

	out := captureStdout(t, func() {
		if err := RunInbox(false, true); err != nil {
			t.Fatalf("RunInbox should not error when window ID is unresolvable: %v", err)
		}
	})

	if strings.TrimSpace(out) != "[]" {
		t.Errorf("RunInbox with no window ID = %q, want %q", strings.TrimSpace(out), "[]")
	}
}

func TestRunInboxJSONShowsPendingNotRead(t *testing.T) {
	isolateInboxEnv(t, "@42")

	if _, err := mailbox.Enqueue(mailbox.Message{To: "@42", Kind: mailbox.KindFYI, Body: "pending msg"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	readID, err := mailbox.Enqueue(mailbox.Message{To: "@42", Kind: mailbox.KindDone, Body: "read msg"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := mailbox.MarkRead("@42", readID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	out := captureStdout(t, func() {
		if err := RunInbox(false, true); err != nil {
			t.Fatalf("RunInbox: %v", err)
		}
	})

	var rows []inboxJSONRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("RunInbox(all=false) returned %d rows, want 1 (only pending): %+v", len(rows), rows)
	}
	if rows[0].Body != "pending msg" {
		t.Errorf("rows[0].Body = %q, want %q", rows[0].Body, "pending msg")
	}
}

func TestRunInboxJSONAllIncludesRead(t *testing.T) {
	isolateInboxEnv(t, "@43")

	if _, err := mailbox.Enqueue(mailbox.Message{To: "@43", Kind: mailbox.KindFYI, Body: "pending msg"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	readID, err := mailbox.Enqueue(mailbox.Message{To: "@43", Kind: mailbox.KindDone, Body: "read msg"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := mailbox.MarkRead("@43", readID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	out := captureStdout(t, func() {
		if err := RunInbox(true, true); err != nil {
			t.Fatalf("RunInbox: %v", err)
		}
	})

	var rows []inboxJSONRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("RunInbox(all=true) returned %d rows, want 2: %+v", len(rows), rows)
	}
}

func TestRunInboxHumanReadableEmptyMailbox(t *testing.T) {
	isolateInboxEnv(t, "@44")

	out := captureStdout(t, func() {
		if err := RunInbox(false, false); err != nil {
			t.Fatalf("RunInbox: %v", err)
		}
	})

	if !strings.Contains(out, "no messages") {
		t.Errorf("expected empty-mailbox message, got %q", out)
	}
}

func TestRunInboxNoWindowIDHumanReadableErrors(t *testing.T) {
	isolateInboxEnv(t, "")

	err := RunInbox(false, false)
	if err == nil {
		t.Error("RunInbox(false, false) with no resolvable window ID should error in human-readable mode")
	}
}

// ── RunInboxWait ─────────────────────────────────────────────────────────

func TestRunInboxWait_AlreadyPendingBeforeWatchReturnsImmediately(t *testing.T) {
	isolateInboxEnv(t, "@60")

	if _, err := mailbox.Enqueue(mailbox.Message{From: "@1", To: "@60", Kind: mailbox.KindFYI, Body: "already here"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	start := time.Now()
	out := captureStdout(t, func() {
		if err := RunInboxWait("", 2*time.Second); err != nil {
			t.Fatalf("RunInboxWait: %v", err)
		}
	})
	elapsed := time.Since(start)

	if !strings.Contains(out, "already here") {
		t.Errorf("RunInboxWait output = %q, want it to contain the pending message", out)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("RunInboxWait took %s for already-pending mail, want near-instant", elapsed)
	}

	pending, err := mailbox.Pending("@60")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("Pending after RunInboxWait = %d, want 0 (message must be claimed)", len(pending))
	}
}

func TestRunInboxWait_ArrivesAfterWatchStarts(t *testing.T) {
	isolateInboxEnv(t, "@61")

	go func() {
		time.Sleep(100 * time.Millisecond)
		if _, err := mailbox.Enqueue(mailbox.Message{From: "@1", To: "@61", Kind: mailbox.KindFYI, Body: "arrived late"}); err != nil {
			t.Errorf("Enqueue: %v", err)
		}
	}()

	out := captureStdout(t, func() {
		if err := RunInboxWait("", 5*time.Second); err != nil {
			t.Fatalf("RunInboxWait: %v", err)
		}
	})

	if !strings.Contains(out, "arrived late") {
		t.Errorf("RunInboxWait output = %q, want it to contain the late-arriving message", out)
	}
}

func TestRunInboxWait_TimesOutExitsCleanly(t *testing.T) {
	isolateInboxEnv(t, "@62")

	out := captureStdout(t, func() {
		if err := RunInboxWait("", 200*time.Millisecond); err != nil {
			t.Fatalf("RunInboxWait on timeout should not return an error, got: %v", err)
		}
	})

	if !strings.Contains(out, "timed out") {
		t.Errorf("RunInboxWait timeout output = %q, want a clear timeout line", out)
	}
}

func TestRunInboxWait_FromFilterLeavesNonMatchingPending(t *testing.T) {
	isolateInboxEnv(t, "@63")

	if _, err := mailbox.Enqueue(mailbox.Message{From: "@99", To: "@63", Kind: mailbox.KindFYI, Body: "wrong sender"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := mailbox.Enqueue(mailbox.Message{From: "@12", To: "@63", Kind: mailbox.KindFYI, Body: "right sender"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	out := captureStdout(t, func() {
		if err := RunInboxWait("@12", time.Second); err != nil {
			t.Fatalf("RunInboxWait: %v", err)
		}
	})

	if !strings.Contains(out, "right sender") {
		t.Errorf("RunInboxWait --from output = %q, want the matching message", out)
	}
	if strings.Contains(out, "wrong sender") {
		t.Errorf("RunInboxWait --from output = %q, must not print the non-matching message", out)
	}

	pending, err := mailbox.Pending("@63")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 || pending[0].From != "@99" {
		t.Fatalf("Pending after RunInboxWait --from = %+v, want the non-matching @99 message still pending", pending)
	}
}

func TestRunInboxWait_NoWindowIDErrors(t *testing.T) {
	isolateInboxEnv(t, "")

	if err := RunInboxWait("", time.Second); err == nil {
		t.Error("RunInboxWait with no resolvable window ID should error")
	}
}

// ── resolveFromFilterWithAgents ─────────────────────────────────────────

func TestResolveFromFilterWithAgents(t *testing.T) {
	tests := []struct {
		name          string
		from          string
		runningAgents map[string]string
		want          string
	}{
		{name: "empty passthrough", from: "", want: ""},
		{name: "window ID passthrough", from: "@41", want: "@41"},
		{
			name:          "agent name resolves to window ID",
			from:          "go-engineer",
			runningAgents: map[string]string{"@10": "go-engineer"},
			want:          "@10",
		},
		{
			name:          "unresolvable agent name falls back to raw value",
			from:          "long-gone-agent",
			runningAgents: map[string]string{"@10": "go-engineer"},
			want:          "long-gone-agent",
		},
		{
			name: "ambiguous agent name falls back to raw value",
			from: "go-engineer",
			runningAgents: map[string]string{
				"@10": "go-engineer",
				"@20": "go-engineer:2",
			},
			want: "go-engineer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveFromFilterWithAgents(tt.from, tt.runningAgents)
			if got != tt.want {
				t.Errorf("resolveFromFilterWithAgents(%q, %v) = %q, want %q", tt.from, tt.runningAgents, got, tt.want)
			}
		})
	}
}

// Sanity check that mailbox files actually land under the isolated temp dir,
// not the developer's real .agentctl/inbox.
func TestIsolateInboxEnvDoesNotTouchRealHome(t *testing.T) {
	dir := t.TempDir()
	mailbox.SetDirForTest(dir)
	t.Cleanup(func() { mailbox.SetDirForTest("") })

	if _, err := mailbox.Enqueue(mailbox.Message{To: "@1", Kind: mailbox.KindFYI, Body: "x"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "@1")); err != nil {
		t.Errorf("expected message written under isolated dir %s: %v", dir, err)
	}
}
