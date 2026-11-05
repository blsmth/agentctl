package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/blsmth/agentctl/internal/shellquote"
	"github.com/blsmth/agentctl/internal/tmux"
)

// ctlEntry describes a ctl sub-command: which agentctl subcommand to invoke and
// what popup dimensions to use when displaying it on the operator's screen.
type ctlEntry struct {
	subcmd string
	width  int
	height int
}

// CtlCommands is the registry of supported ctl commands.
// Add new entries here to expose additional operator-facing actions to agents.
var CtlCommands = map[string]ctlEntry{
	"restart": {subcmd: "restart-menu", width: 50, height: 14},
	"quit":    {subcmd: "quit-menu", width: 50, height: 14},
}

// RunCmdCtl implements `agentctl ctl [--async] <command> [args...]`.
// It triggers a tmux display-popup on the operator's active agentctl window so
// that agents can surface confirmation dialogs without breaking their own loop.
// By default the call blocks until the popup closes; async returns immediately.
// Flag parsing is handled by Kong via CtlCmd; this function contains the logic.
func RunCmdCtl(async bool, rest []string) {
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: agentctl ctl [--async] <command> [args...]")
		fmt.Fprintln(os.Stderr, "Available commands:")
		for name := range CtlCommands {
			fmt.Fprintf(os.Stderr, "  %s\n", name)
		}
		os.Exit(1)
	}

	cmdName := rest[0]
	extraArgs := rest[1:]

	entry, ok := CtlCommands[cmdName]
	if !ok {
		fmt.Fprintf(os.Stderr, "agentctl ctl: unknown command %q\n", cmdName)
		fmt.Fprintln(os.Stderr, "Available commands:")
		for name := range CtlCommands {
			fmt.Fprintf(os.Stderr, "  %s\n", name)
		}
		os.Exit(1)
	}

	// Resolve the agentctl binary — prefer the running executable so dev builds
	// and installed builds both invoke the right version.
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "agentctl"
	}

	// Build the shell command that the popup will run: `agentctl <subcmd> [extra...]`
	popupCmd := ctlPopupCmd(exe, entry.subcmd, extraArgs)

	// Target the operator's last-active window in the agentctl session.
	// tmux.SessionName already accounts for --instance overrides.
	target := tmux.SessionName + ":!"

	tmuxArgs := []string{
		"display-popup", "-E",
		"-t", target,
		"-w", strconv.Itoa(entry.width),
		"-h", strconv.Itoa(entry.height),
		popupCmd,
	}

	popupExec := exec.Command("tmux", tmuxArgs...)
	popupExec.Stdout = os.Stdout
	popupExec.Stderr = os.Stderr

	if async {
		if err := popupExec.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "agentctl ctl: failed to launch popup: %v\n", err)
			os.Exit(1)
		}
		_ = popupExec.Process.Release()
		return
	}

	// Blocking mode: wait for popup to close and propagate exit code.
	if err := popupExec.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// tmux exits non-zero when there's no session — give a clear message.
			if exitErr.ExitCode() == 1 {
				fmt.Fprintf(os.Stderr, "agentctl ctl: no agentctl session running (tmux target %q not found)\n", target)
			} else {
				fmt.Fprintf(os.Stderr, "agentctl ctl: tmux display-popup exited with code %d\n", exitErr.ExitCode())
			}
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "agentctl ctl: %v\n", err)
		os.Exit(1)
	}
}

// ctlPopupCmd returns the shell command line tmux display-popup runs for a
// ctl command. display-popup hands it to the user's shell, so the binary
// path, subcommand, and every caller-supplied argument are quoted
// individually -- extraArgs come straight from an agent's command line.
func ctlPopupCmd(exe, subcmd string, extraArgs []string) string {
	return shellquote.Join(append([]string{exe, subcmd}, extraArgs...)...)
}
