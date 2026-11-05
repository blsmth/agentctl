package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRun is a test helper that runs a git command and fatals on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

// setupFakeRemote creates a bare "remote" repo with one commit on main and
// returns a local clone whose origin points to the bare repo.
func setupFakeRemote(t *testing.T) (localRepo string) {
	t.Helper()

	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	local := filepath.Join(tmp, "local")

	// Create bare repo.
	gitRun(t, tmp, "init", "--bare", bare)

	// Create a temp working repo so we can push an initial commit.
	staging := filepath.Join(tmp, "staging")
	gitRun(t, tmp, "init", staging)
	gitRun(t, staging, "config", "user.email", "test@example.com")
	gitRun(t, staging, "config", "user.name", "Test")
	gitRun(t, staging, "remote", "add", "origin", bare)

	// Commit a file.
	readme := filepath.Join(staging, "README.md")
	if err := os.WriteFile(readme, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, staging, "add", "README.md")
	gitRun(t, staging, "commit", "-m", "initial commit")
	gitRun(t, staging, "push", "origin", "HEAD:main")

	// Clone the bare repo as the local repo under test.
	cmd := exec.Command("git", "clone", bare, local)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git clone: %v\n%s", err, string(out))
	}
	gitRun(t, local, "config", "user.email", "test@example.com")
	gitRun(t, local, "config", "user.name", "Test")

	return local
}

// setupFakeRemoteWithDefaultBranch is like setupFakeRemote but pushes the
// initial commit to the given branch name instead of "main", so the bare
// repo's default branch is whatever defaultBranch is (e.g. "master").
func setupFakeRemoteWithDefaultBranch(t *testing.T, defaultBranch string) (localRepo string) {
	t.Helper()

	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	local := filepath.Join(tmp, "local")

	gitRun(t, tmp, "init", "--bare", bare)

	staging := filepath.Join(tmp, "staging")
	gitRun(t, tmp, "init", staging)
	gitRun(t, staging, "config", "user.email", "test@example.com")
	gitRun(t, staging, "config", "user.name", "Test")
	gitRun(t, staging, "remote", "add", "origin", bare)

	readme := filepath.Join(staging, "README.md")
	if err := os.WriteFile(readme, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, staging, "add", "README.md")
	gitRun(t, staging, "commit", "-m", "initial commit")
	// Push only to defaultBranch -- never create "main" in the bare repo, so
	// the bare's HEAD symref (which init leaves pointing at whatever
	// init.defaultBranch says, typically "main") ends up dangling. This
	// mirrors a real repo whose default branch is "master": git clone will
	// warn "remote HEAD refers to nonexistent ref" and NOT set up
	// refs/remotes/origin/HEAD locally, forcing resolveBaseRef's fallback
	// probe of refs/remotes/origin/master.
	gitRun(t, staging, "push", "origin", "HEAD:"+defaultBranch)

	cmd := exec.Command("git", "clone", bare, local)
	// A dangling remote HEAD makes `git clone` print a warning on stderr
	// and exit 0 -- not an error, just noisy. Don't fail the test on it.
	if out, err := cmd.CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "remote HEAD refers to nonexistent ref") {
			t.Fatalf("git clone: %v\n%s", err, string(out))
		}
	}
	gitRun(t, local, "config", "user.email", "test@example.com")
	gitRun(t, local, "config", "user.name", "Test")

	return local
}

// setupRepoNoRemote creates a plain local git repo with one commit and no
// remotes at all.
func setupRepoNoRemote(t *testing.T) (repoDir string) {
	t.Helper()

	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test")

	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "commit", "-m", "initial commit")

	return dir
}

// setupRepoWithUnfetchedRemote creates a local repo with a commit and an
// "origin" remote configured, but never runs `git fetch` -- so there are no
// refs/remotes/origin/* refs at all locally.
func setupRepoWithUnfetchedRemote(t *testing.T) (repoDir string) {
	t.Helper()

	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	gitRun(t, tmp, "init", "--bare", bare)

	dir := filepath.Join(tmp, "local")
	gitRun(t, tmp, "init", dir)
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test")
	gitRun(t, dir, "remote", "add", "origin", bare)

	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "commit", "-m", "initial commit")

	return dir
}

