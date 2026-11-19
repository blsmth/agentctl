// Package detach starts child processes that outlive the calling process.
package detach

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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

// startSelfFn is StartSelf's implementation; SetStartSelfForTest replaces it
// so spawn sites can assert the args and env they would launch with.
var startSelfFn = func(args, env []string) error {
	exe, err := selfpath.Spawnable()
	if err != nil {
		return fmt.Errorf("resolving executable: %w", err)
	}
	return Start(exe, args, env)
}

// StartSelf is Start with exe set to the current executable.
func StartSelf(args, env []string) error {
	return startSelfFn(args, env)
}

// SetStartSelfForTest replaces what StartSelf (and so StartDaemon) runs and
// returns a restore func. It exists so tests of spawn sites never fork.
func SetStartSelfForTest(fn func(args, env []string) error) (restore func()) {
	old := startSelfFn
	startSelfFn = fn
	return func() { startSelfFn = old }
}

// StartDaemon starts a long-lived or multi-window agentctl process (metrics
// serve, reconcile) detached, with DaemonEnv(os.Environ(), runtimeDir): the
// spawning window's Claude account is stripped and, when runtimeDir is not
// empty, AGENTCTL_RUNTIME_DIR is pinned to it so a process spawned from a hook
// or tmux run-shell resolves that project's runtime dir whatever its cwd is.
func StartDaemon(args []string, runtimeDir string) error {
	return StartSelf(args, DaemonEnv(os.Environ(), runtimeDir))
}

// DaemonEnv returns env with the spawning window's account removed
// (StripAccountEnv) and, when runtimeDir is not empty, AGENTCTL_RUNTIME_DIR
// set to it.
func DaemonEnv(env []string, runtimeDir string) []string {
	env = StripAccountEnv(env)
	if runtimeDir == "" {
		return env
	}
	env = removeVars(env, envRuntimeDir)
	return append(env, envRuntimeDir+"="+runtimeDir)
}

const envRuntimeDir = "AGENTCTL_RUNTIME_DIR"

// The per-window Claude account variables (claudehome.EnvConfigDir,
// claudehome.EnvAccount, and the subprocess env scrub switch set by the
// Claude harness). Spelled out here because detach is a leaf package; a
// claudehome test keeps the names in sync.
const (
	EnvConfigDir       = "CLAUDE_CONFIG_DIR"
	EnvAccount         = "AGENTCTL_ACCOUNT"
	EnvSubprocessScrub = "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"
)

// StripAccountEnv returns a copy of env without the Claude account a window
// launch set for that one window, so a process that outlives the window or
// serves several windows does not resolve claudehome.DefaultDir/Current to
// whichever window happened to spawn it.
//
// AGENTCTL_ACCOUNT and CLAUDE_CODE_SUBPROCESS_ENV_SCRUB are always removed:
// agentctl's launch env is their only source. CLAUDE_CONFIG_DIR is removed
// only together with AGENTCTL_ACCOUNT, the marker that a launch picked a
// named account for the window (dispatch.ApplyAccountEnv sets both). Without
// it the variable was not set per window (the unnamed default dir is
// selected by unsetting it), so it is the user's own machine-wide setting,
// e.g. a single login in a custom dir with no accounts configured, and is
// kept.
func StripAccountEnv(env []string) []string {
	drop := []string{EnvAccount, EnvSubprocessScrub}
	if hasVar(env, EnvAccount) {
		drop = append(drop, EnvConfigDir)
	}
	return removeVars(env, drop...)
}

// hasVar reports whether env assigns name.
func hasVar(env []string, name string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, name+"=") {
			return true
		}
	}
	return false
}

// removeVars returns a copy of env without any assignment to names.
func removeVars(env []string, names ...string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if !slices.Contains(names, k) {
			out = append(out, e)
		}
	}
	return out
}
