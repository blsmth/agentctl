package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInit(t *testing.T) {
	t.Run("sets runtime dir", func(t *testing.T) {
		ResetForTest()
		// AGENTCTL_RUNTIME_DIR is inherited from any agentctl-managed tmux
		// window (agentctl exports it into every window it creates). Pin it
		// explicitly so this test exercises the CWD-derived path regardless
		// of what environment it happens to run in.
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")
		dir := t.TempDir()
		Init(dir)

		want := filepath.Join(dir, ".agentctl")
		if got := RuntimeDir(); got != want {
			t.Errorf("RuntimeDir() = %q, want %q", got, want)
		}
		// Init only resolves; creation is lazy.
		if _, err := os.Stat(want); err == nil {
			t.Errorf("Init created %q; creation must be lazy", want)
		}
		EnsureRuntimeDir()
		if _, err := os.Stat(want); err != nil {
			t.Errorf("RuntimeDir %q not created by EnsureRuntimeDir: %v", want, err)
		}
	})

	t.Run("subdirectories", func(t *testing.T) {
		ResetForTest()
		// See "sets runtime dir" above: pin against an inherited env var.
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")
		dir := t.TempDir()
		Init(dir)

		base := filepath.Join(dir, ".agentctl")
		tests := []struct {
			name string
			fn   func() string
			want string
		}{
			{"LogDir", LogDir, filepath.Join(base, "logs")},
			{"SessionLogDir", SessionLogDir, filepath.Join(base, "logs", "sessions")},
			{"WindowsDir", WindowsDir, filepath.Join(base, "windows")},
			{"SettingsDir", SettingsDir, filepath.Join(base, "settings")},
			{"InboxDir", InboxDir, filepath.Join(base, "inbox")},
			{"AuditDir", AuditDir, filepath.Join(base, "audit")},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := tt.fn(); got != tt.want {
					t.Errorf("%s() = %q, want %q", tt.name, got, tt.want)
				}
			})
		}
	})

	t.Run("DirName", func(t *testing.T) {
		ResetForTest()
		// See "sets runtime dir" above: pin against an inherited env var.
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")
		dir := filepath.Join(t.TempDir(), "my-project")
		os.MkdirAll(dir, 0o755) //nolint:errcheck
		Init(dir)

		if got := DirName(); got != "my-project" {
			t.Errorf("DirName() = %q, want %q", got, "my-project")
		}
	})

	t.Run("only first call takes effect", func(t *testing.T) {
		ResetForTest()
		// See "sets runtime dir" above: pin against an inherited env var.
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")
		dir1 := t.TempDir()
		dir2 := t.TempDir()
		Init(dir1)
		Init(dir2) // should be ignored

		want := filepath.Join(dir1, ".agentctl")
		if got := RuntimeDir(); got != want {
			t.Errorf("RuntimeDir() = %q after second Init, want %q", got, want)
		}
	})
}

func TestInit_AgentctlRuntimeDirEnv(t *testing.T) {
	t.Run("honors the env var as the full runtime dir path", func(t *testing.T) {
		ResetForTest()
		envDir := filepath.Join(t.TempDir(), "some-project", ".agentctl")
		t.Setenv("AGENTCTL_RUNTIME_DIR", envDir)

		// baseDir is deliberately a different, unrelated directory to prove
		// the env var wins over CWD.
		baseDir := t.TempDir()
		Init(baseDir)

		if got := RuntimeDir(); got != envDir {
			t.Errorf("RuntimeDir() = %q, want %q (env var value used as-is)", got, envDir)
		}
		EnsureRuntimeDir()
		if _, err := os.Stat(envDir); err != nil {
			t.Errorf("RuntimeDir %q not created: %v", envDir, err)
		}

		// baseDir/.agentctl must NOT be created when the env var wins.
		unwanted := filepath.Join(baseDir, ".agentctl")
		if _, err := os.Stat(unwanted); err == nil {
			t.Errorf("baseDir/.agentctl %q should not have been created when AGENTCTL_RUNTIME_DIR is set", unwanted)
		}
	})

	t.Run("ignores an empty env var and falls back to baseDir/.agentctl", func(t *testing.T) {
		ResetForTest()
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")

		baseDir := t.TempDir()
		Init(baseDir)

		want := filepath.Join(baseDir, ".agentctl")
		if got := RuntimeDir(); got != want {
			t.Errorf("RuntimeDir() = %q, want %q", got, want)
		}
	})

	t.Run("falls back to CWD-derived path when the env var is unset", func(t *testing.T) {
		ResetForTest()
		t.Setenv("HOME", t.TempDir()) // isolate from the real environment
		t.Setenv("XDG_CONFIG_HOME", "")
		// Explicitly clear rather than relying on the ambient environment:
		// AGENTCTL_RUNTIME_DIR is inherited from any agentctl-managed tmux
		// window, and this test's whole point is the no-env-var code path.
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")

		baseDir := t.TempDir()
		Init(baseDir)

		want := filepath.Join(baseDir, ".agentctl")
		if got := RuntimeDir(); got != want {
			t.Errorf("RuntimeDir() = %q, want %q", got, want)
		}
	})
}