func TestResolveBaseRef_StandardFakeRemote(t *testing.T) {
	repoDir := setupFakeRemote(t)

	got := resolveBaseRef(repoDir)
	if got != "origin/main" {
		t.Errorf("resolveBaseRef() = %q, want %q", got, "origin/main")
	}
}

func TestCreate_MasterDefaultBranch(t *testing.T) {
	repoDir := setupFakeRemoteWithDefaultBranch(t, "master")

	branch := "agent/go-engineer/issue-master-1"
	worktreePath, err := Create(repoDir, branch)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatalf("worktree path does not exist: %s", worktreePath)
	}

	masterSHA := gitOutput(t, repoDir, "rev-parse", "origin/master")
	worktreeSHA := gitOutput(t, worktreePath, "rev-parse", "HEAD")
	if masterSHA != worktreeSHA {
		t.Errorf("worktree HEAD = %s, want origin/master = %s", worktreeSHA, masterSHA)
	}

	worktreeBranch := gitOutput(t, worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if worktreeBranch != branch {
		t.Errorf("worktree branch = %q, want %q", worktreeBranch, branch)
	}
}

func TestCreate_NoRemote(t *testing.T) {
	repoDir := setupRepoNoRemote(t)

	branch := "agent/go-engineer/issue-noremote-1"
	worktreePath, err := Create(repoDir, branch)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatalf("worktree path does not exist: %s", worktreePath)
	}

	headSHA := gitOutput(t, repoDir, "rev-parse", "HEAD")
	worktreeSHA := gitOutput(t, worktreePath, "rev-parse", "HEAD")
	if headSHA != worktreeSHA {
		t.Errorf("worktree HEAD = %s, want local HEAD = %s", worktreeSHA, headSHA)
	}

	worktreeBranch := gitOutput(t, worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if worktreeBranch != branch {
		t.Errorf("worktree branch = %q, want %q", worktreeBranch, branch)
	}
}

func TestCreate_UnfetchedRemote(t *testing.T) {
	repoDir := setupRepoWithUnfetchedRemote(t)

	branch := "agent/go-engineer/issue-unfetched-1"
	worktreePath, err := Create(repoDir, branch)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatalf("worktree path does not exist: %s", worktreePath)
	}

	worktreeBranch := gitOutput(t, worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if worktreeBranch != branch {
		t.Errorf("worktree branch = %q, want %q", worktreeBranch, branch)
	}
}

func TestCreate_BranchesFromOriginMain(t *testing.T) {
	repoDir := setupFakeRemote(t)

	branch := "agent/go-engineer/issue-99"
	worktreePath, err := Create(repoDir, branch)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify the worktree directory was created.
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatalf("worktree path does not exist: %s", worktreePath)
	}

	// Verify the branch in the worktree tracks origin/main's commit.
	originMainSHA := gitOutput(t, repoDir, "rev-parse", "origin/main")
	worktreeSHA := gitOutput(t, worktreePath, "rev-parse", "HEAD")

	if originMainSHA != worktreeSHA {
		t.Errorf("worktree HEAD = %s, want origin/main = %s", worktreeSHA, originMainSHA)
	}

	// Verify the new branch name is correct.
	worktreeBranch := gitOutput(t, worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if worktreeBranch != branch {
		t.Errorf("worktree branch = %q, want %q", worktreeBranch, branch)
	}
}

func TestCreate_SluggedPath(t *testing.T) {
	repoDir := setupFakeRemote(t)

	branch := "agent/go-engineer/issue-42"
	worktreePath, err := Create(repoDir, branch)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	want := filepath.Join(repoDir, ".worktrees", "agent-go-engineer-issue-42")
	if worktreePath != want {
		t.Errorf("Create path = %q, want %q", worktreePath, want)
	}
}

