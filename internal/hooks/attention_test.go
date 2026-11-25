package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/verdicts"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestTurnEndAttention(t *testing.T) {
	now := time.Unix(100, 0)
	verdict := &windowstate.PendingVerdict{PR: 12, Verdict: verdicts.NeedsBrendan}
	missed := &windowstate.MissedApproval{Kind: "dispatch", Detail: "go-engineer on #3"}
	tests := []struct {
		name   string
		in     turnEnd
		reason string
		detail string
	}{
		{"plain report", turnEnd{Text: "Done, PR opened."}, "", ""},
		{"question", turnEnd{Text: "Tests pass. Merge it?"}, windowstate.AttentionQuestion, "Merge it?"},
		{"worker question is for its orchestrator", turnEnd{Autonomous: true, Text: "Merge it?"}, "", ""},
		{"two failures are routine", turnEnd{ToolFailures: 2}, "", ""},
		{"failure streak", turnEnd{ToolFailures: 3, LastError: "Bash: exit 1"}, windowstate.AttentionErrors, "3 tool failures in a row: Bash: exit 1"},
		{"worker failure streak", turnEnd{Autonomous: true, ToolFailures: 4}, windowstate.AttentionErrors, "4 tool failures in a row"},
		{"question beats failures", turnEnd{Text: "Should I retry?", ToolFailures: 3}, windowstate.AttentionQuestion, "Should I retry?"},
		{"verdict beats everything", turnEnd{Text: "Merge it?", ToolFailures: 5, Verdict: verdict, Missed: missed}, windowstate.AttentionMessage, "PR #12 review: NEEDS-BRENDAN"},
		{"missed approval beats a question", turnEnd{Text: "Merge it?", ToolFailures: 5, Missed: missed}, windowstate.AttentionApproval, "dispatch: go-engineer on #3"},
		{"worker missed approval still needs the human", turnEnd{Autonomous: true, Missed: missed}, windowstate.AttentionApproval, "dispatch: go-engineer on #3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := turnEndAttention(tt.in, now)
			if tt.reason == "" {
				if got != nil {
					t.Fatalf("got %+v, want none", got)
				}
				return
			}
			if got == nil || got.Reason != tt.reason || got.Detail != tt.detail || !got.At.Equal(now) {
				t.Fatalf("got %+v, want %s %q", got, tt.reason, tt.detail)
			}
		})
	}
}

func TestIsHumanPrompt(t *testing.T) {
	for p, want := range map[string]bool{
		"merge it": true,
		"":         true,
		"Another Claude session sent a message:\n<x>": false,
		"<cross-session-message from=\"x\">":          false,
		"  <task-notification>done":                   false,
	} {
		if got := isHumanPrompt(p); got != want {
			t.Errorf("isHumanPrompt(%q) = %v, want %v", p, got, want)
		}
	}
}

// alerts installs deps whose Notify records each notification type.
func alerts(t *testing.T) *[]string {
	t.Helper()
	var got []string
	d := stubDeps(t)
	d.Notify = func(o notify.Options) { got = append(got, o.Env["AGENTCTL_NOTIFICATION_TYPE"]) }
	SetDepsForTest(d)
	return &got
}

func TestCheckDone_QuestionAttentionLifecycle(t *testing.T) {
	const w = "@70"
	stopEnv(t, w, "bare")
	setSpawner(t, func(string) error { return nil })
	sent := alerts(t)

	runHookCaptured(t, nil, `{"last_assistant_message":"Both options work.\n\nWhich one do you want?"}`, CheckDone)
	ws, _ := windowstate.Read(w)
	if ws.Attention == nil || ws.Attention.Reason != windowstate.AttentionQuestion || ws.Attention.Detail != "Which one do you want?" {
		t.Fatalf("attention = %+v", ws.Attention)
	}
	if ws.Done {
		t.Fatal("attention must never set Done")
	}
	if len(*sent) != 1 || (*sent)[0] != attentionNotificationType {
		t.Fatalf("alerts = %v, want one attention", *sent)
	}
	// Viewing the window does not clear it; the next prompt does.
	if err := windowstate.Update(w, func(ws *windowstate.WindowState) { ws.Seen = true }); err != nil {
		t.Fatal(err)
	}
	if ws, _ := windowstate.Read(w); ws.Attention == nil {
		t.Fatal("Seen cleared attention")
	}
	runHookCaptured(t, nil, `{"prompt":"the first one"}`, UserPrompt)
	if ws, _ := windowstate.Read(w); ws.Attention != nil || ws.State != "working" {
		t.Fatalf("after the next prompt: attention %+v state %q", ws.Attention, ws.State)
	}
	runHookCaptured(t, nil, `{"last_assistant_message":"Done."}`, CheckDone)
	if ws, _ := windowstate.Read(w); ws.Attention != nil {
		t.Fatalf("a plain turn end raised %+v", ws.Attention)
	}
}

