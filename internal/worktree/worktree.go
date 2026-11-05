package worktree

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/blsmth/agentctl/internal/logging"
)

// WorktreeInfo describes a single git worktree entry.
type WorktreeInfo struct {
	Path   string
	Branch string
	HEAD   string // short SHA
}

// logBaseRef and warnBaseRefFallback are indirections over logging.Log and
// logging.Warn so tests can assert that the HEAD-fallback path is logged at
// a different (louder) level than the healthy remote-tracking path, without
// depending on logging's global singleton state.
var (
	logBaseRef          = logging.Log
	warnBaseRefFallback = logging.Warn
	warnRepoRootFailure = logging.Warn
)

// gitCommonDirOutput runs `git -C dir rev-parse --git-common-dir` and
// returns its trimmed stdout. It is a package-level var so tests can force
// the repo-root resolution failure path without needing a git repo state
// that actually makes the real git command error out.
var gitCommonDirOutput = func(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Create creates a new git worktree and returns the absolute path to it.
// repoDir is used as the anchor for `git -C` invocations and as the
// failure-safe fallback if the main repository root cannot be resolved from
// it; it need not itself be the repo root (it may be any directory inside
// the repo, including a subdirectory or a linked worktree).
// branch is the new or existing branch name.
// The worktree is placed at <mainRepoRoot>/.worktrees/<branch-slug>/, where
// mainRepoRoot is resolved via MainRepoRoot regardless of what repoDir
// points at, so that a dispatch whose working directory happens to be a
// subdirectory (or a linked worktree) still lands new worktrees in one
// canonical place.
//
// If the worktree directory already exists (e.g. from a previous dispatch of
// the same issue), Create returns its path without re-creating it -- but only
// when `git worktree list` reports that path as a worktree of this
// repository. Slugs are predictable, so anything else at that path (a
// directory or symlink the repository itself committed under .worktrees/, or
// a stray leftover) is refused with an error rather than handed to an agent
// as its working directory.
// If the branch already exists but the worktree directory does not, Create
// creates the worktree from the existing branch (without -b).
func Create(repoDir, branch string) (string, error) {
	root := MainRepoRoot(repoDir)
	slug := branchSlug(branch)
	worktreePath := filepath.Join(root, ".worktrees", slug)

	// Fast path: worktree directory already exists — reuse it if git agrees
	// it is one of this repo's worktrees.
	if _, err := os.Lstat(worktreePath); err == nil {
		if err := verifyRegisteredWorktree(root, worktreePath); err != nil {
			return "", err
		}
		logging.Log("worktree: reusing existing worktree at %s (branch %s)", worktreePath, branch)
		return worktreePath, nil
	}

	baseRef := resolveBaseRef(repoDir)
	if baseRef == "HEAD" {
		warnBaseRefFallback("worktree: no remote-tracking ref for %s, branching from local HEAD (dirty/stale local state will be inherited)", repoDir)
	} else {
		logBaseRef("worktree: base ref for %s = %s", repoDir, baseRef)
	}

	// Fetch the base branch from origin so the worktree starts from the
	// latest commit, not stale local HEAD. Only attempt this when a remote
	// actually exists and we resolved a real remote-tracking ref (not the
	// HEAD fallback, which has no remote branch to fetch). If the fetch
	// fails (offline, transient network issue) we log a warning and
	// continue — the worktree will still be created from whatever is
	// cached locally.
	if hasRemote(repoDir) && baseRef != "HEAD" {
		if branchName, ok := strings.CutPrefix(baseRef, "origin/"); ok {
			fetchCmd := exec.Command("git", "-C", repoDir, "fetch", "origin", branchName)
			if out, err := fetchCmd.CombinedOutput(); err != nil {
				logging.Log("worktree: fetch origin %s failed (continuing): %v — %s", branchName, err, strings.TrimSpace(string(out)))
			}
		}
	}

	// Try to create the worktree on a new branch from the resolved base ref.
	cmd := exec.Command("git", "-C", repoDir, "worktree", "add", "-b", branch, worktreePath, baseRef)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Branch may already exist (e.g. a previous dispatch created it but the
		// worktree directory was cleaned up). Retry without -b to check out the
		// existing branch into a new worktree directory.
		cmd2 := exec.Command("git", "-C", repoDir, "worktree", "add", worktreePath, branch)
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			// Return the original error for clarity.
			return "", fmt.Errorf("git worktree add: %w\n%s", err, strings.TrimSpace(string(out)))
		} else {
			logging.Log("worktree: created from existing branch %s at %s: %s", branch, worktreePath, strings.TrimSpace(string(out2)))
		}
	}

	return worktreePath, nil
}

