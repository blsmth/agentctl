package tmux

import (
	"errors"
	"os"
	"testing"
)

// TestPromptStillStaged covers the pure decision function over captured
// pane text: staged multi-line (bracketed-paste marker present), staged
// single-line (literal text after the "❯ " prompt marker), and a
// successfully-submitted, empty input box.
func mustReadFixture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPromptStillStaged(t *testing.T) {
	tests := []struct {
		name     string
		captured string
		want     bool
	}{
		{
			name: "staged multi-line paste attachment",
			captured: "" +
				"Some earlier transcript line\n" +
				"╭──────────────────────────────╮\n" +
				"│ ❯ [Pasted text #1 +6 lines]   │\n" +
				"╰──────────────────────────────╯\n",
			want: true,
		},
		{
			name: "staged single-line literal text after prompt marker",
			captured: "" +
				"╭──────────────────────────────╮\n" +
				"│ ❯ wake up, check the mailbox  │\n" +
				"╰──────────────────────────────╯\n",
			want: true,
		},
		{
			name: "submitted -- empty input box",
			captured: "" +
				"assistant: on it, checking now\n" +
				"╭──────────────────────────────╮\n" +
				"│ ❯                             │\n" +
				"╰──────────────────────────────╯\n",
			want: false,
		},
		{
			name:     "no prompt marker at all",
			captured: "some unrelated pane content\nwith no claude UI in it\n",
			want:     false,
		},
		{
			name: "transcript mentioning the paste marker is not staged when the input box is empty",
			// Regression case: an earlier whole-capture
			// strings.Contains(captured, "[Pasted text") check produced a
			// real false positive here -- any pane whose transcript
			// happens to mention the marker string (discussing this very
			// mechanism, or a doc file with it open) would permanently and
			// falsely appear to have unsubmitted content staged. Only the
			// prompt-marker line matters.
			captured: "" +
				"assistant: earlier turn attached [Pasted text #1 +2 lines] as context\n" +
				"╭──────────────────────────────╮\n" +
				"│ ❯                             │\n" +
				"╰──────────────────────────────╯\n",
			want: false,
		},
		{
			name: "earlier transcript line contains paste marker, empty input box after the glyph, is not staged",
			captured: "" +
				"[Pasted text #1 +6 lines] shown two turns ago, now just history\n" +
				"❯ \n",
			want: false,
		},
		{
			name: "transcript echo of submitted message, empty input box (regression)",
			captured: "❯ some earlier message\n  ⎿  done\n" +
				"──────────────────────────── lead ─\n❯ \n────────────────────────────\n  ⏵⏵ auto mode\n",
			want: false,
		},
		{
			name: "transcript message, input box holds paste placeholder",
			captured: "❯ msg\n" +
				"────────────────────────────\n❯ [Pasted text #1 +6 lines]\n────────────────────────────\n",
			want: true,
		},
		{
			name: "transcript message, input box holds typed text",
			captured: "❯ msg\n" +
				"────────────────────────────\n❯ typed text\n────────────────────────────\n",
			want: true,
		},
		{
			name: "empty box rendered as bare glyph without trailing space",
			captured: "❯ msg\n" +
				"────────────────────────────\n❯\n────────────────────────────\n",
			want: false,
		},
		{
			name:     "fallback considers only the last marker line",
			captured: "❯ earlier\nsome text\n❯ \n",
			want:     false,
		},
		{
			name:     "real captured fixture",
			captured: mustReadFixture(t, "testdata/wake-falseneg-capture.txt"),
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promptStillStaged(tt.captured); got != tt.want {
				t.Errorf("promptStillStaged(%q) = %v, want %v", tt.captured, got, tt.want)
			}
		})
	}
}

