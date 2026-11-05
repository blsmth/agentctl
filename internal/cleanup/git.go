package cleanup

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// IsDirty reports whether dir's git worktree has any uncommitted changes --
// tracked or untracked, staged or not -- via `git status --porcelain`.
func IsDirty(dir string) (bool, error) {
	out, err := exec.Command("git", "--no-optional-locks", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("git status --porcelain: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// HasUpstream reports whether the current branch checked out in dir has a
// configured upstream (remote-tracking) branch at all.
func HasUpstream(dir string) bool {
	err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}").Run()
	return err == nil
}

// UnpushedCount returns the number of commits on HEAD not present on its
// upstream. Callers should check HasUpstream first: with no upstream
// configured, "ahead of a nonexistent upstream" is meaningless, so this
// returns 0 (not an error) in that case rather than shelling out to a ref
// that doesn't exist.
func UnpushedCount(dir string) (int, error) {
	if !HasUpstream(dir) {
		return 0, nil
	}
	return revListCount(dir, "@{u}..HEAD")
}

// AheadOfBase returns the number of commits HEAD has beyond baseRef.
func AheadOfBase(dir, baseRef string) (int, error) {
	return revListCount(dir, baseRef+"..HEAD")
}

func revListCount(dir, rangeSpec string) (int, error) {
	out, err := exec.Command("git", "-C", dir, "rev-list", "--count", rangeSpec).Output()
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count %s: %w", rangeSpec, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parsing git rev-list output %q: %w", string(out), err)
	}
	return n, nil
}

// MergedIntoBase reports whether HEAD in dir is an ancestor of baseRef --
// i.e. every commit on this branch is already present on the base ref,
// independent of whether any PR record exists for it (a manually merged or
// fast-forwarded branch has no PR to ask gh about at all).
func MergedIntoBase(dir, baseRef string) bool {
	err := exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", "HEAD", baseRef).Run()
	return err == nil
}

// DeleteLocalBranch deletes branch from repoDir using the safe delete (`git
// branch -d`), which refuses to delete a branch with unmerged commits.
// Deliberately never falls back to `-D`/force -- a branch delete that safe-
// delete refuses is reported, not forced.
func DeleteLocalBranch(repoDir, branch string) error {
	out, err := exec.Command("git", "-C", repoDir, "branch", "-d", branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch -d %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}
