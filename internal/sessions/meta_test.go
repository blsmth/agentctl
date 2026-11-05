package sessions

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helpers

func setupMetaDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	SetSessionDataDirForTest(dir)
	t.Cleanup(func() { SetSessionDataDirForTest("") })
	return dir
}

func makeMeta(t *testing.T, m SessionMeta) {
	t.Helper()
	if err := WriteMeta(m); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
}

// TestWriteAndReadMeta verifies that WriteMeta + ReadMeta round-trips all fields.
func TestWriteAndReadMeta(t *testing.T) {
	setupMetaDir(t)

	want := SessionMeta{
		SchemaVersion:     SchemaVersion,
		SessionID:         "abc123",
		Agent:             "go-engineer",
		Project:           "/Users/test/project",
		ParentProject:     "/Users/test/project",
		StartTime:         time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC),
		EndTime:           time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC),
		Turns:             5,
		InputTokens:       10000,
		OutputTokens:      2000,
		CacheReadTokens:   3000,
		CacheCreateTokens: 1000,
		ModelName:         "claude-sonnet-4-20250514",
		FirstUserMessage:  "Fix the build error",
		FilesEdited:       []string{"/Users/test/main.go", "/Users/test/util.go"},
		SourcePath:        "/path/to/session.jsonl",
	}

	makeMeta(t, want)

	metaPath := filepath.Join(sessionDataDir(), encodeDirName(want.Project), want.SessionID+".meta.json")
	got, err := ReadMeta(metaPath)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}

	if got.SchemaVersion != want.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, want.SchemaVersion)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, want.SessionID)
	}
	if got.Agent != want.Agent {
		t.Errorf("Agent = %q, want %q", got.Agent, want.Agent)
	}
	if got.Project != want.Project {
		t.Errorf("Project = %q, want %q", got.Project, want.Project)
	}
	if got.ParentProject != want.ParentProject {
		t.Errorf("ParentProject = %q, want %q", got.ParentProject, want.ParentProject)
	}
	if !got.StartTime.Equal(want.StartTime) {
		t.Errorf("StartTime = %v, want %v", got.StartTime, want.StartTime)
	}
	if !got.EndTime.Equal(want.EndTime) {
		t.Errorf("EndTime = %v, want %v", got.EndTime, want.EndTime)
	}
	if got.Turns != want.Turns {
		t.Errorf("Turns = %d, want %d", got.Turns, want.Turns)
	}
	if got.InputTokens != want.InputTokens {
		t.Errorf("InputTokens = %d, want %d", got.InputTokens, want.InputTokens)
	}
	if got.OutputTokens != want.OutputTokens {
		t.Errorf("OutputTokens = %d, want %d", got.OutputTokens, want.OutputTokens)
	}
	if got.CacheReadTokens != want.CacheReadTokens {
		t.Errorf("CacheReadTokens = %d, want %d", got.CacheReadTokens, want.CacheReadTokens)
	}
	if got.CacheCreateTokens != want.CacheCreateTokens {
		t.Errorf("CacheCreateTokens = %d, want %d", got.CacheCreateTokens, want.CacheCreateTokens)
	}
	if got.ModelName != want.ModelName {
		t.Errorf("ModelName = %q, want %q", got.ModelName, want.ModelName)
	}
	if got.FirstUserMessage != want.FirstUserMessage {
		t.Errorf("FirstUserMessage = %q, want %q", got.FirstUserMessage, want.FirstUserMessage)
	}
	if len(got.FilesEdited) != len(want.FilesEdited) {
		t.Errorf("FilesEdited len = %d, want %d", len(got.FilesEdited), len(want.FilesEdited))
	}
	if got.SourcePath != want.SourcePath {
		t.Errorf("SourcePath = %q, want %q", got.SourcePath, want.SourcePath)
	}
}