// withInjectSeams temporarily replaces the injection seams and delay vars
// for the duration of a test, restoring them via t.Cleanup. Delays are
// zeroed so the retry loop under test does not sleep in real wall-clock
// time.
func withInjectSeams(t *testing.T, sendLiteral func(string, string) error, sendEnter func(string) error, capturePane func(string) (string, error)) {
	t.Helper()

	origSendLiteral, origSendEnter, origCapture := injectSendLiteral, injectSendEnter, injectCapturePane
	origSettle, origVerify := pasteSettleDelay, injectVerifyDelay

	injectSendLiteral, injectSendEnter, injectCapturePane = sendLiteral, sendEnter, capturePane
	pasteSettleDelay, injectVerifyDelay = 0, 0

	t.Cleanup(func() {
		injectSendLiteral, injectSendEnter, injectCapturePane = origSendLiteral, origSendEnter, origCapture
		pasteSettleDelay, injectVerifyDelay = origSettle, origVerify
	})
}

const (
	idlePane   = "──────── lead ──\n❯\n──────────\n  ⏸ manual mode on · ← for agents\n"
	stagedPane = "──────── lead ──\n❯ [Pasted text #1 +6 lines]\n──────────\n  ⏸ manual mode on\n"
	dialogPane = " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n Esc to cancel · Tab to amend\n"
)

// recorder records every send through the seams so tests assert on the
// tmux calls actually made, not on side effects.
type recorder struct {
	literals, enters int
	captures         int
}

// install wires the seams; capture is called with the 1-based capture index
// and the number of Enters sent so far.
func (r *recorder) install(t *testing.T, capture func(captureN, enters int) (string, error)) {
	t.Helper()
	withInjectSeams(t,
		func(pane, content string) error { r.literals++; return nil },
		func(pane string) error { r.enters++; return nil },
		func(pane string) (string, error) { r.captures++; return capture(r.captures, r.enters) },
	)
}

func TestSendAndVerifySubmission_SubmitsOnFirstEnter(t *testing.T) {
	var r recorder
	r.install(t, func(_, _ int) (string, error) { return idlePane, nil })

	if err := sendAndVerifySubmission("%1", "hello"); err != nil {
		t.Fatalf("error: %v", err)
	}
	if r.literals != 1 || r.enters != 1 {
		t.Errorf("literals=%d enters=%d, want 1/1", r.literals, r.enters)
	}
}

// Regression: the submitted message is echoed in the transcript as "❯ <msg>"
// while the input box is empty; that is success after one Enter.
func TestSendAndVerifySubmission_TranscriptEchoIsSuccess(t *testing.T) {
	echoPane := "❯ hello\n  ⎿  working\n──────── lead ──\n❯\n──────────\n  ⏵⏵ auto mode on\n"
	var r recorder
	r.install(t, func(_, _ int) (string, error) { return echoPane, nil })

	if err := sendAndVerifySubmission("%1", "hello"); err != nil {
		t.Fatalf("error: %v", err)
	}
	if r.enters != 1 {
		t.Errorf("enters = %d, want 1", r.enters)
	}
}

// First Enter is absorbed into a bracketed paste; a retry lands.
func TestSendAndVerifySubmission_RetriesThenSucceeds(t *testing.T) {
	var r recorder
	r.install(t, func(_, enters int) (string, error) {
		if enters == 1 {
			return stagedPane, nil
		}
		return idlePane, nil
	})

	if err := sendAndVerifySubmission("%1", "a\nb\n"); err != nil {
		t.Fatalf("error: %v", err)
	}
	if r.enters != 2 {
		t.Errorf("enters = %d, want 2", r.enters)
	}
}

func TestSendAndVerifySubmission_ErrorsAfterRetryBudget(t *testing.T) {
	var r recorder
	r.install(t, func(_, enters int) (string, error) {
		if enters == 0 {
			return idlePane, nil
		}
		return stagedPane, nil
	})

	if err := sendAndVerifySubmission("%1", "never lands"); err == nil {
		t.Fatal("want error after staged through retry budget")
	}
	if r.enters != injectMaxRetries+1 {
		t.Errorf("enters = %d, want %d", r.enters, injectMaxRetries+1)
	}
}

func TestSendAndVerifySubmission_LiteralSendErrorPropagates(t *testing.T) {
	withInjectSeams(t,
		func(pane, content string) error { return errors.New("boom") },
		func(pane string) error { t.Fatal("Enter must not be sent when the literal send failed"); return nil },
		func(pane string) (string, error) { return idlePane, nil },
	)
	if err := sendAndVerifySubmission("%1", "hello"); err == nil {
		t.Fatal("expected error")
	}
}

