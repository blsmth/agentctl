package mailbox

// deliver_test.go — tests for DrainFor, the in-band hook-delivery path (see
// docs/agent-messaging-design.md §3). Ported from the removed tmux
// send-keys drain (monitor_mailbox_test.go), which never actually delivered
// a message in production -- these tests exercise the real replacement.

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// setupDeliverTest redirects the mailbox root to a temp dir for the
// duration of the test.
func setupDeliverTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	SetDirForTest(dir)
	t.Cleanup(func() { SetDirForTest("") })
}

func enqueue(t *testing.T, to, toSession, body string) string {
	t.Helper()
	id, err := Enqueue(Message{
		From:      "@1",
		To:        to,
		ToSession: toSession,
		Kind:      KindFYI,
		Body:      body,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return id
}

// TestDrainFor_EmptyMailboxNoOps verifies a window with no pending mail
// returns ("", 0) without error.
func TestDrainFor_EmptyMailboxNoOps(t *testing.T) {
	setupDeliverTest(t)

	body, n := DrainFor("@50", "s1", DefaultTTL)
	if n != 0 || body != "" {
		t.Errorf("DrainFor on empty mailbox = (%q, %d), want (\"\", 0)", body, n)
	}
}

// TestDrainFor_SessionMismatchDiscards is the regression test for the
// window-ID-reuse risk: a message whose ToSession no longer matches the
// window's live sessionID must be discarded (moved to read/), never
// delivered, and never left pending for a retry that can never succeed.
func TestDrainFor_SessionMismatchDiscards(t *testing.T) {
	setupDeliverTest(t)

	msgID := enqueue(t, "@50", "stale-session", "hello?")

	body, n := DrainFor("@50", "current-session", DefaultTTL)
	if n != 0 || body != "" {
		t.Errorf("DrainFor with session mismatch = (%q, %d), want (\"\", 0)", body, n)
	}

	pending, err := Pending("@50")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("Pending after mismatch = %d, want 0 (message must be discarded, not left pending)", len(pending))
	}

	read, err := Read("@50")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read) != 1 || read[0].ID != msgID {
		t.Errorf("Read after mismatch = %v, want [%s] (discard moves to read/)", read, msgID)
	}
}

// TestDrainFor_EmptyToSessionNeverMismatches verifies a message enqueued
// without session verification (empty ToSession, e.g. via `agentctl send`
// outside a tracked window) is still delivered rather than discarded.
func TestDrainFor_EmptyToSessionNeverMismatches(t *testing.T) {
	setupDeliverTest(t)

	enqueue(t, "@50", "", "hello")

	body, n := DrainFor("@50", "some-session", DefaultTTL)
	if n != 1 {
		t.Fatalf("DrainFor with empty ToSession = n=%d, want 1 (must deliver, not discard)", n)
	}
	if !strings.Contains(body, "hello") {
		t.Errorf("body %q missing message text", body)
	}
}

// TestDrainFor_EmptyWindowSessionNeverMismatches verifies that a window
// with no recorded live session (empty sessionID, e.g. windowstate not yet
// written) does not cause every message to be discarded as a mismatch.
func TestDrainFor_EmptyWindowSessionNeverMismatches(t *testing.T) {
	setupDeliverTest(t)

	enqueue(t, "@50", "sender-recorded-session", "hello")

	body, n := DrainFor("@50", "", DefaultTTL)
	if n != 1 {
		t.Fatalf("DrainFor with empty window sessionID = n=%d, want 1 (must deliver, not discard)", n)
	}
	if !strings.Contains(body, "hello") {
		t.Errorf("body %q missing message text", body)
	}
}

