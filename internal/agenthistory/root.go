package agenthistory

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/claudehome"
)

const worktreesDir = ".worktrees"

// CanonicalRoot is the key a project dir is stored under: symlinks resolved
// and a linked worktree (<repo>/.worktrees/<slug>[/...]) mapped to its main
// checkout, so every spelling of a project reaches one store. A path that does
// not exist is only cleaned.
func CanonicalRoot(dir string) string {
	dir = filepath.Clean(dir)
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	if i := strings.Index(dir+string(filepath.Separator), string(filepath.Separator)+worktreesDir+string(filepath.Separator)); i > 0 {
		return dir[:i]
	}
	// A linked worktree elsewhere: .git is a file pointing into
	// <main>/.git/worktrees/<name>.
	if data, err := os.ReadFile(filepath.Join(dir, ".git")); err == nil {
		if gd, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:"); ok {
			gd = filepath.Clean(strings.TrimSpace(gd))
			if i := strings.Index(gd, string(filepath.Separator)+".git"+string(filepath.Separator)+"worktrees"+string(filepath.Separator)); i > 0 {
				return gd[:i]
			}
		}
	}
	return dir
}

// canonicalPath resolves symlinks in the directory part of an absolute path
// (the final element may itself be a tracked symlink) and maps a path inside a
// linked worktree to the same path in the main checkout.
func canonicalPath(p string) string {
	p = filepath.Clean(p)
	dir, base := filepath.Split(p)
	d := filepath.Clean(dir)
	if r, err := filepath.EvalSymlinks(d); err == nil {
		d = r
	}
	p = filepath.Join(d, base)
	if i := strings.Index(p, string(filepath.Separator)+worktreesDir+string(filepath.Separator)); i > 0 {
		rest := p[i+len(worktreesDir)+2:]
		if _, tail, ok := strings.Cut(rest, string(filepath.Separator)); ok {
			return filepath.Join(p[:i], tail)
		}
		return p[:i]
	}
	return p
}

// projectRootOf finds the project dir owning an absolute path under a
// .claude/agents dir: the part before the last ".claude/agents" element pair.
func projectRootOf(p string) (string, bool) {
	parts := strings.Split(filepath.Clean(p), string(filepath.Separator))
	for i := len(parts) - 2; i >= 1; i-- {
		if parts[i] == ".claude" && parts[i+1] == "agents" {
			return string(filepath.Separator) + filepath.Join(parts[:i]...), true
		}
	}
	return "", false
}

// isHomeRoot reports whether root is the dir whose .claude is the global home.
func isHomeRoot(root string) bool {
	h := claudehome.HomeDir()
	if h == "" {
		return false
	}
	return CanonicalRoot(root) == CanonicalRoot(filepath.Dir(h))
}
