// Package cliout keeps CLI command output free of ANSI escapes when stdout is
// not a terminal (a pipe, a file, an agent's Bash tool). lipgloss v2 styles
// emit escapes unconditionally, so the strip is applied once at the process
// boundary instead of in every table renderer.
//
// It is scoped to the stdout of ordinary CLI commands; callers must not use it
// for the statusline, hooks or TUI panes, which need their colors even when
// stdout is a pipe. When stdout is a terminal nothing is touched (the file
// descriptor is never swapped, so TUIs keep their terminal), and NO_COLOR is
// not applied there.
package cliout

import (
	"io"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/colorprofile"
	"golang.org/x/term"
)

// drainTimeout bounds how long a flush waits for the copy goroutine. It only
// matters when a detached child inherited the pipe's write end and keeps it
// open after we exit.
const drainTimeout = time.Second

// ShouldStrip reports whether stdout must be wrapped to strip ANSI escapes.
func ShouldStrip(isTTY bool) bool { return !isTTY }

// StripWriter returns a writer that removes every ANSI escape sequence before
// forwarding to w.
func StripWriter(w io.Writer) io.Writer {
	return &colorprofile.Writer{Forward: w, Profile: colorprofile.NoTTY}
}

var (
	mu    sync.Mutex
	flush func()
)

// Plain wraps os.Stdout so that ANSI escapes are stripped when stdout is not a
// terminal. The returned func flushes and restores stdout and is safe to call
// more than once; call it before exiting (or use Exit). On a terminal it is a
// no-op.
func Plain() (restore func()) {
	real := os.Stdout
	if !ShouldStrip(term.IsTerminal(int(real.Fd()))) {
		return func() {}
	}
	r, w, err := os.Pipe()
	if err != nil {
		return func() {}
	}
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(StripWriter(real), r)
	}()
	var once sync.Once
	restore = func() {
		once.Do(func() {
			os.Stdout = real
			_ = w.Close()
			select {
			case <-done:
			case <-time.After(drainTimeout):
			}
			_ = r.Close()
		})
	}
	mu.Lock()
	flush = restore
	mu.Unlock()
	return restore
}

// Exit flushes any output buffered by Plain and then exits. Use it instead of
// os.Exit anywhere a CLI command can exit after writing to stdout.
func Exit(code int) {
	mu.Lock()
	f := flush
	mu.Unlock()
	if f != nil {
		f()
	}
	os.Exit(code)
}