// gitOutput runs a git command and returns trimmed stdout, fataling on error.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// TestCreate_ExistingWorktreeReused verifies that Create returns immediately
// when the worktree directory already exists, without calling git again.
// This is the primary regression test for the "re-dispatch of issue with
// existing branch/worktree lands in main repo" bug.
func TestCreate_ExistingWorktreeReused(t *testing.T) {
	repoDir := setupFakeRemote(t)

	branch := "agent/go-engineer/issue-711"

	// First call: creates the worktree.
	path1, err := Create(repoDir, branch)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	// Second call with the same branch: must return the same path without error.
	path2, err := Create(repoDir, branch)
	if err != nil {
		t.Fatalf("second Create (reuse): %v", err)
	}
	if path1 != path2 {
		t.Errorf("reuse: got %q, want %q", path2, path1)
	}

	// The directory must still exist and be on the correct branch.
	if _, err := os.Stat(path2); os.IsNotExist(err) {
		t.Fatalf("reused worktree path does not exist: %s", path2)
	}
	worktreeBranch := gitOutput(t, path2, "rev-parse", "--abbrev-ref", "HEAD")
	if worktreeBranch != branch {
		t.Errorf("reused worktree branch = %q, want %q", worktreeBranch, branch)
	}
}

// TestCreate_ExistingBranchNoWorktreeDir verifies that Create succeeds when
// the branch already exists locally but the worktree directory does not.
func TestCreate_ExistingBranchNoWorktreeDir(t *testing.T) {
	repoDir := setupFakeRemote(t)

	branch := "agent/go-engineer/issue-99"

	// Create the branch locally without a worktree.
	gitRun(t, repoDir, "branch", branch, "origin/main")

	// Create should succeed by using the existing branch (no -b).
	worktreePath, err := Create(repoDir, branch)
	if err != nil {
		t.Fatalf("Create with existing branch: %v", err)
	}
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatalf("worktree path does not exist: %s", worktreePath)
	}
	worktreeBranch := gitOutput(t, worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if worktreeBranch != branch {
		t.Errorf("worktree branch = %q, want %q", worktreeBranch, branch)
	}
}

// TestCreate_HeadFallbackLogsWarnDistinctFromNormalPath verifies that when
// resolveBaseRef falls back to local HEAD (no usable remote-tracking ref),
// Create logs at WARN level -- distinguishable from the healthy
// remote-tracking path, which logs at plain INFO via logBaseRef. This is the
// regression test for making the degraded, dirty-local-state-inherited path
// loud instead of indistinguishable from the safe path.
func TestCreate_HeadFallbackLogsWarnDistinctFromNormalPath(t *testing.T) {
	var warnCalls, infoCalls []string

	origWarn, origLog := warnBaseRefFallback, logBaseRef
	warnBaseRefFallback = func(format string, args ...any) {
		warnCalls = append(warnCalls, fmt.Sprintf(format, args...))
	}
	logBaseRef = func(format string, args ...any) {
		infoCalls = append(infoCalls, fmt.Sprintf(format, args...))
	}
	defer func() {
		warnBaseRefFallback = origWarn
		logBaseRef = origLog
	}()

	// No-remote repo: resolveBaseRef must fall back to "HEAD", which must
	// route through warnBaseRefFallback, not logBaseRef.
	noRemoteRepo := setupRepoNoRemote(t)
	if _, err := Create(noRemoteRepo, "agent/go-engineer/issue-head-fallback"); err != nil {
		t.Fatalf("Create (no remote): %v", err)
	}
	if len(warnCalls) != 1 {
		t.Fatalf("no-remote repo: warnBaseRefFallback called %d times, want 1 (calls: %v)", len(warnCalls), warnCalls)
	}
	if !strings.Contains(warnCalls[0], "local HEAD") {
		t.Errorf("no-remote repo: warn message = %q, want it to mention local HEAD", warnCalls[0])
	}

	// Healthy remote-tracking repo: resolveBaseRef resolves "origin/main",
	// which must route through logBaseRef, not warnBaseRefFallback.
	warnCalls, infoCalls = nil, nil
	normalRepo := setupFakeRemote(t)
	if _, err := Create(normalRepo, "agent/go-engineer/issue-head-fallback-normal"); err != nil {
		t.Fatalf("Create (normal remote): %v", err)
	}
	if len(warnCalls) != 0 {
		t.Errorf("normal-remote repo: warnBaseRefFallback called %d times, want 0 (calls: %v)", len(warnCalls), warnCalls)
	}
	if len(infoCalls) != 1 {
		t.Fatalf("normal-remote repo: logBaseRef called %d times, want 1 (calls: %v)", len(infoCalls), infoCalls)
	}
	if !strings.Contains(infoCalls[0], "origin/main") {
		t.Errorf("normal-remote repo: log message = %q, want it to mention origin/main", infoCalls[0])
	}
}

func TestBranchSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"agent/go-engineer/issue-42", "agent-go-engineer-issue-42"},
		{"feature/my_branch", "feature-my-branch"},
		{"simple", "simple"},
		{"--leading-dashes--", "leading-dashes"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := branchSlug(tc.input)
			if got != tc.want {
				t.Errorf("branchSlug(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestCreate_AnchorsToRepoRootFromSubdirectory is the primary regression
// test for the bug where Create anchors ".worktrees/" to whatever directory
// it was handed rather than the repository root -- e.g. a dispatch whose
// resolved workDir is a subdirectory of the repo (see improve sessions,
// which set their workDir to filepath.Dir(target.FilePath)).
func TestCreate_AnchorsToRepoRootFromSubdirectory(t *testing.T) {
	repoDir := setupFakeRemote(t)

	sub := filepath.Join(repoDir, "sub", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", sub, err)
	}

	branch := "agent/go-engineer/issue-subdir-1"
	worktreePath, err := Create(sub, branch)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	want := filepath.Join(repoDir, ".worktrees", branchSlug(branch))
	gotReal, err := filepath.EvalSymlinks(worktreePath)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", worktreePath, err)
	}
	wantReal, err := filepath.EvalSymlinks(filepath.Dir(want))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", filepath.Dir(want), err)
	}
	wantReal = filepath.Join(wantReal, filepath.Base(want))

	if gotReal != wantReal {
		t.Errorf("Create(subdir) path = %q, want anchored at repo root: %q", gotReal, wantReal)
	}

	// Must NOT have landed under the subdirectory.
	wrongPath := filepath.Join(sub, ".worktrees", branchSlug(branch))
	if _, err := os.Stat(wrongPath); err == nil {
		t.Errorf("worktree wrongly created under subdirectory: %s", wrongPath)
	}
}

// TestCreate_AnchorsToMainRepoFromLinkedWorktree is the case that
// `git rev-parse --show-toplevel` gets wrong: called from inside an
// existing linked worktree, --show-toplevel returns that worktree's own
// root, which would nest a second ".worktrees/" inside it. Create must use
// `git rev-parse --git-common-dir` instead, which resolves to the MAIN
// repository regardless of which worktree it's invoked from.
func TestCreate_AnchorsToMainRepoFromLinkedWorktree(t *testing.T) {
	repoDir := setupFakeRemote(t)

	// Create a first, linked worktree the way Create() would.
	firstBranch := "agent/go-engineer/issue-linked-first"
	linkedWorktree, err := Create(repoDir, firstBranch)
	if err != nil {
		t.Fatalf("Create (first, linked): %v", err)
	}

	// Sanity check: --show-toplevel from inside the linked worktree returns
	// the linked worktree's own root, NOT the main repo -- this is exactly
	// the case that makes --show-toplevel unusable here.
	toplevel := gitOutput(t, linkedWorktree, "rev-parse", "--show-toplevel")
	linkedReal, err := filepath.EvalSymlinks(linkedWorktree)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", linkedWorktree, err)
	}
	toplevelReal, err := filepath.EvalSymlinks(toplevel)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", toplevel, err)
	}
	if toplevelReal != linkedReal {
		t.Fatalf("test setup invariant broken: --show-toplevel from linked worktree = %q, want it to equal the linked worktree root %q", toplevelReal, linkedReal)
	}

	// Now dispatch a SECOND Create from inside the first linked worktree,
	// simulating a dispatch whose resolved workDir is a linked worktree.
	secondBranch := "agent/go-engineer/issue-linked-second"
	secondWorktree, err := Create(linkedWorktree, secondBranch)
	if err != nil {
		t.Fatalf("Create (second, from within linked worktree): %v", err)
	}

	want := filepath.Join(repoDir, ".worktrees", branchSlug(secondBranch))
	gotReal, err := filepath.EvalSymlinks(secondWorktree)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", secondWorktree, err)
	}
	wantParentReal, err := filepath.EvalSymlinks(filepath.Dir(want))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", filepath.Dir(want), err)
	}
	wantReal := filepath.Join(wantParentReal, filepath.Base(want))

	if gotReal != wantReal {
		t.Errorf("Create(from linked worktree) path = %q, want anchored at MAIN repo root: %q", gotReal, wantReal)
	}

	// Must NOT have nested a second .worktrees/ inside the first linked worktree.
	wrongPath := filepath.Join(linkedWorktree, ".worktrees", branchSlug(secondBranch))
	if _, err := os.Stat(wrongPath); err == nil {
		t.Errorf("worktree wrongly nested inside linked worktree: %s", wrongPath)
	}
}

