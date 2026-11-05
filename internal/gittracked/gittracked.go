// Package gittracked answers "which of these paths does the repository
// track?" -- the question agentctl asks before trusting (or writing into) a
// directory a cloned repository could have pre-populated.
package gittracked

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// ErrUnavailable is returned (wrapped) when there is no repository to ask:
// git is not installed, or dir is not inside a git work tree. Callers that
// only care about repo-tracked content treat it as "nothing is tracked".
//
// Any other git failure (e.g. "detected dubious ownership", a corrupt
// index) is returned as a plain error carrying git's stderr: dir IS in a
// repository, so the answer is unknown, not "nothing".
var ErrUnavailable = errors.New("git ls-files unavailable")

// Runner runs git with args and returns its stdout.
type Runner func(args ...string) ([]byte, error)

// ExecGit is the production Runner. It forces the C locale so isNoRepo can
// recognise git's "not a git repository" message.
func ExecGit(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=C")
	return cmd.Output()
}

// Files lists the paths under dir that git tracks and that match pathspecs,
// relative to dir, using run. Symlinks are listed like any other tracked
// path. Any git failure is reported as ErrUnavailable.
func Files(run Runner, dir string, pathspecs ...string) ([]string, error) {
	args := append([]string{"-C", dir, "--no-optional-locks", "ls-files", "-z", "--"}, pathspecs...)
	out, err := run(args...)
	if err != nil {
		if isNoRepo(err) {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return nil, fmt.Errorf("git ls-files in %s: %w%s", dir, err, stderrOf(err))
	}
	var files []string
	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) > 0 {
			files = append(files, string(f))
		}
	}
	return files, nil
}

// isNoRepo reports whether err means git is missing or dir is not in a
// repository (as opposed to git failing inside one).
func isNoRepo(err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return bytes.Contains(ee.Stderr, []byte("not a git repository"))
	}
	return false
}

// stderrOf returns ": <git stderr>" for an *exec.ExitError, or "".
func stderrOf(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
		return ": " + string(bytes.TrimSpace(ee.Stderr))
	}
	return ""
}
