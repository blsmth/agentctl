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
			os.MkdirAll(filepath.Dir(pp), 0o700)
			os.WriteFile(gp, []byte(`{}`), 0o600)
			os.WriteFile(pp, []byte(staleProject), 0o600)
			setTestPaths(t, pp, gp)
			resetWarned()
			Load()
			Load() // same process: warnedKeys
			resetWarned()
			os.WriteFile(pp, []byte(tt.second), 0o600)
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
	os.MkdirAll(rt, 0o700)
	os.WriteFile(gp, []byte(`{}`), 0o600)
	os.WriteFile(pp, []byte(staleProject), 0o600)
	os.Chmod(rt, 0o500)
	t.Cleanup(func() { os.Chmod(rt, 0o700) })
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
