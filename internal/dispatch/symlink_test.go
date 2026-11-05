package dispatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/paths"
)

// isolateEnv points HOME and XDG_CONFIG_HOME at a fresh temp dir and clears
// the agentctl pins this session may have set, so nothing here can touch the
// real home directory or the real repository's .agentctl/. Returns the fake
// home.
func isolateEnv(t *testing.T) string {
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
	return home
}

// gitIn runs git in dir with the user's git config ignored.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func readOrFatal(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPoC_SymlinkedSettingsDoesNotWriteThroughToHome reproduces the reported
// exploit shape in a temp dir: a cloned repo commits
// .agentctl/settings/go-engineer.json as a symlink into $HOME/.zshenv, a
// blocked_commands entry carrying a $(...) payload, and a repo-root
// .claude/settings.local.json mentioning "agentctl hook-" to suppress the
// hooks key. Writing the agent settings must leave the symlink target
// untouched, the startup check must refuse the runtime dir, and the
// committed settings.local.json must not be trusted as a hooks source.
func TestPoC_SymlinkedSettingsDoesNotWriteThroughToHome(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := isolateEnv(t)
	zshenv := filepath.Join(home, ".zshenv")
	const original = "# user's real zshenv\n"
	if err := os.WriteFile(zshenv, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(t.TempDir(), "evil")
	settingsDir := filepath.Join(repo, ".agentctl", "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(zshenv, filepath.Join(settingsDir, "go-engineer.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.local.json"),
		[]byte(`{"hooks":{"x":"agentctl hook-"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "add", "-f", ".agentctl", ".claude")

	paths.Init(repo)
	ac := config.AgentConfig{BlockedCommands: []string{"$(touch " + filepath.Join(home, "PWNED") + ")"}}

	if err := paths.CheckRuntimeDir(true); err == nil {
		t.Error("CheckRuntimeDir accepted a runtime dir with a tracked symlink")
	}
	if ancestorHasAgentctlHooks(repo, repo) {
		t.Error("a repo-tracked .claude/settings.local.json was trusted as the hooks source")
	}

	if got := WriteAgentSettings("go-engineer", ac, repo); got != "" {
		t.Errorf("WriteAgentSettings returned %q, want refusal", got)
	}
	if got := readOrFatal(t, zshenv); got != original {
		t.Fatalf("~/.zshenv was modified through the symlink:\n%s", got)
	}
	if _, err := os.Lstat(filepath.Join(home, "PWNED")); err == nil {
		t.Fatal("payload ran")
	}
}

func TestWriteAgentSettings_Symlinks(t *testing.T) {
	tests := []struct {
		name string
		// plant builds the attack in runtimeDir and returns the victim file.
		plant func(t *testing.T, runtimeDir, outside string) string
	}{
		{
			name: "settings file is a symlink",
			plant: func(t *testing.T, runtimeDir, outside string) string {
				victim := filepath.Join(outside, "victim")
				if err := os.MkdirAll(filepath.Join(runtimeDir, "settings"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(victim, filepath.Join(runtimeDir, "settings", "a.json")); err != nil {
					t.Fatal(err)
				}
				return victim
			},
		},
		{
			name: "settings dir is a symlink",
			plant: func(t *testing.T, runtimeDir, outside string) string {
				if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(runtimeDir, "settings")); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(outside, "a.json")
			},
		},
		{
			name: "runtime dir is a symlink",
			plant: func(t *testing.T, runtimeDir, outside string) string {
				if err := os.MkdirAll(filepath.Join(outside, "settings"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, runtimeDir); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(outside, "settings", "a.json")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateEnv(t)
			project := t.TempDir()
			outside := t.TempDir()
			victim := tt.plant(t, filepath.Join(project, ".agentctl"), outside)
			if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
				t.Fatal(err)
			}
			paths.Init(project)

			if got := WriteAgentSettings("a", config.AgentConfig{}, project); got != "" {
				t.Errorf("WriteAgentSettings = %q, want refusal", got)
			}
			if got := readOrFatal(t, victim); got != "original" {
				t.Errorf("victim modified: %q", got)
			}
		})
	}
}

func TestWriteAgentSettings_NormalWriteIsPrivate(t *testing.T) {
	isolateEnv(t)
	project := t.TempDir()
	paths.Init(project)

	path := WriteAgentSettings("a", config.AgentConfig{}, project)
	if path == "" {
		t.Fatal("WriteAgentSettings failed")
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestWriteWorktreeSettings_Symlinks(t *testing.T) {
	t.Run(".claude is a symlink", func(t *testing.T) {
		isolateEnv(t)
		wt := t.TempDir()
		outside := t.TempDir()
		victim := filepath.Join(outside, "settings.local.json")
		if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(wt, ".claude")); err != nil {
			t.Fatal(err)
		}
		if err := WriteWorktreeSettings(wt, filepath.Dir(wt), config.AgentConfig{}); err == nil {
			t.Error("WriteWorktreeSettings wrote through a symlinked .claude")
		}
		if got := readOrFatal(t, victim); got != "original" {
			t.Errorf("victim modified: %q", got)
		}
	})
	t.Run("settings.local.json is a symlink", func(t *testing.T) {
		isolateEnv(t)
		wt := t.TempDir()
		victim := filepath.Join(t.TempDir(), "victim")
		if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(wt, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(wt, ".claude", "settings.local.json")); err != nil {
			t.Fatal(err)
		}
		if err := WriteWorktreeSettings(wt, filepath.Dir(wt), config.AgentConfig{}); err == nil {
			t.Error("WriteWorktreeSettings wrote through a symlinked settings file")
		}
		if got := readOrFatal(t, victim); got != "original" {
			t.Errorf("victim modified: %q", got)
		}
	})
}

func TestIsUserOwnedSettings(t *testing.T) {
	const hooksJSON = `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"agentctl hook-pre-tool"}]}]}}`
	notRepo := func(...string) ([]byte, error) { return nil, exec.ErrNotFound }
	tracked := func(...string) ([]byte, error) { return []byte(".claude/settings.local.json\x00"), nil }
	untracked := func(...string) ([]byte, error) { return nil, nil }

	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		run   func(...string) ([]byte, error)
		want  bool
	}{
		{name: "untracked regular file", setup: plainSettings(hooksJSON), run: untracked, want: true},
		{name: "not a git repo", setup: plainSettings(hooksJSON), run: notRepo, want: true},
		{name: "tracked by git", setup: plainSettings(hooksJSON), run: tracked, want: false},
		{
			name: "file is a symlink",
			setup: func(t *testing.T, dir string) {
				real := filepath.Join(t.TempDir(), "real.json")
				if err := os.WriteFile(real, []byte(hooksJSON), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, filepath.Join(dir, ".claude", "settings.local.json")); err != nil {
					t.Fatal(err)
				}
			},
			run: untracked, want: false,
		},
		{
			name: ".claude is a symlink",
			setup: func(t *testing.T, dir string) {
				real := t.TempDir()
				if err := os.WriteFile(filepath.Join(real, "settings.local.json"), []byte(hooksJSON), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, filepath.Join(dir, ".claude")); err != nil {
					t.Fatal(err)
				}
			},
			run: untracked, want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := gitRunner
			gitRunner = tt.run
			t.Cleanup(func() { gitRunner = prev })
			dir := t.TempDir()
			tt.setup(t, dir)
			if got := projectHasAgentctlHooks(dir); got != tt.want {
				t.Errorf("projectHasAgentctlHooks = %v, want %v", got, tt.want)
			}
		})
	}
}

// plainSettings returns a setup func writing dir/.claude/settings.local.json.
func plainSettings(content string) func(t *testing.T, dir string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestLaunch_AbortsWhenSettingsRefused: a refused settings write must abort
// the dispatch before any tmux window is created, not launch an agent
// without agentctl's hooks.
func TestLaunch_AbortsWhenSettingsRefused(t *testing.T) {
	isolateEnv(t)
	project := t.TempDir()
	rt := filepath.Join(project, ".agentctl")
	if err := os.MkdirAll(rt, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(rt, "settings")); err != nil {
		t.Fatal(err)
	}
	paths.Init(project)

	_, err := Launch(Request{AgentName: "test", Mode: ModeBare, WorkDir: project})
	if err == nil || !strings.Contains(err.Error(), "refusing to launch") {
		t.Fatalf("Launch err = %v, want refusal", err)
	}
}

// TestLaunch_AbortsWhenWorktreeClaudeIsSymlink: a branch that commits
// .claude as a symlink makes WriteWorktreeSettings refuse; dispatch must
// stop rather than launch the worker hookless.
func TestLaunch_AbortsWhenWorktreeClaudeIsSymlink(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolateEnv(t)
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "config", "user.email", "t@example.com")
	gitIn(t, repo, "config", "user.name", "T")
	if err := os.Symlink(t.TempDir(), filepath.Join(repo, ".claude")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", ".claude")
	gitIn(t, repo, "commit", "-q", "-m", "evil")
	paths.Init(repo)

	useWT := true
	_, err := Launch(Request{AgentName: "test", Mode: ModeAdhoc, WorkDir: repo, UseWorktree: &useWT, CustomPrompt: "hi"})
	if err == nil || !strings.Contains(err.Error(), "worktree settings") {
		t.Fatalf("Launch err = %v, want worktree settings refusal", err)
	}
}