// TestCreate_RepoRootResolutionFailureFallsBackToPassedDir verifies that
// when resolving the main repo root fails, Create degrades gracefully to
// treating the passed directory as the anchor rather than erroring out the
// dispatch -- losing a dispatch is worse than a misplaced worktree. Mirrors
// setupFakeRemoteWithDefaultBranch's approach of deliberately forcing a
// fallback path rather than relying on a real, hard-to-construct git failure.
func TestCreate_RepoRootResolutionFailureFallsBackToPassedDir(t *testing.T) {
	repoDir := setupFakeRemote(t)
	sub := filepath.Join(repoDir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", sub, err)
	}

	var warnCalls []string
	origLookup := gitCommonDirOutput
	origWarn := warnRepoRootFailure
	gitCommonDirOutput = func(dir string) (string, error) {
		return "", fmt.Errorf("simulated git-common-dir failure")
	}
	warnRepoRootFailure = func(format string, args ...any) {
		warnCalls = append(warnCalls, fmt.Sprintf(format, args...))
	}
	defer func() {
		gitCommonDirOutput = origLookup
		warnRepoRootFailure = origWarn
	}()

	branch := "agent/go-engineer/issue-fallback-1"
	worktreePath, err := Create(sub, branch)
	if err != nil {
		t.Fatalf("Create should not error on repo-root resolution failure: %v", err)
	}

	want := filepath.Join(sub, ".worktrees", branchSlug(branch))
	if worktreePath != want {
		t.Errorf("Create path = %q, want fallback to passed dir: %q", worktreePath, want)
	}
	if len(warnCalls) == 0 {
		t.Error("expected warnRepoRootFailure to be called at least once, got none")
	}
}

func TestList(t *testing.T) {
	repoDir := setupFakeRemote(t)

	infos, err := List(repoDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) == 0 {
		t.Fatal("List returned no worktrees (expected at least the main worktree)")
	}

	// Resolve symlinks for both paths before comparing — on macOS /var is a
	// symlink to /private/var so t.TempDir() and git may disagree.
	gotReal, err := filepath.EvalSymlinks(infos[0].Path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", infos[0].Path, err)
	}
	wantReal, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", repoDir, err)
	}
	if gotReal != wantReal {
		t.Errorf("first worktree path = %q, want %q", gotReal, wantReal)
	}
}

