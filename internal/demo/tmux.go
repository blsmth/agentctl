package demo

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Tmux runs tmux against the demo server only: every call carries
// `-L <label>` and the demo environment (no inherited TMUX).
type Tmux struct {
	Layout Layout
	// run is the exec seam; nil runs the real tmux binary.
	run func(env []string, args ...string) ([]byte, error)
}

// Args prefixes args with the demo socket flag.
func (t Tmux) Args(args ...string) []string {
	return append([]string{"-L", t.Layout.Label()}, args...)
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
	if !strings.Contains(out, "/"+t.Layout.Label()+",") {
		return "", fmt.Errorf("demo socket %q does not end in %s", out, t.Layout.Label())
	}
	return out, nil
}

// CheckOwner refuses to touch a running server on this label that records a
// different root. A server with no recorded root (older harness) is accepted.
func (t Tmux) CheckOwner() error {
	if !t.Running() {
		return nil
	}
	out, err := t.Run("show-environment", "-g", ownerVar)
	if err != nil {
		return nil // variable not set
	}
	owner, ok := strings.CutPrefix(out, ownerVar+"=")
	if !ok || owner == t.Layout.Root {
		return nil
	}
	return fmt.Errorf("tmux server %q already belongs to the demo at %s, not %s; stop it with `AGENTCTL_DEMO_ROOT=%s just demo-stop`",
		t.Layout.Label(), owner, t.Layout.Root, owner)
}

// Claim records this root as the server's owner.
func (t Tmux) Claim() error {
	_, err := t.Run("set-environment", "-g", ownerVar, t.Layout.Root)
	return err
}
