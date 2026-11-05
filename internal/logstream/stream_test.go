package logstream

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// recordingSubscriber records every OnEntry call and counts OnReset calls.
type recordingSubscriber struct {
	entries []Entry
	resets  int
}

func (r *recordingSubscriber) OnEntry(e Entry) { r.entries = append(r.entries, e) }
func (r *recordingSubscriber) OnReset()        { r.resets++; r.entries = nil }

// assistantLine builds a minimal assistant JSONL entry with the given token values.
func assistantLine(inputTokens, outputTokens, cacheCreate, cacheRead int) string {
	return fmt.Sprintf(
		`{"type":"assistant","message":{"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}`,
		inputTokens, outputTokens, cacheCreate, cacheRead,
	)
}

// writeTempFile writes lines (each followed by \n) to a temp file and returns its path.
func writeTempFile(t *testing.T, lines ...string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.jsonl")
	if err != nil {
		t.Fatalf("writeTempFile: create: %v", err)
	}
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("writeTempFile: close: %v", err)
	}
	return f.Name()
}

// appendToFile appends lines (each followed by \n) to an existing file.
func appendToFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("appendToFile: open: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
}

func TestStream_InitialRead(t *testing.T) {
	path := writeTempFile(t,
		assistantLine(100, 50, 0, 0),
		assistantLine(200, 80, 0, 0),
		assistantLine(300, 120, 0, 0),
		assistantLine(400, 160, 0, 0),
		assistantLine(500, 200, 0, 0),
	)

	rec := &recordingSubscriber{}
	s := NewStream(path)
	s.Subscribe(rec)

	n := s.Tick()
	if n != 5 {
		t.Errorf("Tick() = %d entries, want 5", n)
	}
	if len(rec.entries) != 5 {
		t.Errorf("subscriber received %d entries, want 5", len(rec.entries))
	}
	if s.offset == 0 {
		t.Error("Offset() = 0 after reading 5 entries, want > 0")
	}
}

func TestStream_TickNoNewData(t *testing.T) {
	path := writeTempFile(t, assistantLine(100, 50, 0, 0))

	rec := &recordingSubscriber{}
	s := NewStream(path)
	s.Subscribe(rec)

	s.Tick()
	offset1 := s.offset

	n := s.Tick()
	if n != 0 {
		t.Errorf("second Tick() = %d, want 0 (no new data)", n)
	}
	if s.offset != offset1 {
		t.Errorf("Offset() changed on no-data tick: %d → %d", offset1, s.offset)
	}
	if len(rec.entries) != 1 {
		t.Errorf("subscriber has %d entries after two ticks, want 1", len(rec.entries))
	}
}

func TestStream_IncrementalAppend(t *testing.T) {
	path := writeTempFile(t,
		assistantLine(100, 50, 0, 0),
		assistantLine(200, 80, 0, 0),
	)

	rec := &recordingSubscriber{}
	s := NewStream(path)
	s.Subscribe(rec)

	// First tick consumes the 2 existing entries.
	n1 := s.Tick()
	if n1 != 2 {
		t.Errorf("first Tick() = %d, want 2", n1)
	}

	// Append exactly 3 more entries.
	appendToFile(t, path,
		assistantLine(300, 120, 0, 0),
		assistantLine(400, 160, 0, 0),
		assistantLine(500, 200, 0, 0),
	)

	n2 := s.Tick()
	if n2 != 3 {
		t.Errorf("second Tick() = %d, want exactly 3 new entries", n2)
	}
	if len(rec.entries) != 5 {
		t.Errorf("subscriber has %d total entries, want 5", len(rec.entries))
	}
}

