package mailbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// isolate points both $HOME and the package's directory override at a fresh
// temp dir, so no test can touch the developer's real ~/.config/agentctl or
// project .agentctl/ directory.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := filepath.Join(home, ".agentctl", "inbox")
	SetDirForTest(dir)
	t.Cleanup(func() { SetDirForTest("") })
	return dir
}

func TestEnqueuePendingRoundTrip(t *testing.T) {
	isolate(t)

	m := Message{
		From:      "@10",
		To:        "@41",
		ToSession: "session-uuid-123",
		Kind:      KindFYI,
		Subject:   "hello",
		Body:      "world",
		ReplyTo:   "some-id",
		Hops:      1,
	}

	id, err := Enqueue(m)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id == "" {
		t.Fatal("Enqueue returned empty ID")
	}

	pending, err := Pending("@41")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("Pending returned %d messages, want 1", len(pending))
	}

	got := pending[0]
	if got.ID != id {
		t.Errorf("ID = %q, want %q", got.ID, id)
	}
	if got.From != m.From || got.To != m.To || got.ToSession != m.ToSession ||
		got.Kind != m.Kind || got.Subject != m.Subject || got.Body != m.Body ||
		got.ReplyTo != m.ReplyTo || got.Hops != m.Hops {
		t.Errorf("Pending() = %+v, want fields to match enqueued %+v (id=%s)", got, m, id)
	}
	if got.Created.IsZero() {
		t.Error("Created was not assigned")
	}
}

func TestEnqueueAssignsIDAndCreatedIfUnset(t *testing.T) {
	isolate(t)

	m := Message{To: "@1", Kind: KindFYI, Body: "hi"}
	id, err := Enqueue(m)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id == "" {
		t.Fatal("expected assigned ID")
	}

	pending, err := Pending("@1")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Created.IsZero() {
		t.Errorf("expected Created to be assigned, got %+v", pending)
	}
}

func TestMessageIDsSortChronologically(t *testing.T) {
	isolate(t)

	var ids []string
	for i := 0; i < 3; i++ {
		id, err := Enqueue(Message{To: "@5", Kind: KindFYI, Body: "msg"})
		if err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond)
	}

	pending, err := Pending("@5")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("Pending returned %d messages, want 3", len(pending))
	}
	for i, m := range pending {
		if m.ID != ids[i] {
			t.Errorf("Pending()[%d].ID = %q, want %q (chronological order)", i, m.ID, ids[i])
		}
	}
}

func TestMarkReadMovesMessage(t *testing.T) {
	isolate(t)

	id, err := Enqueue(Message{To: "@2", Kind: KindDone, Body: "done"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := MarkRead("@2", id); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	pending, err := Pending("@2")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("Pending after MarkRead = %+v, want empty", pending)
	}

	read, err := Read("@2")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read) != 1 || read[0].ID != id {
		t.Errorf("Read() = %+v, want message %q", read, id)
	}
}

