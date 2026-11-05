// Package pidfile manages PID files for long-running agentctl background
// processes (monitor, hud-server).  It provides helpers to write, remove, and
// kill-before-relaunch a process identified by its PID file.
package pidfile

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/safefs"
)

// Write creates <dir>/<name> (mode 0600) containing the current process's
// PID. The directory is created 0700 if it does not already exist. dir is
// normally the runtime dir inside the user's repository, so the write
// refuses a symlinked dir or pidfile rather than following it (see safefs).
func Write(dir, name string) error {
	path := filepath.Join(dir, name)
	data := []byte(fmt.Sprintf("%d\n", os.Getpid()))
	if err := safefs.WriteFile(dir, path, data, 0o600); err != nil {
		return fmt.Errorf("pidfile: write %s: %w", path, err)
	}
	return nil
}

// Remove deletes <dir>/<name>.  Errors are silently ignored because the
// process is likely exiting anyway.
//
// Nothing is removed when dir is a symlink (it would delete a file of that
// name wherever the link points).
func Remove(dir, name string) {
	if safefs.CheckDir(dir, dir) != nil {
		return
	}
	os.Remove(filepath.Join(dir, name)) //nolint:errcheck
}

// Validate reads <dir>/<name>, checks whether the recorded PID is alive, and
// verifies that the process is an agentctl monitor. Returns true only when all
// three checks pass. Skips the current process's own PID.
//
// Use this on monitor startup to detect a stale or racing duplicate:
//
//	if pidfile.Validate(dir, "monitor.pid") {
//	    // another monitor is already running — exit immediately
//	}
func Validate(dir, name string) bool {
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	// Never report the current process as a conflicting monitor.
	if pid == os.Getpid() {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// kill -0: check liveness without sending a real signal.
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return isMonitor(pid)
}

// isMonitor reports whether pid is an agentctl monitor. It is a variable so
// tests can stand in a fake process without relying on its command line.
var isMonitor = isAgentctlMonitor

// isAgentctlMonitor returns true if the process identified by pid has both
// "agentctl" and "monitor" in its command-line arguments. Uses POSIX ps so it
// works on both macOS and Linux without /proc.
func isAgentctlMonitor(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return false
	}
	cmdline := strings.TrimSpace(string(out))
	return strings.Contains(cmdline, "agentctl") && strings.Contains(cmdline, "monitor")
}

// Kill reads <dir>/<name>, parses the PID, and sends SIGTERM to that process.
// It then waits up to 200 ms for the process to exit before returning so the
// caller can safely launch a replacement.  All errors (file missing, bad PID,
// process not found) are silently ignored — they just mean there is nothing to
// kill.
//
// The PID file is untrusted (stale after a reboot, or written by anyone with
// access to the runtime dir), so Kill only signals a process that is not the
// caller and that is verifiably an agentctl monitor — the same guard Validate
// uses. Any other PID is left alone.
func Kill(dir, name string) {
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return // no PID file — nothing to kill
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return
	}
	if pid == os.Getpid() || !isMonitor(pid) {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	// Send SIGTERM; ignore errors (process may already be gone).
	_ = proc.Signal(syscall.SIGTERM)

	// Wait briefly so the old process releases resources (e.g. tmux pane
	// locks) before the replacement starts.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		// kill -0 checks whether the process is still alive.
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			break // process has exited
		}
		time.Sleep(20 * time.Millisecond)
	}
}