func TestStream_FileShrinkTriggersReset(t *testing.T) {
	path := writeTempFile(t,
		assistantLine(100, 50, 0, 0),
		assistantLine(200, 80, 0, 0),
		assistantLine(300, 120, 0, 0),
	)

	rec := &recordingSubscriber{}
	s := NewStream(path)
	s.Subscribe(rec)

	// Read all entries.
	s.Tick()
	if s.offset == 0 {
		t.Fatal("offset should be > 0 after initial read")
	}

	// Truncate the file to simulate /clear.
	if err := os.Truncate(path, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// Tick should detect shrink, call OnReset, and read 0 entries (empty file).
	n := s.Tick()
	if rec.resets != 1 {
		t.Errorf("resets = %d, want 1 after file shrink", rec.resets)
	}
	if n != 0 {
		t.Errorf("Tick() after truncate = %d, want 0 (file now empty)", n)
	}
	if s.offset != 0 {
		t.Errorf("Offset() = %d after reset, want 0", s.offset)
	}
}

func TestStream_PartialLineNotDelivered(t *testing.T) {
	// Write an entry without a trailing newline — must not be delivered.
	f, err := os.CreateTemp(t.TempDir(), "*.jsonl")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	path := f.Name()
	partial := assistantLine(100, 50, 0, 0) // no newline
	if _, err := f.WriteString(partial); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	f.Close()

	rec := &recordingSubscriber{}
	s := NewStream(path)
	s.Subscribe(rec)

	n := s.Tick()
	if n != 0 {
		t.Errorf("Tick() with partial line = %d, want 0", n)
	}
	if len(rec.entries) != 0 {
		t.Errorf("subscriber has %d entries, want 0 for partial line", len(rec.entries))
	}

	// Append newline to complete the first entry, plus a second full entry.
	af, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	fmt.Fprintln(af, "") // complete the partial line
	fmt.Fprintln(af, assistantLine(200, 80, 0, 0))
	af.Close()

	n2 := s.Tick()
	if n2 != 2 {
		t.Errorf("Tick() after completing partial = %d, want 2", n2)
	}
}

func TestStream_SetPath(t *testing.T) {
	path1 := writeTempFile(t,
		assistantLine(100, 50, 0, 0),
		assistantLine(200, 80, 0, 0),
	)
	path2 := writeTempFile(t,
		assistantLine(999, 111, 0, 0),
	)

	rec := &recordingSubscriber{}
	s := NewStream(path1)
	s.Subscribe(rec)

	s.Tick()
	if len(rec.entries) != 2 {
		t.Fatalf("initial tick: got %d entries, want 2", len(rec.entries))
	}

	// Switch to a new path — should reset and read from beginning of path2.
	s.SetPath(path2)
	if rec.resets != 1 {
		t.Errorf("resets = %d after SetPath, want 1", rec.resets)
	}
	if s.offset != 0 {
		t.Errorf("Offset() = %d after SetPath, want 0", s.offset)
	}

	n := s.Tick()
	if n != 1 {
		t.Errorf("Tick() after SetPath = %d, want 1", n)
	}
	if len(rec.entries) != 1 {
		t.Errorf("subscriber has %d entries after SetPath+Tick, want 1 (reset clears old)", len(rec.entries))
	}
}

func TestStream_EmptyPath(t *testing.T) {
	rec := &recordingSubscriber{}
	s := NewStream("")
	s.Subscribe(rec)

	n := s.Tick()
	if n != 0 {
		t.Errorf("Tick() on empty path = %d, want 0", n)
	}
}

func TestStream_EntryParsing(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2024-01-15T10:30:00Z","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":1000,"output_tokens":200,"cache_creation_input_tokens":500,"cache_read_input_tokens":800},"content":[{"type":"text","text":"Hello"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`
	path := writeTempFile(t, line)

	rec := &recordingSubscriber{}
	s := NewStream(path)
	s.Subscribe(rec)
	s.Tick()

	if len(rec.entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(rec.entries))
	}
	e := rec.entries[0]

	if e.Type != "assistant" {
		t.Errorf("Type = %q, want 'assistant'", e.Type)
	}
	if e.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want 'claude-sonnet-4-20250514'", e.Model)
	}
	if e.Timestamp.IsZero() {
		t.Error("Timestamp is zero, want parsed time")
	}
	if e.Usage == nil {
		t.Fatal("Usage is nil, want non-nil")
	}
	if e.Usage.InputTokens != 1000 {
		t.Errorf("Usage.InputTokens = %d, want 1000", e.Usage.InputTokens)
	}
	if e.Usage.OutputTokens != 200 {
		t.Errorf("Usage.OutputTokens = %d, want 200", e.Usage.OutputTokens)
	}
	if e.Usage.CacheCreateTokens != 500 {
		t.Errorf("Usage.CacheCreateTokens = %d, want 500", e.Usage.CacheCreateTokens)
	}
	if e.Usage.CacheReadTokens != 800 {
		t.Errorf("Usage.CacheReadTokens = %d, want 800", e.Usage.CacheReadTokens)
	}
	if len(e.Content) != 2 {
		t.Fatalf("Content len = %d, want 2", len(e.Content))
	}
	if e.Content[0].Type != "text" || e.Content[0].Text != "Hello" {
		t.Errorf("Content[0] = %+v, want text block 'Hello'", e.Content[0])
	}
	if e.Content[1].Type != "tool_use" || e.Content[1].ToolName != "Bash" {
		t.Errorf("Content[1] = %+v, want tool_use 'Bash'", e.Content[1])
	}
}

