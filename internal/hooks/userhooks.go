package hooks

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
)

// userHookTimeout is the per-command timeout for user lifecycle hooks.
// It is a variable so tests can shorten it without rebuilding.
var userHookTimeout = 30 * time.Second

// runUserHooks fires user-configured shell commands for the given lifecycle
// event. It is fire-and-forget: commands run in a goroutine so callers are
// never blocked. Commands run sequentially within the goroutine, with a 30s
// timeout each. If a command fails the error is logged and execution continues
// to the next command.
//
// extraEnv is merged with os.Environ() so PATH, HOME, etc. are all available.
func runUserHooks(event string, extraEnv map[string]string) {
	cfg := config.Load()
	if detachUserHooksFn != nil {
		if !hasUserHooks(cfg, event) {
			return
		}
		// A hook process exits right after its handler returns, which kills
		// any goroutine. Hand the work to a detached child that inherits the
		// environment (plus extras) instead.
		env := os.Environ()
		for k, v := range extraEnv {
			env = append(env, k+"="+v)
		}
		err := detachUserHooksFn(event, env)
		if err == nil {
			return
		}
		logging.Warn("user-hook: event=%s detach failed, running in-process: %v", event, err)
	}

	agentName := os.Getenv("AGENTCTL_AGENT_NAME")
	var agentCfg config.AgentConfig
	if agentName != "" {
		agentCfg = cfg.ForAgent(agentName)
	}

	cmds := cfg.EffectiveHooks(event, agentCfg)
	if len(cmds) == 0 {
		return
	}

	// Build the environment once: base env + caller-supplied extras.
	env := os.Environ()
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}

	go func() {
		for _, cmd := range cmds {
			runSingleUserHook(event, cmd, env)
		}
	}()
}

// detachUserHooksFn, when set, starts `agentctl run-user-hooks <event>` fully
// detached with the given environment. Nil (the default, and in tests) keeps
// the in-process goroutine, which only works in long-lived processes.
var detachUserHooksFn func(event string, env []string) error

// SetDetachUserHooks enables detached user hooks; spawn must start
// `agentctl run-user-hooks <event>` fully detached with env. main.go wires it.
func SetDetachUserHooks(spawn func(event string, env []string) error) {
	detachUserHooksFn = spawn
}

// hasUserHooks reports whether any hook command is configured for event.
func hasUserHooks(cfg *config.Config, event string) bool {
	var agentCfg config.AgentConfig
	if name := os.Getenv("AGENTCTL_AGENT_NAME"); name != "" {
		agentCfg = cfg.ForAgent(name)
	}
	return len(cfg.EffectiveHooks(event, agentCfg)) > 0
}

// RunUserHooksSync runs the user hooks for event to completion in the calling
// process, in order, each under userHookTimeout. It is the body of the
// detached `agentctl run-user-hooks` child.
func RunUserHooksSync(event string) {
	cfg := config.Load()
	var agentCfg config.AgentConfig
	if name := os.Getenv("AGENTCTL_AGENT_NAME"); name != "" {
		agentCfg = cfg.ForAgent(name)
	}
	env := os.Environ()
	for _, cmd := range cfg.EffectiveHooks(event, agentCfg) {
		runSingleUserHook(event, cmd, env)
	}
}

// runSingleUserHook executes one user hook command via "sh -c" with the
// supplied environment. stdout and stderr are captured and written to the log.
//
// The hook command runs in its own process group (Setpgid) so that when the
// timeout fires, we can signal every process the shell spawned -- not just
// the shell itself. This matters because "sh -c '<cmd>'" does not always
// exec-replace itself with <cmd>: some shells fork a real child for it (e.g.
// dash on Linux, depending on the command), in which case killing only the
// direct "sh" process leaves the actual work running and its stdout/stderr
// pipes open, so CombinedOutput blocks until the full command finishes on
// its own -- exactly the timeout-not-enforced bug this guards against. Same
// process-group-kill pattern as internal/tmux.sigTermPaneGroups.
func runSingleUserHook(event, cmd string, env []string) {
	logging.Log("user-hook: event=%s running: %s", event, cmd)
	ctx, cancel := context.WithTimeout(context.Background(), userHookTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Env = env
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process != nil {
			// Negative PID targets the whole process group.
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		}
		return os.ErrProcessDone
	}
	// Belt-and-suspenders: if the process group kill above still leaves some
	// descendant holding the stdout/stderr pipes open, force them closed
	// shortly after the timeout instead of blocking forever.
	c.WaitDelay = 2 * time.Second
	out, err := c.CombinedOutput()
	if len(out) > 0 {
		logging.Log("user-hook: event=%s output: %s", event, strings.TrimSpace(string(out)))
	}
	if err != nil {
		logging.Warn("user-hook: event=%s cmd=%q error: %v", event, cmd, err)
	}
}
