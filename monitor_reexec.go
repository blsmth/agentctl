package main

import (
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/buildid"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/selfpath"
)

// Hot reload: the monitor re-executes itself when its binary on disk changes.
//
// At startup the monitor records the identity (path, dev, inode, size, mtime)
// of its own executable in <runtimeDir>/monitor.build. Each tick it stats that
// path; when the identity changed, the file is a regular executable and its
// mtime is at least reexecMinAge old (so a half-written install is never
// exec'd), it calls syscall.Exec. Exec keeps the PID, so monitor.pid, the
// registry entry and the pidfile identity check stay valid, and the deferred
// pidfile/registry cleanup deliberately does not run.
//
// Known consequences of a re-exec, all in-memory state resets:
//   - the done-unseen check-mark glyph can reappear on windows already viewed;
//   - Prometheus counters restart from zero;
//   - a done edge landing during the roughly one second gap is not observed.
//     The hasPrev gate in detectAndFireTransitions already prevents re-firing
//     old edges after the restart.
//
// The hook watchdog (internal/hooks/watchdog.go) replaces monitors that predate
// this mechanism and never wrote monitor.build.

const (
	reexecMinAge     = time.Second
	reexecRetryEvery = 30 * time.Second
)

// exeIdentity is the on-disk identity of an executable.
type exeIdentity = buildid.Identity

// exeChanged reports whether the binary at cur.Path should trigger a re-exec:
// it is not an ephemeral (temp / go-build) path, its identity differs from
// start, it is a regular executable file, and its mtime is at least
// reexecMinAge before now.
func exeChanged(start, cur exeIdentity, now time.Time) bool {
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
	return now.Sub(mtime) >= reexecMinAge
}

// recordExeIdentity resolves the running executable, writes its identity to
// monitor.build and returns it. ok is false when hot reload is disabled for
// this process (ephemeral path or unresolvable executable).
func recordExeIdentity(runtimeDir string) (id exeIdentity, ok bool) {
	exe, err := selfpath.Resolve(os.Executable)
	if err != nil {
		logging.Log("monitor: hot reload disabled: %v", err)
		return exeIdentity{}, false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if selfpath.IsEphemeral(exe) {
		logging.Log("monitor: hot reload disabled: ephemeral binary %s", exe)
		return exeIdentity{}, false
	}
	id, err = buildid.Stat(exe)
	if err != nil {
		logging.Log("monitor: hot reload disabled: stat %s: %v", exe, err)
		return exeIdentity{}, false
	}
	id.PID = os.Getpid()
	if err := buildid.Write(runtimeDir, id); err != nil {
		logging.Log("monitor: write %s: %v", buildid.FileName, err)
	}
	logging.Log("monitor: binary %s dev=%d ino=%d size=%d mtime=%d", id.Path, id.Dev, id.Ino, id.Size, id.MtimeUnix)
	return id, true
}

// maybeReexec re-executes the monitor when its binary was replaced. It returns
// only when no exec happened (or exec failed). Failed attempts are retried at
// most once per reexecRetryEvery.
func (m *monitorModel) maybeReexec(now time.Time) {
	if m.exeStart.Path == "" {
		return
	}
	if !m.lastReexecTry.IsZero() && now.Sub(m.lastReexecTry) < reexecRetryEvery {
		return
	}
	cur, err := buildid.Stat(m.exeStart.Path)
	if err != nil {
		return // e.g. missing mid-install
	}
	if !exeChanged(m.exeStart, cur, now) {
		return
	}
	m.lastReexecTry = now
	logging.Log("monitor: binary changed, re-executing %s", cur.Path)
	exec := m.execFn
	if exec == nil {
		exec = syscall.Exec
	}
	if err := exec(cur.Path, os.Args, os.Environ()); err != nil {
		logging.Warn("monitor: re-exec %s failed, keeping old image: %v", cur.Path, err)
	}
}