func TestStream_NonAssistantEntries(t *testing.T) {
	// Non-assistant entries should be delivered but have no Usage.
	path := writeTempFile(t,
		`{"type":"user","message":{"content":[{"type":"text","text":"hi"}]}}`,
		assistantLine(100, 50, 0, 0),
		`{"type":"agent_setting","agentSetting":"go-engineer"}`,
	)

	rec := &recordingSubscriber{}
	s := NewStream(path)
	s.Subscribe(rec)
	s.Tick()

	if len(rec.entries) != 3 {
		t.Errorf("got %d entries, want 3 (all types delivered)", len(rec.entries))
	}
	if rec.entries[0].Type != "user" {
		t.Errorf("entries[0].Type = %q, want 'user'", rec.entries[0].Type)
	}
	if rec.entries[0].Usage != nil {
		t.Error("user entry has non-nil Usage, want nil")
	}
}

func TestStream_OversizedLineSkippedLaterEntriesDelivered(t *testing.T) {
	old := maxLineBytes
	maxLineBytes = 1024
	t.Cleanup(func() { maxLineBytes = old })

	big := `{"type":"user","pad":"` + strings.Repeat("x", 200*1024) + `"}`
	path := writeTempFile(t,
		assistantLine(1, 1, 0, 0),
		big,
		assistantLine(2, 2, 0, 0),
		assistantLine(3, 3, 0, 0),
	)
	sub := &recordingSubscriber{}
	s := NewStream(path)
	s.Subscribe(sub)

	if n := s.Tick(); n != 3 {
		t.Fatalf("Tick delivered %d entries, want 3 (oversized line skipped)", n)
	}
	st, _ := os.Stat(path)
	if s.offset != st.Size() {
		t.Fatalf("offset = %d, want file size %d", s.offset, st.Size())
	}

	// Offset must be correct across calls: only new entries are delivered.
	if n := s.Tick(); n != 0 {
		t.Fatalf("second Tick delivered %d, want 0", n)
	}
	appendToFile(t, path, assistantLine(4, 4, 0, 0))
	if n := s.Tick(); n != 1 {
		t.Fatalf("third Tick delivered %d, want 1", n)
	}
	if len(sub.entries) != 4 {
		t.Fatalf("subscriber saw %d entries, want 4", len(sub.entries))
	}
	if got := sub.entries[3].Usage.InputTokens; got != 4 {
		t.Errorf("last entry input tokens = %d, want 4", got)
	}
}

func TestStream_PartialOversizedLineWaitsForNewline(t *testing.T) {
	old := maxLineBytes
	maxLineBytes = 1024
	t.Cleanup(func() { maxLineBytes = old })

	path := writeTempFile(t, assistantLine(1, 1, 0, 0))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Partial oversized line, no newline yet.
	if _, err := f.WriteString(strings.Repeat("y", 10*1024)); err != nil {
		t.Fatal(err)
	}

	s := NewStream(path)
	if n := s.Tick(); n != 1 {
		t.Fatalf("first Tick = %d, want 1", n)
	}
	firstLineEnd := s.offset
	if n := s.Tick(); n != 0 || s.offset != firstLineEnd {
		t.Fatalf("partial line must not advance offset: n=%d off=%d want %d", n, s.offset, firstLineEnd)
	}

	// Finish the oversized line and add a good one.
	if _, err := f.WriteString("\n" + assistantLine(2, 2, 0, 0) + "\n"); err != nil {
		t.Fatal(err)
	}
	if n := s.Tick(); n != 1 {
		t.Fatalf("after completion Tick = %d, want 1", n)
	}
	st, _ := os.Stat(path)
	if s.offset != st.Size() {
		t.Fatalf("offset = %d, want %d", s.offset, st.Size())
	}
}

func TestStream_LineExactlyAtCapIsDelivered(t *testing.T) {
	old := maxLineBytes
	t.Cleanup(func() { maxLineBytes = old })
	line := assistantLine(5, 5, 0, 0)
	maxLineBytes = len(line)
	path := writeTempFile(t, line)
	s := NewStream(path)
	if n := s.Tick(); n != 1 {
		t.Fatalf("Tick = %d, want 1 for line exactly at cap", n)
	}
}