// verifyRegisteredWorktree returns nil only when path is a real directory
// (not a symlink) that `git worktree list` reports as a worktree of the
// repository at root.
func verifyRegisteredWorktree(root, path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("worktree: %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("worktree: %s exists but is not a directory agentctl created (symlink or file); refusing to reuse it", path)
	}
	infos, err := List(root)
	if err != nil {
		return fmt.Errorf("worktree: cannot verify existing %s: %w", path, err)
	}
	want := canonicalPath(path)
	for _, wt := range infos {
		if canonicalPath(wt.Path) == want {
			return nil
		}
	}
	return fmt.Errorf("worktree: %s exists but is not a registered git worktree of %s (it may have been committed to the repository); refusing to reuse it -- remove it or dispatch with a different branch", path, root)
}

// canonicalPath resolves symlinks in path (e.g. macOS /var -> /private/var,
// which git reports resolved) so two spellings of one directory compare
// equal. It falls back to the cleaned path.
func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// Prune runs `git worktree prune` to remove stale administrative metadata
// for worktrees whose directory has been deleted out from under git (e.g. an
// operator running `rm -rf` on a worktree instead of `git worktree remove`).
//
// repoDir is anchored to the MAIN repository root via MainRepoRoot so
// this works when called from inside a linked worktree -- `git worktree
// prune` must run against the repo that owns the worktree administrative
// files, not whichever worktree happens to be cwd.
//
// This is inherently safe: git refuses to prune any worktree entry whose
// directory still exists on disk, so Prune can never delete live work --
// it only clears bookkeeping for worktrees that are already gone.
func Prune(repoDir string) error {
	root := MainRepoRoot(repoDir)
	out, err := exec.Command("git", "-C", root, "worktree", "prune").CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree prune: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// resolveBaseRef determines the ref that a new worktree branch should be
// created from. Hardcoding "origin/main" breaks on any repo whose default
// branch isn't "main" or that has no "origin" remote at all -- both of
// which abort worktree creation entirely. The resolution order,
// cheapest and most authoritative first:
//
//  1. `symbolic-ref refs/remotes/origin/HEAD` -- this is exactly what a
//     `git clone` sets up to record the remote's actual default branch, so
//     when present it is authoritative and requires no network access.
//  2. A local rev-parse probe of refs/remotes/origin/main then
//     refs/remotes/origin/master -- covers a repo that was fetched at some
//     point but never had its origin/HEAD symref set (e.g. a shallow or
//     manually-configured clone). Still no network access.
//  3. Local HEAD -- covers no remote at all, or a remote that has never
//     been fetched. Branching a worktree from local HEAD is always valid
//     and is far better than aborting the launch.
func resolveBaseRef(repoDir string) string {
	if out, err := exec.Command("git", "-C", repoDir, "symbolic-ref", "refs/remotes/origin/HEAD", "--short").Output(); err == nil {
		if ref := strings.TrimSpace(string(out)); ref != "" {
			return ref
		}
	}

	for _, candidate := range []string{"refs/remotes/origin/main", "refs/remotes/origin/master"} {
		if err := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "--quiet", candidate).Run(); err == nil {
			return strings.TrimPrefix(candidate, "refs/remotes/")
		}
	}

	return "HEAD"
}

// ResolveBaseRef exports resolveBaseRef for callers outside this package that
// need the same base-ref resolution (e.g. `agentctl cleanup` deciding whether
// a dispatch worktree's branch has any commits beyond the base it was cut
// from).
func ResolveBaseRef(repoDir string) string {
	return resolveBaseRef(repoDir)
}

