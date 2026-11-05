package pidfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteRefusesSymlinkedPidfile(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "monitor.pid")); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, "monitor.pid"); err == nil {
		t.Error("Write succeeded through a symlink")
	}
	if got, _ := os.ReadFile(victim); string(got) != "original" {
		t.Errorf("victim modified: %q", got)
	}
}

func TestWriteRefusesSymlinkedDir(t *testing.T) {
	outside := t.TempDir()
	dir := filepath.Join(t.TempDir(), ".agentctl")
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, "monitor.pid"); err == nil {
		t.Error("Write succeeded through a symlinked dir")
	}
	if _, err := os.Lstat(filepath.Join(outside, "monitor.pid")); err == nil {
		t.Error("pidfile created through symlinked dir")
	}
}

func TestWriteMode(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, "monitor.pid"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(dir, "monitor.pid"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestRemoveDoesNotDeleteThroughSymlinkedDir(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "monitor.pid")
	if err := os.WriteFile(victim, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), ".agentctl")
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	Remove(dir, "monitor.pid")
	if _, err := os.Lstat(victim); err != nil {
		t.Errorf("victim deleted: %v", err)
	}
}
