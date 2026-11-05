package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/gittracked"
)

// allowedTrackedRuntimeFile is the only path under the runtime dir a
// repository may legitimately commit: the project-local config. Everything
// else in .agentctl/ is runtime state agentctl itself writes.
const allowedTrackedRuntimeFile = "config.json"

// UnsafeRuntimeDirError reports a runtime directory whose contents a cloned
// repository controls. agentctl must not write into it: a committed symlink
// such as `.agentctl/settings/<agent>.json -> ~/.zshenv` turns an ordinary
// settings write into code execution in the user's shell.
type UnsafeRuntimeDirError struct {
	Dir     string
	Reason  string
	Tracked []string // offending repo-relative paths, when Reason concerns tracked content
}

func (e *UnsafeRuntimeDirError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "refusing to use runtime dir %s: %s", e.Dir, e.Reason)
	for _, f := range e.Tracked {
		fmt.Fprintf(&b, "\n  %s", f)
	}
	if len(e.Tracked) > 0 {
		b.WriteString("\nOnly .agentctl/config.json (a regular file) may be committed. Do not run agentctl in a repository you do not trust; if you do trust it, remove the rest from the index (git rm --cached).")
	}
	return b.String()
}

// gitRunner is the git invocation CheckRuntimeDir uses; tests replace it.
var gitRunner gittracked.Runner = gittracked.ExecGit

// CheckRuntimeDir verifies that the resolved runtime dir is safe to write
// into, and must run before EnsureRuntimeDir for any invocation that writes
// runtime state. It refuses when:
//
//   - the runtime dir itself is a symlink;
//   - (withGit) the enclosing git repository tracks anything under it other
//     than config.json, or tracks config.json as a symlink.
//
// withGit=false runs only the free Lstat check; the hook fast path uses it
// when its runtime dir was pinned by a session start that already ran the
// full check. Not being in a git repository, or git being unavailable, is
// not an error. Returns nil when Init has not been called.
func CheckRuntimeDir(withGit bool) error {
	if runtimeDir == "" {
		return nil
	}
	run := gitRunner
	if !withGit {
		run = nil
	}
	return checkRuntimeDir(runtimeDir, run)
}

// checkRuntimeDir is CheckRuntimeDir against an explicit dir and runner. A
// nil run skips the git check.
func checkRuntimeDir(dir string, run gittracked.Runner) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		// Nothing on disk, so nothing a repo could have planted; it will be
		// created fresh by EnsureRuntimeDir.
		return nil
	}
	if err != nil {
		return &UnsafeRuntimeDirError{Dir: dir, Reason: err.Error()}
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return &UnsafeRuntimeDirError{Dir: dir, Reason: "it is a symlink"}
	}
	if !fi.IsDir() {
		return &UnsafeRuntimeDirError{Dir: dir, Reason: "it is not a directory"}
	}
	if run == nil {
		return nil
	}

	// Git pathspecs are case-sensitive but the default macOS (APFS/HFS+)
	// filesystem is not: a repo committing ".AGENTCTL/x" plants files in the
	// very directory agentctl resolves as ".agentctl". So ask git
	// case-insensitively, then keep only entries whose top-level directory
	// is the runtime dir on THIS filesystem (os.SameFile), so a distinct
	// ".AGENTCTL" on a case-sensitive filesystem is not a false positive.
	parent, base := filepath.Dir(dir), filepath.Base(dir)
	files, err := gittracked.Files(run, parent, ":(icase)"+base)
	if errors.Is(err, gittracked.ErrUnavailable) {
		return nil // not a git repo, or no git: nothing is tracked
	}
	if err != nil {
		return &UnsafeRuntimeDirError{Dir: dir, Reason: "cannot tell which files the repository tracks in it: " + err.Error()}
	}
	var bad []string
	for _, f := range files {
		top, rest, _ := strings.Cut(f, "/")
		topFi, err := os.Lstat(filepath.Join(parent, top))
		if err == nil && !os.SameFile(topFi, fi) {
			continue // a different directory that merely differs in case
		}
		if strings.EqualFold(rest, allowedTrackedRuntimeFile) && !isNonRegular(filepath.Join(dir, rest)) {
			continue
		}
		bad = append(bad, f)
	}
	if len(bad) > 0 {
		return &UnsafeRuntimeDirError{Dir: dir, Reason: "the repository tracks files inside it:", Tracked: bad}
	}
	return nil
}

// isNonRegular reports whether path exists as something other than a
// regular file (a symlink, directory, ...), without following a symlink. A
// missing path (tracked but deleted from the work tree) is harmless.
func isNonRegular(path string) bool {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	return err != nil || !fi.Mode().IsRegular()
}