// Regression: with a dialog up, NOTHING is sent -- neither the
// literal (digits could move the selection) nor Enter (would approve).
func TestSendAndVerifySubmission_DialogSendsNothing(t *testing.T) {
	var r recorder
	r.install(t, func(_, _ int) (string, error) { return dialogPane, nil })

	err := sendAndVerifySubmission("%1", "hello")
	if !errors.Is(err, ErrDialogBlocking) {
		t.Fatalf("err = %v, want ErrDialogBlocking", err)
	}
	if r.literals != 0 || r.enters != 0 {
		t.Errorf("literals=%d enters=%d, want 0/0", r.literals, r.enters)
	}
}

// Every uncertain path refuses to send, not just the known-dialog one.
func TestSendAndVerifySubmission_UncertainPathsSendNothing(t *testing.T) {
	tests := []struct {
		name    string
		capture func() (string, error)
	}{
		{"capture error", func() (string, error) { return "", errors.New("window not found") }},
		{"empty capture", func() (string, error) { return "", nil }},
		{"blank capture", func() (string, error) { return "  \n\n", nil }},
		{"unrecognised pane", func() (string, error) { return "$ ls\nfoo bar\n", nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r recorder
			r.install(t, func(_, _ int) (string, error) { return tt.capture() })

			err := sendAndVerifySubmission("%1", "hello")
			if !errors.Is(err, ErrCannotVerify) {
				t.Fatalf("err = %v, want ErrCannotVerify", err)
			}
			if r.literals != 0 || r.enters != 0 {
				t.Errorf("literals=%d enters=%d, want 0/0", r.literals, r.enters)
			}
		})
	}
}

// A dialog appearing between the literal and the Enter must stop the Enter.
func TestSendAndVerifySubmission_DialogAppearsBeforeEnter(t *testing.T) {
	var r recorder
	r.install(t, func(n, _ int) (string, error) {
		if n == 1 { // pre-literal check
			return idlePane, nil
		}
		return dialogPane, nil
	})

	err := sendAndVerifySubmission("%1", "hello")
	if !errors.Is(err, ErrDialogBlocking) {
		t.Fatalf("err = %v, want ErrDialogBlocking", err)
	}
	if r.enters != 0 {
		t.Errorf("enters = %d, want 0", r.enters)
	}
}

// Post-Enter, a dialog hides the input box: no staged marker must NOT be read
// as success, and no further Enter may be sent.
func TestSendAndVerifySubmission_DialogAfterEnterIsFailure(t *testing.T) {
	var r recorder
	r.install(t, func(_, enters int) (string, error) {
		if enters == 0 {
			return idlePane, nil
		}
		return dialogPane, nil
	})

	err := sendAndVerifySubmission("%1", "hello")
	if !errors.Is(err, ErrDialogBlocking) {
		t.Fatalf("err = %v, want ErrDialogBlocking", err)
	}
	if r.enters != 1 {
		t.Errorf("enters = %d, want exactly 1", r.enters)
	}
}

// Cannot-verify after the Enter is a failed delivery, not a success.
func TestSendAndVerifySubmission_CaptureErrorAfterEnterIsFailure(t *testing.T) {
	var r recorder
	r.install(t, func(_, enters int) (string, error) {
		if enters == 0 {
			return idlePane, nil
		}
		return "", errors.New("capture-pane: gone")
	})

	err := sendAndVerifySubmission("%1", "hello")
	if !errors.Is(err, ErrCannotVerify) {
		t.Fatalf("err = %v, want ErrCannotVerify", err)
	}
}

func TestVerifySubmission(t *testing.T) {
	tests := []struct {
		name string
		pane string
		want submission
	}{
		{"idle empty box", idlePane, submissionConfirmed},
		{"staged", stagedPane, submissionStaged},
		{"dialog", dialogPane, submissionUnverifiable},
		{"empty", "", submissionUnverifiable},
		{"unrecognised", "just text\n", submissionUnverifiable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verifySubmission(tt.pane); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
