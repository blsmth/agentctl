package tmux

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupLegacyLaunchDir(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Hour)

	setup := func(t *testing.T) (dir string, files map[string]string) {
		t.Helper()
		dir = filepath.Join(t.TempDir(), "agentctl")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		files = map[string]string{
			"oldLog":    filepath.Join(dir, "launch-go-engineer.log"),
			"oldScript": filepath.Join(dir, "launch-123.sh"),
			"recent":    filepath.Join(dir, "dispatch-sys-1.txt"),
		}
		for _, p := range files {
			if err := os.WriteFile(p, []byte("prompt"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, k := range []string{"oldLog", "oldScript"} {
			if err := os.Chtimes(files[k], old, old); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o700); err != nil {
			t.Fatal(err)
		}
		return dir, files
	}

	t.Run("removes own old files only", func(t *testing.T) {
		dir, files := setup(t)
		victim := filepath.Join(t.TempDir(), "victim")
		if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "launch-link.log")
		if err := os.Symlink(victim, link); err != nil {
			t.Fatal(err)
		}

		if n := cleanupLegacyLaunchDir(dir, os.Getuid(), now.Add(time.Hour)); n != 4 {
			t.Errorf("removed %d files, want 4 (everything is past legacyMinAge an hour from now)", n)
		}
		for _, p := range files {
			if _, err := os.Lstat(p); !os.IsNotExist(err) {
				t.Errorf("%s not removed", p)
			}
		}
		if _, err := os.Stat(victim); err != nil {
			t.Errorf("symlink target removed or followed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "subdir")); err != nil {
			t.Errorf("subdirectory touched: %v", err)
		}
	})

	t.Run("keeps recent files", func(t *testing.T) {
		dir, files := setup(t)
		if n := cleanupLegacyLaunchDir(dir, os.Getuid(), now); n != 2 {
			t.Errorf("removed %d files, want 2", n)
		}
		if _, err := os.Stat(files["recent"]); err != nil {
			t.Errorf("recent file removed: %v", err)
		}
	})

	t.Run("never touches files owned by another user", func(t *testing.T) {
		dir, files := setup(t)
		if n := cleanupLegacyLaunchDir(dir, os.Getuid()+1, now.Add(time.Hour)); n != 0 {
			t.Errorf("removed %d files owned by someone else", n)
		}
		for _, p := range files {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s removed: %v", p, err)
			}
		}
	})

	t.Run("removes the dir when emptied", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "agentctl")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "launch-x.log")
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		cleanupLegacyLaunchDir(dir, os.Getuid(), now.Add(time.Hour))
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("empty legacy dir not removed: %v", err)
		}
	})

	t.Run("missing dir is a no-op", func(t *testing.T) {
		if n := cleanupLegacyLaunchDir(filepath.Join(t.TempDir(), "nope"), os.Getuid(), now); n != 0 {
			t.Errorf("removed %d", n)
		}
	})
}

func TestCleanupLegacyLaunchDirOnceWritesMarker(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	launchDir := t.TempDir()
	cleanupLegacyLaunchDirOnce(launchDir)
	fi, err := os.Lstat(filepath.Join(launchDir, legacyCleanupMarker))
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("marker mode = %s, want 0600", fi.Mode().Perm())
	}
}