// hasRemote reports whether repoDir has any git remote configured.
func hasRemote(repoDir string) bool {
	out, err := exec.Command("git", "-C", repoDir, "remote").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// Remove removes a worktree by path using `git worktree remove --force`.
func Remove(worktreePath string) error {
	// Find the repo root by walking up from the worktree path.
	repoDir, err := Toplevel(worktreePath)
	if err != nil {
		return fmt.Errorf("finding repo root: %w", err)
	}

	cmd := exec.Command("git", "-C", repoDir, "worktree", "remove", "--force", worktreePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// List returns all worktrees for the given repo.
func List(repoDir string) ([]WorktreeInfo, error) {
	cmd := exec.Command("git", "-C", repoDir, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}

	var infos []WorktreeInfo
	var cur WorktreeInfo

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "worktree "):
			if cur.Path != "" {
				infos = append(infos, cur)
				cur = WorktreeInfo{}
			}
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			sha := strings.TrimPrefix(line, "HEAD ")
			if len(sha) > 7 {
				sha = sha[:7]
			}
			cur.HEAD = sha
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			// Convert refs/heads/foo → foo
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
		}
	}
	if cur.Path != "" {
		infos = append(infos, cur)
	}

	return infos, nil
}

// branchSlug converts a branch name like "agent/go-engineer/issue-42"
// into a safe directory name "agent-go-engineer-issue-42".
var nonAlphanumRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func branchSlug(branch string) string {
	slug := nonAlphanumRe.ReplaceAllString(branch, "-")
	slug = strings.Trim(slug, "-")
	return slug
}

// Slug exports branchSlug for callers outside this package that need to
// derive the same directory-safe form of a branch name (e.g. `agentctl
// cleanup` falling back to name-based worktree/window matching for
// dispatches that predate the windowstate WorktreeDir field).
func Slug(branch string) string {
	return branchSlug(branch)
}

// Toplevel returns the root of the working tree that path is inside, via
// `git rev-parse --show-toplevel`. Run from inside a linked worktree it
// returns that worktree's own root; use MainRepoRoot to reach the main
// checkout instead. Returns an error when path is not inside a git repo.
func Toplevel(path string) (string, error) {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// MainRepoRoot resolves the root of the MAIN git repository from dir, which
// may be any directory inside the repo -- a subdirectory, or even a linked
// worktree. It is the single repo-root anchor for worktree creation, dispatch
// runtime-dir fallback, and hooks.SessionEnd's branch/prune cleanup.
//
// Deliberately NOT `git rev-parse --show-toplevel`: run from inside a linked
// worktree, --show-toplevel returns that worktree's own root, not the main
// repo's. Instead this uses `git rev-parse --git-common-dir`, which returns
// the main repository's .git directory from anywhere -- including from
// inside a linked worktree -- so its parent is always the main repo root.
//
// Resolution failures (not a git repo, git not on PATH, unusable output)
// are failure-safe: dir is returned unchanged and a warning is logged, so a
// misplaced worktree never blocks a dispatch.
func MainRepoRoot(dir string) string {
	commonDir, err := gitCommonDirOutput(dir)
	if err != nil {
		warnRepoRootFailure("worktree: failed to resolve main repo root from %s, falling back to it as-is: %v", dir, err)
		return dir
	}

	if commonDir == "" {
		warnRepoRootFailure("worktree: git rev-parse --git-common-dir returned empty output from %s, falling back to it as-is", dir)
		return dir
	}

	// --git-common-dir may be printed relative to dir (e.g. ".git" or
	// "../../.git" from a subdirectory); anchor it there before cleaning.
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(dir, commonDir)
	}
	commonDir = filepath.Clean(commonDir)

	root := filepath.Dir(commonDir)
	if root == "" || root == "." || root == commonDir {
		warnRepoRootFailure("worktree: resolved unusable repo root %q from git-common-dir %q (dir=%s), falling back to dir as-is", root, commonDir, dir)
		return dir
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		warnRepoRootFailure("worktree: resolved repo root %q does not exist or is not a directory (dir=%s), falling back to dir as-is: %v", root, dir, err)
		return dir
	}

	return root
}
