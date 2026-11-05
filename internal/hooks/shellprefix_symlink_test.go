package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/paths"
)

// shellPrefixEnv isolates HOME/XDG and points paths at project.
func shellPrefixEnv(t *testing.T, project string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	t.Setenv("AGENTCTL_INSTANCE", "")
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	paths.Init(project)
}

func TestEnsureShellPrefixScript_RefusesSymlinks(t *testing.T) {
	t.Run("script is a symlink", func(t *testing.T) {
		project := t.TempDir()
		shellPrefixEnv(t, project)
		victim := filepath.Join(t.TempDir(), ".zshenv")
		if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(paths.RuntimeDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(paths.RuntimeDir(), "shell-prefix.sh")); err != nil {
			t.Fatal(err)
		}
		if err := EnsureShellPrefixScript(); err == nil {
			t.Error("EnsureShellPrefixScript wrote through a symlink")
		}
		if got, _ := os.ReadFile(victim); string(got) != "original" {
			t.Errorf("victim modified: %q", got)
		}
	})
	t.Run("runtime dir is a symlink", func(t *testing.T) {
		project := t.TempDir()
		shellPrefixEnv(t, project)
		outside := t.TempDir()
		if err := os.Symlink(outside, paths.RuntimeDir()); err != nil {
			t.Fatal(err)
		}
		if err := EnsureShellPrefixScript(); err == nil {
			t.Error("EnsureShellPrefixScript wrote through a symlinked runtime dir")
		}
		if _, err := os.Lstat(filepath.Join(outside, "shell-prefix.sh")); err == nil {
			t.Error("script created through symlinked dir")
		}
	})
	t.Run("normal write is 0700", func(t *testing.T) {
		project := t.TempDir()
		shellPrefixEnv(t, project)
		if err := EnsureShellPrefixScript(); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(filepath.Join(paths.RuntimeDir(), "shell-prefix.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("mode = %v, want 0700", fi.Mode().Perm())
		}
	})
}

func TestAppendFileRefusesSymlink(t *testing.T) {
	victim := filepath.Join(t.TempDir(), ".zshenv")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "agent.md")
	if err := os.Symlink(victim, p); err != nil {
		t.Fatal(err)
	}
	if err := appendFile(p, []byte("x")); err == nil {
		t.Error("appendFile wrote through a symlink")
	}
	if got, _ := os.ReadFile(victim); string(got) != "original" {
		t.Errorf("victim modified: %q", got)
	}
}
