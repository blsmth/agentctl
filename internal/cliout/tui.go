package cliout

import (
	"fmt"
	"os"
)

// openTTY and stderr are seams so tests need no controlling terminal.
var (
	openTTY = func() error {
		f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			return err
		}
		return f.Close()
	}
	tuiStderr = func() *os.File { return os.Stderr }
	tuiExit   = Exit
)

// HasTTY reports whether a controlling terminal can be opened, which is what
// Bubble Tea needs. Stdout being piped does not matter.
func HasTTY() bool { return openTTY() == nil }

// RunTUI runs tui when a controlling terminal exists. Without one it prints a
// note to stderr and calls fallback (the command's non-TUI output). A nil
// fallback means there is nothing to show instead: it exits 1.
func RunTUI(tui func(), fallback func() error) error {
	if HasTTY() {
		tui()
		return nil
	}
	if fallback == nil {
		fmt.Fprintln(tuiStderr(), "--tui needs an interactive terminal")
		tuiExit(1)
		return nil
	}
	fmt.Fprintln(tuiStderr(), "--tui needs an interactive terminal; showing the table instead")
	return fallback()
}
