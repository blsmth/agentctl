package artifacts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "artifacts")
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	return &Store{Dir: func() (string, error) { return dir, nil }, Now: func() time.Time { return now }}
}

func write(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func setLimits(t *testing.T, files int, rev, store int64, revs int) {
	t.Helper()
	of, orb, osb, ors := MaxFiles, MaxRevBytes, MaxStoreBytes, MaxRevs
	t.Cleanup(func() { MaxFiles, MaxRevBytes, MaxStoreBytes, MaxRevs = of, orb, osb, ors })
	if files > 0 {
		MaxFiles = files
	}
	if rev > 0 {
		MaxRevBytes = rev
	}
	if store > 0 {
		MaxStoreBytes = store
	}
	if revs > 0 {
		MaxRevs = revs
	}
}

func TestAddKindsAndTitles(t *testing.T) {
	tests := []struct {
		name, file, content string
		kind, title, entry  string
		wantErr             string
	}{
		{"html title", "page.html", "<html><head><TITLE> My  &amp; Page </TITLE></head></html>", KindHTML, "My & Page", "page.html", ""},
		{"html no title falls back to file", "page.html", "<p>x</p>", KindHTML, "page.html", "page.html", ""},
		{"markdown heading", "r.md", "intro\n# The Report\n", KindMarkdown, "The Report", "r.md", ""},
		{"markdown heading in fence ignored", "r.md", "```\n# not\n```\n# Real\n", KindMarkdown, "Real", "r.md", ""},
		{"markdown no heading", "notes.md", "just text", KindMarkdown, "notes.md", "notes.md", ""},
		{"image", "a.png", "\x89PNG\x00\x01", KindImage, "a.png", "a.png", ""},
		{"text json", "d.json", `{"a":1}`, KindText, "d.json", "d.json", ""},
		{"unknown ext utf8 is text", "data.xyz", "hello", KindText, "data.xyz", "data.xyz", ""},
		{"unknown ext binary refused", "data.bin", "\xff\xfe\x00\x01", "", "", "", "unknown extension"},
		{"txt with binary refused", "x.txt", "ab\x00cd", "", "", "", "not valid UTF-8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			src := write(t, filepath.Join(t.TempDir(), tc.file), tc.content)
			m, err := s.Add(AddOptions{Source: Source{Path: src}})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if all, _ := s.List(Filter{}); len(all) != 0 {
					t.Fatalf("refused add left %d artifacts", len(all))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if m.Kind != tc.kind || m.Title != tc.title || m.Entry != tc.entry {
				t.Errorf("got kind=%s title=%q entry=%s, want %s %q %s", m.Kind, m.Title, m.Entry, tc.kind, tc.title, tc.entry)
			}
			if !ValidID(m.ID) || len(m.Revs) != 1 || m.Revs[0].N != 1 {
				t.Errorf("bad id/revs: %+v", m)
			}
		})
	}
}

func TestAddOverrides(t *testing.T) {
	s := newStore(t)
	src := write(t, filepath.Join(t.TempDir(), "x.dat"), "hello")
	m, err := s.Add(AddOptions{Source: Source{Path: src}, Title: "  Mine ", Kind: KindText, Pin: true, Description: "d",
		Origin: Origin{Agent: "go-engineer", WindowID: "@4", Project: "agentctl"}})
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Mine" || !m.Pinned || m.Agent != "go-engineer" || m.WindowID != "@4" || m.Slug != "mine" {
		t.Errorf("meta = %+v", m)
	}
	if _, err := s.Add(AddOptions{Source: Source{Path: src}, Kind: "pdf"}); err == nil {
		t.Error("unknown --kind accepted")
	}
}

func TestPermissions(t *testing.T) {
	s := newStore(t)
	d := t.TempDir()
	write(t, filepath.Join(d, "p", "index.html"), "<title>t</title>")
	write(t, filepath.Join(d, "p", "css", "a.css"), "x")
	m, err := s.Add(AddOptions{Source: Source{Path: filepath.Join(d, "p")}})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := s.Dir()
	for _, p := range []string{root, filepath.Join(root, m.ID), filepath.Join(root, m.ID, "rev", "1", "css")} {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o700 {
			t.Errorf("%s perm %v, want 0700", p, fi.Mode().Perm())
		}
	}
	for _, p := range []string{filepath.Join(root, m.ID, "meta.json"), filepath.Join(root, m.ID, "rev", "1", "css", "a.css")} {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s perm %v, want 0600", p, fi.Mode().Perm())
		}
	}
}

