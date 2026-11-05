package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		t.Run(tt.want, func(t *testing.T) {
			got := FormatTokenCount(tt.n)
			if got != tt.want {
				t.Errorf("FormatTokenCount(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

// minimalReportJSONL is a JSONL log with one user turn and one assistant turn.
const minimalReportJSONL = `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"implement ticket #201"}]},"uuid":"u1","timestamp":"2024-01-01T12:00:00.000Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I will implement the changes."}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":10,"cache_read_input_tokens":5}},"uuid":"u2","timestamp":"2024-01-01T12:01:00.000Z"}
`

func TestBuildReport(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test-session.jsonl")
	if err := os.WriteFile(logFile, []byte(minimalReportJSONL), 0o644); err != nil {
		t.Fatalf("write test JSONL: %v", err)
	}

	sess := []Session{
		{
			ID:        "test-session",
			Agent:     "go-engineer",
			Project:   dir,
			StartTime: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2024, 1, 1, 12, 1, 0, 0, time.UTC),
			LogFile:   logFile,
		},
	}

	got := BuildReport("go-engineer", sess)
	if got == "" {
		t.Fatal("BuildReport returned empty string; expected non-empty report")
	}
	for _, want := range []string{"go-engineer", "implement ticket #201", "Token Usage"} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestBuildReport_EmptySessions(t *testing.T) {
	got := BuildReport("go-engineer", nil)
	if got != "" {
		t.Errorf("BuildReport with nil sessions = %q, want empty string", got)
	}
}

func TestBuildReport_MultiSession(t *testing.T) {
	dir := t.TempDir()

	writeLog := func(name, userMsg string) string {
		path := filepath.Join(dir, name+".jsonl")
		content := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + userMsg + `"}]},"uuid":"u1","timestamp":"2024-01-01T12:00:00.000Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":5}},"uuid":"u2","timestamp":"2024-01-01T12:01:00.000Z"}
`
		_ = os.WriteFile(path, []byte(content), 0o644)
		return path
	}

	sess := []Session{
		{
			ID:        "s1",
			Agent:     "go-engineer",
			Project:   dir,
			StartTime: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2024, 1, 1, 10, 30, 0, 0, time.UTC),
			LogFile:   writeLog("s1", "first task"),
		},
		{
			ID:        "s2",
			Agent:     "go-engineer",
			Project:   dir,
			StartTime: time.Date(2024, 1, 1, 11, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2024, 1, 1, 11, 30, 0, 0, time.UTC),
			LogFile:   writeLog("s2", "second task"),
		},
	}

	got := BuildReport("go-engineer", sess)
	if !strings.Contains(got, "first task") {
		t.Errorf("report missing first session content")
	}
	if !strings.Contains(got, "second task") {
		t.Errorf("report missing second session content")
	}
}

func TestGetSessionPreview(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "preview-session.jsonl")
	if err := os.WriteFile(logFile, []byte(minimalReportJSONL), 0o644); err != nil {
		t.Fatalf("write test JSONL: %v", err)
	}

	s := Session{
		ID:        "preview-session",
		Agent:     "go-engineer",
		Project:   dir,
		StartTime: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		LogFile:   logFile,
	}

	got := GetSessionPreview(s)
	if got == "" {
		t.Fatal("GetSessionPreview returned empty string")
	}
	if !strings.Contains(got, "implement ticket") {
		t.Errorf("GetSessionPreview = %q, want to contain 'implement ticket'", got)
	}
}

func TestGetSessionPreview_Truncated(t *testing.T) {
	dir := t.TempDir()
	// Long user message (> 55 chars)
	longMsg := "this is a very long user message that exceeds the fifty-five character limit for previews"
	content := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + longMsg + `"}]},"uuid":"u1","timestamp":"2024-01-01T12:00:00.000Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":5}},"uuid":"u2","timestamp":"2024-01-01T12:01:00.000Z"}
`
	logFile := filepath.Join(dir, "long-session.jsonl")
	if err := os.WriteFile(logFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write test JSONL: %v", err)
	}

	s := Session{LogFile: logFile, StartTime: time.Now()}
	got := GetSessionPreview(s)
	if len(got) > 58 { // 55 chars + "..."
		t.Errorf("GetSessionPreview did not truncate: len=%d, got=%q", len(got), got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("GetSessionPreview truncated but missing '...': %q", got)
	}
}

func TestGetSessionPreview_NoMessages(t *testing.T) {
	dir := t.TempDir()
	// JSONL with no user messages
	content := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":10,"output_tokens":5}},"uuid":"u1","timestamp":"2024-01-01T12:00:00.000Z"}
`
	logFile := filepath.Join(dir, "no-user-session.jsonl")
	if err := os.WriteFile(logFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write test JSONL: %v", err)
	}

	s := Session{LogFile: logFile, StartTime: time.Now()}
	got := GetSessionPreview(s)
	if got != "" {
		t.Errorf("GetSessionPreview with no user messages = %q, want empty string", got)
	}
}