// TestDrainFor_TTLExpiryDiscards verifies a message older than ttl is
// discarded (moved to read/) rather than delivered. This is the fix for the
// 135-message backlog that piled up while delivery was silently broken --
// without expiry, the moment delivery starts working every window gets
// flooded with days-old mail.
func TestDrainFor_TTLExpiryDiscards(t *testing.T) {
	setupDeliverTest(t)

	id, err := Enqueue(Message{
		From:      "@1",
		To:        "@50",
		ToSession: "s1",
		Kind:      KindFYI,
		Body:      "ancient",
		Created:   time.Now().Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	body, n := DrainFor("@50", "s1", time.Hour)
	if n != 0 || body != "" {
		t.Errorf("DrainFor with expired TTL = (%q, %d), want (\"\", 0)", body, n)
	}

	read, err := Read("@50")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read) != 1 || read[0].ID != id {
		t.Errorf("Read after TTL discard = %v, want [%s]", read, id)
	}
}

// TestDrainFor_WithinTTLDelivers verifies a message younger than ttl is not
// treated as expired.
func TestDrainFor_WithinTTLDelivers(t *testing.T) {
	setupDeliverTest(t)

	if _, err := Enqueue(Message{
		From:      "@1",
		To:        "@50",
		ToSession: "s1",
		Kind:      KindFYI,
		Body:      "fresh",
		Created:   time.Now().Add(-5 * time.Minute),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	body, n := DrainFor("@50", "s1", time.Hour)
	if n != 1 {
		t.Fatalf("DrainFor within TTL = n=%d, want 1", n)
	}
	if !strings.Contains(body, "fresh") {
		t.Errorf("body %q missing message text", body)
	}
}

// TestDrainFor_FIFOOrderPreserved verifies that multiple pending messages
// are coalesced in enqueue (FIFO) order, matching Pending's documented
// chronological ordering.
func TestDrainFor_FIFOOrderPreserved(t *testing.T) {
	setupDeliverTest(t)

	const n = 5
	for i := 0; i < n; i++ {
		enqueue(t, "@50", "s1", fmt.Sprintf("body-%d", i))
		// newMessageID's chronological ordering only sorts correctly if
		// timestamps differ; a tiny sleep guarantees distinct timestamps
		// without depending on the nanosecond clock resolution.
		time.Sleep(2 * time.Millisecond)
	}

	body, got := DrainFor("@50", "s1", DefaultTTL)
	if got != n {
		t.Fatalf("DrainFor n = %d, want %d", got, n)
	}

	// Every body-i must appear, and in increasing order.
	lastIdx := -1
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("body-%d", i)
		idx := strings.Index(body, want)
		if idx < 0 {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
		if idx < lastIdx {
			t.Errorf("message %q appears out of FIFO order in:\n%s", want, body)
		}
		lastIdx = idx
	}
}

// TestDrainFor_ClaimsBeforeReturning verifies that every delivered message
// is moved to read/ as part of DrainFor itself, not left for the caller to
// mark read afterward. This is the "claim is atomic with drain" contract
// PostToolUse depends on: it fires on every subsequent tool call, so a
// caller that marked read after writing stdout would re-deliver on the very
// next tool call.
func TestDrainFor_ClaimsBeforeReturning(t *testing.T) {
	setupDeliverTest(t)

	enqueue(t, "@50", "s1", "one")
	enqueue(t, "@50", "s1", "two")

	_, n := DrainFor("@50", "s1", DefaultTTL)
	if n != 2 {
		t.Fatalf("DrainFor n = %d, want 2", n)
	}

	pending, err := Pending("@50")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("Pending after DrainFor = %d, want 0 (must be claimed, not left pending)", len(pending))
	}
	read, err := Read("@50")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read) != 2 {
		t.Errorf("Read after DrainFor = %d, want 2", len(read))
	}

	// A second call must find nothing left to deliver.
	body2, n2 := DrainFor("@50", "s1", DefaultTTL)
	if n2 != 0 || body2 != "" {
		t.Errorf("second DrainFor = (%q, %d), want (\"\", 0) -- messages must not be re-delivered", body2, n2)
	}
}

// TestDrainFor_TruncatesLongBody verifies the 8000-character cap and its
// explicit truncation marker.
func TestDrainFor_TruncatesLongBody(t *testing.T) {
	setupDeliverTest(t)

	huge := strings.Repeat("x", maxDeliverBodyLen+500)
	enqueue(t, "@50", "s1", huge)

	body, n := DrainFor("@50", "s1", DefaultTTL)
	if n != 1 {
		t.Fatalf("DrainFor n = %d, want 1", n)
	}
	if len(body) > maxDeliverBodyLen {
		t.Errorf("body length = %d, want <= %d", len(body), maxDeliverBodyLen)
	}
	if !strings.Contains(body, "[truncated]") {
		t.Errorf("truncated body missing explicit marker:\n%s", body)
	}
}

// TestDrainFor_TruncatesOnRuneBoundary is the regression test for the
// byte-slice truncation bug: a multi-byte rune (em-dash) placed to straddle
// the exact cut point must never be split. Before the fix, s[:cut] sliced
// mid-rune, producing invalid UTF-8 that json.Marshal silently replaced with
// U+FFFD ("�") rather than erroring -- so the bug was invisible until you
// looked at the actual delivered bytes.
func TestDrainFor_TruncatesOnRuneBoundary(t *testing.T) {
	setupDeliverTest(t)

	// Build a body where a 3-byte rune (em-dash, U+2014) straddles the cut
	// point: fill up to one byte short of the cut with ASCII, then place
	// em-dashes across the boundary.
	limit := maxDeliverBodyLen - len(truncatedMarker)
	var b strings.Builder
	b.WriteString(strings.Repeat("x", limit-1))
	b.WriteString(strings.Repeat("—", 50)) // U+2014, 3 bytes each in UTF-8
	huge := b.String()

	enqueue(t, "@50", "s1", huge)

	body, n := DrainFor("@50", "s1", DefaultTTL)
	if n != 1 {
		t.Fatalf("DrainFor n = %d, want 1", n)
	}
	if !utf8.ValidString(body) {
		t.Fatalf("truncated body is not valid UTF-8:\n%q", body)
	}
	if !strings.Contains(body, "[truncated]") {
		t.Errorf("truncated body missing explicit marker:\n%s", body)
	}
	if strings.ContainsRune(body, utf8.RuneError) {
		t.Errorf("truncated body contains the UTF-8 replacement character (mojibake):\n%q", body)
	}
}

// TestTruncateBody_ValidUTF8AtEveryCutOffset is a broader sweep: em-dashes
// placed at every byte offset near the cut point must never produce invalid
// UTF-8, regardless of exactly where the multi-byte rune lands relative to
// maxDeliverBodyLen.
func TestTruncateBody_ValidUTF8AtEveryCutOffset(t *testing.T) {
	for offset := -3; offset <= 3; offset++ {
		t.Run(fmt.Sprintf("offset_%d", offset), func(t *testing.T) {
			padLen := maxDeliverBodyLen + offset
			if padLen < 0 {
				padLen = 0
			}
			s := strings.Repeat("x", padLen) + "—————" // trailing multi-byte runes
			got := truncateBody(s)
			if !utf8.ValidString(got) {
				t.Fatalf("truncateBody produced invalid UTF-8 at offset %d:\n%q", offset, got)
			}
		})
	}
}

// TestDrainForIDs_ReturnsClaimedMessageIDs verifies that DrainForIDs returns
// the IDs of every message it claims, in delivery order -- `agentctl wake`
// depends on these IDs to log recoverably if the tmux injection after
// draining fails (the mail is already claimed by then).
func TestDrainForIDs_ReturnsClaimedMessageIDs(t *testing.T) {
	setupDeliverTest(t)

	id1 := enqueue(t, "@50", "s1", "one")
	time.Sleep(2 * time.Millisecond)
	id2 := enqueue(t, "@50", "s1", "two")

	body, ids := DrainForIDs("@50", "s1", DefaultTTL)
	if len(ids) != 2 {
		t.Fatalf("DrainForIDs ids = %v, want 2 entries", ids)
	}
	if ids[0] != id1 || ids[1] != id2 {
		t.Errorf("DrainForIDs ids = %v, want [%s %s]", ids, id1, id2)
	}
	if !strings.Contains(body, "one") || !strings.Contains(body, "two") {
		t.Errorf("body %q missing expected message text", body)
	}
}

// TestDrainForIDs_EmptyMailboxReturnsNilIDs verifies the no-op path returns
// a nil/empty ID slice rather than erroring.
func TestDrainForIDs_EmptyMailboxReturnsNilIDs(t *testing.T) {
	setupDeliverTest(t)

	body, ids := DrainForIDs("@50", "s1", DefaultTTL)
	if body != "" || len(ids) != 0 {
		t.Errorf("DrainForIDs on empty mailbox = (%q, %v), want (\"\", empty)", body, ids)
	}
}

// ── formatCoalescedMessages ──────────────────────────────────────────────

func TestFormatCoalescedMessages_Single(t *testing.T) {
	msgs := []Message{
		{From: "@12", Subject: "PR opened", Body: "opened #101"},
	}
	got := formatCoalescedMessages(msgs)
	if !strings.Contains(got, "@12") {
		t.Errorf("body missing sender: %q", got)
	}
	if !strings.Contains(got, "opened #101") {
		t.Errorf("body missing message text: %q", got)
	}
}

// TestFormatCoalescedMessages_Multiple verifies that N messages produce a
// single body with every sender and every body distinguishable, per
// docs/agent-messaging-design.md §3's coalescing rule.
func TestFormatCoalescedMessages_Multiple(t *testing.T) {
	msgs := []Message{
		{From: "@12", Body: "first message"},
		{From: "@36", Body: "second message"},
		{From: "@41", Body: "third message"},
	}
	got := formatCoalescedMessages(msgs)

	for _, want := range []string{"@12", "@36", "@41", "first message", "second message", "third message"} {
		if !strings.Contains(got, want) {
			t.Errorf("coalesced body missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "3") {
		t.Errorf("coalesced body should mention the message count:\n%s", got)
	}
}
