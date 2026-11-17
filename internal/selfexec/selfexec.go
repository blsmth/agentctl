// Package selfexec implements hot reload for long-running agentctl processes:
// capture the identity of the running executable at startup, detect that
// `just install` replaced it, and re-exec the new image with the same args.
package selfexec

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/buildid"
	"github.com/blsmth/agentctl/internal/selfpath"
)

// MinAge is how old the new binary's mtime must be before it is exec'd, so a
// half-written install is never run.
const MinAge = time.Second

// Identity is the on-disk identity of an executable.
type Identity = buildid.Identity

// Capture resolves the running executable and returns its identity. It errors
// when hot reload must be disabled for this process (ephemeral path or
// unresolvable/unstattable executable).
func Capture() (Identity, error) {
	exe, err := selfpath.Spawnable()
	if err != nil {
		return Identity{}, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if selfpath.IsEphemeral(exe) {
		return Identity{}, fmt.Errorf("ephemeral binary %s", exe)
	}
	id, err := buildid.Stat(exe)
	if err != nil {
		return Identity{}, fmt.Errorf("stat %s: %w", exe, err)
	}
	id.PID = os.Getpid()
	return id, nil
}

// Changed reports whether the binary at cur.Path should trigger a re-exec: it
// is not an ephemeral (temp / go-build) path, its identity differs from start,
// it is a regular executable file, and its mtime is at least MinAge before now.
func Changed(start, cur Identity, now time.Time) bool {
	if selfpath.IsEphemeral(cur.Path) {
		return false
	}
	if buildid.SameBuild(start, cur) {
		return false
	}
	if !cur.Mode.IsRegular() || cur.Mode.Perm()&0o111 == 0 {
		return false
	}
	mtime := time.Unix(cur.MtimeUnix, cur.MtimeNsec)
	return now.Sub(mtime) >= MinAge
}

// CheckChanged stats start.Path and reports whether the binary was replaced
// (see Changed). A missing file (mid-install) reports false.
func CheckChanged(start Identity, now time.Time) bool {
	if start.Path == "" {
		return false
	}
	cur, err := buildid.Stat(start.Path)
	if err != nil {
		return false
	}
	return Changed(start, cur, now)
}

// Reexec replaces the process image with the binary at path, keeping the
// original args and environment. It returns only on failure.
func Reexec(path string) error {
	return syscall.Exec(path, os.Args, os.Environ())
}

// ReexecEnv is like Reexec but adds extra KEY=VALUE entries to the environment.
func ReexecEnv(path string, extra ...string) error {
	return syscall.Exec(path, os.Args, append(os.Environ(), extra...))
}
