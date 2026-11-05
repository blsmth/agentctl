package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/paths"
)

func TestSaveRefusesSymlinks(t *testing.T) {
	t.Run("state.json is a symlink", func(t *testing.T) {
		dir := t.TempDir()
		victim := filepath.Join(t.TempDir(), "victim")
		if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "state.json")
		if err := os.Symlink(victim, path); err != nil {
			t.Fatal(err)
		}
		SetStatePathForTest(path)
		if err := Save(&State{}); err == nil {
			t.Error("Save succeeded through a symlink")
		}
		if got, _ := os.ReadFile(victim); string(got) != "original" {
			t.Errorf("victim modified: %q", got)
		}
	})
	t.Run("runtime dir is a symlink", func(t *testing.T) {
		outside := t.TempDir()
		rt := filepath.Join(t.TempDir(), ".agentctl")
		if err := os.Symlink(outside, rt); err != nil {
			t.Fatal(err)
		}
		SetStatePathForTest(filepath.Join(rt, "state.json"))
		if err := Save(&State{}); err == nil {
			t.Error("Save succeeded through a symlinked dir")
		}
		if _, err := os.Lstat(filepath.Join(outside, "state.json")); err == nil {
			t.Error("state.json created through symlinked dir")
		}
	})
}

func TestSaveMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	// An existing world-readable file from an older version is tightened.
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetStatePathForTest(path)
	if err := Save(&State{}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestInitFallbackIsPerUserPrivateDir(t *testing.T) {
	prev := FilePath()
	t.Cleanup(func() { SetStatePathForTest(prev) })
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)

	Init()
	got := FilePath()
	if got == "/tmp/agentctl-state.json" || !strings.HasPrefix(got, filepath.Clean(tmp)) {
		t.Fatalf("FilePath = %q, want under per-user dir in %s", got, tmp)
	}
	fi, err := os.Lstat(filepath.Dir(got))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", fi.Mode().Perm())
	}
}

func TestClearDoesNotDeleteThroughSymlinkedDir(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "state.json")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := filepath.Join(t.TempDir(), ".agentctl")
	if err := os.Symlink(outside, rt); err != nil {
		t.Fatal(err)
	}
	SetStatePathForTest(filepath.Join(rt, "state.json"))
	if err := Clear(); err == nil {
		t.Error("Clear succeeded through a symlinked dir")
	}
	if _, err := os.Lstat(victim); err != nil {
		t.Errorf("victim deleted: %v", err)
	}
}