func TestDirectoryEntry(t *testing.T) {
	tests := []struct {
		name    string
		files   []string
		entry   string
		want    string
		wantErr string
	}{
		{"index.html", []string{"index.html", "a.css"}, "", "index.html", ""},
		{"single file", []string{"only.md"}, "", "only.md", ""},
		{"ambiguous", []string{"a.md", "b.md"}, "", "", "no index.html"},
		{"explicit entry", []string{"a.md", "sub/b.md"}, "sub/b.md", "sub/b.md", ""},
		{"entry dotdot", []string{"a.md"}, "../a.md", "", "no .."},
		{"entry absolute", []string{"a.md"}, "/etc/passwd", "", "relative"},
		{"entry missing", []string{"a.md"}, "nope.md", "", "not a file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			d := t.TempDir()
			for _, f := range tc.files {
				write(t, filepath.Join(d, "dir", f), "# x\n")
			}
			m, err := s.Add(AddOptions{Source: Source{Path: filepath.Join(d, "dir")}, Entry: tc.entry})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || m.Entry != tc.want {
				t.Fatalf("entry = %q err=%v, want %q", m.Entry, err, tc.want)
			}
		})
	}
}

func TestStdin(t *testing.T) {
	s := newStore(t)
	if _, err := s.Add(AddOptions{Source: Source{Stdin: strings.NewReader("<p>x</p>")}}); err == nil || !strings.Contains(err.Error(), "--entry") {
		t.Errorf("stdin without hint: %v", err)
	}
	m, err := s.Add(AddOptions{Source: Source{Stdin: strings.NewReader("<title>From stdin</title>")}, Entry: "report.html"})
	if err != nil || m.Kind != KindHTML || m.Entry != "report.html" || m.Title != "From stdin" {
		t.Fatalf("stdin add: %+v %v", m, err)
	}
	m2, err := s.Add(AddOptions{Source: Source{Stdin: strings.NewReader("# Hi\n")}, Kind: KindMarkdown})
	if err != nil || m2.Entry != "report.md" || m2.Title != "Hi" {
		t.Fatalf("kind default entry: %+v %v", m2, err)
	}
	// update from stdin reuses the entry name
	u, err := s.Update(m.ID, UpdateOptions{Source: Source{Stdin: strings.NewReader("<title>v2</title>")}})
	if err != nil || u.Entry != "report.html" || u.Latest() != 2 {
		t.Fatalf("stdin update: %+v %v", u, err)
	}
	if _, err := s.Add(AddOptions{Source: Source{Stdin: strings.NewReader("")}, Entry: "a.txt"}); err == nil {
		t.Error("empty stdin accepted")
	}
}

func TestLimits(t *testing.T) {
	t.Run("file count names the limit", func(t *testing.T) {
		setLimits(t, 5, 0, 0, 0)
		s := newStore(t)
		d := t.TempDir()
		for i := 0; i < 50; i++ {
			write(t, filepath.Join(d, "node_modules", fmt.Sprintf("pkg%d", i), "index.js"), "x")
		}
		_, err := s.Add(AddOptions{Source: Source{Path: filepath.Join(d, "node_modules")}})
		if err == nil || !strings.Contains(err.Error(), "more than 5 files") {
			t.Fatalf("err = %v", err)
		}
		assertEmptyStore(t, s)
	})
	t.Run("rev size", func(t *testing.T) {
		setLimits(t, 0, 100, 0, 0)
		s := newStore(t)
		src := write(t, filepath.Join(t.TempDir(), "big.txt"), strings.Repeat("a", 101))
		if _, err := s.Add(AddOptions{Source: Source{Path: src}}); err == nil || !strings.Contains(err.Error(), "per-revision limit") {
			t.Fatalf("err = %v", err)
		}
		if _, err := s.Add(AddOptions{Source: Source{Stdin: strings.NewReader(strings.Repeat("a", 101))}, Entry: "a.txt"}); err == nil {
			t.Error("big stdin accepted")
		}
		d := t.TempDir()
		write(t, filepath.Join(d, "x", "a.txt"), strings.Repeat("a", 60))
		write(t, filepath.Join(d, "x", "b.txt"), strings.Repeat("a", 60))
		if _, err := s.Add(AddOptions{Source: Source{Path: filepath.Join(d, "x")}, Entry: "a.txt"}); err == nil {
			t.Error("dir over size accepted")
		}
		assertEmptyStore(t, s)
	})
	t.Run("store cap", func(t *testing.T) {
		setLimits(t, 0, 0, 100, 0)
		s := newStore(t)
		a := write(t, filepath.Join(t.TempDir(), "a.txt"), strings.Repeat("a", 60))
		if _, err := s.Add(AddOptions{Source: Source{Path: a}}); err != nil {
			t.Fatal(err)
		}
		_, err := s.Add(AddOptions{Source: Source{Path: a}})
		if err == nil || !strings.Contains(err.Error(), "artifact rm") || !strings.Contains(err.Error(), "prune") {
			t.Fatalf("err = %v", err)
		}
	})
}

