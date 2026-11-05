package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDataStateDirIn(t *testing.T) {
	abs := t.TempDir()
	tests := []struct {
		name, env, val string
		fn             func(string) string
		want           string
	}{
		{"data unset", "XDG_DATA_HOME", "", DataDirIn, "/h/.local/share/agentctl"},
		{"data abs", "XDG_DATA_HOME", abs, DataDirIn, filepath.Join(abs, "agentctl")},
		{"data relative ignored", "XDG_DATA_HOME", "rel/x", DataDirIn, "/h/.local/share/agentctl"},
		{"state unset", "XDG_STATE_HOME", "", StateDirIn, "/h/.local/state/agentctl"},
		{"state abs", "XDG_STATE_HOME", abs, StateDirIn, filepath.Join(abs, "agentctl")},
		{"state relative ignored", "XDG_STATE_HOME", "rel/x", StateDirIn, "/h/.local/state/agentctl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.env, tt.val)
			if got := tt.fn("/h"); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDataStateDirUsesHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	if got, err := DataDir(); err != nil || got != filepath.Join(home, ".local", "share", "agentctl") {
		t.Errorf("DataDir = %q, %v", got, err)
	}
	if got, err := StateDir(); err != nil || got != filepath.Join(home, ".local", "state", "agentctl") {
		t.Errorf("StateDir = %q, %v", got, err)
	}
}

func TestMigrateLegacy(t *testing.T) {
	mk := func(t *testing.T, p string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "f"), []byte(filepath.Base(p)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(p string) bool { _, err := os.Stat(filepath.Join(p, "f")); return err == nil }

	t.Run("old only moved", func(t *testing.T) {
		r := t.TempDir()
		o, n := filepath.Join(r, "old"), filepath.Join(r, "sub", "new")
		mk(t, o)
		if got := MigrateLegacy(o, n); got != n || !exists(n) || exists(o) {
			t.Errorf("got %q new=%v old=%v", got, exists(n), exists(o))
		}
		if fi, _ := os.Stat(filepath.Dir(n)); fi.Mode().Perm() != 0o700 {
			t.Errorf("parent mode %v", fi.Mode().Perm())
		}
	})
	t.Run("new only untouched", func(t *testing.T) {
		r := t.TempDir()
		o, n := filepath.Join(r, "old"), filepath.Join(r, "new")
		mk(t, n)
		if got := MigrateLegacy(o, n); got != n || !exists(n) {
			t.Errorf("got %q", got)
		}
	})
	t.Run("both exist", func(t *testing.T) {
		r := t.TempDir()
		o, n := filepath.Join(r, "old"), filepath.Join(r, "new")
		mk(t, o)
		mk(t, n)
		b, _ := os.ReadFile(filepath.Join(n, "f"))
		if got := MigrateLegacy(o, n); got != n || !exists(o) || string(b) != "new" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("neither", func(t *testing.T) {
		r := t.TempDir()
		o, n := filepath.Join(r, "old"), filepath.Join(r, "new")
		if got := MigrateLegacy(o, n); got != n {
			t.Errorf("got %q", got)
		}
		if _, err := os.Stat(filepath.Dir(n)); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(n); err == nil {
			t.Error("new should not be created")
		}
	})
	t.Run("rename failure returns old", func(t *testing.T) {
		r := t.TempDir()
		o := filepath.Join(r, "old")
		mk(t, o)
		// Parent of new is a regular file, so MkdirAll fails.
		blocker := filepath.Join(r, "blocker")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		n := filepath.Join(blocker, "new")
		if got := MigrateLegacy(o, n); got != o || !exists(o) {
			t.Errorf("got %q", got)
		}
	})
}
