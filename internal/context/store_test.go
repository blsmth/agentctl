package context_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	ctxstore "github.com/blsmth/agentctl/internal/context"
)

func setup(t *testing.T) {
	t.Helper()
	ctxstore.SetPathForTest(t.TempDir())
}

func TestSetAndGet(t *testing.T) {
	setup(t)

	e := ctxstore.Entry{
		Agent:   "go-engineer",
		Content: "hello world",
		Tags:    []string{"test"},
	}
	if err := ctxstore.Set("mykey", e); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Verify file exists on disk as .md.
	mdPath := filepath.Join(ctxstore.Dir(), "mykey.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Fatalf("expected file %s to exist: %v", mdPath, err)
	}

	got, ok, err := ctxstore.Get("mykey")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected entry to be found")
	}
	if got.Key != "mykey" {
		t.Errorf("Key: got %q, want %q", got.Key, "mykey")
	}
	if got.Content != "hello world" {
		t.Errorf("Content: got %q, want %q", got.Content, "hello world")
	}
	if got.Agent != "go-engineer" {
		t.Errorf("Agent: got %q, want %q", got.Agent, "go-engineer")
	}
	if len(got.Tags) != 1 || got.Tags[0] != "test" {
		t.Errorf("Tags: got %v, want [test]", got.Tags)
	}
	if got.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
}

func TestGetMissing(t *testing.T) {
	setup(t)

	_, ok, err := ctxstore.Get("nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Error("expected false for missing key")
	}
}

