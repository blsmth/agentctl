package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/claude"
)

// withSearchClaudeDir points the package's claudeDir at a fresh temp
// directory for the duration of the test and restores it afterward.
func withSearchClaudeDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	orig := claudeDir
	SetClaudeDirForTest(root)
	t.Cleanup(func() { SetClaudeDirForTest(orig) })
	return root
}

// writeSearchSession writes a session JSONL fixture under claudeRoot's
// projects directory for projectAbsPath, prefixed with an agentSetting line
// when agentName is non-empty (required for FindRecentSessions/agent
// discovery to pick the session up).
func writeSearchSession(t *testing.T, claudeRoot, projectAbsPath, sessionID, agentName string, lines []string) string {
	t.Helper()
	dirName := claude.ProjectSlug(projectAbsPath)
	dir := filepath.Join(claudeRoot, "projects", dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	var sb strings.Builder
	if agentName != "" {
		fmt.Fprintf(&sb, `{"agentSetting":%q}`, agentName)
		sb.WriteString("\n")
	}
	for _, l := range lines {
		sb.WriteString(l)
		sb.WriteString("\n")
	}

	fp := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(fp, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", fp, err)
	}
	return fp
}

// ts formats a timestamp offset from now, matching the RFC3339 shape Claude
// Code writes.
func ts(offset time.Duration) string {
	return time.Now().Add(offset).UTC().Format(time.RFC3339)
}

func TestSearch_StringAndArrayFormUserContent(t *testing.T) {
	root := withSearchClaudeDir(t)
	proj := "/tmp/agentctl-search-string-array"

	lines := []string{
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"string-form-marker-alpha please fix this"}}`, ts(-time.Minute)),
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":[{"type":"text","text":"array-form-marker-beta please fix that"}]}}`, ts(-time.Minute)),
	}
	writeSearchSession(t, root, proj, "sess-string-array", "test-agent", lines)

	hits, err := Search(SearchOptions{Query: "string-form-marker-alpha", Project: proj})
	if err != nil {
		t.Fatalf("Search (string form): %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("string-form hits = %d, want 1", len(hits))
	}
	if hits[0].Role != "user" {
		t.Errorf("string-form hit role = %q, want user", hits[0].Role)
	}

	hits, err = Search(SearchOptions{Query: "array-form-marker-beta", Project: proj})
	if err != nil {
		t.Fatalf("Search (array form): %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("array-form hits = %d, want 1", len(hits))
	}
}

func TestSearch_ToolResultExcludedByDefaultIncludedWithFlag(t *testing.T) {
	root := withSearchClaudeDir(t)
	proj := "/tmp/agentctl-search-tool-result"

	lines := []string{
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"toolresult-marker-gamma file contents here"}]}]}}`, ts(-time.Minute)),
	}
	writeSearchSession(t, root, proj, "sess-tool-result", "test-agent", lines)

	hits, err := Search(SearchOptions{Query: "toolresult-marker-gamma", Project: proj})
	if err != nil {
		t.Fatalf("Search default: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("default hits = %d, want 0 (tool_result excluded by default)", len(hits))
	}

	hits, err = Search(SearchOptions{Query: "toolresult-marker-gamma", Project: proj, IncludeOutput: true})
	if err != nil {
		t.Fatalf("Search include-output: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("include-output hits = %d, want 1", len(hits))
	}
	if hits[0].Role != "user" {
		t.Errorf("tool_result hit role = %q, want user", hits[0].Role)
	}
}

func TestSearch_ToolUseRequiresToolFlag(t *testing.T) {
	root := withSearchClaudeDir(t)
	proj := "/tmp/agentctl-search-tool-use"

	lines := []string{
		fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo tooluse-marker-delta"}}]}}`, ts(-time.Minute)),
	}
	writeSearchSession(t, root, proj, "sess-tool-use", "test-agent", lines)

	hits, err := Search(SearchOptions{Query: "tooluse-marker-delta", Project: proj})
	if err != nil {
		t.Fatalf("Search without --tool: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("hits without --tool = %d, want 0", len(hits))
	}

	hits, err = Search(SearchOptions{Query: "tooluse-marker-delta", Project: proj, Tool: "Bash"})
	if err != nil {
		t.Fatalf("Search with --tool Bash: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits with --tool Bash = %d, want 1", len(hits))
	}

	// A different tool name should not match.
	hits, err = Search(SearchOptions{Query: "tooluse-marker-delta", Project: proj, Tool: "Read"})
	if err != nil {
		t.Fatalf("Search with --tool Read: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("hits with --tool Read = %d, want 0 (wrong tool)", len(hits))
	}
}

// TestSearch_OversizedLineDoesNotStopTheScan is the regression test that
// matters most: a bufio.Scanner-based implementation caps its token size
// (the rest of the codebase uses a 10MB cap) and returns bufio.ErrTooLong on
// a bigger line, after which Scan can never be resumed -- silently dropping
// everything after that line, including matches that are otherwise
// perfectly findable. extractRecords must use a bufio.Reader so it can skip
// past an oversized line and keep scanning.
func TestSearch_OversizedLineDoesNotStopTheScan(t *testing.T) {
	root := withSearchClaudeDir(t)
	proj := "/tmp/agentctl-search-oversized-line"

	// Bigger than the 10MB bufio.Scanner cap used elsewhere in this package.
	giant := strings.Repeat("A", 11*1024*1024)

	lines := []string{
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"before-giant-marker"}}`, ts(-3*time.Minute)),
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"%sgiant-line-marker-epsilon"}]}]}}`, ts(-2*time.Minute), giant),
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"after-giant-marker"}}`, ts(-time.Minute)),
	}
	writeSearchSession(t, root, proj, "sess-oversized", "test-agent", lines)

	// The line after the giant one must still be reachable.
	hits, err := Search(SearchOptions{Query: "after-giant-marker", Project: proj})
	if err != nil {
		t.Fatalf("Search after giant line: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits for text after the giant line = %d, want 1 (scan must not stop)", len(hits))
	}

	// The giant tool_result line itself must be searchable when requested.
	hits, err = Search(SearchOptions{Query: "giant-line-marker-epsilon", Project: proj, IncludeOutput: true})
	if err != nil {
		t.Fatalf("Search giant line content: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits inside the giant line = %d, want 1", len(hits))
	}
	if len(hits[0].Text) > 100_000 {
		t.Errorf("hit Text len = %d, want a bounded snippet, not the whole 11MB blob", len(hits[0].Text))
	}
}

func TestSearch_RoleFiltering(t *testing.T) {
	root := withSearchClaudeDir(t)
	proj := "/tmp/agentctl-search-role"

	lines := []string{
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"roletest-marker from the user"}}`, ts(-2*time.Minute)),
		fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"text","text":"roletest-marker from the assistant"}]}}`, ts(-time.Minute)),
	}
	writeSearchSession(t, root, proj, "sess-role", "test-agent", lines)

	hits, err := Search(SearchOptions{Query: "roletest-marker", Project: proj})
	if err != nil {
		t.Fatalf("Search any role: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("any-role hits = %d, want 2", len(hits))
	}

	hits, err = Search(SearchOptions{Query: "roletest-marker", Project: proj, Role: "user"})
	if err != nil {
		t.Fatalf("Search role=user: %v", err)
	}
	if len(hits) != 1 || hits[0].Role != "user" {
		t.Fatalf("role=user hits = %+v, want exactly one user hit", hits)
	}

	hits, err = Search(SearchOptions{Query: "roletest-marker", Project: proj, Role: "assistant"})
	if err != nil {
		t.Fatalf("Search role=assistant: %v", err)
	}
	if len(hits) != 1 || hits[0].Role != "assistant" {
		t.Fatalf("role=assistant hits = %+v, want exactly one assistant hit", hits)
	}
}

func TestSearch_RegexVsSubstringCaseInsensitivity(t *testing.T) {
	root := withSearchClaudeDir(t)
	proj := "/tmp/agentctl-search-regex"

	lines := []string{
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"CamelCaseWord123 more text"}}`, ts(-time.Minute)),
	}
	writeSearchSession(t, root, proj, "sess-regex", "test-agent", lines)

	tests := []struct {
		name  string
		query string
		regex bool
		want  int
	}{
		{"lowercase substring matches mixed case", "camelcase", false, 1},
		{"substring miss", "notpresent", false, 0},
		{"regex uppercase pattern", `Word\d+`, true, 1},
		{"regex lowercase pattern is case-insensitive", `word\d+`, true, 1},
		{"regex miss", `Word\d{5,}`, true, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits, err := Search(SearchOptions{Query: tt.query, Project: proj, Regex: tt.regex})
			if err != nil {
				t.Fatalf("Search(%q, regex=%v): %v", tt.query, tt.regex, err)
			}
			if len(hits) != tt.want {
				t.Errorf("Search(%q, regex=%v) hits = %d, want %d", tt.query, tt.regex, len(hits), tt.want)
			}
		})
	}
}

