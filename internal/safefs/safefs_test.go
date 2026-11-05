package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// mustWrite writes a file the test owns, failing the test on error.
func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// assertContent fails unless path holds exactly want.
func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func TestWriteFile(t *testing.T) {
	tests := []struct {
		name string
		// setup builds the scenario under root and returns the target path
		// plus the path of a victim file that must stay untouched (or "").
		setup   func(t *testing.T, root, outside string) (target, victim string)
		wantErr bool
	}{
		{
			name: "normal write creates parents",
			setup: func(t *testing.T, root, _ string) (string, string) {
				return filepath.Join(root, "a", "b", "f.json"), ""
			},
		},
		{
			name: "overwrite regular file",
			setup: func(t *testing.T, root, _ string) (string, string) {
				p := filepath.Join(root, "f.json")
				mustWrite(t, p, "old")
				os.Chmod(p, 0o644) //nolint:errcheck
				return p, ""
			},
		},
		{
			name: "target is symlink",
			setup: func(t *testing.T, root, outside string) (string, string) {
				victim := filepath.Join(outside, "victim")
				mustWrite(t, victim, "original")
				p := filepath.Join(root, "f.json")
				if err := os.Symlink(victim, p); err != nil {
					t.Fatal(err)
				}
				return p, victim
			},
			wantErr: true,
		},
		{
			name: "parent dir is symlink",
			setup: func(t *testing.T, root, outside string) (string, string) {
				victim := filepath.Join(outside, "f.json")
				mustWrite(t, victim, "original")
				if err := os.Symlink(outside, filepath.Join(root, "settings")); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(root, "settings", "f.json"), victim
			},
			wantErr: true,
		},
		{
			name: "root is symlink",
			setup: func(t *testing.T, root, outside string) (string, string) {
				victim := filepath.Join(outside, "f.json")
				mustWrite(t, victim, "original")
				os.Remove(root) //nolint:errcheck
				if err := os.Symlink(outside, root); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(root, "f.json"), victim
			},
			wantErr: true,
		},
		{
			name: "target outside root",
			setup: func(t *testing.T, _, outside string) (string, string) {
				return filepath.Join(outside, "f.json"), ""
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "root")
			outside := filepath.Join(base, "outside")
			for _, d := range []string{root, outside} {
				if err := os.Mkdir(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			target, victim := tt.setup(t, root, outside)

			err := WriteFile(root, target, []byte("new"), 0o600)
			if tt.wantErr {
				if err == nil {
					t.Fatal("WriteFile succeeded, want error")
				}
				if victim != "" {
					assertContent(t, victim, "original")
				}
				return
			}
			if err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			assertContent(t, target, "new")
			fi, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
			}
		})
	}
}

func TestWriteFileSymlinkErrorIsErrSymlink(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "f")
	if err := os.Symlink(filepath.Join(root, "elsewhere"), p); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(root, p, nil, 0o600); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "elsewhere")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target was created: %v", err)
	}
}

func TestOpenNoFollow(t *testing.T) {
	t.Run("creates regular file", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "w", "x.lock")
		f, err := OpenNoFollow(root, p, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.Close() //nolint:errcheck
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
		}
		di, err := os.Lstat(filepath.Dir(p))
		if err != nil {
			t.Fatal(err)
		}
		if di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
		}
	})
	t.Run("target symlink refused", func(t *testing.T) {
		root := t.TempDir()
		victim := filepath.Join(t.TempDir(), "victim")
		mustWrite(t, victim, "original")
		p := filepath.Join(root, "x.lock")
		if err := os.Symlink(victim, p); err != nil {
			t.Fatal(err)
		}
		_, err := OpenNoFollow(root, p, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
		if !errors.Is(err, ErrSymlink) {
			t.Fatalf("err = %v, want ErrSymlink", err)
		}
		assertContent(t, victim, "original")
	})
	t.Run("parent symlink refused", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "w")); err != nil {
			t.Fatal(err)
		}
		_, err := OpenNoFollow(root, filepath.Join(root, "w", "x.lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if !errors.Is(err, ErrSymlink) {
			t.Fatalf("err = %v, want ErrSymlink", err)
		}
		if _, err := os.Lstat(filepath.Join(outside, "x.lock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("lock file created through symlinked parent")
		}
	})
}

func TestPrivateTempDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	dir, err := PrivateTempDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != filepath.Clean(tmp) {
		t.Fatalf("dir = %s, want under %s", dir, tmp)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want 0700", fi.Mode().Perm())
	}

	// A loosened existing dir is tightened.
	os.Chmod(dir, 0o755) //nolint:errcheck
	if _, err := PrivateTempDir(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("mode after re-run = %v, want 0700", fi.Mode().Perm())
	}

	// A symlink planted at the path is refused.
	os.Remove(dir) //nolint:errcheck
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := PrivateTempDir(); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
}

func TestCheckDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		dir     string
		wantErr error // nil: ok
		anyErr  bool
	}{
		{dir: root},
		{dir: filepath.Join(root, "a", "b")},
		{dir: filepath.Join(root, "missing"), wantErr: os.ErrNotExist},
		{dir: filepath.Join(root, "link"), wantErr: ErrSymlink},
		{dir: filepath.Join(root, "link", "x"), wantErr: ErrSymlink},
		{dir: t.TempDir(), anyErr: true}, // outside root
	}
	for _, tt := range tests {
		err := CheckDir(root, tt.dir)
		switch {
		case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
			t.Errorf("CheckDir(%s) = %v, want %v", tt.dir, err, tt.wantErr)
		case tt.anyErr && err == nil:
			t.Errorf("CheckDir(%s) = nil, want error", tt.dir)
		case tt.wantErr == nil && !tt.anyErr && err != nil:
			t.Errorf("CheckDir(%s) = %v, want nil", tt.dir, err)
		}
	}
}

func TestChmodDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ChmodDir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want 0700", fi.Mode().Perm())
	}

	target := t.TempDir()
	os.Chmod(target, 0o755) //nolint:errcheck
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := ChmodDir(link, 0o700); !errors.Is(err, ErrSymlink) {
		t.Errorf("err = %v, want ErrSymlink", err)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o755 {
		t.Errorf("symlink target chmodded to %v", fi.Mode().Perm())
	}
}

func TestReplaceFile(t *testing.T) {
	t.Run("writes via unpredictable temp and tolerates symlinked parent", func(t *testing.T) {
		real := t.TempDir()
		linkDir := filepath.Join(t.TempDir(), "cfg")
		if err := os.Symlink(real, linkDir); err != nil { // dotfile-managed config dir
			t.Fatal(err)
		}
		p := filepath.Join(linkDir, "f.json")
		// A planted symlink at the old fixed temp name must not be followed.
		victim := filepath.Join(t.TempDir(), "victim")
		mustWrite(t, victim, "original")
		if err := os.Symlink(victim, p+".tmp"); err != nil {
			t.Fatal(err)
		}
		if err := ReplaceFile(p, []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
		assertContent(t, p, "new")
		assertContent(t, victim, "original")
	})
	t.Run("symlinked target refused", func(t *testing.T) {
		dir := t.TempDir()
		victim := filepath.Join(t.TempDir(), "victim")
		mustWrite(t, victim, "original")
		p := filepath.Join(dir, "f.json")
		if err := os.Symlink(victim, p); err != nil {
			t.Fatal(err)
		}
		if err := ReplaceFile(p, []byte("new"), 0o644); !errors.Is(err, ErrSymlink) {
			t.Fatalf("err = %v, want ErrSymlink", err)
		}
		assertContent(t, victim, "original")
	})
}
