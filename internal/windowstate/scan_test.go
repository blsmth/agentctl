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
