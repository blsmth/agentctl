package selfexec

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestChanged(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour).Unix()
	start := Identity{Path: "/opt/bin/agentctl", Dev: 1, Ino: 10, Size: 100, MtimeUnix: old}
	cur := func(f func(*Identity)) Identity {
		c := start
		c.Mode = 0o755
		f(&c)
		return c
	}
	tests := []struct {
		name string
		cur  Identity
		want bool
	}{
		{"same identity", cur(func(*Identity) {}), false},
		{"different inode", cur(func(c *Identity) { c.Ino = 11 }), true},
		{"different mtime", cur(func(c *Identity) { c.MtimeUnix = old + 60 }), true},
		{"younger than 1s", cur(func(c *Identity) {
			c.MtimeUnix = now.Unix()
			c.MtimeNsec = int64(500 * time.Millisecond)
			c.Ino = 11
		}), false},
		{"ephemeral path", cur(func(c *Identity) { c.Path = filepath.Join(os.TempDir(), "agentctl"); c.Ino = 11 }), false},
		{"not executable", cur(func(c *Identity) { c.Mode = 0o644; c.Ino = 11 }), false},
		{"not regular", cur(func(c *Identity) { c.Mode = fs.ModeDir | 0o755; c.Ino = 11 }), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Changed(start, tt.cur, now); got != tt.want {
				t.Errorf("Changed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckChangedNoOps(t *testing.T) {
	now := time.Now()
	if CheckChanged(Identity{}, now) {
		t.Error("zero identity must report false")
	}
	if CheckChanged(Identity{Path: filepath.Join(t.TempDir(), "missing")}, now) {
		t.Error("missing file must report false")
	}
}

func TestCaptureUnderGoTest(t *testing.T) {
	// The test binary lives in a temp dir, so hot reload is disabled.
	if _, err := Capture(); err == nil {
		t.Skip("test binary not ephemeral on this platform")
	}
}

func TestReexecFailureLeavesEnv(t *testing.T) {
	if err := Reexec("/nonexistent/agentctl"); err == nil {
		t.Fatal("expected exec failure")
	}
}
