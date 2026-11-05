package cleanup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRun runs a git command against dir and fatals the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupBareRemoteWithClone creates a bare "remote" repo with one commit on
// main, and a local clone tracking it -- the shape a real dispatch worktree
// pushed to origin would have.
//
// This must be fully hermetic against the machine's ambient git config --
// specifically `init.defaultBranch` (which git defaults to "master" unless
// overridden, and CI runners commonly leave at that default even though many
// developer machines set it to "main") and the presence of a global
// user.email/user.name. Getting either wrong doesn't fail loudly at fixture
// setup: `git init` and `git commit` both succeed regardless, but a bare
// repo whose HEAD symref never resolves to a real branch leaves `git clone`
// unable to check anything out, silently handing every test that follows an
// empty working tree with no local "main" ref at all -- see the CI failure
// this fixed (TestAheadOfBase/TestMergedIntoBase/TestDeleteLocalBranch all
// failed on a runner where the ambient default branch was "master", not
// "main", even though every one of these tests passed on a Mac configured
// with init.defaultBranch=main). Force every branch name and every commit
// identity explicitly; never rely on ambient state.
func setupBareRemoteWithClone(t *testing.T) (local string) {
	t.Helper()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	staging := filepath.Join(tmp, "staging")
	local = filepath.Join(tmp, "local")

	gitRun(t, tmp, "init", "--bare", bare)
	// Force the bare repo's HEAD to point at refs/heads/main *before* the
	// first push, regardless of the runner's init.defaultBranch. Without
	// this, a bare repo created on a runner defaulting to "master" keeps
	// HEAD -> refs/heads/master even after a push creates refs/heads/main,
	// leaving HEAD dangling (no "master" branch was ever created) -- and a
	// clone of a bare repo with a dangling HEAD checks nothing out at all.
	gitRun(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	gitRun(t, tmp, "init", "-b", "main", staging)
	gitRun(t, staging, "config", "user.email", "test@example.com")
	gitRun(t, staging, "config", "user.name", "Test")
	gitRun(t, staging, "remote", "add", "origin", bare)
	writeFile(t, filepath.Join(staging, "README.md"), "hello\n")
	gitRun(t, staging, "add", "README.md")
	gitRun(t, staging, "commit", "-m", "initial commit")
	gitRun(t, staging, "push", "origin", "HEAD:main")

	cmd := exec.Command("git", "clone", bare, local)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, string(out))
	}
	gitRun(t, local, "config", "user.email", "test@example.com")
	gitRun(t, local, "config", "user.name", "Test")

	// Belt-and-braces: fail loudly here, at fixture setup, rather than
	// letting every test that follows fail later with a confusing "not a
	// valid object name main" -- if "main" isn't checked out and tracking
	// origin/main, something about the hermetic setup above regressed.
	branch := strings.TrimSpace(gitOutputForTest(t, local, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != "main" {
		t.Fatalf("setupBareRemoteWithClone: clone checked out branch %q, want \"main\" (bare repo's HEAD symref did not resolve)", branch)
	}
	if err := exec.Command("git", "-C", local, "rev-parse", "--verify", "--quiet", "origin/main").Run(); err != nil {
		t.Fatalf("setupBareRemoteWithClone: origin/main does not resolve in the clone")
	}

	return local
}

// gitOutputForTest runs a git command and returns its trimmed stdout,
// fataling the test on error.
func gitOutputForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

func TestIsDirty(t *testing.T) {
	dir := setupBareRemoteWithClone(t)

	dirty, err := IsDirty(dir)
	if err != nil {
		t.Fatalf("IsDirty: %v", err)
	}
	if dirty {
		t.Fatal("freshly cloned repo should not be dirty")
	}

	writeFile(t, filepath.Join(dir, "README.md"), "modified\n")
	dirty, err = IsDirty(dir)
	if err != nil {
		t.Fatalf("IsDirty: %v", err)
	}
	if !dirty {
		t.Fatal("repo with an uncommitted modification should be dirty")
	}
}

func TestIsDirty_UntrackedFile(t *testing.T) {
	dir := setupBareRemoteWithClone(t)
	writeFile(t, filepath.Join(dir, "untracked.txt"), "new\n")

	dirty, err := IsDirty(dir)
	if err != nil {
		t.Fatalf("IsDirty: %v", err)
	}
	if !dirty {
		t.Fatal("an untracked file should count as dirty")
	}
}

func TestHasUpstream(t *testing.T) {
	dir := setupBareRemoteWithClone(t)

	// The clone's local "main" tracks origin/main by default.
	if !HasUpstream(dir) {
		t.Fatal("cloned branch should have an upstream")
	}

	gitRun(t, dir, "checkout", "-b", "no-upstream-branch")
	if HasUpstream(dir) {
		t.Fatal("freshly created local-only branch should have no upstream")
	}
}

func TestUnpushedCount(t *testing.T) {
	dir := setupBareRemoteWithClone(t)

	n, err := UnpushedCount(dir)
	if err != nil {
		t.Fatalf("UnpushedCount: %v", err)
	}
	if n != 0 {
		t.Fatalf("freshly cloned repo: UnpushedCount = %d, want 0", n)
	}

	writeFile(t, filepath.Join(dir, "new.txt"), "content\n")
	gitRun(t, dir, "add", "new.txt")
	gitRun(t, dir, "commit", "-m", "local only commit")

	n, err = UnpushedCount(dir)
	if err != nil {
		t.Fatalf("UnpushedCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("after one local commit: UnpushedCount = %d, want 1", n)
	}
}

func TestUnpushedCount_NoUpstreamReturnsZeroNotError(t *testing.T) {
	dir := setupBareRemoteWithClone(t)
	gitRun(t, dir, "checkout", "-b", "no-upstream-branch")

	n, err := UnpushedCount(dir)
	if err != nil {
		t.Fatalf("UnpushedCount on branch with no upstream: unexpected error %v", err)
	}
	if n != 0 {
		t.Fatalf("UnpushedCount on branch with no upstream = %d, want 0", n)
	}
}

func TestAheadOfBase(t *testing.T) {
	dir := setupBareRemoteWithClone(t)
	gitRun(t, dir, "checkout", "-b", "feature")

	n, err := AheadOfBase(dir, "origin/main")
	if err != nil {
		t.Fatalf("AheadOfBase: %v", err)
	}
	if n != 0 {
		t.Fatalf("freshly branched repo: AheadOfBase = %d, want 0", n)
	}

	writeFile(t, filepath.Join(dir, "feature.txt"), "content\n")
	gitRun(t, dir, "add", "feature.txt")
	gitRun(t, dir, "commit", "-m", "feature commit 1")
	writeFile(t, filepath.Join(dir, "feature2.txt"), "content\n")
	gitRun(t, dir, "add", "feature2.txt")
	gitRun(t, dir, "commit", "-m", "feature commit 2")

	n, err = AheadOfBase(dir, "origin/main")
	if err != nil {
		t.Fatalf("AheadOfBase: %v", err)
	}
	if n != 2 {
		t.Fatalf("after two feature commits: AheadOfBase = %d, want 2", n)
	}
}

func TestMergedIntoBase(t *testing.T) {
	dir := setupBareRemoteWithClone(t)
	gitRun(t, dir, "checkout", "-b", "feature")

	if !MergedIntoBase(dir, "origin/main") {
		t.Fatal("a branch with no new commits should be merged into base")
	}

	writeFile(t, filepath.Join(dir, "feature.txt"), "content\n")
	gitRun(t, dir, "add", "feature.txt")
	gitRun(t, dir, "commit", "-m", "feature commit")

	if MergedIntoBase(dir, "origin/main") {
		t.Fatal("a branch with an unmerged commit should not be merged into base")
	}

	// Fast-forward main to include the feature commit, simulating a merged PR
	// with no squash -- HEAD is now an ancestor of the (updated) base.
	gitRun(t, dir, "checkout", "main")
	gitRun(t, dir, "merge", "feature", "--ff-only")
	gitRun(t, dir, "checkout", "feature")
	if !MergedIntoBase(dir, "main") {
		t.Fatal("after fast-forwarding base to include the branch, it should read as merged into base")
	}
}

func TestDeleteLocalBranch(t *testing.T) {
	dir := setupBareRemoteWithClone(t)
	gitRun(t, dir, "branch", "mergeable", "main")

	if err := DeleteLocalBranch(dir, "mergeable"); err != nil {
		t.Fatalf("DeleteLocalBranch on a fully-merged branch: %v", err)
	}

	out, err := exec.Command("git", "-C", dir, "branch", "--list", "mergeable").Output()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatal("branch should no longer exist after DeleteLocalBranch")
	}
}

func TestDeleteLocalBranch_RefusesUnmergedRatherThanForcing(t *testing.T) {
	dir := setupBareRemoteWithClone(t)
	gitRun(t, dir, "checkout", "-b", "unmerged")
	writeFile(t, filepath.Join(dir, "unmerged.txt"), "content\n")
	gitRun(t, dir, "add", "unmerged.txt")
	gitRun(t, dir, "commit", "-m", "unmerged commit")
	gitRun(t, dir, "checkout", "main")

	if err := DeleteLocalBranch(dir, "unmerged"); err == nil {
		t.Fatal("DeleteLocalBranch should refuse to delete an unmerged branch (git branch -d, never -D)")
	}

	out, err := exec.Command("git", "-C", dir, "branch", "--list", "unmerged").Output()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		t.Fatal("branch should still exist -- DeleteLocalBranch must never force-delete")
	}
}