func TestCheckDone_QuestionFromTranscriptFallback(t *testing.T) {
	const w = "@71"
	stopEnv(t, w, "bare")
	setSpawner(t, func(string) error { return nil })
	tr := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"look at it"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Found two bugs. Fix both?"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{}}]}}`,
	}
	if err := os.WriteFile(tr, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runHookCaptured(t, nil, `{"transcript_path":"`+tr+`"}`, CheckDone)
	ws, _ := windowstate.Read(w)
	if ws.Attention == nil || ws.Attention.Detail != "Fix both?" {
		t.Fatalf("attention = %+v", ws.Attention)
	}
}

func TestLastAssistantTextStopsAtPrompt(t *testing.T) {
	tr := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Old question?"}]}}`,
		`{"type":"user","message":{"role":"user","content":"next"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{}}]}}`,
	}
	if err := os.WriteFile(tr, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := lastAssistantText(tr); got != "" {
		t.Errorf("lastAssistantText = %q, want \"\" (no text since the prompt)", got)
	}
	if got := lastAssistantText(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("missing transcript = %q", got)
	}
}

func TestToolFailureStreakRaisesErrors(t *testing.T) {
	const w = "@72"
	stopEnv(t, w, "dispatch")
	setSpawner(t, func(string) error { return nil })
	fail := `{"tool_name":"Bash","error":"exit 1"}`
	for range windowstate.AttentionToolFailures - 1 {
		runHookCaptured(t, nil, fail, PostToolFailure)
	}
	runHookCaptured(t, nil, `{"tool_name":"Read"}`, PostTool) // a success resets the streak
	for range windowstate.AttentionToolFailures {
		runHookCaptured(t, nil, fail, PostToolFailure)
	}
	if ws, _ := windowstate.Read(w); ws.ToolFailures != windowstate.AttentionToolFailures {
		t.Fatalf("ToolFailures = %d", ws.ToolFailures)
	}
	runHookCaptured(t, nil, stopStdin, CheckDone)
	ws, _ := windowstate.Read(w)
	if ws.Attention == nil || ws.Attention.Reason != windowstate.AttentionErrors {
		t.Fatalf("attention = %+v", ws.Attention)
	}
	if !ws.Done {
		t.Error("completion must be unchanged by attention")
	}
	runHookCaptured(t, nil, `{"prompt":"try again"}`, UserPrompt)
	if ws, _ := windowstate.Read(w); ws.ToolFailures != 0 || ws.Attention != nil {
		t.Fatalf("after a prompt: failures %d attention %+v", ws.ToolFailures, ws.Attention)
	}
}

func TestStopFailureRaisesErrors(t *testing.T) {
	const w = "@73"
	stopEnv(t, w, "bare")
	runHookCaptured(t, nil, `{"error_type":"rate_limit"}`, StopFailure)
	ws, _ := windowstate.Read(w)
	if ws.Attention == nil || ws.Attention.Reason != windowstate.AttentionErrors || ws.Attention.Detail != "Rate limited" {
		t.Fatalf("attention = %+v", ws.Attention)
	}
}

