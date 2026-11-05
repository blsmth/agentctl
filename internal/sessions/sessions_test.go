package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		name  string
		entry map[string]any
		want  time.Time
	}{
		{
			name:  "RFC3339Nano",
			entry: map[string]any{"timestamp": "2026-03-25T10:30:00.123456789Z"},
			want:  time.Date(2026, 3, 25, 10, 30, 0, 123456789, time.UTC),
		},
		{
			name:  "RFC3339 no nanos",
			entry: map[string]any{"timestamp": "2026-03-25T10:30:00Z"},
			want:  time.Date(2026, 3, 25, 10, 30, 0, 0, time.UTC),
		},
		{
			name:  "invalid string",
			entry: map[string]any{"timestamp": "not-a-date"},
			want:  time.Time{},
		},
		{
			name:  "missing key",
			entry: map[string]any{},
			want:  time.Time{},
		},
		{
			name:  "wrong type",
			entry: map[string]any{"timestamp": 12345},
			want:  time.Time{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTimestamp(tt.entry)
			if !got.Equal(tt.want) {
				t.Errorf("parseTimestamp(%v) = %v, want %v", tt.entry, got, tt.want)
			}
		})
	}
}

func TestEncodeDirName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"typical path", "/Users/alice/src", "Users-alice-src"},
		{"short path", "/tmp", "tmp"},
		{"deep path", "/a/b/c/d", "a-b-c-d"},
		{"dots in path", "/Users/alice/src/acme-ops", "Users-alice-src-acme-ops"},
		{"underscore and dot", "/Users/alice/acme/data_pipelines.v2", "Users-alice-acme-data-pipelines-v2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := encodeDirName(tt.input)
			if got != tt.want {
				t.Errorf("encodeDirName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeProjectName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "typical encoded path",
			input: "/Users-alice-src-agentctl",
			want:  "agentctl",
		},
		{
			name:  "simple project",
			input: "/Users-alice-src-webapp",
			want:  "webapp",
		},
		{
			name:  "worktree path stripped to parent project",
			input: "/Users-alice-src-agentctl--worktrees-go-engineer-issue-42",
			want:  "agentctl",
		},
		{
			name:  "worktree with complex slug",
			input: "/Users-alice-src-agentctl--worktrees-agent-go-engineer-issue-748",
			want:  "agentctl",
		},
		{
			name:  "no hyphens — single segment",
			input: "/agentctl",
			want:  "agentctl",
		},
		{
			name:  "empty string — returned unchanged",
			input: "",
			want:  "",
		},
		{
			name:  "just a slash — returned as slash",
			input: "/",
			want:  "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeProjectName(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeProjectName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDecodeDirName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"typical", "Users-alice-src", "/Users-alice-src"},
		{"short", "tmp", "/tmp"},
		{"empty", "", "/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeDirName(tt.input)
			if got != tt.want {
				t.Errorf("decodeDirName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAbbreviateHome(t *testing.T) {
	// This test depends on the actual home dir, so we test the logic pattern
	// rather than hardcoding paths.
	t.Run("non-home path unchanged", func(t *testing.T) {
		got := abbreviateHome("/etc/config")
		if got != "/etc/config" {
			t.Errorf("abbreviateHome(/etc/config) = %q, want /etc/config", got)
		}
	})

	t.Run("empty string", func(t *testing.T) {
		got := abbreviateHome("")
		if got != "" {
			t.Errorf("abbreviateHome('') = %q, want empty", got)
		}
	})
}

func TestExtractToolDetail(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    map[string]any
		want     string
	}{
		{
			"Bash with command",
			"Bash",
			map[string]any{"command": "go build ./..."},
			"go build ./...",
		},
		{
			"Bash with long command truncated",
			"Bash",
			map[string]any{"command": "this is a very long command that exceeds eighty characters and should be truncated at the boundary here"},
			"this is a very long command that exceeds eighty characters and should be truncat…",
		},
		{
			"Bash with only description",
			"Bash",
			map[string]any{"description": "Build the project"},
			"Build the project",
		},
		{
			"Read with file_path",
			"Read",
			map[string]any{"file_path": "/etc/config.yaml"},
			"/etc/config.yaml",
		},
		{
			"Edit with file_path",
			"Edit",
			map[string]any{"file_path": "/etc/hosts"},
			"/etc/hosts",
		},
		{
			"Write with file_path",
			"Write",
			map[string]any{"file_path": "/tmp/out.txt"},
			"/tmp/out.txt",
		},
		{
			"Glob with pattern",
			"Glob",
			map[string]any{"pattern": "**/*.go"},
			"**/*.go",
		},
		{
			"Grep with pattern",
			"Grep",
			map[string]any{"pattern": "TODO"},
			"TODO",
		},
		{
			"Agent with description",
			"Agent",
			map[string]any{"description": "Run tests"},
			"Run tests",
		},
		{
			"unknown tool",
			"UnknownTool",
			map[string]any{"foo": "bar"},
			"",
		},
		{
			"empty input",
			"Bash",
			map[string]any{},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolDetail(tt.toolName, tt.input)
			if got != tt.want {
				t.Errorf("extractToolDetail(%q, ...) = %q, want %q", tt.toolName, got, tt.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"seconds", 30 * time.Second, "30s"},
		{"minutes", 5 * time.Minute, "5m"},
		{"hours", 2 * time.Hour, "2h"},
		{"hours and minutes", 90 * time.Minute, "1h30m"},
		{"zero", 0, "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.d)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// -- Fixture-based tests --

func TestParseSummary_Complete(t *testing.T) {
	s := Session{LogFile: "testdata/complete.jsonl"}
	sum, err := ParseSummary(s)
	if err != nil {
		t.Fatalf("ParseSummary error: %v", err)
	}

	if len(sum.UserMsgs) != 1 {
		t.Errorf("UserMsgs count = %d, want 1", len(sum.UserMsgs))
	} else if got := sum.UserMsgs[0]; got != "Fix the build error in main.go" {
		t.Errorf("UserMsgs[0] = %q, want 'Fix the build error in main.go'", got)
	}

	if len(sum.ToolCalls) != 2 {
		t.Errorf("ToolCalls count = %d, want 2", len(sum.ToolCalls))
	} else {
		if sum.ToolCalls[0].Name != "Read" {
			t.Errorf("ToolCalls[0].Name = %q, want Read", sum.ToolCalls[0].Name)
		}
		if sum.ToolCalls[1].Name != "Edit" {
			t.Errorf("ToolCalls[1].Name = %q, want Edit", sum.ToolCalls[1].Name)
		}
	}

	if len(sum.FilesRead) != 1 || sum.FilesRead[0] != "/Users/test/main.go" {
		t.Errorf("FilesRead = %v, want [/Users/test/main.go]", sum.FilesRead)
	}

	if len(sum.FilesEdited) != 1 || sum.FilesEdited[0] != "/Users/test/main.go" {
		t.Errorf("FilesEdited = %v, want [/Users/test/main.go]", sum.FilesEdited)
	}

	if sum.TurnCount != 3 {
		t.Errorf("TurnCount = %d, want 3", sum.TurnCount)
	}

	if sum.TotalInputTokens <= 0 {
		t.Errorf("TotalInputTokens = %d, want > 0", sum.TotalInputTokens)
	}
	if sum.TotalOutputTokens <= 0 {
		t.Errorf("TotalOutputTokens = %d, want > 0", sum.TotalOutputTokens)
	}
}

func TestParseSummary_Empty(t *testing.T) {
	s := Session{LogFile: "testdata/empty.jsonl"}
	sum, err := ParseSummary(s)
	if err != nil {
		t.Fatalf("ParseSummary error: %v", err)
	}

	if len(sum.UserMsgs) != 0 {
		t.Errorf("UserMsgs count = %d, want 0", len(sum.UserMsgs))
	}
	if len(sum.ToolCalls) != 0 {
		t.Errorf("ToolCalls count = %d, want 0", len(sum.ToolCalls))
	}
}

func TestParseSummary_Malformed(t *testing.T) {
	s := Session{LogFile: "testdata/malformed.jsonl"}
	sum, err := ParseSummary(s)
	if err != nil {
		t.Fatalf("ParseSummary error: %v", err)
	}

	if len(sum.UserMsgs) != 1 {
		t.Errorf("UserMsgs count = %d, want 1", len(sum.UserMsgs))
	}
	if len(sum.TextBlocks) != 1 {
		t.Errorf("TextBlocks count = %d, want 1", len(sum.TextBlocks))
	}
}

func TestSessionAgent_Complete(t *testing.T) {
	got := sessionAgent("testdata/complete.jsonl")
	if got != "test-agent" {
		t.Errorf("sessionAgent(complete) = %q, want 'test-agent'", got)
	}
}

func TestSessionAgent_Empty(t *testing.T) {
	got := sessionAgent("testdata/empty.jsonl")
	if got != "" {
		t.Errorf("sessionAgent(empty) = %q, want empty", got)
	}
}

func TestToolBreakdown(t *testing.T) {
	sum := &Summary{
		ToolCalls: []ToolCall{
			{Name: "Read"},
			{Name: "Edit"},
			{Name: "Read"},
			{Name: "Bash"},
			{Name: "Read"},
			{Name: "Edit"},
		},
	}

	got := sum.ToolBreakdown()

	if got["Read"] != 3 {
		t.Errorf("ToolBreakdown[Read] = %d, want 3", got["Read"])
	}
	if got["Edit"] != 2 {
		t.Errorf("ToolBreakdown[Edit] = %d, want 2", got["Edit"])
	}
	if got["Bash"] != 1 {
		t.Errorf("ToolBreakdown[Bash] = %d, want 1", got["Bash"])
	}
	if len(got) != 3 {
		t.Errorf("ToolBreakdown len = %d, want 3", len(got))
	}
}

func TestToolBreakdown_Empty(t *testing.T) {
	sum := &Summary{}
	got := sum.ToolBreakdown()
	if len(got) != 0 {
		t.Errorf("ToolBreakdown(empty) = %v, want empty map", got)
	}
}

func TestParseSummary_GitBranch(t *testing.T) {
	s := Session{LogFile: "testdata/gitbranch.jsonl"}
	sum, err := ParseSummary(s)
	if err != nil {
		t.Fatalf("ParseSummary error: %v", err)
	}

	want := "agent/go-engineer/issue-697"
	if sum.GitBranch != want {
		t.Errorf("GitBranch = %q, want %q", sum.GitBranch, want)
	}
}

func TestParseSummary_GitBranch_Missing(t *testing.T) {
	// complete.jsonl has no gitBranch field — should remain empty.
	s := Session{LogFile: "testdata/complete.jsonl"}
	sum, err := ParseSummary(s)
	if err != nil {
		t.Fatalf("ParseSummary error: %v", err)
	}

	if sum.GitBranch != "" {
		t.Errorf("GitBranch = %q, want empty (no gitBranch in file)", sum.GitBranch)
	}
}

// TestParseSummary_LastInputTokens verifies that LastInputTokens reflects the
// LAST assistant turn's input token count, not the accumulated total.
func TestParseSummary_LastInputTokens(t *testing.T) {
	s := Session{LogFile: "testdata/multiturn.jsonl"}
	sum, err := ParseSummary(s)
	if err != nil {
		t.Fatalf("ParseSummary error: %v", err)
	}

	// Turn 1: input=100 (raw), cache_create=200, cache_read=50 → LastInputTokens=100
	// Turn 2: input=500 (raw), cache_create=0, cache_read=300 → LastInputTokens=500
	// LastInputTokens should be 500 (last turn raw input only).
	const wantLast = 500
	if sum.LastInputTokens != wantLast {
		t.Errorf("LastInputTokens = %d, want %d (last turn raw input only)", sum.LastInputTokens, wantLast)
	}

	// TotalInputTokens accumulates raw input across all turns (100+500=600).
	const wantTotal = 600
	if sum.TotalInputTokens != wantTotal {
		t.Errorf("TotalInputTokens = %d, want %d (raw input only, not counting cache)", sum.TotalInputTokens, wantTotal)
	}

	// LastCacheReadTokens and LastCacheCreateTokens reflect the final turn only.
	// Turn 2: cache_create=0, cache_read=300.
	const wantLastCacheRead = 300
	if sum.LastCacheReadTokens != wantLastCacheRead {
		t.Errorf("LastCacheReadTokens = %d, want %d (last turn cache_read only)", sum.LastCacheReadTokens, wantLastCacheRead)
	}
	const wantLastCacheCreate = 0
	if sum.LastCacheCreateTokens != wantLastCacheCreate {
		t.Errorf("LastCacheCreateTokens = %d, want %d (last turn cache_create only)", sum.LastCacheCreateTokens, wantLastCacheCreate)
	}
}

// ── FindLatestLogIn ───────────────────────────────────────────────────────────

// writeSessionFixture creates a JSONL log file under projectsDir/hash/filename
// with the given agentSetting, sets its mod time to modtime, and returns the
// full file path.
func writeSessionFixture(t *testing.T, projectsDir, hash, filename, agentName string, modtime time.Time) string {
	t.Helper()
	dir := filepath.Join(projectsDir, hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	fp := filepath.Join(dir, filename)
	content := fmt.Sprintf(`{"agentSetting":"%s"}`, agentName)
	if err := os.WriteFile(fp, []byte(content+"\n"), 0o644); err != nil {
		t.Fatalf("writeFile %s: %v", fp, err)
	}
	if err := os.Chtimes(fp, modtime, modtime); err != nil {
		t.Fatalf("chtimes %s: %v", fp, err)
	}
	return fp
}

func TestFindLatestLogIn_ReturnsNewest(t *testing.T) {
	projectsDir := t.TempDir()
	now := time.Now()

	writeSessionFixture(t, projectsDir, "hash1", "old.jsonl", "go-engineer", now.Add(-30*time.Minute))
	want := writeSessionFixture(t, projectsDir, "hash2", "new.jsonl", "go-engineer", now.Add(-5*time.Minute))
	writeSessionFixture(t, projectsDir, "hash3", "other.jsonl", "python-engineer", now.Add(-1*time.Minute))

	got := FindLatestLogIn(projectsDir, "go-engineer", time.Time{}, nil)
	if got != want {
		t.Errorf("FindLatestLogIn = %q, want %q", got, want)
	}
}

func TestFindLatestLogIn_RespectsExcludePaths(t *testing.T) {
	projectsDir := t.TempDir()
	now := time.Now()

	old := writeSessionFixture(t, projectsDir, "hash1", "old.jsonl", "go-engineer", now.Add(-20*time.Minute))
	newest := writeSessionFixture(t, projectsDir, "hash2", "newest.jsonl", "go-engineer", now.Add(-5*time.Minute))

	// Exclude the newest — should fall back to old.
	got := FindLatestLogIn(projectsDir, "go-engineer", time.Time{}, map[string]bool{newest: true})
	if got != old {
		t.Errorf("FindLatestLogIn with exclusion = %q, want %q", got, old)
	}
}

func TestFindLatestLogIn_WrongAgentNotReturned(t *testing.T) {
	projectsDir := t.TempDir()
	now := time.Now()

	writeSessionFixture(t, projectsDir, "hash1", "session.jsonl", "python-engineer", now.Add(-5*time.Minute))

	got := FindLatestLogIn(projectsDir, "go-engineer", time.Time{}, nil)
	if got != "" {
		t.Errorf("FindLatestLogIn = %q, want empty (wrong agent)", got)
	}
}

func TestFindLatestLogIn_EmptyDir(t *testing.T) {
	projectsDir := t.TempDir()
	got := FindLatestLogIn(projectsDir, "go-engineer", time.Time{}, nil)
	if got != "" {
		t.Errorf("FindLatestLogIn on empty dir = %q, want empty", got)
	}
}

func TestFindLatestLogIn_MissingDir(t *testing.T) {
	got := FindLatestLogIn("/nonexistent/path", "go-engineer", time.Time{}, nil)
	if got != "" {
		t.Errorf("FindLatestLogIn on missing dir = %q, want empty", got)
	}
}

// ── ReadStartTime ─────────────────────────────────────────────────────────────

func TestReadStartTime_ParsesTimestamp(t *testing.T) {
	tmp := t.TempDir()
	fp := filepath.Join(tmp, "session.jsonl")
	content := `{"type":"user","timestamp":"2026-01-15T10:30:00Z","message":{"content":[]}}` + "\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := ReadStartTime(fp)
	want := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ReadStartTime = %v, want %v", got, want)
	}
}

func TestReadStartTime_NoTimestamp(t *testing.T) {
	tmp := t.TempDir()
	fp := filepath.Join(tmp, "session.jsonl")
	content := `{"type":"user","message":{"content":[]}}` + "\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := ReadStartTime(fp)
	if !got.IsZero() {
		t.Errorf("ReadStartTime = %v, want zero time", got)
	}
}

func TestReadStartTime_MissingFile(t *testing.T) {
	got := ReadStartTime("/nonexistent/path/session.jsonl")
	if !got.IsZero() {
		t.Errorf("ReadStartTime on missing file = %v, want zero time", got)
	}
}

// ── ReadFirstUserMessage ──────────────────────────────────────────────────────

func TestReadFirstUserMessage_ReturnsFirstText(t *testing.T) {
	tmp := t.TempDir()
	fp := filepath.Join(tmp, "session.jsonl")
	content := `{"type":"assistant","message":{"content":[]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"text","text":"Fix the bug in main.go"}]}}` + "\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := ReadFirstUserMessage(fp)
	want := "Fix the bug in main.go"
	if got != want {
		t.Errorf("ReadFirstUserMessage = %q, want %q", got, want)
	}
}

func TestReadFirstUserMessage_Truncates(t *testing.T) {
	tmp := t.TempDir()
	fp := filepath.Join(tmp, "session.jsonl")
	longMsg := fmt.Sprintf(`{"type":"user","message":{"content":[{"type":"text","text":"%s"}]}}`,
		string(make([]byte, 300)))
	if err := os.WriteFile(fp, []byte(longMsg+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := ReadFirstUserMessage(fp)
	if len(got) > 200 {
		t.Errorf("ReadFirstUserMessage len = %d, want ≤200", len(got))
	}
}

func TestReadFirstUserMessage_NoUserMessage(t *testing.T) {
	tmp := t.TempDir()
	fp := filepath.Join(tmp, "session.jsonl")
	content := `{"type":"assistant","message":{"content":[]}}` + "\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := ReadFirstUserMessage(fp)
	if got != "" {
		t.Errorf("ReadFirstUserMessage with no user message = %q, want empty", got)
	}
}

func TestReadFirstUserMessage_MissingFile(t *testing.T) {
	got := ReadFirstUserMessage("/nonexistent/path/session.jsonl")
	if got != "" {
		t.Errorf("ReadFirstUserMessage on missing file = %q, want empty", got)
	}
}

// TestParseSummary_SyntheticModelDoesNotClobberModelName verifies that a
// "<synthetic>" assistant entry does not overwrite a previously-observed
// real model name in the parsed Summary.
func TestParseSummary_SyntheticModelDoesNotClobberModelName(t *testing.T) {
	tmp := t.TempDir()
	fp := filepath.Join(tmp, "session.jsonl")
	lines := []string{
		`{"type":"assistant","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":100,"output_tokens":50}}}`,
		`{"type":"assistant","message":{"model":"<synthetic>","usage":{"input_tokens":10,"output_tokens":5}}}`,
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	sum, err := ParseSummary(Session{LogFile: fp})
	if err != nil {
		t.Fatalf("ParseSummary error: %v", err)
	}

	if sum.ModelName != "claude-sonnet-4-20250514" {
		t.Errorf("ModelName = %q, want claude-sonnet-4-20250514 (must not be clobbered by <synthetic>)", sum.ModelName)
	}
	if sum.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2 (synthetic entry still counts as a turn)", sum.TurnCount)
	}
}

// ── Missing ~/.claude/projects directory (regression: a fresh install with
// no sessions recorded yet, or a machine where ~/.claude has never been
// created, must not be treated as a hard error) ─────────────────────────────

func withTempClaudeDir(t *testing.T, dir string) {
	t.Helper()
	orig := claudeDir
	SetClaudeDirForTest(dir)
	t.Cleanup(func() { SetClaudeDirForTest(orig) })
}

func TestFindAllSessions_MissingProjectsDirReturnsEmpty(t *testing.T) {
	// claudeDir exists but has no "projects" subdirectory at all — the
	// state of a brand new install.
	withTempClaudeDir(t, t.TempDir())

	got, err := findAllSessions()
	if err != nil {
		t.Fatalf("findAllSessions() error = %v, want nil (missing projects dir should degrade to empty)", err)
	}
	if len(got) != 0 {
		t.Errorf("findAllSessions() = %v, want empty map", got)
	}
}

func TestFindRecentSessions_MissingProjectsDirReturnsEmpty(t *testing.T) {
	withTempClaudeDir(t, t.TempDir())

	got, err := FindRecentSessions(50, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("FindRecentSessions() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("FindRecentSessions() = %v, want empty slice", got)
	}
}

func TestFindAllSessions_UnrelatedReadError(t *testing.T) {
	// A projects "directory" that is actually a file should still surface
	// as a real error (only ENOENT degrades to empty).
	base := t.TempDir()
	fakeProjectsAsFile := filepath.Join(base, "projects")
	if err := os.WriteFile(fakeProjectsAsFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	withTempClaudeDir(t, base)

	_, err := findAllSessions()
	if err == nil {
		t.Fatal("findAllSessions() error = nil, want error for non-ENOENT failure")
	}
}

func TestReadStartTime_LongFirstLineDoesNotEndScan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	long := `{"type":"x","pad":"` + strings.Repeat("a", 2*1024*1024) + `"}`
	content := long + "\n" + `{"timestamp":"2026-03-04T05:06:07Z"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ReadStartTime(path)
	want := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("ReadStartTime = %v, want %v", got, want)
	}
}

func TestReadStartTime_LongButUnderCapFirstLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	// > 64KB (old scanner cap) but under the new cap, with the timestamp on it.
	line := `{"pad":"` + strings.Repeat("a", 200*1024) + `","timestamp":"2026-03-04T05:06:07Z"}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadStartTime(path); got.IsZero() {
		t.Fatal("ReadStartTime returned zero for 200KB first line")
	}
}

func TestReadFirstUserMessage_TruncatesOnRuneBoundary(t *testing.T) {
	text := strings.Repeat("é", 150) // 2 bytes each = 300 bytes
	entry := map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	}
	b, _ := json.Marshal(entry)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ReadFirstUserMessage(path)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated message is invalid UTF-8: %q", got)
	}
	if len(got) > 200 || len(got) < 198 {
		t.Fatalf("len = %d, want 198..200", len(got))
	}
}

func TestReadFirstUserMessage_ThreeByteRunes(t *testing.T) {
	text := strings.Repeat("日本語", 100) // 3 bytes per rune; 200 is not a multiple of 3 boundary start
	entry := map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	}
	b, _ := json.Marshal(entry)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ReadFirstUserMessage(path)
	if !utf8.ValidString(got) || len(got) > 200 || len(got) < 198 {
		t.Fatalf("got len %d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestParseSummary_PreviewsRuneSafe(t *testing.T) {
	long := strings.Repeat("日本語", 100)
	line, _ := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": long}},
		},
	})
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	sum, err := ParseSummary(Session{LogFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.TextBlocks) == 0 {
		t.Fatal("no text blocks parsed")
	}
	for _, p := range sum.TextBlocks {
		if !utf8.ValidString(p) {
			t.Fatalf("invalid UTF-8 preview: %q", p)
		}
	}
}

func TestTruncateBytes(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 3, "hel"},
		{"héllo", 2, "h"},  // cut would split é
		{"héllo", 3, "hé"}, // exact boundary
		{"日本語", 4, "日"},
		{"", 5, ""},
	}
	for _, tt := range tests {
		if got := truncateBytes(tt.in, tt.max); got != tt.want {
			t.Errorf("truncateBytes(%q,%d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
}
