package closedlog

import (
	"path/filepath"
	"strings"
	"time"
)

// Recent project directories: the candidates the hub's "+ open" dropdown and
// the phone's "Open project" sheet list, from the closed ledger (newest
// first) plus an optional origin dir.

const (
	// RecentWindow is how far back the ledger is read for recent dirs.
	RecentWindow = 30 * 24 * time.Hour
	// RecentScan bounds how many candidate dirs are examined (stat'ed).
	RecentScan = 60
)

// DirKey is p with symlinks resolved, or the cleaned p when it cannot be: the
// identity two spellings of one directory share.
func DirKey(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// IsWorktreePath reports a path inside a repo's .worktrees directory.
func IsWorktreePath(p string) bool {
	return strings.Contains(filepath.ToSlash(filepath.Clean(p))+"/", "/.worktrees/")
}

// RecentDirs lists origin (when set) and then the records' project dirs,
// deduplicated by DirKey, without relative paths, worktree paths and dirs
// isDir rejects (gone). At most RecentScan candidates are examined.
func RecentDirs(origin string, recs []Record, isDir func(string) bool) []string {
	cands := make([]string, 0, len(recs)+1)
	if origin != "" {
		cands = append(cands, origin)
	}
	for _, r := range recs {
		if r.ProjectDir != "" {
			cands = append(cands, r.ProjectDir)
		}
	}
	seen := map[string]bool{}
	var out []string
	for i, c := range cands {
		if i >= RecentScan {
			break
		}
		if !filepath.IsAbs(c) || IsWorktreePath(c) {
			continue
		}
		k := DirKey(c)
		if seen[k] || IsWorktreePath(k) {
			continue
		}
		seen[k] = true
		if isDir(k) {
			out = append(out, filepath.Clean(c))
		}
	}
	return out
}

// WithoutDirs is dirs minus those that name (by DirKey) one of exclude, in
// order. The hub and the phone drop projects that already have a live group.
func WithoutDirs(dirs, exclude []string) []string {
	skip := make(map[string]bool, len(exclude))
	for _, d := range exclude {
		if d != "" {
			skip[DirKey(d)] = true
		}
	}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if !skip[DirKey(d)] {
			out = append(out, d)
		}
	}
	return out
}
