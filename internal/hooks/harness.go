package hooks

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/logging"
)

// active is the harness whose hook contract this process speaks: Claude
// Code unless the hook was invoked with --harness (the pi bridge passes
// --harness pi). It decides how stdin is decoded and how decisions are
// written back; the state logic itself is shared.
var active = harness.For(harness.Default)

// currentHook is the hook subcommand being handled (e.g. "hook-pre-tool"),
// which a harness decoder needs to map its payload.
var currentHook string

// SetHarness selects the hook contract by harness name ("" = default). An
// unknown name is an error and leaves the default in place.
func SetHarness(name string) error {
	h, err := harness.Lookup(name)
	if err != nil {
		return err
	}
	active = h
	return nil
}

// HarnessFromArgs returns the value of a --harness flag in args ("--harness
// pi" or "--harness=pi"), or "".
func HarnessFromArgs(args []string) string {
	for i, a := range args {
		if a == "--harness" && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--harness="); ok {
			return v
		}
	}
	return ""
}

// readHookInput reads the hook payload from stdin and decodes it into the
// canonical (Claude Code) shape through the active harness.
func readHookInput() (harness.Event, error) {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return harness.Event{}, fmt.Errorf("reading stdin: %w", err)
	}
	ev, err := active.DecodeHook(currentHook, raw)
	if err != nil {
		return ev, err
	}
	if len(ev.Raw) == 0 {
		return ev, io.EOF
	}
	return ev, nil
}

// emitDecision writes d to stdout in the active harness's contract. A
// harness with nothing to receive for d gets nothing.
func emitDecision(d harness.Decision) {
	out := active.EncodeDecision(d)
	if len(out) == 0 {
		return
	}
	if _, err := os.Stdout.Write(out); err != nil {
		logging.Warn("hook: writing %s output: %v", active.Name(), err)
	}
}
