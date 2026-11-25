package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// missedWindow points the approval seams at a fresh windowstate record for
// wid, optionally carrying an earlier miss and its attention.
func missedWindow(t *testing.T, wid string, earlier bool) {
	t.Helper()
	setupWindowstate(t)
	origID, origRT := approvalWindowID, approvalRuntimeDir
	t.Cleanup(func() { approvalWindowID, approvalRuntimeDir = origID, origRT })
	rt := t.TempDir()
	approvalWindowID = func() string { return wid }
	approvalRuntimeDir = func() string { return rt }
	if err := windowstate.Update(wid, func(w *windowstate.WindowState) {
		w.State = "working"
		if earlier {
			w.MissedApproval = &windowstate.MissedApproval{Kind: "cron job", Detail: "old", Alerted: true}
			w.Attention = &windowstate.Attention{Reason: windowstate.AttentionApproval, Detail: "cron job: old"}
		}
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPopupRecordsMissedApproval(t *testing.T) {
	tests := []struct {
		name    string
		earlier bool
		run     func(ctx context.Context, argsFile string) error
		// want is the MissedApproval detail afterwards, "" for none.
		want string
	}{
		{
			name: "timeout records",
			run:  func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() },
			want: "go-engineer on #3",
		},
		{
			name:    "second miss replaces the first",
			earlier: true,
			run:     func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() },
			want:    "go-engineer on #3",
		},
		{
			name:    "approved clears an earlier miss",
			earlier: true,
			run: func(_ context.Context, argsFile string) error {
				return WritePopupResult(popupResultPath(argsFile), "allow")
			},
		},
		{
			name:    "denied clears an earlier miss",
			earlier: true,
			run: func(_ context.Context, argsFile string) error {
				return WritePopupResult(popupResultPath(argsFile), "deny")
			},
		},
		{
			name:    "signal cancel neither records nor clears",
			earlier: true,
			run: func(ctx context.Context, _ string) error {
				_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
				<-ctx.Done()
				return ctx.Err()
			},
			want: "old",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const wid = "@41"
			stubPopupSeams(t, 80*time.Millisecond)
			if strings.HasPrefix(tc.name, "signal") {
				popupDeadline = 10 * time.Second
			}
			popupCloser = func(string) error { return nil }
			deps = stubDeps(t)
			missedWindow(t, wid, tc.earlier)
			dispatchPopupRunner = func(ctx context.Context, _ string, _, _ int, argsFile string) error {
				return tc.run(ctx, argsFile)
			}

			runPopupWithDeadline(popupTarget{}, "dispatch", "test", 10, 10, filepath.Join(t.TempDir(), "args"),
				popupNotice{Agent: "lead", Tool: "dispatch", Detail: "go-engineer on #3", Action: "dispatch go-engineer on #3"})

			ws, err := windowstate.Read(wid)
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if ws.MissedApproval != nil {
				got = ws.MissedApproval.Detail
			}
			if got != tc.want {
				t.Fatalf("missed approval = %+v, want detail %q", ws.MissedApproval, tc.want)
			}
			if tc.want == "go-engineer on #3" && (ws.MissedApproval.Kind != "dispatch" || ws.MissedApproval.Alerted || ws.MissedApproval.At.IsZero()) {
				t.Errorf("missed approval = %+v", ws.MissedApproval)
			}
			if ws.State != "working" {
				t.Errorf("state = %q, want working", ws.State)
			}
		})
	}
}

func TestPopupUnresolvedWindowRecordsNothing(t *testing.T) {
	stubPopupSeams(t, 30*time.Millisecond)
	popupCloser = func(string) error { return nil }
	deps = stubDeps(t)
	setupWindowstate(t)
	dispatchPopupRunner = func(ctx context.Context, _ string, _, _ int, _ string) error { <-ctx.Done(); return ctx.Err() }
	_, _, reason := runTestPopup(t)
	if !strings.Contains(reason, "timed out") {
		t.Fatalf("reason = %q", reason)
	}
	if all := windowstate.All(); len(all) != 0 {
		t.Fatalf("records written without a window: %+v", all)
	}
}