func TestPendingVerdictAttention(t *testing.T) {
	const orch, rev = "@74", "@75"
	stopEnv(t, orch, "bare")
	setSpawner(t, func(string) error { return nil })
	sent := alerts(t)
	if err := windowstate.Update(orch, func(ws *windowstate.WindowState) { ws.SessionID, ws.State = "sess-o", "working" }); err != nil {
		t.Fatal(err)
	}
	if err := windowstate.Update(rev, func(ws *windowstate.WindowState) {
		ws.Mode, ws.SpawnedBy, ws.SpawnedBySession = "review-pr", orch, "sess-o"
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0)
	notePendingVerdict(windowstate.All(), rev, 12, verdicts.NeedsBrendan, now)
	ws, _ := windowstate.Read(orch)
	if ws.PendingVerdict == nil || ws.PendingVerdict.PR != 12 {
		t.Fatalf("pending verdict = %+v", ws.PendingVerdict)
	}
	if ws.Attention != nil {
		t.Fatal("a working orchestrator gets its attention at the turn end, not now")
	}

	// The verdict arrives as a peer message: that turn does not answer it.
	runHookCaptured(t, nil, `{"prompt":"Another Claude session sent a message:\n<cross-session-message>REVIEW PR #12"}`, UserPrompt)
	runHookCaptured(t, nil, `{"last_assistant_message":"Relayed the verdict."}`, CheckDone)
	ws, _ = windowstate.Read(orch)
	if ws.Attention == nil || ws.Attention.Reason != windowstate.AttentionMessage || ws.Attention.Detail != "PR #12 review: NEEDS-BRENDAN" {
		t.Fatalf("attention = %+v", ws.Attention)
	}
	if len(*sent) != 1 {
		t.Fatalf("alerts after the delivery turn = %v, want one", *sent)
	}
	// Another peer turn raises it again, silently.
	runHookCaptured(t, nil, `{"prompt":"Another Claude session sent a message:\nworker reply"}`, UserPrompt)
	runHookCaptured(t, nil, `{"last_assistant_message":"Noted."}`, CheckDone)
	if ws, _ := windowstate.Read(orch); ws.Attention == nil || ws.Attention.Reason != windowstate.AttentionMessage {
		t.Fatalf("attention after a second peer turn = %+v", ws.Attention)
	}
	if len(*sent) != 1 {
		t.Fatalf("alerts after a second peer turn = %v, want still one", *sent)
	}

	// A later MERGE for the same PR lifts it.
	notePendingVerdict(windowstate.All(), rev, 12, verdicts.Merge, now)
	if ws, _ := windowstate.Read(orch); ws.PendingVerdict != nil || ws.Attention != nil {
		t.Fatalf("after MERGE: pending %+v attention %+v", ws.PendingVerdict, ws.Attention)
	}

	// A human reply answers a pending verdict.
	notePendingVerdict(windowstate.All(), rev, 13, verdicts.Blocked, now)
	if ws, _ := windowstate.Read(orch); ws.Attention == nil {
		t.Fatal("an idle orchestrator gets the attention at once")
	}
	if len(*sent) != 2 {
		t.Fatalf("alerts after a new verdict on an idle orchestrator = %v, want two", *sent)
	}
	runHookCaptured(t, nil, `{"prompt":"Another Claude session sent a message:\nREVIEW PR #13"}`, UserPrompt)
	runHookCaptured(t, nil, `{"last_assistant_message":"Relayed."}`, CheckDone)
	if len(*sent) != 2 {
		t.Fatalf("the delivery turn of an already alerted verdict alerted again: %v", *sent)
	}
	runHookCaptured(t, nil, `{"prompt":"ok, I will look"}`, UserPrompt)
	if ws, _ := windowstate.Read(orch); ws.PendingVerdict != nil || ws.Attention != nil {
		t.Fatalf("after a human prompt: pending %+v attention %+v", ws.PendingVerdict, ws.Attention)
	}

	// Another session's reviewer (recycled window ID) is ignored.
	if err := windowstate.Update(rev, func(ws *windowstate.WindowState) { ws.SpawnedBySession = "old" }); err != nil {
		t.Fatal(err)
	}
	notePendingVerdict(windowstate.All(), rev, 14, verdicts.Blocked, now)
	if ws, _ := windowstate.Read(orch); ws.PendingVerdict != nil {
		t.Fatal("verdict recorded for a different session")
	}
}

func TestSessionStartClearDropsAttention(t *testing.T) {
	const w = "@76"
	stopEnv(t, w, "bare")
	t.Setenv("CLAUDE_ENV_FILE", filepath.Join(t.TempDir(), "env"))
	if err := windowstate.Update(w, func(ws *windowstate.WindowState) {
		ws.State = "idle"
		ws.Attention = &windowstate.Attention{Reason: windowstate.AttentionQuestion}
		ws.PendingVerdict = &windowstate.PendingVerdict{PR: 1, Verdict: verdicts.Blocked}
		ws.MissedApproval = &windowstate.MissedApproval{Kind: "dispatch"}
	}); err != nil {
		t.Fatal(err)
	}
	runHookCaptured(t, nil, `{"session_id":"s2","source":"compact"}`, SessionStart)
	if ws, _ := windowstate.Read(w); ws.Attention == nil {
		t.Fatal("compact cleared attention")
	}
	runHookCaptured(t, nil, `{"session_id":"s3","source":"clear"}`, SessionStart)
	if ws, _ := windowstate.Read(w); ws.Attention != nil || ws.PendingVerdict != nil || ws.MissedApproval != nil {
		t.Fatalf("/clear kept attention %+v pending %+v missed %+v", ws.Attention, ws.PendingVerdict, ws.MissedApproval)
	}
}

func TestShouldAlertAttentionHonoursBlockedToggle(t *testing.T) {
	ac := config.DefaultAgentConfig()
	if !shouldAlert(ac, attentionNotificationType) {
		t.Error("attention should alert by default")
	}
	ac.AlertConfig.Blocked = false
	if shouldAlert(ac, attentionNotificationType) {
		t.Error("alert_config.blocked=false should suppress attention")
	}
}
