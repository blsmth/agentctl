package tmux

import (
	"slices"
	"testing"
)

func TestCapturePaneArgs(t *testing.T) {
	plain := capturePaneArgs("s:@1", 50, false)
	if slices.Contains(plain, "-e") {
		t.Errorf("plain capture has -e: %v", plain)
	}
	ansiArgs := capturePaneArgs("s:@1", 50, true)
	if !slices.Contains(ansiArgs, "-e") {
		t.Errorf("ansi capture missing -e: %v", ansiArgs)
	}
	if !slices.Contains(ansiArgs, "-S") || !slices.Contains(ansiArgs, "-50") {
		t.Errorf("missing scrollback: %v", ansiArgs)
	}
}

func TestTrimTrailingBlankLinesEscapeOnly(t *testing.T) {
	in := "hello\n\x1b[31m  \x1b[0m\n\x1b[0m\n"
	if got := trimTrailingBlankLines(in); got != "hello" {
		t.Fatalf("got %q", got)
	}
}