// TestInitForSession covers the session-starting entry point, whose defining
// difference from Init is that an inherited AGENTCTL_RUNTIME_DIR only wins
// when it anchors a project containing baseDir.
func TestInitForSession(t *testing.T) {
	// project builds <root>/<name> plus its .agentctl dir and returns both.
	project := func(t *testing.T, root, name string) (projectRoot, rtDir string) {
		t.Helper()
		projectRoot = filepath.Join(root, name)
		rtDir = filepath.Join(projectRoot, ".agentctl")
		if err := os.MkdirAll(rtDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", rtDir, err)
		}
		return projectRoot, rtDir
	}

	t.Run("honors the env var when baseDir is the project root", func(t *testing.T) {
		ResetForTest()
		projectRoot, rtDir := project(t, t.TempDir(), "repo")
		t.Setenv("AGENTCTL_RUNTIME_DIR", rtDir)

		InitForSession(projectRoot)

		if got := RuntimeDir(); got != rtDir {
			t.Errorf("RuntimeDir() = %q, want %q", got, rtDir)
		}
	})

	t.Run("honors the env var when baseDir is a subdirectory of the project", func(t *testing.T) {
		ResetForTest()
		projectRoot, rtDir := project(t, t.TempDir(), "repo")
		t.Setenv("AGENTCTL_RUNTIME_DIR", rtDir)

		// A worktree lives at <project>/.worktrees/<branch>; a dispatched
		// agent launching from there still belongs to this project.
		worktree := filepath.Join(projectRoot, ".worktrees", "agent-issue-42")
		if err := os.MkdirAll(worktree, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", worktree, err)
		}

		InitForSession(worktree)

		if got := RuntimeDir(); got != rtDir {
			t.Errorf("RuntimeDir() = %q, want %q", got, rtDir)
		}
	})

	// The reported bug: sibling repos under an umbrella directory. Launching
	// from ~/src/acme/widgetco-infra while holding the agentctl-acme session's
	// runtime dir must start widgetco-infra's OWN session, not attach to acme's.
	t.Run("ignores a runtime dir inherited from a sibling project", func(t *testing.T) {
		ResetForTest()
		umbrella := t.TempDir()
		_, siblingRT := project(t, umbrella, "sibling-repo")
		targetRoot, _ := project(t, umbrella, "target-repo")
		t.Setenv("AGENTCTL_RUNTIME_DIR", siblingRT)

		InitForSession(targetRoot)

		want := filepath.Join(targetRoot, ".agentctl")
		if got := RuntimeDir(); got != want {
			t.Errorf("RuntimeDir() = %q, want %q", got, want)
		}
		if got := DirName(); got != "target-repo" {
			t.Errorf("DirName() = %q, want %q", got, "target-repo")
		}
	})

	t.Run("ignores a runtime dir inherited from the umbrella parent", func(t *testing.T) {
		ResetForTest()
		umbrella := t.TempDir()
		umbrellaRT := filepath.Join(umbrella, ".agentctl")
		if err := os.MkdirAll(umbrellaRT, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", umbrellaRT, err)
		}
		targetRoot, _ := project(t, umbrella, "target-repo")
		t.Setenv("AGENTCTL_RUNTIME_DIR", umbrellaRT)

		InitForSession(targetRoot)

		want := filepath.Join(targetRoot, ".agentctl")
		if got := RuntimeDir(); got != want {
			t.Errorf("RuntimeDir() = %q, want %q", got, want)
		}
	})

	// Init must NOT gain the ownership test: dispatch, hooks, chrome bars and
	// monitors all rely on the pin winning even when cwd disagrees.
	t.Run("Init still honors a foreign runtime dir unconditionally", func(t *testing.T) {
		ResetForTest()
		umbrella := t.TempDir()
		_, siblingRT := project(t, umbrella, "sibling-repo")
		targetRoot, _ := project(t, umbrella, "target-repo")
		t.Setenv("AGENTCTL_RUNTIME_DIR", siblingRT)

		Init(targetRoot)

		if got := RuntimeDir(); got != siblingRT {
			t.Errorf("RuntimeDir() = %q, want %q", got, siblingRT)
		}
	})

	t.Run("falls back to baseDir when the env var is empty", func(t *testing.T) {
		ResetForTest()
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")

		baseDir := t.TempDir()
		InitForSession(baseDir)

		want := filepath.Join(baseDir, ".agentctl")
		if got := RuntimeDir(); got != want {
			t.Errorf("RuntimeDir() = %q, want %q", got, want)
		}
	})
}