func TestMarkReadIsIdempotent(t *testing.T) {
	isolate(t)

	id, err := Enqueue(Message{To: "@3", Kind: KindFYI, Body: "hi"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := MarkRead("@3", id); err != nil {
		t.Fatalf("first MarkRead: %v", err)
	}
	if err := MarkRead("@3", id); err != nil {
		t.Fatalf("second MarkRead (idempotent) should return nil, got: %v", err)
	}

	read, err := Read("@3")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read) != 1 {
		t.Errorf("Read() after double MarkRead = %+v, want exactly 1 (no duplication)", read)
	}
}

func TestMarkReadNonexistentIDReturnsNil(t *testing.T) {
	isolate(t)

	if err := MarkRead("@99", "no-such-id"); err != nil {
		t.Errorf("MarkRead on nonexistent message should return nil, got: %v", err)
	}
}

func TestPendingSkipsCorruptFile(t *testing.T) {
	dir := isolate(t)

	if _, err := Enqueue(Message{To: "@7", Kind: KindFYI, Body: "good"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	windowDir := filepath.Join(dir, "@7")
	if err := os.MkdirAll(windowDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	corrupt := filepath.Join(windowDir, "20260101T000000.000000000Z-deadbeef.json")
	if err := os.WriteFile(corrupt, []byte(`{"id": "broken",`), 0o644); err != nil {
		t.Fatalf("writing corrupt file: %v", err)
	}

	pending, err := Pending("@7")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("Pending returned %d messages, want 1 (corrupt file skipped)", len(pending))
	}
	if pending[0].Body != "good" {
		t.Errorf("Pending()[0] = %+v, want the good message", pending[0])
	}
}

func TestEnqueueValidation(t *testing.T) {
	isolate(t)

	tests := []struct {
		name string
		m    Message
	}{
		{"empty To", Message{To: "", Kind: KindFYI, Body: "x"}},
		{"unknown Kind", Message{To: "@1", Kind: "not-a-kind", Body: "x"}},
		{"Hops exceeds MaxHops", Message{To: "@1", Kind: KindFYI, Body: "x", Hops: MaxHops + 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Enqueue(tt.m); err == nil {
				t.Errorf("Enqueue(%+v) = nil error, want error", tt.m)
			}
		})
	}
}

func TestEnqueueAcceptsMaxHops(t *testing.T) {
	isolate(t)

	if _, err := Enqueue(Message{To: "@1", Kind: KindFYI, Body: "x", Hops: MaxHops}); err != nil {
		t.Errorf("Enqueue with Hops == MaxHops should succeed, got: %v", err)
	}
}

func TestPruneOnlyDeletesOldReadMessages(t *testing.T) {
	isolate(t)

	now := time.Now()

	// Fresh read message: must survive.
	freshID, err := Enqueue(Message{To: "@1", Kind: KindFYI, Body: "fresh", Created: now})
	if err != nil {
		t.Fatalf("Enqueue fresh: %v", err)
	}
	if err := MarkRead("@1", freshID); err != nil {
		t.Fatalf("MarkRead fresh: %v", err)
	}

	// Old read message: must be deleted.
	oldID, err := Enqueue(Message{To: "@1", Kind: KindFYI, Body: "old", Created: now.Add(-48 * time.Hour)})
	if err != nil {
		t.Fatalf("Enqueue old: %v", err)
	}
	if err := MarkRead("@1", oldID); err != nil {
		t.Fatalf("MarkRead old: %v", err)
	}

	// Old but still pending message: must survive regardless of age.
	pendingOldID, err := Enqueue(Message{To: "@1", Kind: KindFYI, Body: "old-pending", Created: now.Add(-48 * time.Hour)})
	if err != nil {
		t.Fatalf("Enqueue old-pending: %v", err)
	}

	deleted, err := Prune(24 * time.Hour)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 1 {
		t.Errorf("Prune deleted %d messages, want 1", deleted)
	}

	read, err := Read("@1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read) != 1 || read[0].ID != freshID {
		t.Errorf("Read() after Prune = %+v, want only the fresh message %q", read, freshID)
	}

	pending, err := Pending("@1")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != pendingOldID {
		t.Errorf("Pending() after Prune = %+v, want the old pending message %q untouched", pending, pendingOldID)
	}
}

func TestPendingCount(t *testing.T) {
	isolate(t)

	if n, err := PendingCount("@60"); err != nil || n != 0 {
		t.Fatalf("PendingCount on fresh mailbox = (%d, %v), want (0, nil)", n, err)
	}

	if _, err := Enqueue(Message{To: "@60", Kind: KindFYI, Body: "one"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	id2, err := Enqueue(Message{To: "@60", Kind: KindFYI, Body: "two"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if n, err := PendingCount("@60"); err != nil || n != 2 {
		t.Fatalf("PendingCount after 2 enqueues = (%d, %v), want (2, nil)", n, err)
	}

	// Marking one read must decrement the pending count without touching
	// PendingCount's disregard for the read/ subdirectory.
	if err := MarkRead("@60", id2); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if n, err := PendingCount("@60"); err != nil || n != 1 {
		t.Fatalf("PendingCount after MarkRead = (%d, %v), want (1, nil)", n, err)
	}
}

func TestPruneStale_DeletesOldPendingOnly(t *testing.T) {
	isolate(t)

	now := time.Now()

	// Fresh pending message: must survive.
	freshID, err := Enqueue(Message{To: "@70", Kind: KindFYI, Body: "fresh", Created: now})
	if err != nil {
		t.Fatalf("Enqueue fresh: %v", err)
	}

	// Stale pending message (older than MaxPendingAge): must be deleted.
	staleID, err := Enqueue(Message{To: "@70", Kind: KindFYI, Body: "stale", Created: now.Add(-25 * time.Hour)})
	if err != nil {
		t.Fatalf("Enqueue stale: %v", err)
	}

	// Stale but already-read message: PruneStale must never touch read/ --
	// that stays Prune's job. Give it an old Created so it would be caught
	// by PruneStale if PruneStale mistakenly walked read/ too.
	staleReadID, err := Enqueue(Message{To: "@70", Kind: KindFYI, Body: "stale-read", Created: now.Add(-48 * time.Hour)})
	if err != nil {
		t.Fatalf("Enqueue stale-read: %v", err)
	}
	if err := MarkRead("@70", staleReadID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	deleted, err := PruneStale(24 * time.Hour)
	if err != nil {
		t.Fatalf("PruneStale: %v", err)
	}
	if deleted != 1 {
		t.Errorf("PruneStale deleted = %d, want 1 (only the stale pending message)", deleted)
	}

	pending, err := Pending("@70")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != freshID {
		t.Errorf("Pending after PruneStale = %+v, want only the fresh message %q", pending, freshID)
	}

	read, err := Read("@70")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read) != 1 || read[0].ID != staleReadID {
		t.Errorf("Read after PruneStale = %+v, want the stale-read message %q left untouched", read, staleReadID)
	}

	_ = staleID // deleted; nothing further to assert on it directly
}

func TestPruneStale_ReturnsZeroWhenNothingStale(t *testing.T) {
	isolate(t)

	if _, err := Enqueue(Message{To: "@71", Kind: KindFYI, Body: "fresh"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	deleted, err := PruneStale(MaxPendingAge)
	if err != nil {
		t.Fatalf("PruneStale: %v", err)
	}
	if deleted != 0 {
		t.Errorf("PruneStale deleted = %d, want 0", deleted)
	}
}

func TestPendingOnNonexistentWindowReturnsEmpty(t *testing.T) {
	isolate(t)

	pending, err := Pending("@does-not-exist")
	if err != nil {
		t.Fatalf("Pending on nonexistent window returned error: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("Pending on nonexistent window = %+v, want empty", pending)
	}

	read, err := Read("@does-not-exist")
	if err != nil {
		t.Fatalf("Read on nonexistent window returned error: %v", err)
	}
	if len(read) != 0 {
		t.Errorf("Read on nonexistent window = %+v, want empty", read)
	}
}

func TestRemoveInbox_RemovesPendingAndReadSubdir(t *testing.T) {
	isolate(t)

	msgID, err := Enqueue(Message{To: "@41", Kind: KindFYI, Body: "hello"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := MarkRead("@41", msgID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	// A second, still-pending message so RemoveInbox has to clear both the
	// pending directory and the read/ subdirectory.
	if _, err := Enqueue(Message{To: "@41", Kind: KindFYI, Body: "still pending"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := RemoveInbox("@41"); err != nil {
		t.Fatalf("RemoveInbox: %v", err)
	}

	pending, err := Pending("@41")
	if err != nil {
		t.Fatalf("Pending after RemoveInbox: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("Pending after RemoveInbox = %+v, want empty", pending)
	}
	read, err := Read("@41")
	if err != nil {
		t.Fatalf("Read after RemoveInbox: %v", err)
	}
	if len(read) != 0 {
		t.Errorf("Read after RemoveInbox = %+v, want empty", read)
	}
}

func TestRemoveInbox_MissingDirIsNotAnError(t *testing.T) {
	isolate(t)

	if err := RemoveInbox("@never-existed"); err != nil {
		t.Fatalf("RemoveInbox on a nonexistent inbox: %v", err)
	}
}
