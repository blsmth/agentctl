package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupSyncDirs configures temp directories for both Claude projects (source)
// and agentctl session metadata (destination), wiring up the test overrides.
func setupSyncDirs(t *testing.T) (claudeRoot string, metaRoot string) {
	t.Helper()
	claudeRoot = t.TempDir()
	metaRoot = t.TempDir()
	SetClaudeDirForTest(claudeRoot)
	SetSessionDataDirForTest(metaRoot)
	t.Cleanup(func() {
		SetClaudeDirForTest(claudeBaseDir())
		SetSessionDataDirForTest("")
	})
	return claudeRoot, metaRoot
}

// writeTestJSONL writes a minimal valid JSONL session file to disk and returns
// the path. agentName is embedded in the first line's agentSetting field.
func writeTestJSONL(t *testing.T, dir, sessionID, agentName string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := strings.Join([]string{
		`{"type":"system","timestamp":"2026-03-25T10:00:00Z","agentSetting":"` + agentName + `"}`,
		`{"type":"user","timestamp":"2026-03-25T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","timestamp":"2026-03-25T10:00:05Z","message":{"role":"assistant","model":"claude-sonnet-4","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":10}}}`,
	}, "\n") + "\n"

	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
	return path
}

// TestSync verifies that Sync creates .meta.json files for each JSONL session.
func TestSync(t *testing.T) {
	claudeRoot, metaRoot := setupSyncDirs(t)

	// Create a fake Claude project with one session.
	projEncoded := "Users-test-myproject"
	projDir := filepath.Join(claudeRoot, "projects", projEncoded)
	writeTestJSONL(t, projDir, "session-abc", "go-engineer")

	added, updated, _, err := Sync(30)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1", added)
	}
	if updated != 0 {
		t.Errorf("updated = %d, want 0", updated)
	}

	// Verify the .meta.json was created in the expected location.
	metaPath := filepath.Join(metaRoot, projEncoded, "session-abc.meta.json")
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("meta.json not created at %s: %v", metaPath, err)
	}

	// Verify the meta has correct content.
	meta, err := ReadMeta(metaPath)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if meta.SessionID != "session-abc" {
		t.Errorf("SessionID = %q, want %q", meta.SessionID, "session-abc")
	}
	if meta.Agent != "go-engineer" {
		t.Errorf("Agent = %q, want %q", meta.Agent, "go-engineer")
	}
	if meta.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", meta.SchemaVersion, SchemaVersion)
	}
	if meta.FirstUserMessage != "hello" {
		t.Errorf("FirstUserMessage = %q, want %q", meta.FirstUserMessage, "hello")
	}
}

// TestSync_SkipsExisting verifies that a second sync with unchanged JSONL files
// does not produce any additional added or updated counts.
func TestSync_SkipsExisting(t *testing.T) {
	claudeRoot, _ := setupSyncDirs(t)

	projDir := filepath.Join(claudeRoot, "projects", "Users-test-proj2")
	writeTestJSONL(t, projDir, "sess1", "go-engineer")

	// First sync — creates the meta.
	added1, _, _, err := Sync(30)
	if err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if added1 != 1 {
		t.Fatalf("first Sync: added = %d, want 1", added1)
	}

	// Second sync — JSONL unchanged, so nothing should be added or updated.
	added2, updated2, _, err := Sync(30)
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if added2 != 0 {
		t.Errorf("second Sync: added = %d, want 0", added2)
	}
	if updated2 != 0 {
		t.Errorf("second Sync: updated = %d, want 0", updated2)
	}
}

// TestSync_UpdatesChanged verifies that when a JSONL file is modified after the
// initial sync, a subsequent sync updates the meta.
func TestSync_UpdatesChanged(t *testing.T) {
	claudeRoot, metaRoot := setupSyncDirs(t)

	projEncoded := "Users-test-proj3"
	projDir := filepath.Join(claudeRoot, "projects", projEncoded)
	jsonlPath := writeTestJSONL(t, projDir, "sess2", "go-engineer")

	// First sync.
	added, _, _, err := Sync(30)
	if err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if added != 1 {
		t.Fatalf("first Sync: added = %d, want 1", added)
	}

	// Backdate the meta so the JSONL looks newer.
	metaPath := filepath.Join(metaRoot, projEncoded, "sess2.meta.json")
	oldTime := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(metaPath, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes meta: %v", err)
	}

	// Touch the JSONL (update its mtime to now).
	now := time.Now()
	if err := os.Chtimes(jsonlPath, now, now); err != nil {
		t.Fatalf("Chtimes jsonl: %v", err)
	}

	// Second sync — JSONL is newer than meta, should update.
	_, updated, _, err := Sync(30)
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if updated != 1 {
		t.Errorf("second Sync: updated = %d, want 1", updated)
	}
}

// TestSync_PrunesOld verifies that metas older than the retention window are
// removed after a sync.
func TestSync_PrunesOld(t *testing.T) {
	claudeRoot, metaRoot := setupSyncDirs(t)

	// Write an old meta directly (bypass Sync's source check).
	projEncoded := "Users-test-pruneproj"
	projDir := filepath.Join(metaRoot, projEncoded)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	oldMeta := SessionMeta{
		SchemaVersion: 1,
		SessionID:     "oldsess",
		Agent:         "a",
		Project:       decodeDirName(projEncoded),
		ParentProject: decodeDirName(projEncoded),
		StartTime:     time.Now().Add(-40 * 24 * time.Hour), // 40 days old
	}
	if err := WriteMeta(oldMeta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	// Backdate the mtime so the safety window doesn't block the purge.
	oldMetaPath := filepath.Join(projDir, "oldsess.meta.json")
	backdated := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldMetaPath, backdated, backdated); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// Sync with no Claude JSONL files (empty projects dir). Purge should still run.
	if err := os.MkdirAll(filepath.Join(claudeRoot, "projects"), 0o755); err != nil {
		t.Fatalf("MkdirAll projects: %v", err)
	}

	_, _, pruned, err := Sync(30) // 30-day retention, meta is 40 days old
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1", pruned)
	}

	// Verify the old meta is gone.
	if _, err := os.Stat(oldMetaPath); !os.IsNotExist(err) {
		t.Error("old meta should have been pruned")
	}
}
