package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/registry"
)

// ---------------------------------------------------------------------------
// Filter.Project
// ---------------------------------------------------------------------------

func TestQuery_FilterByProject(t *testing.T) {
	setupAuditTest(t)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	records := []Record{
		{Timestamp: base.Format(time.RFC3339Nano), Event: EventTool, Tool: "Bash", Session: "s1", Project: "agentctl", RepoRoot: "/home/user/agentctl", OK: true},
		{Timestamp: base.Add(time.Minute).Format(time.RFC3339Nano), Event: EventTool, Tool: "Edit", Session: "s2", Project: "other-repo", RepoRoot: "/home/user/other-repo", OK: true},
		{Timestamp: base.Add(2 * time.Minute).Format(time.RFC3339Nano), Event: EventTool, Tool: "Read", Session: "s3", OK: true}, // v1-style: no project
	}
	for _, r := range records {
		if err := Append(r); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	t.Run("matches by project basename", func(t *testing.T) {
		got, err := Query(Filter{Project: "agentctl"})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1", len(got))
		}
		if got[0].Session != "s1" || got[0].RepoRoot != "/home/user/agentctl" {
			t.Errorf("got %+v, want session s1 with RepoRoot set", got[0])
		}
	})

	t.Run("no match returns empty, not an error", func(t *testing.T) {
		got, err := Query(Filter{Project: "does-not-exist"})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("len(got) = %d, want 0", len(got))
		}
	})

	t.Run("unset filter returns every record including projectless v1-style ones", func(t *testing.T) {
		got, err := Query(Filter{})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("len(got) = %d, want 3", len(got))
		}
	})
}

