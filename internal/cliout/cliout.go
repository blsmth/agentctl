// Package cliout keeps CLI command output free of ANSI escapes when it is not
// going to a terminal (a pipe, a file, an agent's Bash tool) or when NO_COLOR
// is set. lipgloss v2 styles emit escapes unconditionally, so the strip is
// applied once at the process boundary instead of in every table renderer.
//
// It is scoped to the stdout of ordinary CLI commands; callers must not use it
// for the statusline, hooks or TUI panes, which need their colors even when
// stdout is a pipe.
package cliout

import (
	"io"
	"os"

	"github.com/charmbracelet/colorprofile"
	"golang.org/x/term"
)

// ShouldStrip reports whether output must be stripped of ANSI escapes.
func ShouldStrip(isTTY bool, getenv func(string) string) bool {
	if getenv("NO_COLOR") != "" {
		return true
	}
	return !isTTY
}

// StripWriter returns a writer that removes every ANSI escape sequence before
// forwarding to w.
func StripWriter(w io.Writer) io.Writer {
	return &colorprofile.Writer{Forward: w, Profile: colorprofile.NoTTY}
}

// Plain wraps os.Stdout so that ANSI escapes are stripped when stdout is not a
// terminal or NO_COLOR is set. The returned func flushes and restores stdout;
// call it before exiting. When no stripping is needed it is a no-op.
func Plain() (restore func()) {
	real := os.Stdout
	if !ShouldStrip(term.IsTerminal(int(real.Fd())), os.Getenv) {
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
	return func() {
		os.Stdout = real
		_ = w.Close()
		<-done
		_ = r.Close()
	}
}
