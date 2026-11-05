package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/sessions"
)

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{1234, "1,234"},
		{999_999, "999,999"},
		{1_000_000, "1,000,000"},
	}

	for _, tt := range tests {
		got := sessions.FormatTokenCount(tt.n)
		if got != tt.want {
			t.Errorf("FormatTokenCount(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

// minimalSessionJSONL is a JSONL log with one user turn and one assistant turn,
// enough for ParseSummary to produce a non-empty Summary.
const minimalSessionJSONL = `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"implement ticket #201"}]},"uuid":"u1","timestamp":"2024-01-01T12:00:00.000Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I will implement the smoke tests."}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":10,"cache_read_input_tokens":5}},"uuid":"u2","timestamp":"2024-01-01T12:01:00.000Z"}
`

// TestBuildReport verifies that buildReport does not panic and produces
// non-empty output when given a valid Session pointing at a real JSONL file.
func TestBuildReport(t *testing.T) {
	// Write a minimal JSONL log to a temp dir.
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test-session.jsonl")
	if err := os.WriteFile(logFile, []byte(minimalSessionJSONL), 0o644); err != nil {
		t.Fatalf("write test JSONL: %v", err)
	}

	sess := []sessions.Session{
		{
			ID:        "test-session",
			Agent:     "go-engineer",
			Project:   dir,
			StartTime: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2024, 1, 1, 12, 1, 0, 0, time.UTC),
			LogFile:   logFile,
		},
	}

	got := buildReport("go-engineer", sess)
	if got == "" {
		t.Fatal("buildReport returned empty string; expected non-empty report")
	}
	// Spot-check that key sections appear.
	if !strings.Contains(got, "go-engineer") {
		t.Errorf("report missing agent name; got:\n%s", got)
	}
	if !strings.Contains(got, "implement ticket #201") {
		t.Errorf("report missing user message; got:\n%s", got)
	}
	if !strings.Contains(got, "Token Usage") {
		t.Errorf("report missing Token Usage section; got:\n%s", got)
	}
}
