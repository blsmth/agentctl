package demo

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Tmux runs tmux against the demo server only: every call carries
// `-L agentctl-demo` and the demo environment (no inherited TMUX).
type Tmux struct {
	Layout Layout
	// run is the exec seam; nil runs the real tmux binary.
	run func(env []string, args ...string) ([]byte, error)
}

// Args prefixes args with the demo socket flag.
func (t Tmux) Args(args ...string) []string {
	return append([]string{"-L", Label}, args...)
}

// Run runs tmux and returns trimmed stdout, or an error with stderr.
func (t Tmux) Run(args ...string) (string, error) {
	run := t.run
	if run == nil {
		run = execTmux
	}
	out, err := run(t.Layout.Env(""), t.Args(args...)...)
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func execTmux(env []string, args ...string) ([]byte, error) {
	cmd := exec.Command("tmux", args...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Running reports whether the demo server is up.
func (t Tmux) Running() bool {
	_, err := t.Run("list-sessions")
	return err == nil
}

// SocketEnv is the TMUX value (socket,pid,session) that makes a plain
// `tmux` call, and so agentctl itself, talk to the demo server.
func (t Tmux) SocketEnv() (string, error) {
	out, err := t.Run("display-message", "-p", "#{socket_path},#{pid},0")
	if err != nil {
		return "", err
	}
	if !strings.Contains(out, "/"+Label+",") {
		return "", fmt.Errorf("demo socket %q does not end in %s", out, Label)
	}
	return out, nil
}