// TestNormalizeRuntimeDir covers repair of an AGENTCTL_RUNTIME_DIR that names
// a project root instead of the project's .agentctl directory.
func TestNormalizeRuntimeDir(t *testing.T) {
	tests := []struct {
		name string
		dir  string
		want string
	}{
		{
			name: "already a runtime dir is left alone",
			dir:  "/src/acme/repo/.agentctl",
			want: "/src/acme/repo/.agentctl",
		},
		{
			name: "a project root gains the missing element",
			dir:  "/src/acme/repo",
			want: "/src/acme/repo/.agentctl",
		},
		{
			name: "empty stays empty so a deliberate clear is preserved",
			dir:  "",
			want: "",
		},
		{
			name: "a trailing separator does not defeat the check",
			dir:  "/src/acme/repo/.agentctl/",
			want: "/src/acme/repo/.agentctl",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeRuntimeDir(tt.dir); got != tt.want {
				t.Errorf("normalizeRuntimeDir(%q) = %q, want %q", tt.dir, got, tt.want)
			}
		})
	}
}

// TestNormalizedRuntimeDirNamesTheRightSession is the regression for the
// hand-set value that started this investigation: a runtime dir pointing at a
// repo root inside an umbrella resolved DirName() to the UMBRELLA's basename,
// aiming every tmux call at a foreign session.
func TestNormalizedRuntimeDirNamesTheRightSession(t *testing.T) {
	ResetForTest()
	umbrella := filepath.Join(t.TempDir(), "acme")
	projectRoot := filepath.Join(umbrella, "acme-ops")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", projectRoot, err)
	}
	// Missing the ".agentctl" element, exactly as typed by hand.
	t.Setenv("AGENTCTL_RUNTIME_DIR", projectRoot)

	Init(projectRoot)

	want := filepath.Join(projectRoot, ".agentctl")
	if got := RuntimeDir(); got != want {
		t.Errorf("RuntimeDir() = %q, want %q", got, want)
	}
	if got := DirName(); got != "acme-ops" {
		t.Errorf("DirName() = %q, want %q (got the umbrella's basename?)", got, "acme-ops")
	}
	// State must not be scattered into the repo root.
	for _, stray := range []string{"logs", "windows", "audit", "inbox", "settings"} {
		if _, err := os.Stat(filepath.Join(projectRoot, stray)); err == nil {
			t.Errorf("%q was created in the project root; runtime state must stay under .agentctl", stray)
		}
	}
}

