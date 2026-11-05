package cmd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// chdirForTest moves the process into dir for the duration of the test.
// resolveCleanupRoot reads os.Getwd(), so there is no seam to inject a directory
// through; the cwd itself is the input.
func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%q): %v", dir, err)
	}
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck
}

// TestResolveRepoRootOutsideAGitRepo is the regression test for `agentctl
// cleanup` failing with a bare "listing worktrees: git worktree list: exit
// status 128" when run from a directory that is not a git repository. That
// error named neither the directory nor the actual problem; resolveCleanupRoot
// now reports the condition itself so RunCleanup can say so plainly.
func TestResolveRepoRootOutsideAGitRepo(t *testing.T) {
	// A tempdir under a repo-less root. t.TempDir() lives under /tmp (or
	// TMPDIR), which is not a git checkout on any supported platform.
	dir := t.TempDir()
	chdirForTest(t, dir)

	got, err := resolveCleanupRoot()
	if !errors.Is(err, errNotAGitRepo) {
		t.Fatalf("resolveCleanupRoot() error = %v, want errNotAGitRepo", err)
	}
	// The cwd is still returned so the caller can name it in its message.
	if got == "" {
		t.Error("resolveCleanupRoot() returned an empty directory; the caller needs it for the message")
	}
}

// TestResolveRepoRootInsideAGitRepo covers the happy path, including the
// "run from a subdirectory" case that resolveCleanupRoot exists to support: the
// repo ROOT must come back, not the subdirectory.
func TestResolveRepoRootInsideAGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Skipf("git init failed: %s: %v", out, err)
	}
	subdir := filepath.Join(root, "internal", "deep")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", subdir, err)
	}
	chdirForTest(t, subdir)

	got, err := resolveCleanupRoot()
	if err != nil {
		t.Fatalf("resolveCleanupRoot() error = %v, want nil", err)
	}
	// Compare through EvalSymlinks: on macOS t.TempDir() hands back a /var
	// path that git reports as its /private/var realpath.
	want := root
	if resolved, rerr := filepath.EvalSymlinks(want); rerr == nil {
		want = resolved
	}
	if resolved, rerr := filepath.EvalSymlinks(got); rerr == nil {
		got = resolved
	}
	if got != want {
		t.Errorf("resolveCleanupRoot() = %q, want the repo root %q", got, want)
	}
}

// TestRunCleanupOutsideAGitRepo asserts the user-facing behaviour: not being
// in a repo is a normal, non-error outcome, and --json still emits valid JSON
// so a scripted caller does not have to special-case it.
func TestRunCleanupOutsideAGitRepo(t *testing.T) {
	dir := t.TempDir()

	t.Run("plain output", func(t *testing.T) {
		chdirForTest(t, dir)
		if err := RunCleanup(CleanupOptions{DryRun: true}); err != nil {
			t.Errorf("RunCleanup() error = %v, want nil", err)
		}
	})

	t.Run("json output", func(t *testing.T) {
		chdirForTest(t, dir)
		if err := RunCleanup(CleanupOptions{DryRun: true, JSON: true}); err != nil {
			t.Errorf("RunCleanup(--json) error = %v, want nil", err)
		}
	})
}
