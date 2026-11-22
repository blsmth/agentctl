package windowstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsDirFor(t *testing.T) {
	if got := WindowsDirFor("/proj/.agentctl"); got != filepath.Join("/proj/.agentctl", "windows") {
		t.Errorf("WindowsDirFor() = %q", got)
	}
	if got := WindowsDirFor(""); got != "" {
		t.Errorf("WindowsDirFor(\"\") = %q, want empty", got)
	}
}

func TestAllFrom(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeFile("@1.json", `{"state":"idle","mode":"adhoc","spawned_by":"@0","spawned_by_session":"uuid-0"}`)
	writeFile("@2.json", `{"state":"working","mode":"bare"}`)
	// The windows dir also holds lock and temp siblings from the flock
	// helper; only .json records are window state.
	writeFile("@1.json.lock", "")
	writeFile("@3.json.tmp", `{"state":"idle"}`)
	writeFile("notes.txt", "ignore me")
	// A corrupt record must be skipped, not abort the whole scan — a single
	// half-written file should not blind a cross-session query.
	writeFile("@4.json", `{"state":`)

	got := AllFrom(dir)

	// Only @1 and @2 are valid records: @1.json.lock and @3.json.tmp do not
	// end in .json, notes.txt is not a record, and @4.json is corrupt.
	if len(got) != 2 {
		t.Fatalf("AllFrom() returned %d records, want 2: %v", len(got), got)
	}
	if got["@1"].Mode != "adhoc" || got["@1"].SpawnedBySession != "uuid-0" {
		t.Errorf("@1 = %+v", got["@1"])
	}
	if got["@2"].State != "working" {
		t.Errorf("@2 = %+v", got["@2"])
	}
	if _, ok := got["@4"]; ok {
		t.Error("corrupt record was returned rather than skipped")
	}
	for _, bad := range []string{"@1.json", "@3.json", "notes"} {
		if _, ok := got[bad]; ok {
			t.Errorf("non-record file %q was parsed as window state", bad)
		}
	}
}

// AllFrom must not depend on, or disturb, the package-level windowsDir that
// Init sets for the current project — that global is what makes All() answer
// only about the caller's own session.
func TestAllFrom_IndependentOfPackageDir(t *testing.T) {
	saved := windowsDir
	t.Cleanup(func() { windowsDir = saved })

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "@9.json"), []byte(`{"state":"idle"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	windowsDir = "/nonexistent-on-purpose"
	got := AllFrom(other)
	if len(got) != 1 {
		t.Fatalf("AllFrom() = %v, want the one record from the foreign dir", got)
	}
	if windowsDir != "/nonexistent-on-purpose" {
		t.Errorf("AllFrom mutated the package dir to %q", windowsDir)
	}
}

func TestAllFrom_MissingDir(t *testing.T) {
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "nope")} {
		got := AllFrom(dir)
		if got == nil {
			t.Fatalf("AllFrom(%q) returned nil, want empty map", dir)
		}
		if len(got) != 0 {
			t.Errorf("AllFrom(%q) = %v, want empty", dir, got)
		}
	}
}

func TestPRFieldsRoundTrip(t *testing.T) {
	SetDirForTest(t.TempDir())
	if err := Update("@1", func(ws *WindowState) { ws.PRNumber = 12; ws.PRURL = "https://github.com/o/r/pull/12" }); err != nil {
		t.Fatal(err)
	}
	// An unrelated later write must keep them.
	if err := Update("@1", func(ws *WindowState) { ws.State = "idle" }); err != nil {
		t.Fatal(err)
	}
	ws, err := Read("@1")
	if err != nil {
		t.Fatal(err)
	}
	if ws.PRNumber != 12 || ws.PRURL != "https://github.com/o/r/pull/12" {
		t.Errorf("got %d %q", ws.PRNumber, ws.PRURL)
	}
}

func TestDirCacheRecords(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		tmp := filepath.Join(dir, "."+name+".tmp")
		if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	write("@1.json", `{"state":"working"}`)
	write("@2.json", `{"state":"idle"}`)
	write("@3.json", `not json`)
	write("@9.json", `{"state":"blocked"}`) // not asked for
	if err := os.Symlink(filepath.Join(dir, "@1.json"), filepath.Join(dir, "@4.json")); err != nil {
		t.Fatal(err)
	}

	c := NewDirCache(dir)
	got := c.Records([]string{"@1", "@2", "@3", "@4", "@5", "../x"})
	if len(got) != 2 || got["@1"].State != "working" || got["@2"].State != "idle" {
		t.Fatalf("Records = %+v, want @1 working and @2 idle only (no corrupt, symlink, missing or unasked)", got)
	}

	// A rewritten record is re-read; an unchanged one comes from the cache.
	write("@1.json", `{"state":"blocked"}`)
	if got := c.Records([]string{"@1", "@2"}); got["@1"].State != "blocked" || got["@2"].State != "idle" {
		t.Fatalf("after rewrite: %+v", got)
	}

	// A removed record disappears.
	if err := os.Remove(filepath.Join(dir, "@2.json")); err != nil {
		t.Fatal(err)
	}
	if got := c.Records([]string{"@1", "@2"}); len(got) != 1 {
		t.Fatalf("after remove: %+v", got)
	}

	// A missing dir yields nothing.
	if got := NewDirCache(filepath.Join(dir, "nope")).Records([]string{"@1"}); len(got) != 0 {
		t.Fatalf("missing dir: %+v", got)
	}
}

// TestDirCacheSkipsUnchangedFiles: an unchanged file is not read again (an
// unreadable but unchanged file still returns the cached record).
func TestDirCacheSkipsUnchangedFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads mode 000 files")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "@1.json")
	if err := os.WriteFile(p, []byte(`{"state":"working"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewDirCache(dir)
	if got := c.Records([]string{"@1"}); got["@1"].State != "working" {
		t.Fatalf("first read: %+v", got)
	}
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	if got := c.Records([]string{"@1"}); got["@1"].State != "working" {
		t.Errorf("unchanged file was re-read: %+v", got)
	}
}
