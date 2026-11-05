package mailbox

// wait_test.go -- tests for WaitAndClaim, the blocking mailbox wait behind
// `agentctl inbox --wait` and `agentctl send --wait-reply` (see wait.go).

import (
	"strings"
	"testing"
	"time"
)

func setupWaitTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	SetDirForTest(dir)
	t.Cleanup(func() { SetDirForTest("") })
}

// TestWaitAndClaim_AlreadyPendingReturnsImmediately verifies the critical
// race fix: a message already sitting in the pending dir before the wait
// starts must be found and claimed WITHOUT waiting for an fsnotify event,
// since no event will ever fire for a file that already existed before the
// watch was armed.
func TestWaitAndClaim_AlreadyPendingReturnsImmediately(t *testing.T) {
	setupWaitTest(t)

	enqueue(t, "@50", "s1", "already here")

	start := time.Now()
	matched, timedOut, err := WaitAndClaim("@50", "", 2*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("WaitAndClaim: %v", err)
	}
	if timedOut {
		t.Fatal("WaitAndClaim reported timeout for already-pending mail")
	}
	if len(matched) != 1 || matched[0].Body != "already here" {
		t.Fatalf("WaitAndClaim matched = %+v, want one message %q", matched, "already here")
	}
	// This must return near-instantly -- if it took anywhere close to the
	// 2s timeout, the "check before watch" ordering regressed and the
	// implementation fell back to waiting for an event that never comes.
	if elapsed > 500*time.Millisecond {
		t.Errorf("WaitAndClaim took %s for already-pending mail, want near-instant", elapsed)
	}

	pending, err := Pending("@50")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("Pending after WaitAndClaim = %d, want 0 (message must be claimed)", len(pending))
	}
}

// TestWaitAndClaim_ArrivesAfterWatchStarts verifies the fsnotify path: a
// message enqueued AFTER the wait begins (so it must be a genuinely
// concurrent write, not something already on disk) is still detected and
// claimed before the timeout.
func TestWaitAndClaim_ArrivesAfterWatchStarts(t *testing.T) {
	setupWaitTest(t)

	go func() {
		time.Sleep(100 * time.Millisecond)
		enqueue(t, "@50", "s1", "arrived late")
	}()

	matched, timedOut, err := WaitAndClaim("@50", "", 5*time.Second)
	if err != nil {
		t.Fatalf("WaitAndClaim: %v", err)
	}
	if timedOut {
		t.Fatal("WaitAndClaim timed out even though mail arrived within the timeout")
	}
	if len(matched) != 1 || matched[0].Body != "arrived late" {
		t.Fatalf("WaitAndClaim matched = %+v, want one message %q", matched, "arrived late")
	}
}

// TestWaitAndClaim_TimesOutWhenNoMailArrives verifies that WaitAndClaim
// returns (nil, true, nil) -- not an error -- when nothing shows up within
// the timeout.
func TestWaitAndClaim_TimesOutWhenNoMailArrives(t *testing.T) {
	setupWaitTest(t)

	start := time.Now()
	matched, timedOut, err := WaitAndClaim("@50", "", 200*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("WaitAndClaim: %v", err)
	}
	if !timedOut {
		t.Fatalf("WaitAndClaim matched = %+v, want timeout", matched)
	}
	if len(matched) != 0 {
		t.Errorf("WaitAndClaim on timeout returned %d messages, want 0", len(matched))
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("WaitAndClaim returned after %s, want at least the 200ms timeout", elapsed)
	}
}

// TestWaitAndClaim_FromFilterLeavesNonMatchingPending verifies --from
// filtering: only messages whose From matches are claimed; a non-matching
// message already pending must be left untouched, and the wait must not
// return until a MATCHING message arrives.
func TestWaitAndClaim_FromFilterLeavesNonMatchingPending(t *testing.T) {
	setupWaitTest(t)

	if _, err := Enqueue(Message{From: "@99", To: "@50", ToSession: "s1", Kind: KindFYI, Body: "wrong sender"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		if _, err := Enqueue(Message{From: "@12", To: "@50", ToSession: "s1", Kind: KindFYI, Body: "right sender"}); err != nil {
			t.Errorf("Enqueue: %v", err)
		}
	}()

	matched, timedOut, err := WaitAndClaim("@50", "@12", 5*time.Second)
	if err != nil {
		t.Fatalf("WaitAndClaim: %v", err)
	}
	if timedOut {
		t.Fatal("WaitAndClaim timed out even though a matching message arrived")
	}
	if len(matched) != 1 || matched[0].Body != "right sender" {
		t.Fatalf("WaitAndClaim matched = %+v, want one message %q", matched, "right sender")
	}

	// The non-matching message from @99 must still be pending -- --from
	// filtering must never claim a message it wasn't asked to match.
	pending, err := Pending("@50")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 || pending[0].From != "@99" {
		t.Fatalf("Pending after WaitAndClaim = %+v, want the non-matching @99 message still pending", pending)
	}
}

// TestWaitAndClaim_FromFilterCaseInsensitive verifies From matching is
// case-insensitive, matching resolveSendAddress's own case-insensitivity.
func TestWaitAndClaim_FromFilterCaseInsensitive(t *testing.T) {
	setupWaitTest(t)

	if _, err := Enqueue(Message{From: "@Weird-Case", To: "@50", ToSession: "s1", Kind: KindFYI, Body: "hi"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	matched, timedOut, err := WaitAndClaim("@50", "@weird-case", time.Second)
	if err != nil {
		t.Fatalf("WaitAndClaim: %v", err)
	}
	if timedOut || len(matched) != 1 {
		t.Fatalf("WaitAndClaim(from=%q) = matched=%+v timedOut=%v, want one case-insensitive match", "@weird-case", matched, timedOut)
	}
}

// TestWaitAndClaim_MissingInboxDirIsNotAnError verifies a window that has
// never received mail (so its inbox directory does not exist yet) does not
// error -- WaitAndClaim must create the directory itself before watching.
func TestWaitAndClaim_MissingInboxDirIsNotAnError(t *testing.T) {
	setupWaitTest(t)

	_, timedOut, err := WaitAndClaim("@never-seen", "", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitAndClaim on a window with no inbox dir: %v", err)
	}
	if !timedOut {
		t.Error("expected timeout for a window with no mail at all")
	}
}

// TestWaitAndClaim_FIFOOrderPreserved verifies claimed messages are
// returned in Pending's chronological order, matching DrainForIDs' contract.
func TestWaitAndClaim_FIFOOrderPreserved(t *testing.T) {
	setupWaitTest(t)

	enqueue(t, "@50", "s1", "first")
	time.Sleep(2 * time.Millisecond)
	enqueue(t, "@50", "s1", "second")

	matched, timedOut, err := WaitAndClaim("@50", "", time.Second)
	if err != nil {
		t.Fatalf("WaitAndClaim: %v", err)
	}
	if timedOut || len(matched) != 2 {
		t.Fatalf("WaitAndClaim matched = %+v timedOut=%v, want 2 messages", matched, timedOut)
	}
	if !strings.Contains(matched[0].Body, "first") || !strings.Contains(matched[1].Body, "second") {
		t.Errorf("WaitAndClaim did not preserve FIFO order: %+v", matched)
	}
}