func TestDelete(t *testing.T) {
	setup(t)

	if err := ctxstore.Set("k", ctxstore.Entry{Content: "v"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ctxstore.Delete("k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, ok, err := ctxstore.Get("k")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if ok {
		t.Error("expected entry to be gone after delete")
	}
}

func TestDeleteMissing(t *testing.T) {
	setup(t)
	// Should not error when key doesn't exist.
	if err := ctxstore.Delete("missing"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
}

func TestList(t *testing.T) {
	setup(t)

	keys := []string{"a", "b", "c"}
	for _, k := range keys {
		// Small sleep so mtimes differ.
		time.Sleep(10 * time.Millisecond)
		if err := ctxstore.Set(k, ctxstore.Entry{Content: k}); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	entries, err := ctxstore.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("List: got %d entries, want 3", len(entries))
	}
	// Newest first
	if entries[0].Key != "c" {
		t.Errorf("entries[0].Key = %q, want c", entries[0].Key)
	}
}

func TestSearch(t *testing.T) {
	setup(t)

	if err := ctxstore.Set("x", ctxstore.Entry{Content: "x", Tags: []string{"alpha"}}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ctxstore.Set("y", ctxstore.Entry{Content: "y", Tags: []string{"beta"}}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ctxstore.Set("z", ctxstore.Entry{Content: "z", Tags: []string{"alpha", "beta"}}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	results, err := ctxstore.Search("alpha")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Search alpha: got %d, want 2", len(results))
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	ctxstore.SetPathForTest(dir)

	if err := ctxstore.Set("persist", ctxstore.Entry{Content: "stays"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Re-point at the same dir (simulates restart).
	ctxstore.SetPathForTest(dir)

	e, ok, err := ctxstore.Get("persist")
	if err != nil {
		t.Fatalf("Get after reset: %v", err)
	}
	if !ok {
		t.Fatal("entry should persist across reloads")
	}
	if e.Content != "stays" {
		t.Errorf("Content: got %q, want %q", e.Content, "stays")
	}
}

func TestFormat(t *testing.T) {
	e := ctxstore.Entry{
		Key:     "api-notes",
		Agent:   "go-engineer",
		Content: "Use REST over gRPC for this service.",
	}
	got := ctxstore.Format(e)
	want := "## Context: api-notes (from go-engineer)\n\nUse REST over gRPC for this service."
	if got != want {
		t.Errorf("Format:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestTicketSpec(t *testing.T) {
	setup(t)
	for _, k := range []string{"ticket-2-spec", "ticket-3-spec", "notes"} {
		if err := ctxstore.Set(k, ctxstore.Entry{Content: k, Agent: "some-agent"}); err != nil {
			t.Fatal(err)
		}
	}
	e, ok, err := ctxstore.TicketSpec(2)
	if err != nil || !ok || e.Key != "ticket-2-spec" {
		t.Fatalf("TicketSpec(2) = %v, %v, %v", e, ok, err)
	}
	if _, ok, _ := ctxstore.TicketSpec(9); ok {
		t.Error("TicketSpec(9) found a spec that does not exist")
	}
}

func TestTicketSpec_Truncates(t *testing.T) {
	setup(t)
	old := ctxstore.MaxEntryBytes
	ctxstore.MaxEntryBytes = 100
	t.Cleanup(func() { ctxstore.MaxEntryBytes = old })
	if err := ctxstore.Set("ticket-1-spec", ctxstore.Entry{Content: strings.Repeat("é", 200)}); err != nil {
		t.Fatal(err)
	}
	e, ok, err := ctxstore.TicketSpec(1)
	if err != nil || !ok {
		t.Fatalf("got %v, %v", ok, err)
	}
	c := e.Content
	if !strings.HasSuffix(c, "[truncated, full text: agentctl context get ticket-1-spec]") || !utf8.ValidString(c) || len(c) > 100+70 {
		t.Errorf("content = %q", c)
	}
}

func TestMarkUsed(t *testing.T) {
	setup(t)
	if err := ctxstore.Set("k", ctxstore.Entry{Content: "body", Tags: []string{"t"}}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(filepath.Join(ctxstore.Dir(), "k.md"), old, old); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := ctxstore.MarkUsed("k", now); err != nil {
		t.Fatal(err)
	}
	e, _, _ := ctxstore.Get("k")
	if !e.Used.Equal(now) || e.Content != "body" || len(e.Tags) != 1 {
		t.Errorf("entry after MarkUsed = %+v", e)
	}
	if !e.Timestamp.Equal(old) {
		t.Errorf("mtime moved: %v, want %v", e.Timestamp, old)
	}
	if err := ctxstore.MarkUsed("missing", now); err != nil {
		t.Errorf("MarkUsed(missing) = %v, want nil", err)
	}
	// A rewrite is new content: not used yet.
	if err := ctxstore.Set("k", ctxstore.Entry{Content: "new"}); err != nil {
		t.Fatal(err)
	}
	if e, _, _ := ctxstore.Get("k"); !e.Used.IsZero() {
		t.Errorf("Set kept Used = %v", e.Used)
	}
}

func TestLegacyEntryHasNoUsed(t *testing.T) {
	setup(t)
	legacy := "---\nagent: go-engineer\ntags:\n    - x\n---\nold body"
	if err := os.WriteFile(filepath.Join(ctxstore.Dir(), "old.md"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	e, ok, err := ctxstore.Get("old")
	if err != nil || !ok || !e.Used.IsZero() || e.Content != "old body" {
		t.Fatalf("got %+v, %v, %v", e, ok, err)
	}
}

func TestPruneUsed(t *testing.T) {
	setup(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, k := range []string{"unused", "recent", "stale", "exact"} {
		if err := ctxstore.Set(k, ctxstore.Entry{Content: k}); err != nil {
			t.Fatal(err)
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(ctxstore.MarkUsed("recent", now.Add(-time.Hour)))
	must(ctxstore.MarkUsed("stale", now.Add(-3*24*time.Hour)))
	must(ctxstore.MarkUsed("exact", now.Add(-24*time.Hour)))

	got, err := ctxstore.PruneUsed(24*time.Hour, now, true)
	must(err)
	sort.Strings(got)
	if strings.Join(got, ",") != "exact,stale" {
		t.Fatalf("dry run = %v", got)
	}
	if l, _ := ctxstore.List(); len(l) != 4 {
		t.Fatalf("dry run deleted: %d left", len(l))
	}
	_, err = ctxstore.PruneUsed(24*time.Hour, now, false)
	must(err)
	var left []string
	l, _ := ctxstore.List()
	for _, e := range l {
		left = append(left, e.Key)
	}
	sort.Strings(left)
	if strings.Join(left, ",") != "recent,unused" {
		t.Errorf("left = %v", left)
	}
}

func TestFileIsHumanReadable(t *testing.T) {
	dir := t.TempDir()
	ctxstore.SetPathForTest(dir)

	if err := ctxstore.Set("readable", ctxstore.Entry{
		Agent:   "go-engineer",
		Content: "some content here",
		Tags:    []string{"dx"},
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "readable.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	s := string(data)

	// Must be valid markdown with YAML frontmatter, not JSON.
	if !strings.HasPrefix(s, "---\n") {
		t.Errorf("expected YAML frontmatter delimiter, got %q", s[:20])
	}

	for _, needle := range []string{"go-engineer", "some content here", "dx"} {
		if !strings.Contains(s, needle) {
			t.Errorf("expected %q in store file", needle)
		}
	}

	// Must NOT be JSON.
	if strings.Contains(s, "{") || strings.Contains(s, "}") {
		t.Error("file looks like JSON, expected markdown with YAML frontmatter")
	}
}

func TestKeyValidation(t *testing.T) {
	setup(t)

	tests := []struct {
		name string
		key  string
	}{
		{"slash", "foo/bar"},
		{"backslash", "foo\\bar"},
		{"dot-prefix", ".hidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ctxstore.Set(tt.key, ctxstore.Entry{Content: "test"})
			if err == nil {
				t.Errorf("Set(%q) should have returned an error", tt.key)
			}
		})
	}
}

func TestEmptyFrontmatter(t *testing.T) {
	dir := t.TempDir()
	ctxstore.SetPathForTest(dir)

	if err := ctxstore.Set("plain", ctxstore.Entry{Content: "just markdown"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "plain.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	s := string(data)
	if strings.Contains(s, "---") {
		t.Error("expected no frontmatter delimiters for entry with no agent/tags")
	}
	if s != "just markdown" {
		t.Errorf("Content: got %q, want %q", s, "just markdown")
	}
}

func TestSet_FileModeIs0600AndOverwriteTightens(t *testing.T) {
	setup(t)
	path := filepath.Join(ctxstore.Dir(), "secret.md")

	// A pre-existing world-readable entry must be tightened on rewrite.
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ctxstore.Set("secret", ctxstore.Entry{Content: "token"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %v, want 0600", got)
	}

	entries, _ := os.ReadDir(ctxstore.Dir())
	for _, e := range entries {
		if e.Name() != "secret.md" {
			t.Errorf("stray file left behind: %s", e.Name())
		}
	}
}
