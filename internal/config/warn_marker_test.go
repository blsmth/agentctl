package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const staleProject = `{"notify_cmd":["x"],"auto_poll":true}`

var logged int

func captureLog(t *testing.T) {
	t.Helper()
	logged = 0
	old := warnLog
	warnLog = func(format string, args ...any) {
		if strings.HasPrefix(format, "%s") && strings.HasPrefix(fmt.Sprintf(format, args...), "config: ignoring ") {
			logged++
		}
	}
	t.Cleanup(func() { warnLog = old })
}

func countIgnoring(t *testing.T) int { t.Helper(); return logged }

func TestIgnoredKeysWarnOncePerContents(t *testing.T) {
	tests := []struct {
		name    string
		second  string
		wantLog int
	}{
		{"same contents", staleProject, 1},
		{"changed contents", `{"notify_cmd":["y"],"auto_poll":true}`, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			captureLog(t)
			dir := t.TempDir()
			gp := filepath.Join(dir, "global.json")
			pp := filepath.Join(dir, "rt", "config.json")
			mustMkdir(t, filepath.Dir(pp))
			mustWrite(t, gp, []byte(`{}`))
			mustWrite(t, pp, []byte(staleProject))
			setTestPaths(t, pp, gp)
			resetWarned()
			Load()
			Load() // same process: warnedKeys
			resetWarned()
			mustWrite(t, pp, []byte(tt.second))
			cfg := Load()
			if cfg.NotifyCmd != nil || cfg.AutoPoll {
				t.Fatalf("keys not stripped: %+v", cfg)
			}
			if got := countIgnoring(t); got != tt.wantLog {
				t.Errorf("logged %d times, want %d", got, tt.wantLog)
			}
		})
	}
}

func TestIgnoredKeysUnwritableMarkerStillLogsAndStrips(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores dir perms")
	}
	captureLog(t)
	dir := t.TempDir()
	gp := filepath.Join(dir, "global.json")
	rt := filepath.Join(dir, "rt")
	pp := filepath.Join(rt, "config.json")
	mustMkdir(t, rt)
	mustWrite(t, gp, []byte(`{}`))
	mustWrite(t, pp, []byte(staleProject))
	if err := os.Chmod(rt, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(rt, 0o700) })
	setTestPaths(t, pp, gp)
	for i := 0; i < 2; i++ {
		resetWarned()
		cfg := Load()
		if cfg.NotifyCmd != nil || cfg.AutoPoll {
			t.Fatal("keys not stripped")
		}
	}
	if got := countIgnoring(t); got != 2 {
		t.Errorf("logged %d times, want 2 (fail loud)", got)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
