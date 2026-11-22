package demo

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// controlSocket mirrors servectl.ControlSocket. The demo does not import
// servectl: the stub only has to accept connections, which is all the hub's
// probe checks.
const controlSocket = "control.sock"

// ServeDir is where the fake agentctl serve listens, under the demo's own
// state home (never the real one).
func (l Layout) ServeDir() string { return filepath.Join(l.StateHome(), "agentctl", "serve") }

// startServeStub starts the fake serve as a detached process of the seeder
// binary. It exits by itself once the demo root is cleared or re-seeded.
func startServeStub(l Layout, self string) error {
	if err := os.MkdirAll(l.ServeDir(), 0o700); err != nil {
		return fmt.Errorf("creating fake serve dir: %w", err)
	}
	sock := filepath.Join(l.ServeDir(), controlSocket)
	if len(sock) >= 100 {
		return fmt.Errorf("fake serve socket path is too long for a unix socket (%d chars); use a shorter AGENTCTL_DEMO_ROOT", len(sock))
	}
	cmd := exec.Command(self, "serve-stub", "--dir", l.ServeDir())
	cmd.Env = l.Env("")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting fake serve: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	// Wait for the socket so the hub's first probe finds it.
	for range 50 {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("fake serve did not start listening on %s", sock)
}

// RunServeStub listens on the control socket in dir and accepts (then closes)
// connections until the socket file is removed or replaced.
func RunServeStub(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sock := filepath.Join(dir, controlSocket)
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", sock, err)
	}
	// A stub that outlives its root must not unlink the socket of the next
	// demo's stub, which listens on the same path.
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	defer func() { _ = ln.Close() }()
	mine, err := os.Stat(sock)
	if err != nil {
		return err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	for {
		time.Sleep(time.Second)
		cur, err := os.Stat(sock)
		if err != nil || !os.SameFile(mine, cur) {
			return nil
		}
	}
}
