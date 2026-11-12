package hooks

import (
	"context"
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
	popupCloser = func() error { *closes++; return nil }
	dispatchPopupRunner = func(ctx context.Context, _, _ int, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	return closes
}

func TestDispatchGateTimeoutDenies(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", t.Name())
	closes := withBlockingPopup(t)
	approved, decided, reason := dispatchPreviewGate("a", "agentctl dispatch --agent go-engineer --prompt hi")
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