// TestInitForSessionOwnershipDiscriminator covers the "contained but is it its
// own project?" cases that separate an umbrella's sibling repos (own session)
// from a project's plain subdirectories (enclosing session).
func TestInitForSessionOwnershipDiscriminator(t *testing.T) {
	t.Run("a git-only sibling repo gets its own session", func(t *testing.T) {
		ResetForTest()
		umbrella := t.TempDir()
		umbrellaRT := filepath.Join(umbrella, ".agentctl")
		// The sibling has never been launched, so it has no .agentctl yet --
		// only .git marks it as a project. This is the common first-launch
		// case and must not fall through to the umbrella's runtime dir.
		sibling := filepath.Join(umbrella, "fresh-repo")
		for _, d := range []string{umbrellaRT, filepath.Join(sibling, ".git")} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q): %v", d, err)
			}
		}
		t.Setenv("AGENTCTL_RUNTIME_DIR", umbrellaRT)

		InitForSession(sibling)

		want := filepath.Join(sibling, ".agentctl")
		if got := RuntimeDir(); got != want {
			t.Errorf("RuntimeDir() = %q, want %q", got, want)
		}
		if got := DirName(); got != "fresh-repo" {
			t.Errorf("DirName() = %q, want %q", got, "fresh-repo")
		}
	})

	t.Run("a plain subdirectory joins the enclosing project", func(t *testing.T) {
		ResetForTest()
		projectRoot := t.TempDir()
		rtDir := filepath.Join(projectRoot, ".agentctl")
		// A non-project subdirectory: no .git, no .agentctl. Launching from
		// here must reuse the project's runtime dir rather than mint a
		// per-subdirectory session and a stray .agentctl.
		subdir := filepath.Join(projectRoot, "modules", "vpc")
		for _, d := range []string{rtDir, subdir, filepath.Join(projectRoot, ".git")} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q): %v", d, err)
			}
		}
		t.Setenv("AGENTCTL_RUNTIME_DIR", rtDir)

		InitForSession(subdir)

		if got := RuntimeDir(); got != rtDir {
			t.Errorf("RuntimeDir() = %q, want %q", got, rtDir)
		}
		if _, err := os.Stat(filepath.Join(subdir, ".agentctl")); err == nil {
			t.Error("a stray .agentctl was created in the subdirectory")
		}
	})

	t.Run("a directory outside the project is not owned", func(t *testing.T) {
		ResetForTest()
		root := t.TempDir()
		projectRT := filepath.Join(root, "project", ".agentctl")
		elsewhere := filepath.Join(root, "elsewhere")
		for _, d := range []string{projectRT, elsewhere} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q): %v", d, err)
			}
		}
		t.Setenv("AGENTCTL_RUNTIME_DIR", projectRT)

		InitForSession(elsewhere)

		want := filepath.Join(elsewhere, ".agentctl")
		if got := RuntimeDir(); got != want {
			t.Errorf("RuntimeDir() = %q, want %q", got, want)
		}
	})
}

func TestProjectDir(t *testing.T) {
	t.Run("is the parent of the runtime dir", func(t *testing.T) {
		ResetForTest()
		base := t.TempDir()
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")

		Init(base)

		if got := ProjectDir(); got != base {
			t.Errorf("ProjectDir() = %q, want %q", got, base)
		}
	})

	t.Run("follows the environment pin, not cwd", func(t *testing.T) {
		ResetForTest()
		root := t.TempDir()
		projectRoot := filepath.Join(root, "project")
		rtDir := filepath.Join(projectRoot, ".agentctl")
		worktree := filepath.Join(projectRoot, ".worktrees", "agent-go-engineer-issue-1")
		for _, d := range []string{rtDir, worktree} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q): %v", d, err)
			}
		}
		t.Setenv("AGENTCTL_RUNTIME_DIR", rtDir)

		Init(worktree)

		if got := ProjectDir(); got != projectRoot {
			t.Errorf("ProjectDir() = %q, want %q", got, projectRoot)
		}
	})

	t.Run("is empty before Init", func(t *testing.T) {
		ResetForTest()
		if got := ProjectDir(); got != "" {
			t.Errorf("ProjectDir() = %q, want %q", got, "")
		}
	})
}

