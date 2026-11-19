package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/detach"
	"github.com/blsmth/agentctl/internal/paths"
)

func captureSpawn(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := spawnReconcileFn
	spawnReconcileFn = func(rd string) error { got = append(got, rd); return nil }
	t.Cleanup(func() { spawnReconcileFn = old })
	return &got
}

func TestSpawnReconcilePinsRuntimeDir(t *testing.T) {
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	paths.Init(t.TempDir())
	got := captureSpawn(t)
	SpawnReconcile()
	if len(*got) != 1 || (*got)[0] != paths.RuntimeDir() {
		t.Fatalf("spawn calls = %v, want one with %q", *got, paths.RuntimeDir())
	}
}

func TestSpawnReconcileNoRuntimeDir(t *testing.T) {
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	got := captureSpawn(t)
	SpawnReconcile()
	if len(*got) != 0 {
		t.Fatalf("spawned with no runtime dir: %v", *got)
	}
}

// KillWindow spawns a reconcile after the kill succeeds, not when it fails.
// It runs against a private tmux server (TMUX_TMPDIR), never the user's.
func TestKillWindowSpawnsReconcile(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Setenv("TMUX", "")
	if out, err := exec.Command("tmux", "-f", "/dev/null", "new-session", "-d", "-s", "kwtest", "sleep 600").CombinedOutput(); err != nil {
		t.Skipf("cannot start private tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-server").Run() })
	idOut, err := exec.Command("tmux", "new-window", "-d", "-t", "kwtest", "-P", "-F", "#{window_id}", "sleep 600").Output()
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(idOut))

	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	paths.Init(filepath.Join(t.TempDir(), "proj"))
	oldSession := SessionName
	SessionName = "kwtest"
	t.Cleanup(func() { SessionName = oldSession })
	got := captureSpawn(t)

	if err := KillWindow("@999"); err == nil {
		t.Fatal("KillWindow of a missing window succeeded")
	}
	if len(*got) != 0 {
		t.Fatalf("reconcile spawned for a failed kill: %v", *got)
	}
	if err := KillWindow(id); err != nil {
		t.Fatalf("KillWindow(%s): %v", id, err)
	}
	if len(*got) != 1 {
		t.Fatalf("spawns after successful kill = %d, want 1", len(*got))
	}
}

// KillWindowIn kills a window in a named session and spawns a reconcile
// pinned to that session's runtime dir, never the caller's.
func TestKillWindowInSpawnsPinnedReconcile(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("TMUX_TMPDIR", shortTempDir(t))
	t.Setenv("TMUX", "")
	if out, err := exec.Command("tmux", "-f", "/dev/null", "new-session", "-d", "-s", "kwin", "sleep 600").CombinedOutput(); err != nil {
		t.Skipf("cannot start private tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-server").Run() })
	idOut, err := exec.Command("tmux", "new-window", "-d", "-t", "kwin", "-P", "-F", "#{window_id}", "sleep 600").Output()
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(idOut))
	oldSession := SessionName
	SessionName = "some-other-session"
	t.Cleanup(func() { SessionName = oldSession })
	got := captureSpawn(t)
	rd := filepath.Join(t.TempDir(), "child", ".agentctl")

	if err := KillWindowIn("kwin", rd, "@999"); err == nil {
		t.Fatal("KillWindowIn of a missing window succeeded")
	}
	if len(*got) != 0 {
		t.Fatalf("reconcile spawned for a failed kill: %v", *got)
	}
	if err := KillWindowIn("kwin", rd, id); err != nil {
		t.Fatalf("KillWindowIn(%s): %v", id, err)
	}
	if len(*got) != 1 || (*got)[0] != rd {
		t.Fatalf("spawns = %v, want one pinned to %q", *got, rd)
	}
}

// shortTempDir returns a temp dir short enough for a tmux socket path (the
// macOS t.TempDir is too long for sun_path).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "kw")
	if err != nil {
		t.Skipf("no short temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestSessionHookArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		hook string
		cmd  string
	}{
		{"resized", chromeSizeHookArgs("s", "client-resized", "/bin/agentctl", "/p/.agentctl"), "client-resized", "enforce-chrome"},
		{"attached", chromeSizeHookArgs("s", "client-attached", "/bin/agentctl", "/p/.agentctl"), "client-attached", "enforce-chrome"},
		{"session-changed", chromeSizeHookArgs("s", "client-session-changed", "/bin/agentctl", "/p/.agentctl"), "client-session-changed", "enforce-chrome"},
		{"unlinked", reconcileHookArgs("s", "/bin/agentctl", "/p/.agentctl"), "window-unlinked", "reconcile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.args[0] != "set-hook" || tc.args[1] != "-t" || tc.args[2] != "=s:" || tc.args[3] != tc.hook {
				t.Fatalf("not a session-scoped %s hook: %v", tc.hook, tc.args)
			}
			run := tc.args[4]
			for _, want := range []string{"run-shell -b", "AGENTCTL_RUNTIME_DIR=", "/p/.agentctl", "/bin/agentctl", tc.cmd} {
				if !strings.Contains(run, want) {
					t.Errorf("hook command %q missing %q", run, want)
				}
			}
		})
	}
}

// TestSpawnReconcileStripsWindowAccount: reconcile claims every gone window of
// the project and can launch improve sessions, so it must not run under the
// account of the window that spawned it.
func TestSpawnReconcileStripsWindowAccount(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/accounts/work")
	t.Setenv("AGENTCTL_ACCOUNT", "work")
	t.Setenv("CLAUDE_CODE_SUBPROCESS_ENV_SCRUB", "1")
	var gotArgs, gotEnv []string
	t.Cleanup(detach.SetStartSelfForTest(func(args, env []string) error {
		gotArgs, gotEnv = args, env
		return nil
	}))
	rd := t.TempDir()
	if err := spawnReconcileFn(rd); err != nil {
		t.Fatal(err)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "reconcile" {
		t.Fatalf("args = %q", gotArgs)
	}
	pinned := false
	for _, e := range gotEnv {
		for _, name := range []string{"CLAUDE_CONFIG_DIR=", "AGENTCTL_ACCOUNT=", "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB="} {
			if strings.HasPrefix(e, name) {
				t.Errorf("child env keeps %s", e)
			}
		}
		pinned = pinned || e == "AGENTCTL_RUNTIME_DIR="+rd
	}
	if !pinned {
		t.Errorf("child env lacks AGENTCTL_RUNTIME_DIR=%s", rd)
	}
}
