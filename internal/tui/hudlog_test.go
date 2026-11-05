package tui

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTestLog writes content to a new file in dir and returns its path.
func writeTestLog(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write test log: %v", err)
	}
	return path
}

func TestTailAgentctlLog_ShrinkResetsOffset(t *testing.T) {
	dir := t.TempDir()
	path := writeTestLog(t, dir, "agentctl.log", "line one\nline two\nline three\n")

	// Simulate a stored offset far beyond the (now rotated/truncated) file's
	// current size, as would happen after lumberjack rotation replaces the
	// live file with a fresh, smaller one.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	staleOffset := info.Size() + 10_000

	lines, newOffset, err := TailAgentctlLog(path, staleOffset)
	if err != nil {
		t.Fatalf("TailAgentctlLog: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines after shrink reset, got %d: %v", len(lines), lines)
	}
	if newOffset != info.Size() {
		t.Fatalf("expected new offset %d (file size), got %d", info.Size(), newOffset)
	}
}

func TestTailAgentctlLog_IncrementalTail(t *testing.T) {
	dir := t.TempDir()
	path := writeTestLog(t, dir, "agentctl.log", "line one\nline two\n")

	lines, offset1, err := TailAgentctlLog(path, 0)
	if err != nil {
		t.Fatalf("TailAgentctlLog: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}

	// Append more content, then tail again from the returned offset.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString("line three\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	lines2, offset2, err := TailAgentctlLog(path, offset1)
	if err != nil {
		t.Fatalf("TailAgentctlLog: %v", err)
	}
	if len(lines2) != 1 {
		t.Fatalf("expected 1 new line, got %d: %v", len(lines2), lines2)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if offset2 != info.Size() {
		t.Fatalf("expected offset %d (file size), got %d", info.Size(), offset2)
	}
}

func TestTailAgentctlLog_OffsetEqualsSize(t *testing.T) {
	dir := t.TempDir()
	path := writeTestLog(t, dir, "agentctl.log", "line one\nline two\n")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	lines, newOffset, err := TailAgentctlLog(path, info.Size())
	if err != nil {
		t.Fatalf("TailAgentctlLog: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("expected no lines when offset == size, got %d: %v", len(lines), lines)
	}
	if newOffset != info.Size() {
		t.Fatalf("expected offset unchanged at %d, got %d", info.Size(), newOffset)
	}
}

func TestTailLog_ShrinkResetsOffset(t *testing.T) {
	dir := t.TempDir()
	content := `{"type":"assistant","message":{"model":"claude-x","content":[{"type":"text","text":"hello"}]}}` + "\n"
	path := writeTestLog(t, dir, "session.jsonl", content)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	staleOffset := info.Size() + 10_000

	result, err := TailLog(path, staleOffset)
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected 1 entry after shrink reset, got %d", len(result.Entries))
	}
	if result.NewOffset != info.Size() {
		t.Fatalf("expected new offset %d (file size), got %d", info.Size(), result.NewOffset)
	}
}

func TestTailLog_IncrementalTail(t *testing.T) {
	dir := t.TempDir()
	entry1 := `{"type":"assistant","message":{"model":"claude-x","content":[{"type":"text","text":"first"}]}}` + "\n"
	path := writeTestLog(t, dir, "session.jsonl", entry1)

	result1, err := TailLog(path, 0)
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if len(result1.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result1.Entries))
	}

	entry2 := `{"type":"assistant","message":{"model":"claude-x","content":[{"type":"text","text":"second"}]}}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString(entry2); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	result2, err := TailLog(path, result1.NewOffset)
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if len(result2.Entries) != 1 {
		t.Fatalf("expected 1 new entry, got %d", len(result2.Entries))
	}
	if result2.Entries[0].FullText != "second" {
		t.Fatalf("expected new entry text %q, got %q", "second", result2.Entries[0].FullText)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if result2.NewOffset != info.Size() {
		t.Fatalf("expected offset %d (file size), got %d", info.Size(), result2.NewOffset)
	}
}

func TestTailLog_OffsetEqualsSize(t *testing.T) {
	dir := t.TempDir()
	entry := `{"type":"assistant","message":{"model":"claude-x","content":[{"type":"text","text":"hello"}]}}` + "\n"
	path := writeTestLog(t, dir, "session.jsonl", entry)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	result, err := TailLog(path, info.Size())
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if len(result.Entries) != 0 {
		t.Fatalf("expected no entries when offset == size, got %d", len(result.Entries))
	}
	if result.NewOffset != info.Size() {
		t.Fatalf("expected offset unchanged at %d, got %d", info.Size(), result.NewOffset)
	}
}
