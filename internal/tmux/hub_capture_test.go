package tmux

import (
	"slices"
	"testing"
)

func TestCapturePaneArgs(t *testing.T) {
	args := capturePaneArgs("s:@1", 50)
	if slices.Contains(args, "-e") {
		t.Errorf("capture has -e: %v", args)
	}
	if !slices.Contains(args, "-S") || !slices.Contains(args, "-50") {
		t.Errorf("missing scrollback: %v", args)
	}
}

func TestTrimTrailingBlankLinesEscapeOnly(t *testing.T) {
	in := "hello\n\x1b[31m  \x1b[0m\n\x1b[0m\n"
	if got := trimTrailingBlankLines(in); got != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestCapturePaneANSIArgs(t *testing.T) {
	args := capturePaneANSIArgs("s:@1", 50)
	for _, want := range []string{"-e", "-J", "-p", "-t", "-S", "-50"} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %s: %v", want, args)
		}
	}
	if args[0] != "capture-pane" {
		t.Errorf("args = %v", args)
	}
}