// TestHonorCwdPin covers the relocation a keybinding-spawned agentctl performs
// on itself. The binding cannot pin a cwd (bind-key is server-global and tmux
// format-expands neither `run-shell -c` nor `display-popup -d`), so it sets
// CwdPinEnv and the process moves itself to the project root that the SESSION
// environment's AGENTCTL_RUNTIME_DIR identifies.
func TestHonorCwdPin(t *testing.T) {
	// chdir is process-global; restore it so later tests are unaffected.
	restoreCwd := func(t *testing.T) {
		t.Helper()
		orig, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd: %v", err)
		}
		t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck
	}

	t.Run("relocates a pinned process to the project root", func(t *testing.T) {
		restoreCwd(t)
		ResetForTest()
		root := t.TempDir()
		projectRoot := filepath.Join(root, "project")
		rtDir := filepath.Join(projectRoot, ".agentctl")
		worktree := filepath.Join(projectRoot, ".worktrees", "agent-go-engineer-issue-1")
		for _, d := range []string{rtDir, worktree} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q): %v", d, err)
			}
		}
		t.Setenv("AGENTCTL_RUNTIME_DIR", rtDir)
		t.Setenv(CwdPinEnv, "1")
		if err := os.Chdir(worktree); err != nil {
			t.Fatalf("Chdir(%q): %v", worktree, err)
		}

		Init(worktree)
		HonorCwdPin()

		got, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd: %v", err)
		}
		if resolvePath(got) != resolvePath(projectRoot) {
			t.Errorf("cwd = %q, want %q", got, projectRoot)
		}
	})

	t.Run("clears the pin so children never inherit it", func(t *testing.T) {
		restoreCwd(t)
		ResetForTest()
		base := t.TempDir()
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")
		t.Setenv(CwdPinEnv, "1")

		Init(base)
		HonorCwdPin()

		// A dispatched agent runs in a worktree deliberately; if it inherited
		// the pin, its own agentctl calls would yank it back to the repo root.
		if v := os.Getenv(CwdPinEnv); v != "" {
			t.Errorf("%s = %q after HonorCwdPin, want it unset", CwdPinEnv, v)
		}
	})

	t.Run("does nothing when the pin is absent", func(t *testing.T) {
		restoreCwd(t)
		ResetForTest()
		root := t.TempDir()
		projectRoot := filepath.Join(root, "project")
		rtDir := filepath.Join(projectRoot, ".agentctl")
		elsewhere := filepath.Join(root, "elsewhere")
		for _, d := range []string{rtDir, elsewhere} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q): %v", d, err)
			}
		}
		t.Setenv("AGENTCTL_RUNTIME_DIR", rtDir)
		os.Unsetenv(CwdPinEnv) //nolint:errcheck
		if err := os.Chdir(elsewhere); err != nil {
			t.Fatalf("Chdir(%q): %v", elsewhere, err)
		}

		Init(elsewhere)
		HonorCwdPin()

		got, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd: %v", err)
		}
		if resolvePath(got) != resolvePath(elsewhere) {
			t.Errorf("cwd = %q, want it left at %q", got, elsewhere)
		}
	})
}

func TestInitWritesSelfIgnoringGitignore(t *testing.T) {
	for _, branch := range []string{"default", "env"} {
		t.Run(branch+"/creates when absent", func(t *testing.T) {
			ResetForTest()
			dir := t.TempDir()
			want := filepath.Join(dir, ".agentctl")
			if branch == "env" {
				want = filepath.Join(dir, "elsewhere", ".agentctl")
				t.Setenv("AGENTCTL_RUNTIME_DIR", want)
			} else {
				t.Setenv("AGENTCTL_RUNTIME_DIR", "")
			}
			Init(dir)
			EnsureRuntimeDir()
			got, err := os.ReadFile(filepath.Join(want, ".gitignore"))
			if err != nil {
				t.Fatalf("read .gitignore: %v", err)
			}
			if string(got) != selfIgnoreContents {
				t.Errorf(".gitignore = %q, want %q", got, selfIgnoreContents)
			}
		})
		t.Run(branch+"/preserves existing", func(t *testing.T) {
			ResetForTest()
			dir := t.TempDir()
			rt := filepath.Join(dir, ".agentctl")
			if branch == "env" {
				rt = filepath.Join(dir, "elsewhere", ".agentctl")
				t.Setenv("AGENTCTL_RUNTIME_DIR", rt)
			} else {
				t.Setenv("AGENTCTL_RUNTIME_DIR", "")
			}
			if err := os.MkdirAll(rt, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(rt, ".gitignore"), []byte("mine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			Init(dir)
			EnsureRuntimeDir()
			got, _ := os.ReadFile(filepath.Join(rt, ".gitignore"))
			if string(got) != "mine\n" {
				t.Errorf(".gitignore overwritten: %q", got)
			}
		})
	}
}
