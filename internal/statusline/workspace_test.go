package statusline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

// ── gitBranch ────────────────────────────────────────────────────────────────

func TestGitBranch_DotGitDirectory_RefHead(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(dir); got != "main" {
		t.Errorf("gitBranch() = %q, want %q", got, "main")
	}
}

func TestGitBranch_DotGitDirectory_NestedBranchName(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/agent/go-engineer/issue-1041\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "agent/go-engineer/issue-1041"
	if got := gitBranch(dir); got != want {
		t.Errorf("gitBranch() = %q, want %q", got, want)
	}
}

func TestGitBranch_DotGitFile_AbsoluteGitdir(t *testing.T) {
	worktreeDir := t.TempDir()
	realGitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(realGitDir, "HEAD"), []byte("ref: refs/heads/feature-x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFile := filepath.Join(worktreeDir, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: "+realGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(worktreeDir); got != "feature-x" {
		t.Errorf("gitBranch() = %q, want %q", got, "feature-x")
	}
}

func TestGitBranch_DotGitFile_RelativeGitdir(t *testing.T) {
	root := t.TempDir()
	worktreeDir := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realGitDir := filepath.Join(root, "real-git")
	if err := os.Mkdir(realGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realGitDir, "HEAD"), []byte("ref: refs/heads/relative-branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFile := filepath.Join(worktreeDir, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: ../real-git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(worktreeDir); got != "relative-branch" {
		t.Errorf("gitBranch() = %q, want %q", got, "relative-branch")
	}
}

func TestGitBranch_DetachedHead_ReturnsFirst7Chars(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sha := "abcdef0123456789abcdef0123456789abcdef01"
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(sha+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(dir); got != sha[:7] {
		t.Errorf("gitBranch() = %q, want %q", got, sha[:7])
	}
}

func TestGitBranch_MissingDotGit_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := gitBranch(dir); got != "" {
		t.Errorf("gitBranch() = %q, want empty", got)
	}
}

func TestGitBranch_MalformedHead_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("not a valid HEAD at all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(dir); got != "" {
		t.Errorf("gitBranch() = %q, want empty", got)
	}
}

func TestGitBranch_DotGitFile_MalformedGitdirLine_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	gitFile := filepath.Join(dir, ".git")
	if err := os.WriteFile(gitFile, []byte("not a gitdir line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(dir); got != "" {
		t.Errorf("gitBranch() = %q, want empty", got)
	}
}

func TestGitBranch_NeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("gitBranch panicked: %v", r)
		}
	}()
	gitBranch("/does/not/exist/at/all/hopefully")
	gitBranch("")
}

// ── gitDirtyCount ────────────────────────────────────────────────────────────

func TestGitDirtyCount_NonGitDir_ReturnsNotOK(t *testing.T) {
	dir := t.TempDir()
	if _, ok := gitDirtyCount(context.Background(), dir); ok {
		t.Error("gitDirtyCount() ok = true for a non-git directory, want false")
	}
}

// runGit is a small test helper that shells out to a real git binary to set
// up fixture repos -- this is the *test* doing setup, not the statusline's
// own no-subprocess-except-dirty-count code path under test.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestGitDirtyCount_CleanRepo_ZeroCount(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	n, ok := gitDirtyCount(context.Background(), dir)
	if !ok {
		t.Fatal("gitDirtyCount() ok = false for a real git repo")
	}
	if n != 0 {
		t.Errorf("gitDirtyCount() = %d, want 0 for a freshly initialized repo", n)
	}
}

func TestGitDirtyCount_DirtyRepo_CountsPorcelainLines(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, ok := gitDirtyCount(context.Background(), dir)
	if !ok {
		t.Fatal("gitDirtyCount() ok = false for a real git repo")
	}
	if n != 2 {
		t.Errorf("gitDirtyCount() = %d, want 2 untracked files", n)
	}
}

