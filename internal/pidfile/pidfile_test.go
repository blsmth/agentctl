package pidfile_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/pidfile"
)

func TestWriteAndRemove(t *testing.T) {
	dir := t.TempDir()
	const name = "test.pid"

	if err := pidfile.Write(dir, name); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("ReadFile after Write: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse PID %q: %v", string(data), err)
	}
	if pid != os.Getpid() {
		t.Errorf("written PID = %d, want %d", pid, os.Getpid())
	}

	pidfile.Remove(dir, name)
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Errorf("PID file still exists after Remove")
	}
}

func TestWrite_CreatesDir(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "nested", "dir")

	if err := pidfile.Write(dir, "test.pid"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "test.pid")); err != nil {
		t.Errorf("PID file not created: %v", err)
	}
}

func TestRemove_MissingFile(t *testing.T) {
	// Remove of a non-existent file must not panic.
	pidfile.Remove(t.TempDir(), "does-not-exist.pid")
}

func TestKill_MissingFile(t *testing.T) {
	// Kill with no PID file must not panic.
	pidfile.Kill(t.TempDir(), "does-not-exist.pid")
}

func TestKill_InvalidPID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.pid"), []byte("not-a-pid\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Must not panic.
	pidfile.Kill(dir, "bad.pid")
}

func TestKill_StaleProcess(t *testing.T) {
	// Write a PID that will never exist (PID 1 is init on Linux / launchd on macOS;
	// we can't kill it, but Kill must not panic even if Signal returns an error).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stale.pid"), []byte("99999999\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Must not panic.
	pidfile.Kill(dir, "stale.pid")
}

func TestValidate(t *testing.T) {
	t.Run("missing file returns false", func(t *testing.T) {
		dir := t.TempDir()
		if pidfile.Validate(dir, "monitor.pid") {
			t.Error("Validate() = true for missing file, want false")
		}
	})

	t.Run("invalid PID content returns false", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "bad.pid"), []byte("not-a-pid\n"), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if pidfile.Validate(dir, "bad.pid") {
			t.Error("Validate() = true for bad PID, want false")
		}
	})

	t.Run("stale PID returns false", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "stale.pid"), []byte("99999999\n"), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if pidfile.Validate(dir, "stale.pid") {
			t.Error("Validate() = true for stale PID, want false")
		}
	})

	t.Run("own PID returns false", func(t *testing.T) {
		dir := t.TempDir()
		ownPID := strconv.Itoa(os.Getpid())
		if err := os.WriteFile(filepath.Join(dir, "self.pid"), []byte(ownPID+"\n"), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		// Validate skips the current process's own PID.
		if pidfile.Validate(dir, "self.pid") {
			t.Error("Validate() = true for own PID, want false")
		}
	})
}
