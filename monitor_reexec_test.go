package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExeChanged(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour).Unix()
	start := exeIdentity{Path: "/opt/bin/agentctl", Dev: 1, Ino: 10, Size: 100, MtimeUnix: old}
	cur := func(f func(*exeIdentity)) exeIdentity {
		c := start
		c.Mode = 0o755
		f(&c)
		return c
	}
	tests := []struct {
		name string
		cur  exeIdentity
		want bool
	}{
		{"same identity", cur(func(*exeIdentity) {}), false},
		{"different inode", cur(func(c *exeIdentity) { c.Ino = 11 }), true},
		{"different mtime", cur(func(c *exeIdentity) { c.MtimeUnix = old + 60 }), true},
		{"younger than 1s", cur(func(c *exeIdentity) {
			c.MtimeUnix = now.Unix()
			c.MtimeNsec = int64(500 * time.Millisecond)
			c.Ino = 11
		}), false},
		{"ephemeral path", cur(func(c *exeIdentity) { c.Path = filepath.Join(os.TempDir(), "agentctl"); c.Ino = 11 }), false},
		{"not executable", cur(func(c *exeIdentity) { c.Mode = 0o644; c.Ino = 11 }), false},
		{"not regular", cur(func(c *exeIdentity) { c.Mode = fs.ModeDir | 0o755; c.Ino = 11 }), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exeChanged(start, tt.cur, now); got != tt.want {
				t.Errorf("exeChanged = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMaybeReexec(t *testing.T) {
	dir := t.TempDir()
	_ = dir
	// Use a non-temp path is impossible in a test, so only verify the
	// no-op paths: disabled and missing file.
	var calls int
	m := &monitorModel{execFn: func(string, []string, []string) error { calls++; return errors.New("x") }}
	m.maybeReexec(time.Now())
	m.exeStart = exeIdentity{Path: filepath.Join(dir, "missing")}
	m.maybeReexec(time.Now())
	if calls != 0 {
		t.Errorf("exec called %d times, want 0", calls)
	}
}
