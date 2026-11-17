package hooks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/approval"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+string(rune('0'+code))).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	return err
}

func TestPopupApprovalState(t *testing.T) {
	tests := []struct {
		name     string
		run      func(t *testing.T, ctx context.Context) error
		deadline time.Duration
		approved bool
		decided  bool
	}{
		{"approve", func(*testing.T, context.Context) error { return nil }, time.Minute, true, true},
		{"deny", func(t *testing.T, _ context.Context) error { return exitErr(t, 1) }, time.Minute, false, true},
		{"other exit", func(t *testing.T, _ context.Context) error { return exitErr(t, 2) }, time.Minute, false, false},
		{"error", func(*testing.T, context.Context) error { return errors.New("boom") }, time.Minute, false, false},
		{"timeout", func(_ *testing.T, ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, 30 * time.Millisecond, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setupWindowstate(t)
			rt := t.TempDir()
			const wid = "@5"
			if err := windowstate.Update(wid, func(w *windowstate.WindowState) {
				w.State = "working"
				w.PendingTool = "Bash"
				w.PendingToolDetail = "agentctl dispatch"
				w.SpawnedBy = "@1"
				w.SpawnedBySession = "agentctl-x"
			}); err != nil {
				t.Fatal(err)
			}

			origID, origRT := approvalWindowID, approvalRuntimeDir
			origRun, origDL, origClose := dispatchPopupRunner, popupDeadline, popupCloser
			origDeps := deps
			t.Cleanup(func() {
				approvalWindowID, approvalRuntimeDir = origID, origRT
				dispatchPopupRunner, popupDeadline, popupCloser = origRun, origDL, origClose
				deps = origDeps
			})
			approvalWindowID = func() string { return wid }
			approvalRuntimeDir = func() string { return rt }
			popupDeadline = tc.deadline
			popupCloser = func(string) error { return nil }

			var alerts []notify.Options
			var mails []mailbox.Message
			d := stubDeps(t)
			d.Notify = func(o notify.Options) { alerts = append(alerts, o) }
			d.EnqueueMailboxMessage = func(m mailbox.Message) (string, error) { mails = append(mails, m); return "id", nil }
			deps = d

			dispatchPopupRunner = func(ctx context.Context, _ string, _, _ int, argsFile string) error {
				ws, err := windowstate.Read(wid)
				if err != nil {
					t.Errorf("read: %v", err)
				}
				if ws.State != "blocked" || ws.BlockedSince.IsZero() || ws.PendingTool != "dispatch" {
					t.Errorf("during popup: state=%q since=%v tool=%q", ws.State, ws.BlockedSince, ws.PendingTool)
				}
				if !approval.Pending(rt, time.Now()) {
					t.Error("marker missing during popup")
				}
				if _, err := os.Stat(filepath.Join(rt, "approval-pending", wid)); err != nil {
					t.Errorf("marker file: %v", err)
				}
				err = tc.run(t, ctx)
				recordFakeVerdict(argsFile, err)
				return err
			}

			approved, decided, _ := runPopupWithDeadline(popupTarget{}, "dispatch", "test", 10, 10, filepath.Join(t.TempDir(), "args"),
				popupNotice{Agent: "lead", Tool: "dispatch", Detail: "go-engineer on #3", Action: "dispatch go-engineer on #3"})
			if approved != tc.approved || decided != tc.decided {
				t.Fatalf("approved=%v decided=%v", approved, decided)
			}

			ws, _ := windowstate.Read(wid)
			if ws.State != "working" || !ws.BlockedSince.IsZero() {
				t.Errorf("after popup: state=%q since=%v", ws.State, ws.BlockedSince)
			}
			if ws.PendingTool != "Bash" || ws.PendingToolDetail != "agentctl dispatch" {
				t.Errorf("pending tool not restored: %q %q", ws.PendingTool, ws.PendingToolDetail)
			}
			if approval.Pending(rt, time.Now()) {
				t.Error("marker left behind")
			}
			if len(alerts) != 1 {
				t.Fatalf("alerts = %d, want 1", len(alerts))
			}
			if alerts[0].Env["AGENTCTL_NOTIFICATION_TYPE"] != "approval_pending" ||
				alerts[0].Message != "lead wants to dispatch go-engineer on #3, approve in agentctl-test" {
				t.Errorf("alert = %+v", alerts[0])
			}
			if len(mails) != 1 || mails[0].To != "@1" || mails[0].Kind != mailbox.KindBlocked {
				t.Errorf("mails = %+v", mails)
			}
		})
	}
}

func TestPopupUnresolvedWindowStillShowsPopup(t *testing.T) {
	setupWindowstate(t)
	origRun := dispatchPopupRunner
	t.Cleanup(func() { dispatchPopupRunner = origRun })
	called := false
	dispatchPopupRunner = func(_ context.Context, _ string, _, _ int, argsFile string) error {
		called = true
		recordFakeVerdict(argsFile, nil)
		return nil
	}
	var alerts int
	d := stubDeps(t)
	d.Notify = func(notify.Options) { alerts++ }
	origDeps := deps
	deps = d
	t.Cleanup(func() { deps = origDeps })

	approved, decided, _ := runPopupWithDeadline(popupTarget{}, "bash command", "test", 1, 1, filepath.Join(t.TempDir(), "args"), popupNotice{Agent: "a", Tool: "Bash", Action: "run a command"})
	if !called || !approved || !decided || alerts != 1 {
		t.Fatalf("called=%v approved=%v decided=%v alerts=%d", called, approved, decided, alerts)
	}
}

func TestShouldAlertApprovalPending(t *testing.T) {
	if !shouldAlert(config.DefaultAgentConfig(), approvalNotificationType) {
		t.Error("approval_pending should alert by default")
	}
}

func TestDispatchPopupNotice(t *testing.T) {
	n := dispatchPopupNotice("lead", "go-engineer", 0, "Fix the thing\nmore")
	if n.Action != "dispatch go-engineer" || !strings.HasPrefix(n.Detail, "go-engineer: Fix the thing") || strings.Contains(n.Detail, "more") {
		t.Errorf("%+v", n)
	}
	if n := dispatchPopupNotice("lead", "go-engineer", 7, ""); n.Action != "dispatch go-engineer on #7" {
		t.Errorf("%+v", n)
	}
}

func TestShouldAlertApprovalPendingHonoursBlockedToggle(t *testing.T) {
	ac := config.DefaultAgentConfig()
	ac.AlertConfig.Blocked = false
	if shouldAlert(ac, approvalNotificationType) {
		t.Error("alert_config.blocked=false should suppress approval_pending")
	}
}
