// Package selfpath resolves the absolute path of the running agentctl binary.
//
// Hooks, popups and relaunch scripts must invoke the binary that is actually
// running, not a copy at a conventional install location: `go install`,
// release tarballs and package managers all put it somewhere else, and a stale
// ~/.local/bin copy must never be preferred over the running one.
package selfpath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Fallback is returned by Path when the running executable cannot be
// determined; it relies on PATH lookup.
const Fallback = "agentctl"

// Resolve returns the symlink-free absolute path reported by lookup
// (normally os.Executable). If the symlink target cannot be resolved the
// unresolved path is returned, since it is still a valid way to run the binary.
func Resolve(lookup func() (string, error)) (string, error) {
	exe, err := lookup()
	if err != nil {
		return "", fmt.Errorf("locating executable: %w", err)
	}
	if exe == "" {
		return "", fmt.Errorf("locating executable: empty path")
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}

// Path returns the resolved path of the running binary, or Fallback if it
// cannot be determined.
func Path() string {
	exe, err := Resolve(os.Executable)
	if err != nil {
		return Fallback
	}
	return exe
}

// ErrEphemeral is returned by Persisted when the running binary lives in a
// temporary or go-build location that will not exist later.
var ErrEphemeral = errors.New("running binary is in a temporary or go-build directory")

// Persisted returns the command prefix to write into long-lived config such
// as Claude Code settings.json hooks. Unlike Path it must stay valid across
// upgrades, so it never persists a symlink-resolved (e.g. Homebrew Cellar)
// path:
//
//   - if lookPath("agentctl") resolves to the same file as the running binary,
//     the bare name "agentctl" is returned;
//   - otherwise the unresolved executable path, shell-quoted when needed;
//   - if the binary is under tmpDir or a go-build directory, ErrEphemeral.
func Persisted(lookup func() (string, error), lookPath func(string) (string, error), tmpDir string) (string, error) {
	exe, err := lookup()
	if err != nil {
		return "", fmt.Errorf("locating executable: %w", err)
	}
	if exe == "" {
		return "", fmt.Errorf("locating executable: empty path")
	}
	if isEphemeral(exe, tmpDir) {
		return "", fmt.Errorf("%w: %s (install agentctl somewhere permanent, e.g. `go install` or a release tarball, and re-run)", ErrEphemeral, exe)
	}
	if found, err := lookPath(Fallback); err == nil && sameFile(found, exe) {
		return Fallback, nil
	}
	return Quote(exe), nil
}

func isEphemeral(exe, tmpDir string) bool {
	cands := []string{exe}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		cands = append(cands, r)
	}
	roots := []string{}
	if tmpDir != "" {
		roots = append(roots, filepath.Clean(tmpDir))
		if r, err := filepath.EvalSymlinks(tmpDir); err == nil {
			roots = append(roots, r)
		}
	}
	for _, c := range cands {
		if strings.Contains(filepath.ToSlash(c), "/go-build") {
			return true
		}
		for _, root := range roots {
			if c == root || strings.HasPrefix(c, root+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

func sameFile(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	ia, err := os.Stat(ra)
	if err != nil {
		return false
	}
	ib, err := os.Stat(rb)
	if err != nil {
		return false
	}
	return os.SameFile(ia, ib)
}

// Quote single-quotes s for POSIX sh when it contains anything outside a safe
// character set; safe strings are returned unchanged.
func Quote(s string) string {
	safe := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@%~", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
