package serve

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/blsmth/agentctl/internal/tmux"
)

// TerminalTmux is the tmux side of the live terminal: grouped phone viewer
// sessions (see internal/tmux/mobileview.go). Tests substitute a fake.
type TerminalTmux interface {
	// CreateViewer creates a viewer session grouped with home, showing
	// windowID, and returns its name.
	CreateViewer(home, windowID string) (string, error)
	// WindowSize returns the window's size in cells.
	WindowSize(windowID string) (cols, rows int, err error)
	// KillViewer kills a viewer session; one already gone is not an error.
	KillViewer(name string) error
	// Viewers lists the live viewer sessions.
	Viewers() ([]string, error)
}

// PTYStarter runs a command inside a pseudo-terminal. Tests substitute a
// fake that records the argv.
type PTYStarter interface {
	Start(name string, args []string, cols, rows int) (PTY, error)
}

// PTY is a running command's pseudo-terminal: reads are its output, writes
// are its input.
type PTY interface {
	io.ReadWriter
	// Resize sets the terminal size.
	Resize(cols, rows int) error
	// Close kills the command and releases the terminal. Idempotent.
	Close() error
}

type tmuxTerminal struct{}

func (tmuxTerminal) CreateViewer(home, windowID string) (string, error) {
	return tmux.CreateMobileViewer(home, windowID)
}

func (tmuxTerminal) WindowSize(windowID string) (int, int, error) {
	return tmux.WindowSize(windowID)
}

func (tmuxTerminal) KillViewer(name string) error { return tmux.KillMobileViewer(name) }

func (tmuxTerminal) Viewers() ([]string, error) { return tmux.MobileViewerSessions() }

// maxTermDim bounds a PTY dimension (Winsize fields are uint16).
const maxTermDim = 1000

func clampDim(n int) uint16 {
	return uint16(min(max(n, 1), maxTermDim))
}

// terminalEnv is the environment of the nested tmux client: not inside tmux
// (tmux refuses to nest otherwise) and with a terminal type xterm.js speaks.
func terminalEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "TMUX", "TMUX_PANE", "TERM":
			continue
		}
		out = append(out, kv)
	}
	return append(out, "TERM=xterm-256color")
}

// creackPTYs starts commands with github.com/creack/pty.
type creackPTYs struct{}

func (creackPTYs) Start(name string, args []string, cols, rows int) (PTY, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = terminalEnv(os.Environ())
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: clampDim(cols), Rows: clampDim(rows)})
	if err != nil {
		return nil, fmt.Errorf("starting %s in a pty: %w", name, err)
	}
	p := &ptyProcess{f: f, cmd: cmd, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()
	return p, nil
}

// ptyProcess is a command running in a creack/pty terminal.
type ptyProcess struct {
	f      *os.File
	cmd    *exec.Cmd
	exited chan struct{}
	once   sync.Once
}

func (p *ptyProcess) Read(b []byte) (int, error) { return p.f.Read(b) }

func (p *ptyProcess) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *ptyProcess) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: clampDim(cols), Rows: clampDim(rows)})
}

// ptyKillWait is how long Close waits for the client to exit on SIGHUP
// before killing it.
const ptyKillWait = 2 * time.Second

func (p *ptyProcess) Close() error {
	p.once.Do(func() {
		// SIGHUP is what a terminal closing sends; the tmux client detaches.
		_ = p.cmd.Process.Signal(syscall.SIGHUP)
		select {
		case <-p.exited:
		case <-time.After(ptyKillWait):
			_ = p.cmd.Process.Kill()
			<-p.exited
		}
		_ = p.f.Close()
	})
	return nil
}