// TestQuery_V1RecordsStillParse pins that a record written without a "project"
// key (either genuinely written before the "project" field existed, or a v2 record where resolution failed and
// omitempty dropped the field) still round-trips through Query -- it must not
// be mistaken for a malformed line the way a missing "v" field is.
func TestQuery_V1RecordsStillParse(t *testing.T) {
	setupAuditTest(t)

	auditDir := paths.AuditDir()
	path := filepath.Join(auditDir, "legacy.jsonl")
	// Schema v1 line: has "v" but predates "project"/"repo_root" entirely.
	line := `{"v":1,"ts":"2026-01-01T00:00:00Z","event":"tool","session":"legacy","tool":"Bash","ok":true}`
	if err := writeRawLine(t, path, line); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := Query(Filter{Session: "legacy"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Project != "" || got[0].RepoRoot != "" {
		t.Errorf("legacy record got Project=%q RepoRoot=%q, want both empty", got[0].Project, got[0].RepoRoot)
	}
}

// ---------------------------------------------------------------------------
// QueryDirs / AllProjectAuditDirs
// ---------------------------------------------------------------------------

func TestQueryDirs_MergesAndSortsAcrossProjects(t *testing.T) {
	root := t.TempDir()

	dirA := filepath.Join(root, "proja", ".agentctl", "audit")
	dirB := filepath.Join(root, "projb", ".agentctl", "audit")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	writeRecordToDir(t, dirA, Record{
		SchemaVersion: SchemaVersion, Timestamp: base.Format(time.RFC3339Nano),
		Event: EventTool, Session: "sa", Project: "proja", OK: true,
	})
	writeRecordToDir(t, dirB, Record{
		SchemaVersion: SchemaVersion, Timestamp: base.Add(time.Minute).Format(time.RFC3339Nano),
		Event: EventTool, Session: "sb", Project: "projb", OK: true,
	})

	got, err := QueryDirs([]string{dirA, dirB}, Filter{})
	if err != nil {
		t.Fatalf("QueryDirs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Project != "proja" || got[1].Project != "projb" {
		t.Errorf("got projects [%q, %q], want [proja, projb] sorted by timestamp", got[0].Project, got[1].Project)
	}
}

func TestQueryDirs_SkipsUnreadableDirWithoutFailing(t *testing.T) {
	root := t.TempDir()
	dirGood := filepath.Join(root, "good", ".agentctl", "audit")
	dirMissing := filepath.Join(root, "does-not-exist", ".agentctl", "audit")

	writeRecordToDir(t, dirGood, Record{
		SchemaVersion: SchemaVersion, Timestamp: time.Now().Format(time.RFC3339Nano),
		Event: EventTool, Session: "s", Project: "good", OK: true,
	})

	got, err := QueryDirs([]string{dirGood, dirMissing}, Filter{})
	if err != nil {
		t.Fatalf("QueryDirs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
}

func TestAllProjectAuditDirs(t *testing.T) {
	isolateRegistry(t)

	registerEntry(t, registry.Entry{
		SessionName: "agentctl-proja",
		ProjectDir:  "/home/user/proja",
		RuntimeDir:  "/home/user/proja/.agentctl",
	})
	registerEntry(t, registry.Entry{
		SessionName: "agentctl-projb",
		ProjectDir:  "/home/user/projb",
		RuntimeDir:  "/home/user/projb/.agentctl",
	})

	dirs, err := AllProjectAuditDirs()
	if err != nil {
		t.Fatalf("AllProjectAuditDirs: %v", err)
	}
	want := []string{
		"/home/user/proja/.agentctl/audit",
		"/home/user/projb/.agentctl/audit",
	}
	if len(dirs) != len(want) {
		t.Fatalf("dirs = %v, want %v", dirs, want)
	}
	for i, d := range dirs {
		if d != want[i] {
			t.Errorf("dirs[%d] = %q, want %q", i, d, want[i])
		}
	}
}

// TestAllProjectAuditDirs_SupersetOfDefaultQuery pins the invariant that
// --all-projects never returns less than the default query: with an EMPTY
// registry but a populated current-project audit dir, the current dir must
// still be enumerated and its records returned.
func TestAllProjectAuditDirs_SupersetOfDefaultQuery(t *testing.T) {
	setupAuditTest(t)
	isolateRegistry(t) // empty registry

	if err := Append(Record{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Event:     EventTool, Tool: "Bash", Session: "s1", OK: true,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	dirs, err := AllProjectAuditDirs()
	if err != nil {
		t.Fatalf("AllProjectAuditDirs: %v", err)
	}
	if len(dirs) != 1 || dirs[0] != paths.AuditDir() {
		t.Fatalf("dirs = %v, want [%s]", dirs, paths.AuditDir())
	}
	got, err := QueryDirs(dirs, Filter{})
	if err != nil {
		t.Fatalf("QueryDirs: %v", err)
	}
	def, err := Query(Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || len(got) < len(def) {
		t.Errorf("all-projects returned %d records, default returned %d", len(got), len(def))
	}
}

// TestAllProjectAuditDirs_CurrentProjectDedupedWhenRegistered verifies the
// current dir appears exactly once, and first, when it is also registered.
func TestAllProjectAuditDirs_CurrentProjectDedupedWhenRegistered(t *testing.T) {
	setupAuditTest(t)
	isolateRegistry(t)

	registerEntry(t, registry.Entry{SessionName: "agentctl-a", RuntimeDir: "/aaa/.agentctl"})
	registerEntry(t, registry.Entry{SessionName: "agentctl-cur", RuntimeDir: filepath.Dir(paths.AuditDir())})

	dirs, err := AllProjectAuditDirs()
	if err != nil {
		t.Fatalf("AllProjectAuditDirs: %v", err)
	}
	want := []string{paths.AuditDir(), "/aaa/.agentctl/audit"}
	if len(dirs) != 2 || dirs[0] != want[0] || dirs[1] != want[1] {
		t.Errorf("dirs = %v, want %v", dirs, want)
	}
}

// TestAllProjectAuditDirs_SkipsMalformedRegistryEntry pins that one corrupt
// registry file (a partial write, disk corruption, etc.) does not blank
// --all-projects entirely -- registry.List already guarantees this, this
// test guards the contract at the audit-package boundary too.
func TestAllProjectAuditDirs_SkipsMalformedRegistryEntry(t *testing.T) {
	dir := isolateRegistry(t)

	registerEntry(t, registry.Entry{
		SessionName: "agentctl-good",
		ProjectDir:  "/home/user/good",
		RuntimeDir:  "/home/user/good/.agentctl",
	})
	if err := writeRawLine(t, filepath.Join(dir, "agentctl-bad.json"), "{not valid json"); err != nil {
		t.Fatalf("writeRawLine: %v", err)
	}

	dirs, err := AllProjectAuditDirs()
	if err != nil {
		t.Fatalf("AllProjectAuditDirs: %v", err)
	}
	if len(dirs) != 1 || dirs[0] != "/home/user/good/.agentctl/audit" {
		t.Errorf("dirs = %v, want exactly the good project's audit dir", dirs)
	}
}

// TestAllProjectAuditDirs_EntryWithNoAuditDirYieldsNoRecords covers a
// registered project that has never had a tool call recorded (no audit/ dir
// on disk yet): it must not error, just contribute nothing.
func TestAllProjectAuditDirs_EntryWithNoAuditDirYieldsNoRecords(t *testing.T) {
	isolateRegistry(t)

	registerEntry(t, registry.Entry{
		SessionName: "agentctl-empty",
		ProjectDir:  "/home/user/empty",
		RuntimeDir:  "/home/user/empty/.agentctl", // deliberately never created on disk
	})

	dirs, err := AllProjectAuditDirs()
	if err != nil {
		t.Fatalf("AllProjectAuditDirs: %v", err)
	}
	got, err := QueryDirs(dirs, Filter{})
	if err != nil {
		t.Fatalf("QueryDirs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

func isolateRegistry(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	registry.SetDirForTest(dir)
	t.Cleanup(func() { registry.SetDirForTest("") })
	return dir
}

func registerEntry(t *testing.T, e registry.Entry) {
	t.Helper()
	if err := registry.Register(e); err != nil {
		t.Fatalf("registry.Register(%+v): %v", e, err)
	}
}

// writeRawLine writes content as the sole line of path, creating parent
// directories as needed. Used to plant hand-crafted (legacy-schema or
// malformed) content that Append would never produce itself.
func writeRawLine(t *testing.T, path, content string) error {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content+"\n"), 0o600)
}

// writeRecordToDir marshals rec and appends it directly to
// <dir>/<rec.Session>.jsonl, bypassing paths/Append entirely -- for building
// multi-project fixtures QueryDirs can scan without touching the
// paths-singleton-backed current project.
func writeRecordToDir(t *testing.T, dir string, rec Record) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	path := filepath.Join(dir, rec.Session+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