// TestWriteMeta_AtomicWrite verifies the temp file does not persist after success.
func TestWriteMeta_AtomicWrite(t *testing.T) {
	dir := setupMetaDir(t)

	meta := SessionMeta{
		SchemaVersion: SchemaVersion,
		SessionID:     "sess1",
		Agent:         "agent",
		Project:       "/test/proj",
		ParentProject: "/test/proj",
		StartTime:     time.Now(),
	}
	if err := WriteMeta(meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	// Walk all files and ensure no .tmp files remain.
	err := filepath.Walk(dir, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasSuffix(path, ".tmp") {
			t.Errorf("temp file persisted: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
}

// makeTestJSONL creates a temp JSONL file with known content and returns its path.
func makeTestJSONL(t *testing.T, agentName string) string {
	t.Helper()
	lines := []string{
		`{"type":"system","timestamp":"2026-03-25T10:00:00Z","agentSetting":"` + agentName + `"}`,
		`{"type":"user","timestamp":"2026-03-25T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"Fix the build error in main.go"}]}}`,
		`{"type":"assistant","timestamp":"2026-03-25T10:00:05Z","message":{"role":"assistant","model":"claude-sonnet-4-20250514","content":[{"type":"text","text":"I'll fix it."},{"type":"tool_use","name":"Read","input":{"file_path":"/Users/test/main.go"}}],"usage":{"input_tokens":1500,"output_tokens":200,"cache_creation_input_tokens":1000,"cache_read_input_tokens":500}}}`,
		`{"type":"assistant","timestamp":"2026-03-25T10:00:10Z","message":{"role":"assistant","model":"claude-sonnet-4-20250514","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/Users/test/main.go","old_string":"broken","new_string":"fixed"}}],"usage":{"input_tokens":2000,"output_tokens":300,"cache_creation_input_tokens":0,"cache_read_input_tokens":1500}}}`,
		`{"type":"assistant","timestamp":"2026-03-25T10:00:15Z","message":{"role":"assistant","model":"claude-sonnet-4-20250514","content":[{"type":"text","text":"Fixed."}],"usage":{"input_tokens":2500,"output_tokens":150,"cache_creation_input_tokens":0,"cache_read_input_tokens":2000}}}`,
	}
	f, err := os.CreateTemp(t.TempDir(), "test-*.jsonl")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()
	for _, line := range lines {
		f.WriteString(line + "\n") //nolint:errcheck
	}
	return f.Name()
}

// TestExtractMeta verifies that ExtractMeta correctly maps ParseSummary output.
func TestExtractMeta(t *testing.T) {
	logFile := makeTestJSONL(t, "go-engineer")

	s := Session{
		ID:        "testsession",
		Agent:     "go-engineer",
		Project:   "/Users/test/project",
		StartTime: time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 3, 25, 10, 0, 15, 0, time.UTC),
		LogFile:   logFile,
	}

	meta, err := ExtractMeta(s)
	if err != nil {
		t.Fatalf("ExtractMeta: %v", err)
	}

	if meta.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", meta.SchemaVersion, SchemaVersion)
	}
	if meta.Agent != "go-engineer" {
		t.Errorf("Agent = %q, want %q", meta.Agent, "go-engineer")
	}
	if meta.Turns != 3 {
		t.Errorf("Turns = %d, want 3", meta.Turns)
	}
	if meta.FirstUserMessage != "Fix the build error in main.go" {
		t.Errorf("FirstUserMessage = %q, want %q", meta.FirstUserMessage, "Fix the build error in main.go")
	}
	if len(meta.FilesEdited) == 0 {
		t.Error("FilesEdited should not be empty")
	}
	if meta.SourcePath != logFile {
		t.Errorf("SourcePath = %q, want %q", meta.SourcePath, logFile)
	}
	if !meta.StartTime.Equal(s.StartTime) {
		t.Errorf("StartTime = %v, want %v", meta.StartTime, s.StartTime)
	}
	if meta.ModelName != "claude-sonnet-4-20250514" {
		t.Errorf("ModelName = %q, want %q", meta.ModelName, "claude-sonnet-4-20250514")
	}
	if meta.InputTokens == 0 {
		t.Error("InputTokens should be non-zero")
	}
	if meta.OutputTokens == 0 {
		t.Error("OutputTokens should be non-zero")
	}
}

// TestExtractMeta_WorktreeDetection verifies that a session with /.worktrees/ in
// the project path gets the correct ParentProject.
func TestExtractMeta_WorktreeDetection(t *testing.T) {
	logFile := makeTestJSONL(t, "go-engineer")

	s := Session{
		ID:      "worktreesess",
		Agent:   "go-engineer",
		Project: "/Users/test/myrepo/.worktrees/agent-go-engineer-issue-42",
		LogFile: logFile,
	}

	meta, err := ExtractMeta(s)
	if err != nil {
		t.Fatalf("ExtractMeta: %v", err)
	}

	want := "/Users/test/myrepo"
	if meta.ParentProject != want {
		t.Errorf("ParentProject = %q, want %q", meta.ParentProject, want)
	}
	if meta.Project != s.Project {
		t.Errorf("Project = %q, want %q", meta.Project, s.Project)
	}
}

// TestExtractMeta_NoWorktree verifies that a normal project path gets
// ParentProject == Project.
func TestExtractMeta_NoWorktree(t *testing.T) {
	logFile := makeTestJSONL(t, "go-engineer")

	s := Session{
		ID:      "normalsess",
		Agent:   "go-engineer",
		Project: "/Users/test/myrepo",
		LogFile: logFile,
	}

	meta, err := ExtractMeta(s)
	if err != nil {
		t.Fatalf("ExtractMeta: %v", err)
	}

	if meta.ParentProject != meta.Project {
		t.Errorf("ParentProject %q should equal Project %q for non-worktree session",
			meta.ParentProject, meta.Project)
	}
}

// writeMetas is a helper that writes a slice of SessionMeta values.
func writeMetas(t *testing.T, metas []SessionMeta) {
	t.Helper()
	for _, m := range metas {
		if err := WriteMeta(m); err != nil {
			t.Fatalf("WriteMeta(%s): %v", m.SessionID, err)
		}
	}
}

// TestFindMirroredSessions_DateFilter verifies filtering by calendar date.
func TestFindMirroredSessions_DateFilter(t *testing.T) {
	setupMetaDir(t)

	day1 := time.Date(2026, 3, 24, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 3, 26, 10, 0, 0, 0, time.UTC)

	writeMetas(t, []SessionMeta{
		{SchemaVersion: 1, SessionID: "s1", Agent: "a", Project: "/p", ParentProject: "/p", StartTime: day1},
		{SchemaVersion: 1, SessionID: "s2", Agent: "a", Project: "/p", ParentProject: "/p", StartTime: day2},
		{SchemaVersion: 1, SessionID: "s3", Agent: "a", Project: "/p", ParentProject: "/p", StartTime: day3},
	})

	results, err := FindMirroredSessions(QueryOpts{Date: day2})
	if err != nil {
		t.Fatalf("FindMirroredSessions: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].SessionID != "s2" {
		t.Errorf("SessionID = %q, want %q", results[0].SessionID, "s2")
	}
}

// TestFindMirroredSessions_ProjectFilter verifies filtering by project directory.
func TestFindMirroredSessions_ProjectFilter(t *testing.T) {
	setupMetaDir(t)

	writeMetas(t, []SessionMeta{
		{SchemaVersion: 1, SessionID: "s1", Agent: "a", Project: "/proj/alpha", ParentProject: "/proj/alpha", StartTime: time.Now()},
		{SchemaVersion: 1, SessionID: "s2", Agent: "a", Project: "/proj/beta", ParentProject: "/proj/beta", StartTime: time.Now()},
		{SchemaVersion: 1, SessionID: "s3", Agent: "a", Project: "/proj/alpha", ParentProject: "/proj/alpha", StartTime: time.Now()},
	})

	results, err := FindMirroredSessions(QueryOpts{ProjectDir: "/proj/alpha"})
	if err != nil {
		t.Fatalf("FindMirroredSessions: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.Project != "/proj/alpha" {
			t.Errorf("unexpected project %q", r.Project)
		}
	}
}

// TestFindMirroredSessions_WorktreeConsolidation verifies that worktree sessions
// whose ParentProject matches the queried project are included.
func TestFindMirroredSessions_WorktreeConsolidation(t *testing.T) {
	setupMetaDir(t)

	parentProj := "/Users/test/myrepo"
	writeMetas(t, []SessionMeta{
		// Direct session in the parent project.
		{SchemaVersion: 1, SessionID: "s1", Agent: "a", Project: parentProj, ParentProject: parentProj, StartTime: time.Now()},
		// Worktree session whose parent is parentProj.
		{SchemaVersion: 1, SessionID: "s2", Agent: "a", Project: parentProj + "/.worktrees/branch", ParentProject: parentProj, StartTime: time.Now()},
		// Unrelated session.
		{SchemaVersion: 1, SessionID: "s3", Agent: "a", Project: "/other/project", ParentProject: "/other/project", StartTime: time.Now()},
	})

	results, err := FindMirroredSessions(QueryOpts{ProjectDir: parentProj})
	if err != nil {
		t.Fatalf("FindMirroredSessions: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2; sessions: %v", len(results), sessionIDs(results))
	}
	ids := sessionIDs(results)
	if !ids["s1"] || !ids["s2"] {
		t.Errorf("expected s1 and s2, got %v", ids)
	}
}

// TestFindMirroredSessions_AgentFilter verifies filtering by agent name.
func TestFindMirroredSessions_AgentFilter(t *testing.T) {
	setupMetaDir(t)

	writeMetas(t, []SessionMeta{
		{SchemaVersion: 1, SessionID: "s1", Agent: "go-engineer", Project: "/p", ParentProject: "/p", StartTime: time.Now()},
		{SchemaVersion: 1, SessionID: "s2", Agent: "python-engineer", Project: "/p", ParentProject: "/p", StartTime: time.Now()},
		{SchemaVersion: 1, SessionID: "s3", Agent: "go-engineer", Project: "/p", ParentProject: "/p", StartTime: time.Now()},
	})

	results, err := FindMirroredSessions(QueryOpts{Agent: "go-engineer"})
	if err != nil {
		t.Fatalf("FindMirroredSessions: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.Agent != "go-engineer" {
			t.Errorf("unexpected agent %q", r.Agent)
		}
	}
}

func sessionIDs(metas []SessionMeta) map[string]bool {
	m := make(map[string]bool)
	for _, s := range metas {
		m[s.SessionID] = true
	}
	return m
}

// TestEstimateCost covers the pricing table and edge cases.
func TestEstimateCost(t *testing.T) {
	tests := []struct {
		name string
		meta SessionMeta
		want float64 // USD
	}{
		{
			name: "zero tokens",
			meta: SessionMeta{},
			want: 0,
		},
		{
			name: "sonnet pricing — raw input only",
			meta: SessionMeta{
				ModelName:    "claude-sonnet-4-20250514",
				InputTokens:  1_000_000,
				OutputTokens: 0,
			},
			want: 3.0,
		},
		{
			name: "sonnet pricing — output only",
			meta: SessionMeta{
				ModelName:    "claude-sonnet-4-20250514",
				OutputTokens: 1_000_000,
			},
			want: 15.0,
		},
		{
			name: "haiku pricing — raw input",
			meta: SessionMeta{
				ModelName:    "claude-haiku-3-20240307",
				InputTokens:  1_000_000,
				OutputTokens: 0,
			},
			want: 0.25,
		},
		{
			name: "haiku pricing — output",
			meta: SessionMeta{
				ModelName:    "claude-haiku-3-20240307",
				OutputTokens: 1_000_000,
			},
			want: 1.25,
		},
		{
			name: "opus pricing — input",
			meta: SessionMeta{
				ModelName:    "claude-opus-4-20250514",
				InputTokens:  1_000_000,
				OutputTokens: 0,
			},
			want: 15.0, // Opus: $15/1M input
		},
		{
			name: "opus pricing — output",
			meta: SessionMeta{
				ModelName:    "claude-opus-4-5",
				InputTokens:  0,
				OutputTokens: 1_000_000,
			},
			want: 75.0, // Opus: $75/1M output
		},
		{
			name: "unknown model defaults to sonnet",
			meta: SessionMeta{
				ModelName:    "claude-future-model-99",
				InputTokens:  1_000_000,
				OutputTokens: 0,
			},
			want: 3.0, // defaultPricing == sonnet
		},
		{
			name: "empty model defaults to sonnet",
			meta: SessionMeta{
				InputTokens:  1_000_000,
				OutputTokens: 0,
			},
			want: 3.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateCost(tt.meta)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("EstimateCost() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEstimateCost_CacheBreakdown verifies that cache read, cache write, and raw
// input tokens are billed at distinct rates.
func TestEstimateCost_CacheBreakdown(t *testing.T) {
	// Sonnet: input $3, cacheWrite $3.75, cacheRead $0.30, output $15 (all per 1M).
	// InputTokens is raw (uncached) input only; cache tokens tracked separately.
	meta := SessionMeta{
		ModelName:         "claude-sonnet-4-20250514",
		InputTokens:       600_000, // raw input only
		CacheCreateTokens: 300_000,
		CacheReadTokens:   200_000,
		OutputTokens:      500_000,
	}
	// cost = (600k * 3.0 + 500k * 15.0 + 300k * 3.75 + 200k * 0.30) / 1M
	//      = (1.80 + 7.50 + 1.125 + 0.06)
	//      = 10.485
	const want = 10.485

	got := EstimateCost(meta)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("EstimateCost() = %.6f, want %.6f", got, want)
	}
}

// TestPurge verifies that stale metas are removed and recent ones are kept,
// and that empty project directories are cleaned up.
func TestPurge(t *testing.T) {
	dir := setupMetaDir(t)

	now := time.Now()
	project := "/purge/test"

	// Write three metas: two old, one recent.
	oldMeta1 := SessionMeta{SchemaVersion: 1, SessionID: "old1", Agent: "a", Project: project, ParentProject: project, StartTime: now.Add(-40 * 24 * time.Hour)}
	oldMeta2 := SessionMeta{SchemaVersion: 1, SessionID: "old2", Agent: "a", Project: project, ParentProject: project, StartTime: now.Add(-35 * 24 * time.Hour)}
	recentMeta := SessionMeta{SchemaVersion: 1, SessionID: "recent", Agent: "a", Project: project, ParentProject: project, StartTime: now.Add(-10 * 24 * time.Hour)}

	for _, m := range []SessionMeta{oldMeta1, oldMeta2, recentMeta} {
		makeMeta(t, m)
	}

	// Backdate the mtime of old files so the safety window check doesn't block them.
	projDir := filepath.Join(dir, encodeDirName(project))
	for _, id := range []string{"old1", "old2"} {
		p := filepath.Join(projDir, id+".meta.json")
		twoHoursAgo := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(p, twoHoursAgo, twoHoursAgo); err != nil {
			t.Fatalf("Chtimes %s: %v", p, err)
		}
	}

	removed, err := Purge(30 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}

	// recent should still exist.
	recentPath := filepath.Join(projDir, "recent.meta.json")
	if _, err := os.Stat(recentPath); err != nil {
		t.Errorf("recent meta should still exist: %v", err)
	}

	// old files should be gone.
	for _, id := range []string{"old1", "old2"} {
		p := filepath.Join(projDir, id+".meta.json")
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("old meta %s should have been purged", id)
		}
	}
}

// TestPurge_SkipsRecentlyModified verifies that a file with an old StartTime but a
// recent mtime is NOT purged (safety guard for concurrent writers).
func TestPurge_SkipsRecentlyModified(t *testing.T) {
	dir := setupMetaDir(t)
	project := "/purge/recent-mtime"

	// Write a meta whose StartTime is very old but mtime is NOW (just written).
	m := SessionMeta{
		SchemaVersion: 1,
		SessionID:     "safefile",
		Agent:         "a",
		Project:       project,
		ParentProject: project,
		StartTime:     time.Now().Add(-365 * 24 * time.Hour), // 1 year old
	}
	makeMeta(t, m)

	// Do NOT backdate the mtime — the file was just written so mtime is very recent.
	removed, err := Purge(30 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 (safety window should protect the file)", removed)
	}

	// The file should still be there.
	projDir := filepath.Join(dir, encodeDirName(project))
	p := filepath.Join(projDir, "safefile.meta.json")
	if _, err := os.Stat(p); err != nil {
		t.Errorf("file should still exist after purge: %v", err)
	}
}

// TestDiskUsage verifies that DiskUsage returns a non-zero byte count and correct
// file count after writing some metas.
func TestDiskUsage(t *testing.T) {
	setupMetaDir(t)

	project := "/disk/usage/test"
	metas := []SessionMeta{
		{SchemaVersion: 1, SessionID: "du1", Agent: "a", Project: project, ParentProject: project, StartTime: time.Now()},
		{SchemaVersion: 1, SessionID: "du2", Agent: "a", Project: project, ParentProject: project, StartTime: time.Now()},
	}
	writeMetas(t, metas)

	totalBytes, fileCount, err := DiskUsage()
	if err != nil {
		t.Fatalf("DiskUsage: %v", err)
	}
	if fileCount != 2 {
		t.Errorf("fileCount = %d, want 2", fileCount)
	}
	if totalBytes <= 0 {
		t.Errorf("totalBytes = %d, want > 0", totalBytes)
	}
}