func TestRemoteDeadlineRecordsMissedApproval(t *testing.T) {
	const wid = "@42"
	stubPopupSeams(t, 80*time.Millisecond)
	deps = stubDeps(t)
	missedWindow(t, wid, false)
	fs := withFakeServe(t)
	installBlockingRunner(t)
	recordCloser(t)
	out := runRemoteGate(t, laptop)
	fs.waitRequest()
	if g := waitGate(t, out); !strings.Contains(g.reason, "timed out") {
		t.Fatalf("got %+v, want timeout deny", g)
	}
	ws, _ := windowstate.Read(wid)
	if ws.MissedApproval == nil || ws.MissedApproval.Detail != "dispatch go-engineer" {
		t.Fatalf("missed approval = %+v", ws.MissedApproval)
	}
}

func TestRemoteAnswerClearsMissedApproval(t *testing.T) {
	const wid = "@43"
	stubPopupSeams(t, 5*time.Second)
	deps = stubDeps(t)
	missedWindow(t, wid, true)
	fs := withFakeServe(t)
	installBlockingRunner(t)
	recordCloser(t)
	out := runRemoteGate(t, laptop)
	fs.waitRequest()
	fs.answer(remoteapproval.Allow)
	if g := waitGate(t, out); !g.approved {
		t.Fatalf("got %+v, want phone approve", g)
	}
	if ws, _ := windowstate.Read(wid); ws.MissedApproval != nil || ws.Attention != nil {
		t.Fatalf("after a phone answer: missed %+v attention %+v", ws.MissedApproval, ws.Attention)
	}
}

func TestMissedApprovalDetailIsOneLine(t *testing.T) {
	m := missedApproval("bash command", popupNotice{Detail: "echo a\necho b" + strings.Repeat("x", 300)}, time.Unix(1, 0))
	if strings.Contains(m.Detail, "\n") || len([]rune(m.Detail)) > 200 {
		t.Fatalf("detail = %q", m.Detail)
	}
	if m := missedApproval("cron job", popupNotice{Action: "add cron job x"}, time.Unix(1, 0)); m.Detail != "add cron job x" {
		t.Fatalf("empty detail falls back to the action, got %q", m.Detail)
	}
}

func TestMissedApprovalAttentionLifecycle(t *testing.T) {
	const w = "@77"
	stopEnv(t, w, "bare")
	setSpawner(t, func(string) error { return nil })
	sent := alerts(t)
	if err := windowstate.Update(w, func(ws *windowstate.WindowState) {
		ws.State = "working"
		ws.MissedApproval = &windowstate.MissedApproval{Kind: "dispatch", Detail: "go-engineer on #3"}
	}); err != nil {
		t.Fatal(err)
	}

	runHookCaptured(t, nil, `{"last_assistant_message":"The dispatch was denied."}`, CheckDone)
	ws, _ := windowstate.Read(w)
	if ws.Attention == nil || ws.Attention.Reason != windowstate.AttentionApproval || ws.Attention.Detail != "dispatch: go-engineer on #3" {
		t.Fatalf("attention = %+v", ws.Attention)
	}
	if len(*sent) != 1 || (*sent)[0] != attentionNotificationType {
		t.Fatalf("alerts = %v, want one attention alert", *sent)
	}

	// A peer message does not answer it; the next turn end raises it again,
	// silently.
	runHookCaptured(t, nil, `{"prompt":"Another Claude session sent a message:\nstatus?"}`, UserPrompt)
	if ws, _ := windowstate.Read(w); ws.MissedApproval == nil {
		t.Fatal("a peer message cleared the missed approval")
	}
	runHookCaptured(t, nil, `{"last_assistant_message":"Replied."}`, CheckDone)
	if ws, _ := windowstate.Read(w); ws.Attention == nil || ws.Attention.Reason != windowstate.AttentionApproval {
		t.Fatalf("attention after a peer turn = %+v", ws.Attention)
	}
	if len(*sent) != 1 {
		t.Fatalf("alerts after a peer turn = %v, want still one", *sent)
	}

	// The human typing answers it.
	runHookCaptured(t, nil, `{"prompt":"retry the dispatch"}`, UserPrompt)
	if ws, _ := windowstate.Read(w); ws.MissedApproval != nil || ws.Attention != nil {
		t.Fatalf("after a human prompt: missed %+v attention %+v", ws.MissedApproval, ws.Attention)
	}
	runHookCaptured(t, nil, `{"last_assistant_message":"Done."}`, CheckDone)
	if ws, _ := windowstate.Read(w); ws.Attention != nil {
		t.Fatalf("attention after the human's turn = %+v", ws.Attention)
	}
}
