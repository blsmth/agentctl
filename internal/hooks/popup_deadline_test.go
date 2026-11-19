package hooks

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withBlockingPopup installs a runner that blocks until ctx is cancelled, a
// tiny deadline, and a recording closer. Returns the close-call counter.
func withBlockingPopup(t *testing.T) *int {
	t.Helper()
	origRun, origDL, origClose := dispatchPopupRunner, popupDeadline, popupCloser
	t.Cleanup(func() { dispatchPopupRunner, popupDeadline, popupCloser = origRun, origDL, origClose })
	popupDeadline = 50 * time.Millisecond
	closes := new(int)
	popupCloser = func(string) error { *closes++; return nil }
	dispatchPopupRunner = func(ctx context.Context, _ string, _, _ int, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	return closes
}

func TestDispatchGateTimeoutDenies(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", t.Name())
	closes := withBlockingPopup(t)
	approved, decided, reason := dispatchPreviewGate("a", "agentctl dispatch --agent go-engineer --prompt hi", "")
	if approved || decided || !strings.Contains(reason, "timed out") {
		t.Fatalf("got approved=%v decided=%v reason=%q", approved, decided, reason)
	}
	if *closes != 1 {
		t.Fatalf("popup closer called %d times, want 1", *closes)
	}
}

func TestBashGateTimeoutDenies(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", t.Name())
	origAtt := hasAttachedClient
	hasAttachedClient = func() bool { return true }
	t.Cleanup(func() { hasAttachedClient = origAtt })
	closes := withBlockingPopup(t)
	approved, decided, reason := bashPreviewGate("a", "go build ./...")
	if approved || decided || !strings.Contains(reason, "timed out") {
		t.Fatalf("got approved=%v decided=%v reason=%q", approved, decided, reason)
	}
	if *closes != 1 {
		t.Fatalf("popup closer called %d times, want 1", *closes)
	}
}

func TestValidateBashHookRegistrationTimeout(t *testing.T) {
	cfg := BuildHooksConfig("agentctl")
	pre := cfg["PreToolUse"].([]interface{})
	var found bool
	for _, e := range pre {
		m := e.(map[string]interface{})
		h := m["hooks"].([]interface{})[0].(map[string]interface{})
		if h["command"] != "agentctl hook-validate-bash" {
			continue
		}
		found = true
		secs, ok := h["timeout"].(int)
		if !ok || secs != validateBashTimeoutSeconds {
			t.Fatalf("timeout = %v, want %d", h["timeout"], validateBashTimeoutSeconds)
		}
		if popupDeadline >= time.Duration(secs)*time.Second {
			t.Fatalf("popupDeadline %v must be < registered timeout %ds", popupDeadline, secs)
		}
	}
	if !found {
		t.Fatal("hook-validate-bash not registered")
	}
}

// stubPopupSeams installs a closer, a short deadline and retry interval, and a
// private deps with a no-op Notify. The runner is set by the caller.
func stubPopupSeams(t *testing.T, deadline time.Duration) {
	t.Helper()
	origRun, origDL, origClose, origRetry := dispatchPopupRunner, popupDeadline, popupCloser, popupRetryInterval
	origDeps := deps
	t.Cleanup(func() {
		dispatchPopupRunner, popupDeadline, popupCloser, popupRetryInterval = origRun, origDL, origClose, origRetry
		deps = origDeps
	})
	popupDeadline = deadline
	popupRetryInterval = 5 * time.Millisecond
	popupCloser = func(string) error { return nil }
	deps = stubDeps(t)
}

func runTestPopup(t *testing.T) (approved, decided bool, reason string) {
	t.Helper()
	return runPopupWithDeadline(popupTarget{}, "dispatch", "test", 10, 10, filepath.Join(t.TempDir(), "args"),
		popupNotice{Agent: "a", Tool: "dispatch", Action: "x"})
}

// tmux display-popup exits 0 without running the command when another overlay
// is open; that must never count as approval.
func TestPopupExitZeroWithoutResultDoesNotApprove(t *testing.T) {
	stubPopupSeams(t, 60*time.Millisecond)
	calls := 0
	dispatchPopupRunner = func(context.Context, string, int, int, string) error { calls++; return nil }
	approved, decided, reason := runTestPopup(t)
	if approved || decided || !strings.Contains(reason, "timed out") {
		t.Fatalf("approved=%v decided=%v reason=%q", approved, decided, reason)
	}
	if calls < 2 {
		t.Fatalf("expected retries, runner called %d times", calls)
	}
}

func TestPopupCollisionThenSuccessApproves(t *testing.T) {
	for _, tc := range []struct {
		verdict  string
		approved bool
	}{{"allow", true}, {"deny", false}} {
		t.Run(tc.verdict, func(t *testing.T) {
			stubPopupSeams(t, 5*time.Second)
			calls := 0
			dispatchPopupRunner = func(_ context.Context, _ string, _, _ int, argsFile string) error {
				calls++
				if calls < 3 {
					return nil // collision: exit 0, nothing ran
				}
				return WritePopupResult(popupResultPath(argsFile), tc.verdict)
			}
			approved, decided, reason := runTestPopup(t)
			if approved != tc.approved || !decided || reason != "" || calls != 3 {
				t.Fatalf("approved=%v decided=%v reason=%q calls=%d", approved, decided, reason, calls)
			}
		})
	}
}

func TestPopupStaleResultIgnored(t *testing.T) {
	stubPopupSeams(t, 40*time.Millisecond)
	dispatchPopupRunner = func(_ context.Context, _ string, _, _ int, argsFile string) error { return nil }
	args := filepath.Join(t.TempDir(), "args")
	if err := WritePopupResult(popupResultPath(args), "allow"); err != nil {
		t.Fatal(err)
	}
	approved, decided, _ := runPopupWithDeadline(popupTarget{}, "dispatch", "test", 1, 1, args, popupNotice{Agent: "a", Tool: "dispatch", Action: "x"})
	if approved || decided {
		t.Fatalf("stale result approved: approved=%v decided=%v", approved, decided)
	}
}

// Exit 1 with no result file means the popup ran and failed; it must deny
// immediately rather than be retried like a collision.
func TestPopupExitOneWithoutResultDeniesWithoutRetry(t *testing.T) {
	stubPopupSeams(t, 5*time.Second)
	calls := 0
	dispatchPopupRunner = func(context.Context, string, int, int, string) error {
		calls++
		return exec.Command("sh", "-c", "exit 1").Run()
	}
	approved, decided, reason := runTestPopup(t)
	if approved || !decided || reason != "" || calls != 1 {
		t.Fatalf("approved=%v decided=%v reason=%q calls=%d", approved, decided, reason, calls)
	}
}
