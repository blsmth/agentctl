package worktree

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// Checkout describes the git checkout a directory belongs to.
type Checkout struct {
	// Repo is whether the directory is inside a git repository at all.
	Repo bool
	// Linked is whether the directory is in a linked worktree (one made by
	// `git worktree add`) rather than the repository's main checkout.
	Linked bool
	// Branch is the checked-out branch, "" when HEAD is detached or unknown.
	Branch string
}

// Inspect reports which kind of checkout dir is in. A linked worktree has a
// git dir (.git/worktrees/<name>) that differs from the common dir; in the
// main checkout the two are the same. A directory outside any repository,
// or one git cannot read, is reported as Checkout{} (Repo false). It takes
// no optional locks, so it never contends with another session's git.
func Inspect(dir string) Checkout {
	out, err := gitNoLocks(dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return Checkout{}
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		return Checkout{}
	}
	c := Checkout{Repo: true, Linked: filepath.Clean(lines[0]) != filepath.Clean(lines[1])}
	// symbolic-ref works on an unborn branch and exits non-zero when HEAD
	// is detached, which leaves Branch empty.
	if branch, err := gitNoLocks(dir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		c.Branch = branch
	}
	return c
}

// gitNoLocks runs git with --no-optional-locks in dir and returns its
// trimmed stdout.
func gitNoLocks(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"--no-optional-locks", "-C", dir}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