// TestGitDirtyCount_DoesNotWriteIndex pins the --no-optional-locks contract:
// even when a tracked file's mtime is stale relative to the index (which makes
// a plain `git status` refresh and rewrite the index under index.lock), the
// call must leave .git/index untouched and create no lock file.
func TestGitDirtyCount_DoesNotWriteIndex(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	tracked := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "commit", "-m", "init")

	// Touch the tracked file so the index's cached stat data is stale.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(tracked, future, future); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("u"), 0o644); err != nil {
		t.Fatal(err)
	}

	indexPath := filepath.Join(dir, ".git", "index")
	before, err := os.Stat(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}

	n, ok := gitDirtyCount(context.Background(), dir)
	if !ok {
		t.Fatal("gitDirtyCount() ok = false for a real git repo")
	}
	if n != 1 {
		t.Errorf("gitDirtyCount() = %d, want 1 (the untracked file)", n)
	}

	if _, err := os.Stat(filepath.Join(dir, ".git", "index.lock")); !os.IsNotExist(err) {
		t.Errorf("index.lock exists after gitDirtyCount (stat err = %v)", err)
	}
	after, err := os.Stat(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("index mtime changed: %v -> %v", before.ModTime(), after.ModTime())
	}
	afterBytes, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterBytes) != string(beforeBytes) {
		t.Error("index contents changed; gitDirtyCount wrote the index")
	}
}

func TestGitDirtyCount_CanceledContext_ReturnsNotOK(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := gitDirtyCount(ctx, dir); ok {
		t.Error("gitDirtyCount() ok = true with an already-canceled context, want false")
	}
}

func TestGitDirtyCount_NeverExceedsTimeoutBudget(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	start := time.Now()
	gitDirtyCount(context.Background(), dir)
	if elapsed := time.Since(start); elapsed > 2*gitDirtyCountTimeout {
		t.Errorf("gitDirtyCount() took %v, want well under 2x the %v budget", elapsed, gitDirtyCountTimeout)
	}
}

// ── branch glyph ─────────────────────────────────────────────────────────────

func TestBranchGlyphIsOneColumnWide(t *testing.T) {
	if got := lipgloss.Width(string(branchGlyph)); got != 1 {
		t.Fatalf("lipgloss.Width(branchGlyph) = %d, want 1", got)
	}
}

// ── formatDir ────────────────────────────────────────────────────────────────

func TestFormatDir_HomeRelativeTilde(t *testing.T) {
	orig := homeDirForDisplay
	t.Cleanup(func() { homeDirForDisplay = orig })
	homeDirForDisplay = func() (string, error) { return "/home/user", nil }

	tests := []struct {
		name string
		dir  string
		want string
	}{
		{"exact home", "/home/user", "~"},
		{"subdirectory", "/home/user/project", "~/project"},
		{"unrelated path unchanged", "/var/log", "/var/log"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatDir(tt.dir); got != tt.want {
				t.Errorf("formatDir(%q) = %q, want %q", tt.dir, got, tt.want)
			}
		})
	}
}

func TestFormatDir_TruncatesOverBudget(t *testing.T) {
	orig := homeDirForDisplay
	t.Cleanup(func() { homeDirForDisplay = orig })
	homeDirForDisplay = func() (string, error) { return "", os.ErrNotExist }

	long := "/very/deeply/nested/path/that/keeps/going/and/going/until/it/exceeds/forty/columns/agentctl/internal"
	got := formatDir(long)
	if lipgloss.Width(got) > maxDirDisplayWidth {
		t.Errorf("formatDir(long) = %q, width %d exceeds budget %d", got, lipgloss.Width(got), maxDirDisplayWidth)
	}
	if !strings.HasPrefix(got, "…/") {
		t.Errorf("formatDir(long) = %q, want ellipsis-prefixed truncation", got)
	}
	if !strings.HasSuffix(got, "internal") {
		t.Errorf("formatDir(long) = %q, want to end with the last path component", got)
	}
}
