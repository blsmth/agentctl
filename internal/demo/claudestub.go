package demo

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// claudeShim is the demo's `claude` on PATH: it hands every argument to the
// seed binary's hidden claude-stub subcommand, so a window launched from the
// hub (or by dispatch) shows a live Claude Code start screen and never runs,
// or reaches, the real claude.
func claudeShim(self string) string {
	return fmt.Sprintf("#!/bin/sh\nexec '%s' claude-stub \"$@\"\n", strings.ReplaceAll(self, "'", `'\''`))
}

// stubArgs is what the stub reads from claude's command line.
type stubArgs struct {
	mode      string // bare, or auto for any --permission-mode but default
	sessionID string // --session-id
	prompt    string // the positional prompt a worker dispatch passes
}

// claudeBoolFlags are the claude flags that take no value; every other flag
// is taken to take one.
var claudeBoolFlags = map[string]bool{
	"--fork-session": true, "--continue": true, "-c": true, "--verbose": true,
	"--dangerously-skip-permissions": true, "--ide": true,
}

func parseStubArgs(args []string) stubArgs {
	a := stubArgs{mode: "bare"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			if i+1 < len(args) {
				a.prompt = args[len(args)-1]
			}
			return a
		case !strings.HasPrefix(arg, "-"):
			a.prompt = arg
			continue
		case claudeBoolFlags[arg] || strings.Contains(arg, "="):
			continue
		}
		if i+1 >= len(args) {
			break
		}
		val := args[i+1]
		i++
		switch arg {
		case "--permission-mode":
			if val != "default" {
				a.mode = "auto"
			}
		case "--session-id":
			a.sessionID = val
		}
	}
	return a
}

// stubPrompt is the prompt as the stub's transcript shows it: its first
// paragraph, at most 240 characters (a dispatch prompt runs on for pages).
func stubPrompt(prompt string) string {
	p, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n\n")
	p = strings.Join(strings.Fields(p), " ")
	if r := []rune(p); len(r) > 240 {
		p = string(r[:239]) + "…"
	}
	return p
}

// RunClaudeStub draws a fresh Claude Code start screen (banner, empty
// prompt, footer) for the current directory and blocks until killed. It
// fires the SessionStart hook so the window's row is idle rather than
// unknown. Started with a prompt (a worker dispatch) it also shows the
// prompt, fires UserPromptSubmit and stays working, its spinner counting
// up, so the row shows ●. It makes no network call and starts no program
// but agentctl's hooks.
func RunClaudeStub(args []string, agentctl string) error {
	a := parseStubArgs(args)
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}
	sid := a.sessionID
	if sid == "" {
		sid = SessionID("stub/" + cwd + "/" + os.Getenv("TMUX_PANE"))
	}
	spec := PaneSpec{Window: Window{Mode: a.mode, State: "idle"}, WorkDir: cwd, SessionID: sid, Agentctl: agentctl}
	p := newPane(spec)
	restore := quietTerminal(int(os.Stdin.Fd()))
	defer restore()
	fmt.Fprint(p.out, "\x1b[?25l")
	p.draw()
	if agentctl != "" {
		fireSessionStart(agentctl, spec)
	}
	if prompt := stubPrompt(a.prompt); prompt != "" {
		p.sleep(700 * time.Millisecond)
		p.startWorking(prompt)
	}
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-winch:
			p.draw()
		case <-tick.C:
			if p.playing() {
				p.draw()
			}
		}
	}
}

// startWorking shows prompt as submitted and fires UserPromptSubmit; the
// turn never finishes.
func (p *pane) startWorking(prompt string) {
	p.mu.Lock()
	p.turn = &turnState{start: p.now(), entries: []Entry{{User: prompt}}}
	p.mu.Unlock()
	p.draw()
	if p.spec.Agentctl == "" {
		return
	}
	pay := p.hookPayload("UserPromptSubmit")
	pay["prompt"] = prompt
	p.hooks.Fire("hook-user-prompt", pay)
}

// fireSessionStart feeds the real SessionStart hook, as Claude Code does.
// Best effort: a failure only leaves the row as it would be without it.
func fireSessionStart(agentctl string, spec PaneSpec) {
	execHooks{agentctl: agentctl, dir: spec.WorkDir}.Fire("hook-session-start", map[string]any{
		"session_id":      spec.SessionID,
		"cwd":             spec.WorkDir,
		"hook_event_name": "SessionStart",
		"source":          "startup",
	})
}
