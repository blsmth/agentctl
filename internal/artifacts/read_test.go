package artifacts

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func addDir(t *testing.T, s *Store, files map[string]string) Meta {
	t.Helper()
	d := filepath.Join(t.TempDir(), "site")
	for rel, content := range files {
		write(t, filepath.Join(d, filepath.FromSlash(rel)), content)
	}
	m, err := s.Add(AddOptions{Source: Source{Path: d}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOpen(t *testing.T) {
	s := newStore(t)
	m := addDir(t, s, map[string]string{
		"index.html":      "<title>Home</title>",
		"css/a.css":       "body{}",
		"sub/index.html":  "sub",
		"assets/logo.svg": "<svg/>",
	})
	root, _ := s.Dir()
	revDir := filepath.Join(root, m.ID, "rev", "1")
	outside := write(t, filepath.Join(t.TempDir(), "secret.txt"), "secret")
	if err := os.Symlink(outside, filepath.Join(revDir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(revDir, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(revDir, "css"), filepath.Join(revDir, "inside")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, rel string
		rev       int
		wantRel   string
		wantBody  string
		wantErr   error
	}{
		{"entry", "", 0, "index.html", "<title>Home</title>", nil},
		{"latest by number", "css/a.css", 1, "css/a.css", "body{}", nil},
		{"dir index", "sub/", 0, "sub/index.html", "sub", nil},
		{"dir index no slash", "sub", 0, "sub/index.html", "sub", nil},
		{"missing file", "nope.html", 0, "", "", ErrNotFound},
		{"missing rev", "", 7, "", "", ErrNotFound},
		{"dotdot", "../meta.json", 0, "", "", ErrBadPath},
		{"inner dotdot", "css/../index.html", 0, "", "", ErrBadPath},
		{"absolute", "/etc/passwd", 0, "", "", ErrBadPath},
		{"double slash", "css//a.css", 0, "", "", ErrBadPath},
		{"dot", "./index.html", 0, "", "", ErrBadPath},
		{"backslash", "..\\meta.json", 0, "", "", ErrBadPath},
		{"nul", "index.html\x00", 0, "", "", ErrBadPath},
		{"symlink file", "link.txt", 0, "", "", ErrBadPath},
		{"symlink dir", "linkdir/secret.txt", 0, "", "", ErrBadPath},
		{"symlink inside rev", "inside/a.css", 0, "", "", ErrBadPath},
		{"file as dir", "index.html/x", 0, "", "", ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := s.Open(m.ID, tc.rev, tc.rel)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close() //nolint:errcheck
			body, _ := io.ReadAll(c)
			if c.Rel != tc.wantRel || string(body) != tc.wantBody || c.Rev != 1 {
				t.Errorf("got %q rev %d %q, want %q %q", c.Rel, c.Rev, body, tc.wantRel, tc.wantBody)
			}
		})
	}
	if _, err := s.Open("notanid!!", 0, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad id: %v", err)
	}
}

func TestOpenOlderRevision(t *testing.T) {
	s := newStore(t)
	src := write(t, filepath.Join(t.TempDir(), "r.md"), "# one")
	m, err := s.Add(AddOptions{Source: Source{Path: src}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, src, "# two")
	if _, err := s.Update(m.ID, UpdateOptions{Source: Source{Path: src}}); err != nil {
		t.Fatal(err)
	}
	for rev, want := range map[int]string{0: "# two", 1: "# one", 2: "# two"} {
		c, err := s.Open(m.ID, rev, "")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(c)
		_ = c.Close()
		if string(body) != want {
			t.Errorf("rev %d = %q, want %q", rev, body, want)
		}
	}
}

// Regression: an update at the revision cap must count the bytes of the
// revision it drops, so a full store can still replace its oldest content.
func TestUpdateCapacityCountsDroppedRevision(t *testing.T) {
	setLimits(t, 0, 0, 25, 2)
	s := newStore(t)
	src := write(t, filepath.Join(t.TempDir(), "a.txt"), strings.Repeat("a", 10))
	m, err := s.Add(AddOptions{Source: Source{Path: src}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(m.ID, UpdateOptions{Source: Source{Path: src}}); err != nil {
		t.Fatal(err) // 20 of 25 used
	}
	// Rev 3 drops rev 1: 20 - 10 + 10 = 20 <= 25. Without the fix: 30 > 25.
	got, err := s.Update(m.ID, UpdateOptions{Source: Source{Path: src}})
	if err != nil {
		t.Fatalf("update at the cap: %v", err)
	}
	if len(got.Revs) != 2 || got.Revs[0].N != 2 {
		t.Errorf("revs = %+v", got.Revs)
	}
	// Still refused when the new revision is bigger than what is freed.
	write(t, src, strings.Repeat("b", 16))
	if _, err := s.Update(m.ID, UpdateOptions{Source: Source{Path: src}}); err == nil || !strings.Contains(err.Error(), "full") {
		t.Errorf("err = %v, want store full", err)
	}
}

func TestNoNotifyPersisted(t *testing.T) {
	s := newStore(t)
	src := write(t, filepath.Join(t.TempDir(), "a.txt"), "x")
	m, err := s.Add(AddOptions{Source: Source{Path: src}, NoNotify: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(m.ID)
	if err != nil || !got.NoNotify {
		t.Errorf("no_notify not persisted: %+v %v", got, err)
	}
}

func TestChangesStamp(t *testing.T) {
	s := newStore(t)
	dir, err := s.ChangesDir()
	if err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(dir, "stamp")
	stat := func() string {
		data, err := os.ReadFile(stamp)
		if err != nil {
			return ""
		}
		return string(data)
	}
	if stat() != "" {
		t.Fatal("stamp before any write")
	}
	src := write(t, filepath.Join(t.TempDir(), "a.txt"), "x")
	m, err := s.Add(AddOptions{Source: Source{Path: src}})
	if err != nil {
		t.Fatal(err)
	}
	steps := []func() error{
		func() error { _, err := s.Update(m.ID, UpdateOptions{Source: Source{Path: src}}); return err },
		func() error { _, err := s.Pin(m.ID, true); return err },
		func() error { _, err := s.Remove(m.ID); return err },
	}
	for i, step := range steps {
		_ = os.Remove(stamp)
		if err := step(); err != nil {
			t.Fatal(err)
		}
		if stat() == "" {
			t.Errorf("step %d did not touch the stamp", i)
		}
	}
	// The change dir is not an artifact.
	if all, _ := s.List(Filter{}); len(all) != 0 {
		t.Errorf("list = %v", all)
	}

}