// countWorktrees returns the number of worktrees List reports whose path
// matches want (after symlink resolution), so tests can assert presence or
// absence without depending on List's overall ordering/count.
func countWorktrees(t *testing.T, repoDir, want string) int {
	t.Helper()
	infos, err := List(repoDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	wantReal, err := filepath.EvalSymlinks(want)
	if err != nil {
		// The directory may no longer exist (that's exactly the case we're
		// testing) -- fall back to a plain Clean comparison.
		wantReal = filepath.Clean(want)
	}
	n := 0
	for _, info := range infos {
		gotReal, err := filepath.EvalSymlinks(info.Path)
		if err != nil {
			gotReal = filepath.Clean(info.Path)
		}
		if gotReal == wantReal {
			n++
		}
	}
	return n
}

func TestPrune_RemovesEntryForDeletedDirectory(t *testing.T) {
	repoDir := setupFakeRemote(t)

	wtPath, err := Create(repoDir, "feature/gone")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if countWorktrees(t, repoDir, wtPath) != 1 {
		t.Fatalf("expected worktree present after Create")
	}

	// Simulate an operator deleting the worktree directory out from under
	// git, instead of using `git worktree remove`.
	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	if err := Prune(repoDir); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if n := countWorktrees(t, repoDir, wtPath); n != 0 {
		t.Errorf("Prune left %d stale worktree entries for a deleted directory, want 0", n)
	}
}

// TestPrune_SurvivesLiveWorktree is the important case: git worktree prune
// must refuse to remove any entry whose directory still exists on disk, so
// Prune can never delete live agent work.
func TestPrune_SurvivesLiveWorktree(t *testing.T) {
	repoDir := setupFakeRemote(t)

	wtPath, err := Create(repoDir, "feature/still-here")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := Prune(repoDir); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if n := countWorktrees(t, repoDir, wtPath); n != 1 {
		t.Errorf("Prune removed a live worktree entry (count=%d), want 1", n)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("worktree directory no longer exists after Prune: %v", err)
	}
}

func TestPrune_AnchorsToMainRepoFromLinkedWorktree(t *testing.T) {
	repoDir := setupFakeRemote(t)

	wtPath, err := Create(repoDir, "feature/from-linked")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Create a second worktree whose directory we'll delete, then prune from
	// *inside* the first (linked) worktree -- Prune must anchor to the main
	// repo root regardless.
	staleWtPath, err := Create(repoDir, "feature/stale")
	if err != nil {
		t.Fatalf("Create (stale): %v", err)
	}
	if err := os.RemoveAll(staleWtPath); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	if err := Prune(wtPath); err != nil {
		t.Fatalf("Prune from linked worktree: %v", err)
	}

	if n := countWorktrees(t, repoDir, staleWtPath); n != 0 {
		t.Errorf("Prune (anchored from linked worktree) left stale entry, count=%d, want 0", n)
	}
}

// TestToplevel covers both outcomes of Toplevel: a subdirectory of a repo
// resolves to the repo root, and a directory outside any repo is an error.
func TestToplevel(t *testing.T) {
	t.Run("subdirectory resolves to repo root", func(t *testing.T) {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("EvalSymlinks: %v", err)
		}
		gitRun(t, root, "init", "-q")
		sub := filepath.Join(root, "a", "b")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		got, err := Toplevel(sub)
		if err != nil {
			t.Fatalf("Toplevel(%q) error = %v", sub, err)
		}
		if got != root {
			t.Errorf("Toplevel(%q) = %q, want %q", sub, got, root)
		}
	})

	t.Run("outside a repo is an error", func(t *testing.T) {
		if _, err := Toplevel(t.TempDir()); err == nil {
			t.Error("Toplevel(non-repo) error = nil, want an error")
		}
	})
}

// TestCreate_UnregisteredDirectoryNotReused is the regression test for a
// repository that commits a directory at a predictable worktree slug path:
// Create must refuse it rather than hand it to an agent as its worktree.
func TestCreate_UnregisteredDirectoryNotReused(t *testing.T) {
	repoDir := setupFakeRemote(t)
	branch := "agent/go-engineer/issue-712"
	planted := filepath.Join(repoDir, ".worktrees", branchSlug(branch))
	if err := os.MkdirAll(planted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planted, "CLAUDE.md"), []byte("attacker\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := Create(repoDir, branch)
	if err == nil {
		t.Fatalf("Create reused unregistered directory %s", path)
	}
	if !strings.Contains(err.Error(), "not a registered git worktree") {
		t.Errorf("err = %v, want it to say the dir is not a registered worktree", err)
	}
}

// TestCreate_SymlinkAtSlugNotReused: a symlink at the slug path is refused
// even if it points at a real worktree.
func TestCreate_SymlinkAtSlugNotReused(t *testing.T) {
	repoDir := setupFakeRemote(t)
	real, err := Create(repoDir, "agent/x/issue-1")
	if err != nil {
		t.Fatal(err)
	}
	branch := "agent/x/issue-2"
	if err := os.Symlink(real, filepath.Join(repoDir, ".worktrees", branchSlug(branch))); err != nil {
		t.Fatal(err)
	}
	if path, err := Create(repoDir, branch); err == nil {
		t.Fatalf("Create reused symlink %s", path)
	}
}
