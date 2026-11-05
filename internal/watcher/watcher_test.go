package watcher

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// fixtureSignalJSONL is the path to the bundled test fixture.
const fixtureSignalJSONL = "testdata/signal.jsonl"

func TestScanMatchesAssistantText(t *testing.T) {
	w := New()

	var fired []ContentMatch
	w.Register(ContentRule{
		Name:    "agent-signal",
		Pattern: regexp.MustCompile(`AGENTCTL:(\w+)`),
		Action: func(m ContentMatch) {
			fired = append(fired, m)
		},
	})

	w.Scan(fixtureSignalJSONL, "go-engineer", "go-engineer~1", "@3")

	if len(fired) != 1 {
		t.Fatalf("expected 1 match, got %d", len(fired))
	}
	m := fired[0]
	if m.Rule != "agent-signal" {
		t.Errorf("Rule = %q, want %q", m.Rule, "agent-signal")
	}
	if m.Agent != "go-engineer" {
		t.Errorf("Agent = %q, want %q", m.Agent, "go-engineer")
	}
	if m.Instance != "go-engineer~1" {
		t.Errorf("Instance = %q, want %q", m.Instance, "go-engineer~1")
	}
	if m.WindowID != "@3" {
		t.Errorf("WindowID = %q, want %q", m.WindowID, "@3")
	}
	if len(m.Submatches) < 2 {
		t.Fatalf("expected at least 2 submatches, got %d: %v", len(m.Submatches), m.Submatches)
	}
	if m.Submatches[0] != "AGENTCTL:DONE" {
		t.Errorf("Submatches[0] = %q, want %q", m.Submatches[0], "AGENTCTL:DONE")
	}
	if m.Submatches[1] != "DONE" {
		t.Errorf("Submatches[1] = %q, want %q", m.Submatches[1], "DONE")
	}
}

func TestScanSkipsNonAssistant(t *testing.T) {
	// Write a JSONL file with only user and system entries.
	dir := t.TempDir()
	path := filepath.Join(dir, "non-assistant.jsonl")
	content := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"AGENTCTL:DONE"}]}}` + "\n" +
		`{"type":"system","message":{"role":"system","content":[{"type":"text","text":"AGENTCTL:DONE"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	w := New()
	callCount := 0
	w.Register(ContentRule{
		Name:    "signal",
		Pattern: regexp.MustCompile(`AGENTCTL:(\w+)`),
		Action: func(m ContentMatch) {
			callCount++
		},
	})

	w.Scan(path, "test-agent", "test-agent", "@1")
	if callCount != 0 {
		t.Errorf("callback fired %d times on non-assistant entries, want 0", callCount)
	}
}

func TestScanIncremental(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "incremental.jsonl")

	// Write first entry (no signal).
	first := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Starting work."}]}}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatalf("writing initial file: %v", err)
	}

	w := New()
	var fired []ContentMatch
	w.Register(ContentRule{
		Name:    "signal",
		Pattern: regexp.MustCompile(`AGENTCTL:(\w+)`),
		Action: func(m ContentMatch) {
			fired = append(fired, m)
		},
	})

	// First scan — no signal yet.
	w.Scan(path, "agent", "agent", "@1")
	if len(fired) != 0 {
		t.Fatalf("first scan: expected 0 matches, got %d", len(fired))
	}

	// Append a second entry with a signal.
	second := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"AGENTCTL:DONE"}]}}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("opening file for append: %v", err)
	}
	if _, err := f.WriteString(second); err != nil {
		f.Close()
		t.Fatalf("appending: %v", err)
	}
	f.Close()

	// Second scan — should only see the new entry.
	w.Scan(path, "agent", "agent", "@1")
	if len(fired) != 1 {
		t.Fatalf("second scan: expected 1 match, got %d", len(fired))
	}
}

func TestResetCursor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reset.jsonl")

	content := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"AGENTCTL:DONE"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	w := New()
	var fired []ContentMatch
	w.Register(ContentRule{
		Name:    "signal",
		Pattern: regexp.MustCompile(`AGENTCTL:(\w+)`),
		Action: func(m ContentMatch) {
			fired = append(fired, m)
		},
	})

	// First scan — cursor advances past the entry.
	w.Scan(path, "agent", "agent", "@1")
	if len(fired) != 1 {
		t.Fatalf("first scan: expected 1 match, got %d", len(fired))
	}

	// Second scan without reset — no new content, so no new matches.
	w.Scan(path, "agent", "agent", "@1")
	if len(fired) != 1 {
		t.Fatalf("second scan (no reset): expected still 1 match, got %d", len(fired))
	}

	// Reset cursor and scan again — should re-read from the beginning.
	w.ResetCursor(path)
	w.Scan(path, "agent", "agent", "@1")
	if len(fired) != 2 {
		t.Fatalf("after reset: expected 2 total matches, got %d", len(fired))
	}
}

func TestScanEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatalf("creating empty file: %v", err)
	}

	w := New()
	w.Register(ContentRule{
		Name:    "signal",
		Pattern: regexp.MustCompile(`AGENTCTL:(\w+)`),
		Action: func(m ContentMatch) {
			t.Errorf("callback fired unexpectedly on empty file")
		},
	})

	// Should not panic.
	w.Scan(path, "agent", "agent", "@1")
}

func TestScanNoRules(t *testing.T) {
	w := New()

	// No rules registered — should not panic even with content present.
	w.Scan(fixtureSignalJSONL, "go-engineer", "go-engineer", "@1")
}

// TestResetCursor_ClearsMap verifies that ResetCursor removes the cursor entry
// from the internal map so memory doesn't grow unboundedly as log paths come and go.
func TestResetCursor_ClearsMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor-map.jsonl")

	content := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"AGENTCTL:DONE"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	w := New()
	w.Register(ContentRule{
		Name:    "signal",
		Pattern: regexp.MustCompile(`AGENTCTL:(\w+)`),
		Action:  func(ContentMatch) {},
	})

	// After a scan the cursor map should have exactly one entry.
	w.Scan(path, "agent", "agent", "@1")
	if len(w.cursors) != 1 {
		t.Fatalf("after Scan: expected 1 cursor entry, got %d", len(w.cursors))
	}

	// ResetCursor must remove the entry entirely so the map doesn't grow forever.
	w.ResetCursor(path)
	if len(w.cursors) != 0 {
		t.Fatalf("after ResetCursor: expected 0 cursor entries, got %d", len(w.cursors))
	}
}