func assertEmptyStore(t *testing.T, s *Store) {
	t.Helper()
	all, _ := s.List(Filter{})
	if len(all) != 0 {
		t.Errorf("store has %d artifacts, want 0", len(all))
	}
	root, _ := s.Dir()
	if ents, _ := os.ReadDir(filepath.Join(root, ".tmp")); len(ents) != 0 {
		t.Errorf("staging leftovers: %v", ents)
	}
}

func TestSymlinksRefused(t *testing.T) {
	s := newStore(t)
	d := t.TempDir()
	secret := write(t, filepath.Join(d, "secret.txt"), "s")
	write(t, filepath.Join(d, "site", "index.html"), "<p>")
	if err := os.Symlink(secret, filepath.Join(d, "site", "leak.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(AddOptions{Source: Source{Path: filepath.Join(d, "site")}}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink in dir: %v", err)
	}
	link := filepath.Join(d, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(AddOptions{Source: Source{Path: link}}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink arg: %v", err)
	}
	// An escaping entry is refused even when the dir is clean.
	write(t, filepath.Join(d, "ok", "index.html"), "<p>")
	if _, err := s.Add(AddOptions{Source: Source{Path: filepath.Join(d, "ok")}, Entry: "../secret.txt"}); err == nil {
		t.Error(".. entry accepted")
	}
	assertEmptyStore(t, s)
}

func TestUpdateRevisions(t *testing.T) {
	setLimits(t, 0, 0, 0, 3)
	s := newStore(t)
	d := t.TempDir()
	src := write(t, filepath.Join(d, "r.md"), "# One\n")
	m, err := s.Add(AddOptions{Source: Source{Path: src}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 5; i++ {
		write(t, src, fmt.Sprintf("# Rev %d\n", i))
		m, err = s.Update(m.ID, UpdateOptions{Source: Source{Path: src}, Note: fmt.Sprintf("n%d", i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(m.Revs) != 3 || m.Revs[0].N != 3 || m.Latest() != 5 || m.Revs[2].Note != "n5" {
		t.Fatalf("revs = %+v", m.Revs)
	}
	if m.Title != "One" {
		t.Errorf("update changed the title to %q", m.Title)
	}
	root, _ := s.Dir()
	ents, _ := os.ReadDir(filepath.Join(root, m.ID, "rev"))
	if len(ents) != 3 {
		t.Errorf("rev dirs = %d, want 3 (oldest dropped)", len(ents))
	}
	if _, err := s.RevDir(m.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("dropped rev: %v", err)
	}
	dir, err := s.RevDir(m.ID, 0)
	if err != nil || filepath.Base(dir) != "5" {
		t.Errorf("latest dir = %q %v", dir, err)
	}
	if u, err := s.Update(m.ID, UpdateOptions{Source: Source{Path: src}, Title: "Renamed"}); err != nil || u.Title != "Renamed" {
		t.Errorf("--title on update: %+v %v", u, err)
	}
	if _, err := s.Update("aaaaaaaaaa", UpdateOptions{Source: Source{Path: src}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}
}

func TestRemoveAndPin(t *testing.T) {
	s := newStore(t)
	src := write(t, filepath.Join(t.TempDir(), "a.txt"), "a")
	m, _ := s.Add(AddOptions{Source: Source{Path: src}})
	if p, err := s.Pin(m.ID, true); err != nil || !p.Pinned {
		t.Fatalf("pin: %+v %v", p, err)
	}
	if g, _ := s.Get(m.ID); !g.Pinned {
		t.Error("pin not persisted")
	}
	if _, err := s.Remove(m.ID); err != nil {
		t.Fatal(err)
	}
	assertEmptyStore(t, s)
	for _, id := range []string{m.ID, "aaaaaaaaaa", "../etc", "", "ABC"} {
		_, err := s.Remove(id)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Remove(%q) = %v, want ErrNotFound", id, err)
		}
	}
	if _, err := s.Pin("aaaaaaaaaa", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("pin missing: %v", err)
	}
}

func TestListFilterAndOrder(t *testing.T) {
	s := newStore(t)
	clock := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return clock }
	src := write(t, filepath.Join(t.TempDir(), "a.txt"), "a")
	add := func(o Origin) Meta {
		clock = clock.Add(24 * time.Hour)
		m, err := s.Add(AddOptions{Source: Source{Path: src}, Origin: o})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	a := add(Origin{Project: "p1", ProjectDir: "/r/p1", Agent: "x", WindowID: "@1"})
	b := add(Origin{Project: "p2", ProjectDir: "/r/p2", Agent: "y", WindowID: "@2"})
	got, _ := s.List(Filter{})
	if len(got) != 2 || got[0].ID != b.ID {
		t.Fatalf("order: %+v", got)
	}
	tests := []struct {
		name string
		f    Filter
		want []string
	}{
		{"project name", Filter{Project: "p1"}, []string{a.ID}},
		{"project dir", Filter{Project: "/r/p2"}, []string{b.ID}},
		{"agent", Filter{Agent: "y"}, []string{b.ID}},
		{"window", Filter{WindowID: "@1"}, []string{a.ID}},
		{"since", Filter{Since: b.Updated}, []string{b.ID}},
		{"none", Filter{Project: "zzz"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := s.List(tc.f)
			var ids []string
			for _, m := range got {
				ids = append(ids, m.ID)
			}
			if fmt.Sprint(ids) != fmt.Sprint(tc.want) {
				t.Errorf("got %v want %v", ids, tc.want)
			}
		})
	}
}

func TestPrune(t *testing.T) {
	s := newStore(t)
	base := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	src := write(t, filepath.Join(t.TempDir(), "a.txt"), "a")
	add := func(age time.Duration, pin bool) Meta {
		s.Now = func() time.Time { return base.Add(-age) }
		m, err := s.Add(AddOptions{Source: Source{Path: src}, Pin: pin})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	old := add(45*24*time.Hour, false)
	oldPinned := add(60*24*time.Hour, true)
	fresh := add(2*24*time.Hour, false)
	s.Now = func() time.Time { return base }

	got, err := s.Prune(30*24*time.Hour, true)
	if err != nil || len(got) != 1 || got[0].ID != old.ID {
		t.Fatalf("dry run = %+v %v", got, err)
	}
	if all, _ := s.List(Filter{}); len(all) != 3 {
		t.Fatalf("dry run deleted: %d left", len(all))
	}
	if _, err := s.Prune(30*24*time.Hour, false); err != nil {
		t.Fatal(err)
	}
	all, _ := s.List(Filter{})
	if len(all) != 2 {
		t.Fatalf("left %d, want 2", len(all))
	}
	for _, m := range all {
		if m.ID != oldPinned.ID && m.ID != fresh.ID {
			t.Errorf("unexpected survivor %s", m.ID)
		}
	}
	if _, err := s.Prune(0, false); err == nil {
		t.Error("zero older-than accepted")
	}
}

func TestPruneMissingStore(t *testing.T) {
	s := newStore(t)
	if got, err := s.Prune(time.Hour, false); err != nil || got != nil {
		t.Errorf("= %v %v", got, err)
	}
	root, _ := s.Dir()
	if _, err := os.Stat(root); err == nil {
		t.Error("prune created the store dir")
	}
}

func TestSweepStaleStaging(t *testing.T) {
	s := newStore(t)
	src := write(t, filepath.Join(t.TempDir(), "a.txt"), "a")
	if _, err := s.Add(AddOptions{Source: Source{Path: src}}); err != nil {
		t.Fatal(err)
	}
	root, _ := s.Dir()
	stale := filepath.Join(root, ".tmp", "stage-crashed")
	write(t, filepath.Join(stale, "content", "half.txt"), "x")
	old := s.now().Add(-3 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	// A crashed stage is invisible to readers...
	if all, _ := s.List(Filter{}); len(all) != 1 {
		t.Fatalf("list sees %d", len(all))
	}
	// ...and swept by prune.
	if _, err := s.Prune(time.Hour, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale staging dir survived prune")
	}
}

func TestConcurrentAdds(t *testing.T) {
	s := newStore(t)
	src := write(t, filepath.Join(t.TempDir(), "a.txt"), "a")
	const n = 24
	var wg sync.WaitGroup
	ids := make(chan string, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := s.Add(AddOptions{Source: Source{Path: src}})
			if err != nil {
				errs <- err
				return
			}
			ids <- m.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate id %s", id)
		}
		seen[id] = true
	}
	if all, _ := s.List(Filter{}); len(all) != n {
		t.Errorf("stored %d, want %d", len(all), n)
	}
}

func TestConcurrentUpdates(t *testing.T) {
	s := newStore(t)
	src := write(t, filepath.Join(t.TempDir(), "a.txt"), "a")
	m, err := s.Add(AddOptions{Source: Source{Path: src}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Update(m.ID, UpdateOptions{Source: Source{Path: src}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, _ := s.Get(m.ID)
	if got.Latest() != 11 || len(got.Revs) != 11 {
		t.Errorf("revs = %d latest %d, want 11", len(got.Revs), got.Latest())
	}
}

func TestLazyDir(t *testing.T) {
	// New must not touch the state dir until used.
	s := New()
	if s.Dir == nil {
		t.Fatal("no Dir")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	d, err := s.Dir()
	if err != nil || !strings.HasSuffix(d, filepath.Join("agentctl", "artifacts")) {
		t.Errorf("dir = %q %v", d, err)
	}
}

func TestNewIDAndSlug(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		id := NewID()
		if len(id) != IDLen || strings.Trim(id, "abcdefghijklmnopqrstuvwxyz234567") != "" {
			t.Fatalf("id %q: want %d characters of a-z2-7", id, IDLen)
		}
		if !ValidID(id) || !CapabilityID(id) {
			t.Fatalf("NewID %q not valid", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	for in, want := range map[string]string{"Q4 Report: Sales!": "q4-report-sales", "  ": "", "ünï": "n"} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q want %q", in, got, want)
		}
	}
}

func TestValidID(t *testing.T) {
	cases := []struct {
		id         string
		valid, cap bool
	}{
		{"abcdefghij", true, false},                   // legacy 10
		{"abcdefghijklmnopqrstuvwxyz", true, true},    // 26
		{"a234567bcdefghijklmnopqrst", true, true},    // 26 with digits
		{"abcdefghi", false, false},                   // 9
		{"abcdefghijk", false, false},                 // 11
		{"abcdefghijklmnopqrstuvwxy", false, false},   // 25
		{"abcdefghijklmnopqrstuvwxyza", false, false}, // 27
		{"ABCDEFGHIJ", false, false},
		{"abcdefghi1", false, false}, // 1 is not base32
		{"abcdefghij/", false, false},
		{"../etc/pas", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			if got := ValidID(c.id); got != c.valid {
				t.Errorf("ValidID = %v want %v", got, c.valid)
			}
			if got := CapabilityID(c.id); got != c.cap {
				t.Errorf("CapabilityID = %v want %v", got, c.cap)
			}
		})
	}
	if got := ShortID("abcdefghijklmnopqrstuvwxyz"); got != "abcdef…" {
		t.Errorf("ShortID = %q", got)
	}
}

func TestLegacyIDStillReadable(t *testing.T) {
	s := newStore(t)
	m := addOne(t, s)
	root, _ := s.Dir()
	legacy := "abcdefghij"
	if err := os.Rename(filepath.Join(root, m.ID), filepath.Join(root, legacy)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, legacy, "meta.json"))
	data = []byte(strings.ReplaceAll(string(data), m.ID, legacy))
	if err := os.WriteFile(filepath.Join(root, legacy, "meta.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(legacy); err != nil {
		t.Fatalf("Get legacy: %v", err)
	}
	list, err := s.List(Filter{})
	if err != nil || len(list) != 1 || list[0].ID != legacy {
		t.Fatalf("List = %v %v", list, err)
	}
	c, err := s.Open(legacy, 0, "")
	if err != nil {
		t.Fatalf("Open legacy: %v", err)
	}
	_ = c.Close()
}
