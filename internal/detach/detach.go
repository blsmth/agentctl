// Package detach starts child processes that outlive the calling process.
package detach

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/selfpath"
)

// DepthEnv carries the detached-spawn nesting depth into the child. Nothing
// legitimate nests detached spawns more than a couple of levels, so Start
// refuses past MaxDepth: a hook loop dies instead of growing exponentially.
const DepthEnv = "AGENTCTL_DETACH_DEPTH"

// MaxDepth is the inherited depth at which Start refuses to spawn.
const MaxDepth = 3

// ErrDepth is returned by Start when the nesting depth limit is reached.
var ErrDepth = errors.New("detached spawn depth limit reached")

// depthOf parses the inherited depth; unset or garbage counts as 0.
func depthOf(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// withDepth returns env (nil = os.Environ) with DepthEnv set to depth+1.
func withDepth(env []string, depth int) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if len(e) > len(DepthEnv) && e[:len(DepthEnv)+1] == DepthEnv+"=" {
			continue
		}
		out = append(out, e)
	}
	return append(out, DepthEnv+"="+strconv.Itoa(depth+1))
}

// isSelf reports whether exe is the running binary.
func isSelf(exe string) bool {
	self, err := selfpath.Lookup()
	if err != nil {
		return false
	}
	a, errA := filepath.EvalSymlinks(exe)
	b, errB := filepath.EvalSymlinks(self)
	if errA != nil || errB != nil {
		return exe == self
	}
	return a == b
}

// Start launches exe with args in its own session, with stdin/stdout/stderr
// pointed at /dev/null, and releases it without waiting. env nil means
// inherit the current environment. The child keeps running after the caller
// exits.
func Start(exe string, args, env []string) error {
	if testing.Testing() && isSelf(exe) {
		return selfpath.ErrTestBinary
	}
	depth := depthOf(os.Getenv(DepthEnv))
	if depth >= MaxDepth {
		logging.Log("detach: refusing to spawn %s %v: %s=%d >= %d", exe, args, DepthEnv, depth, MaxDepth)
		return fmt.Errorf("%w (%s=%d)", ErrDepth, DepthEnv, depth)
	}
	env = withDepth(env, depth)

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
	exe, err := selfpath.Spawnable()
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
