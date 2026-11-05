package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// minimalUsageJSONL has one user turn and one assistant turn with known token counts.
const minimalUsageJSONL = `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"uuid":"u1","timestamp":"2024-01-15T10:00:00.000Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":500,"output_tokens":100,"cache_creation_input_tokens":50,"cache_read_input_tokens":200}},"uuid":"u2","timestamp":"2024-01-15T10:01:00.000Z"}
`

func writeUsageJSONL(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestAggregateUsage_Basic(t *testing.T) {
	dir := t.TempDir()
	logFile := writeUsageJSONL(t, dir, "s1", minimalUsageJSONL)

	start := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 15, 23, 59, 59, 0, time.UTC)

	byAgent := map[string][]Session{
		"go-engineer": {
			{
				ID:        "s1",
				Agent:     "go-engineer",
				Project:   dir,
				StartTime: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
				EndTime:   time.Date(2024, 1, 15, 10, 1, 0, 0, time.UTC),
				LogFile:   logFile,
			},
		},
	}

	got := AggregateUsage(byAgent, start, end)
	if len(got) != 1 {
		t.Fatalf("AggregateUsage returned %d entries, want 1", len(got))
	}
	u := got[0]
	if u.Agent != "go-engineer" {
		t.Errorf("Agent = %q, want go-engineer", u.Agent)
	}
	if u.Sessions != 1 {
		t.Errorf("Sessions = %d, want 1", u.Sessions)
	}
	if u.Turns != 1 {
		t.Errorf("Turns = %d, want 1", u.Turns)
	}
	if u.InputTokens != 500 {
		t.Errorf("InputTokens = %d, want 500", u.InputTokens)
	}
	if u.OutputTokens != 100 {
		t.Errorf("OutputTokens = %d, want 100", u.OutputTokens)
	}
	if u.TotalTokens() != 600 {
		t.Errorf("TotalTokens = %d, want 600", u.TotalTokens())
	}
}

func TestAggregateUsage_SkipsOutOfRange(t *testing.T) {
	dir := t.TempDir()
	logFile := writeUsageJSONL(t, dir, "s1", minimalUsageJSONL)

	// Range is yesterday; session is today — should be skipped.
	yesterday := time.Now().AddDate(0, 0, -1)
	from := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, time.UTC)
	to := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 23, 59, 59, 0, time.UTC)

	byAgent := map[string][]Session{
		"go-engineer": {
			{
				ID:        "s1",
				Agent:     "go-engineer",
				StartTime: time.Now(),
				EndTime:   time.Now().Add(time.Minute),
				LogFile:   logFile,
			},
		},
	}

	got := AggregateUsage(byAgent, from, to)
	if len(got) != 0 {
		t.Errorf("AggregateUsage returned %d entries for out-of-range session, want 0", len(got))
	}
}

func TestAggregateUsage_SortedByTotalTokens(t *testing.T) {
	dir := t.TempDir()

	bigJSONL := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"big"}]},"uuid":"u1","timestamp":"2024-01-15T10:00:00.000Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10000,"output_tokens":2000}},"uuid":"u2","timestamp":"2024-01-15T10:01:00.000Z"}
`
	smallJSONL := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"small"}]},"uuid":"u1","timestamp":"2024-01-15T10:00:00.000Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":100,"output_tokens":20}},"uuid":"u2","timestamp":"2024-01-15T10:01:00.000Z"}
`

	from := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 1, 15, 23, 59, 59, 0, time.UTC)

	byAgent := map[string][]Session{
		"small-agent": {{
			ID: "s", Agent: "small-agent",
			StartTime: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2024, 1, 15, 10, 1, 0, 0, time.UTC),
			LogFile:   writeUsageJSONL(t, dir, "small", smallJSONL),
		}},
		"big-agent": {{
			ID: "b", Agent: "big-agent",
			StartTime: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2024, 1, 15, 10, 1, 0, 0, time.UTC),
			LogFile:   writeUsageJSONL(t, dir, "big", bigJSONL),
		}},
	}

	got := AggregateUsage(byAgent, from, to)
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	if got[0].Agent != "big-agent" {
		t.Errorf("first entry = %q, want big-agent (highest tokens first)", got[0].Agent)
	}
}

func TestAggregateUsage_Empty(t *testing.T) {
	from := time.Now().Add(-time.Hour)
	to := time.Now()
	got := AggregateUsage(nil, from, to)
	if len(got) != 0 {
		t.Errorf("AggregateUsage(nil) = %v, want empty", got)
	}
}

func TestWriteUsageTable_Empty(t *testing.T) {
	var b strings.Builder
	WriteUsageTable(&b, nil)
	got := b.String()
	if !strings.Contains(got, "No usage data") {
		t.Errorf("WriteUsageTable(empty) = %q, want 'No usage data'", got)
	}
}

func TestWriteUsageTable_Single(t *testing.T) {
	usage := []AgentUsage{
		{
			Agent:        "go-engineer",
			Sessions:     2,
			Turns:        10,
			InputTokens:  5000,
			OutputTokens: 1000,
		},
	}

	var b strings.Builder
	WriteUsageTable(&b, usage)
	got := b.String()

	for _, want := range []string{"go-engineer", "TOTAL", "Agent", "Input", "Output"} {
		if !strings.Contains(got, want) {
			t.Errorf("WriteUsageTable output missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestFormatTokensShort(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1_000, "1.0k"},
		{42_381, "42.4k"},
		{100_000, "100k"},
		{142_381, "142k"},
		{999_999, "999k"},
		{1_000_000, "1.0M"},
		{1_409_000, "1.4M"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := FormatTokensShort(tt.n)
			if got != tt.want {
				t.Errorf("FormatTokensShort(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}