func TestSearch_SinceBoundary(t *testing.T) {
	root := withSearchClaudeDir(t)
	proj := "/tmp/agentctl-search-since"

	recentLines := []string{
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"sincetest-marker recent"}}`, ts(-2*time.Hour)),
	}
	oldLines := []string{
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"sincetest-marker old"}}`, ts(-48*time.Hour)),
	}
	writeSearchSession(t, root, proj, "sess-recent", "test-agent", recentLines)
	writeSearchSession(t, root, proj, "sess-old", "test-agent", oldLines)

	hits, err := Search(SearchOptions{Query: "sincetest-marker", Project: proj})
	if err != nil {
		t.Fatalf("Search unfiltered: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("unfiltered hits = %d, want 2", len(hits))
	}

	hits, err = Search(SearchOptions{Query: "sincetest-marker", Project: proj, Since: 24 * time.Hour})
	if err != nil {
		t.Fatalf("Search since=24h: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("since=24h hits = %d, want 1", len(hits))
	}
	if !strings.Contains(hits[0].Text, "recent") {
		t.Errorf("since=24h hit = %q, want the recent session's text", hits[0].Text)
	}
}

func TestSearch_ZeroResultsIsNotAnError(t *testing.T) {
	root := withSearchClaudeDir(t)
	proj := "/tmp/agentctl-search-zero"

	lines := []string{
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"totally unrelated content"}}`, ts(-time.Minute)),
	}
	writeSearchSession(t, root, proj, "sess-zero", "test-agent", lines)

	hits, err := Search(SearchOptions{Query: "no-such-phrase-anywhere", Project: proj})
	if err != nil {
		t.Fatalf("Search with zero results returned error: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("hits = %d, want 0", len(hits))
	}
}

func TestSearch_EmptyQueryErrors(t *testing.T) {
	withSearchClaudeDir(t)
	if _, err := Search(SearchOptions{Query: "  "}); err == nil {
		t.Error("Search with blank query, want error")
	}
}

func TestSearch_AgentFilter(t *testing.T) {
	root := withSearchClaudeDir(t)
	proj := "/tmp/agentctl-search-agent"

	goLines := []string{
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"agentfilter-marker from go-engineer"}}`, ts(-time.Minute)),
	}
	pyLines := []string{
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"agentfilter-marker from python-engineer"}}`, ts(-time.Minute)),
	}
	writeSearchSession(t, root, proj, "sess-go", "go-engineer", goLines)
	writeSearchSession(t, root, proj, "sess-py", "python-engineer", pyLines)

	hits, err := Search(SearchOptions{Query: "agentfilter-marker", Project: proj, Agent: "go-engineer"})
	if err != nil {
		t.Fatalf("Search with agent filter: %v", err)
	}
	if len(hits) != 1 || hits[0].Agent != "go-engineer" {
		t.Fatalf("agent-filtered hits = %+v, want exactly one go-engineer hit", hits)
	}
}

func TestParseSince(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"empty means no filter", "", 0, false},
		{"hours", "24h", 24 * time.Hour, false},
		{"minutes", "30m", 30 * time.Minute, false},
		{"days", "7d", 7 * 24 * time.Hour, false},
		{"days fractional", "1.5d", 36 * time.Hour, false},
		{"another day value", "30d", 30 * 24 * time.Hour, false},
		{"invalid", "banana", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSince(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseSince(%q) error = nil, want error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSince(%q) error = %v, want nil", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseSince(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFindSessionsForProject_UsesClaudeProjectSlug(t *testing.T) {
	tests := []struct {
		name    string
		project string
		slug    string
	}{
		{"plain", "/Users/alice/src/myproj", "-Users-alice-src-myproj"},
		{"dots and underscores", "/Users/alice/acme/data_pipelines.v2", "-Users-alice-acme-data-pipelines-v2"},
		{"worktree dir", "/Users/alice/src/myproj/.worktrees/agent-x-issue-1", "-Users-alice-src-myproj--worktrees-agent-x-issue-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := withSearchClaudeDir(t)
			dir := filepath.Join(root, "projects", tt.slug)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "sess-1.jsonl"), []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := FindSessionsForProject(tt.project, 0)
			if err != nil {
				t.Fatalf("FindSessionsForProject: %v", err)
			}
			if len(got) != 1 || got[0].ID != "sess-1" {
				t.Fatalf("got %+v, want one session sess-1", got)
			}
		})
	}
}
