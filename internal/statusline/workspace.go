package statusline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// branchGlyph prefixes the branch segment on renderRichLine3. U+2387
// ALTERNATIVE KEY SYMBOL (⎇) is the conventional "branch"-adjacent glyph
// used by several shell prompts when a real branch icon (which needs a
// patched/Nerd Font) isn't assumed available. It must render at exactly one
// terminal column -- see TestBranchGlyphIsOneColumnWide, the same discipline
// TestPadRuneIsOneColumnWide applies to padRune in rich_test.go.
const branchGlyph = '⎇'

// homeDirForDisplay is a package var (not a direct os.UserHomeDir() call at
// every call site) so tests can override it without mutating the real
// process environment via t.Setenv("HOME", ...), which is not portable
// across platforms os.UserHomeDir() consults (HOME on Unix, USERPROFILE on
// Windows).
var homeDirForDisplay = os.UserHomeDir

// maxDirDisplayWidth is the column budget formatDir renders a directory
// into before falling back to a two-component ellipsis truncation (spec:
// "If the result exceeds 40 columns...").
const maxDirDisplayWidth = 40

// formatDir renders dir home-relative with a leading tilde, then -- if the
// result still exceeds maxDirDisplayWidth -- collapses it to the last two
// path components prefixed with an ellipsis (e.g. "…/agentctl/internal").
// dir is returned unmodified (beyond the tilde substitution) when it
// already fits.
func formatDir(dir string) string {
	display := dir
	if home, err := homeDirForDisplay(); err == nil && home != "" {
		switch {
		case dir == home:
			display = "~"
		case strings.HasPrefix(dir, home+string(os.PathSeparator)):
			display = "~" + dir[len(home):]
		}
	}

	if lipgloss.Width(display) <= maxDirDisplayWidth {
		return display
	}

	parts := strings.Split(filepath.ToSlash(display), "/")
	var nonEmpty []string
	for _, part := range parts {
		if part != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}
	switch {
	case len(nonEmpty) >= 2:
		return "…/" + strings.Join(nonEmpty[len(nonEmpty)-2:], "/")
	case len(nonEmpty) == 1:
		return "…/" + nonEmpty[0]
	default:
		return display
	}
}

// gitBranch resolves the checked-out branch name for dir WITHOUT shelling
// out to git -- this is the one piece of git plumbing simple enough (and
// hot-path-sensitive enough, at 1Hz) to reimplement directly rather than pay
// a subprocess. It walks up from dir looking for a `.git` entry:
//
//   - `.git` as a DIRECTORY (a normal checkout): read `<dir>/.git/HEAD`.
//   - `.git` as a FILE (a linked worktree, e.g. every agentctl
//     `.worktrees/<slug>/` checkout): parse its `gitdir: <path>` line and
//     read `<gitdir>/HEAD` instead, resolving a relative gitdir path against
//     the directory that contained the `.git` file.
//
// `ref: refs/heads/X` in HEAD yields `X`. A bare 40-hex-character SHA means
// a detached HEAD; the first 7 characters are returned instead.
//
// Any error at any step -- `.git` not found walking all the way to the
// filesystem root, an unreadable HEAD, a HEAD that parses to neither a ref
// nor a SHA -- returns "" rather than a partial or misleading value. This
// function must never panic: every filesystem op is check-then-use, never
// assumed to succeed.
func gitBranch(dir string) string {
	containingDir, gitPath, ok := findGitEntry(dir)
	if !ok {
		return ""
	}

	info, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}

	var headPath string
	if info.IsDir() {
		headPath = filepath.Join(gitPath, "HEAD")
	} else {
		data, err := os.ReadFile(gitPath)
		if err != nil {
			return ""
		}
		gitdir, ok := parseGitdirFile(string(data))
		if !ok {
			return ""
		}
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(containingDir, gitdir)
		}
		headPath = filepath.Join(gitdir, "HEAD")
	}

	data, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}
	return parseHeadRef(string(data))
}

// findGitEntry walks up from dir (inclusive) to the filesystem root looking
// for a `.git` entry (directory or file), returning the directory that
// contains it and the entry's full path. ok is false if none is found by
// the time the walk reaches the root.
func findGitEntry(dir string) (containingDir, gitPath string, ok bool) {
	dir = filepath.Clean(dir)
	for {
		candidate := filepath.Join(dir, ".git")
		if _, err := os.Stat(candidate); err == nil {
			return dir, candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
}

// parseGitdirFile extracts the path from a `.git` FILE's `gitdir: <path>`
// line (the shape git writes for every linked worktree). ok is false if no
// such line is present.
func parseGitdirFile(content string) (gitdir string, ok bool) {
	const prefix = "gitdir:"
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
		}
	}
	return "", false
}

// parseHeadRef parses the contents of a git HEAD file: either a symbolic
// ref (`ref: refs/heads/X`, yielding X) or a detached HEAD's 40-hex-char SHA
// (yielding its first 7 characters). Returns "" for anything else.
func parseHeadRef(content string) string {
	line := strings.TrimSpace(content)

	const refPrefix = "ref: "
	if strings.HasPrefix(line, refPrefix) {
		ref := strings.TrimPrefix(line, refPrefix)
		const headsPrefix = "refs/heads/"
		if strings.HasPrefix(ref, headsPrefix) {
			return strings.TrimPrefix(ref, headsPrefix)
		}
		return ref
	}

	if isHexSHA(line) {
		return line[:7]
	}
	return ""
}

// isHexSHA reports whether s is exactly 40 hexadecimal characters -- the
// shape of a detached-HEAD SHA-1 commit hash.
func isHexSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// gitDirtyCountTimeout bounds gitDirtyCount's subprocess -- see its doc
// comment for why this is the sole exception to agentctl's no-subprocess-
// in-statusline rule.
const gitDirtyCountTimeout = 150 * time.Millisecond

// gitDirtyCount is the SOLE exception to the no-subprocess contract
// RunWithIO's doc comment otherwise states for this package. `git status
// --porcelain` has no reimplementation nearly as simple as gitBranch's
// direct HEAD read (it needs the index, the working tree diff, and
// gitignore rules), so this one path shells out -- hard-bounded by
// gitDirtyCountTimeout (150ms) derived from ctx via
// exec.CommandContext, so a slow or wedged git can never block the
// statusline render past that budget.
//
// Measured at ~30ms against the agentctl repo itself, but that number is
// repo-size dependent (a much larger repo, or one on a slow/networked
// filesystem, could plausibly approach or exceed the timeout) -- ok=false
// on ANY error, non-zero exit, or timeout, and the caller's correct,
// expected response is to omit the dirty-count segment entirely, never to
// wait longer or retry. Degrading to "no dirty marker" is strictly better
// than a slow or hung statusline render.
//
// --no-optional-locks is load-bearing, not an optimization. Plain `git
// status` opportunistically takes .git/index.lock to write back a refreshed
// index. This runs on every statusline refresh in every session, and the
// timeout above kills git with SIGKILL; a kill while the lock is held leaves
// index.lock behind permanently, and concurrent runs contend with real
// `git add`/`commit`. The flag (a global option, so it precedes the
// subcommand) makes the call strictly read-only.
func gitDirtyCount(ctx context.Context, dir string) (count int, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, gitDirtyCountTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, true
}
