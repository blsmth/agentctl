package demo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// claudeShim is the demo's `claude` on PATH: it hands every argument to the
// seed binary's hidden claude-stub subcommand, so a window launched from the
// hub (or by dispatch) shows a live Claude Code start screen and never runs,
// or reaches, the real claude.
func claudeShim(self string) string {
	return fmt.Sprintf("#!/bin/sh\nexec '%s' claude-stub \"$@\"\n", strings.ReplaceAll(self, "'", `'\''`))
}

// RunClaudeStub draws a fresh Claude Code start screen (banner, empty
// prompt, footer) for the current directory and blocks until killed. Every
// argument is accepted and ignored except --permission-mode, which picks the
// footer. It fires the SessionStart hook so the window's row is idle rather
// than unknown; it makes no network call and starts no other program.
func RunClaudeStub(args []string, agentctl string) error {
	mode := "bare"
	for i, a := range args {
		if a == "--permission-mode" && i+1 < len(args) && args[i+1] != "default" {
			mode = "auto"
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}
	spec := PaneSpec{Window: Window{Mode: mode, State: "idle"}, WorkDir: cwd,
		SessionID: SessionID("stub/" + cwd + "/" + os.Getenv("TMUX_PANE"))}
	restore := quietTerminal(int(os.Stdin.Fd()))
	defer restore()
	draw := func() {
		width := 100
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
			width = w
		}
		fmt.Fprint(os.Stdout, "\x1b[?25l\x1b[H\x1b[2J\x1b[3J"+Render(spec, width, "", nil))
	}
	draw()
	if agentctl != "" {
		fireSessionStart(agentctl, spec)
	}
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
	for range winch {
		draw()
	}
	return nil
}

// fireSessionStart feeds the real SessionStart hook, as Claude Code does.
// Best effort: a failure only leaves the row as it would be without it.
func fireSessionStart(agentctl string, spec PaneSpec) {
	payload, err := json.Marshal(map[string]any{
		"session_id":      spec.SessionID,
		"cwd":             spec.WorkDir,
		"hook_event_name": "SessionStart",
		"source":          "startup",
	})
	if err != nil {
		return
	}
	cmd := exec.Command(agentctl, "hook-session-start")
	cmd.Dir = spec.WorkDir
	cmd.Stdin = bytes.NewReader(payload)
	_ = cmd.Run()
}
