// Package detach starts child processes that outlive the calling process.
package detach

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Start launches exe with args in its own session, with stdin/stdout/stderr
// pointed at /dev/null, and releases it without waiting. env nil means
// inherit the current environment. The child keeps running after the caller
// exits.
func Start(exe string, args, env []string) error {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening %s: %w", os.DevNull, err)
	}
	defer devNull.Close()

	c := exec.Command(exe, args...)
	c.Env = env
	c.Stdin = devNull
	c.Stdout = devNull
	c.Stderr = devNull
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", exe, err)
	}
	if err := c.Process.Release(); err != nil {
		return fmt.Errorf("releasing %s: %w", exe, err)
	}
	return nil
}

// StartSelf is Start with exe set to the current executable.
func StartSelf(args, env []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable: %w", err)
	}
	return Start(exe, args, env)
}

// StartSelfPinned is StartSelf with AGENTCTL_RUNTIME_DIR pinned to runtimeDir
// in the child's environment, so a process spawned from a hook or tmux
// run-shell resolves this project's runtime dir whatever its cwd is.
func StartSelfPinned(args []string, runtimeDir string) error {
	env := append(os.Environ(), "AGENTCTL_RUNTIME_DIR="+runtimeDir)
	return StartSelf(args, env)
}
