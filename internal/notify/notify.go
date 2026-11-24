// Package notify implements the desktop-notification command-substitution
// logic behind internal/hooks' desktop notifications. It is its own package
// so the substitution and notify_cmd execution can be tested in isolation
// from the hook handlers.
package notify

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/shellquote"
)

// desktopNotifierRe matches a command word that is alerter or notify-send
// (optionally path-qualified) anywhere in a shell command line.
var desktopNotifierRe = regexp.MustCompile("(?:^|[\\s;&|(`/])(?:alerter|notify-send)(?:$|[\\s;&|)`])")

// invokesDesktopNotifier reports whether cmd runs alerter or notify-send.
func invokesDesktopNotifier(cmd string) bool {
	return desktopNotifierRe.MatchString(cmd)
}

// extraBinDirs are searched after PATH: a detached process spawned from a
// hook inherits a minimal PATH that often lacks Homebrew's bin dirs, where
// alerter lives.
var extraBinDirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}

// cmdWord quotes p unless it is a bare command name.
func cmdWord(p string) string {
	if filepath.Base(p) == p {
		return p
	}
	return shellquote.Quote(p)
}

// lookBin finds name on PATH, then in extraBinDirs; "" when absent.
func lookBin(name string) string {
	if _, err := exec.LookPath(name); err == nil {
		return name
	}
	for _, d := range extraBinDirs {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode().Perm()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// DefaultCmd returns the platform-appropriate default notification command.
// On macOS it uses alerter, and on Linux notify-send, each only when that
// binary is on PATH or in a standard bin dir (then by absolute path);
// otherwise it returns an empty string (no-op).
func DefaultCmd() string {
	switch runtime.GOOS {
	case "darwin":
		p := lookBin("alerter")
		if p == "" {
			return ""
		}
		return cmdWord(p) + " --title {{title}} --subtitle {{subtitle}} --message {{message}} --sound default --timeout 10"
	case "linux":
		if p := lookBin("notify-send"); p != "" {
			return cmdWord(p) + " {{title}} {{message}}"
		}
	}
	return ""
}

// Options is the full set of inputs to a notification: title/subtitle/
// message plus the env map used both for {{agent}}/{{type}} placeholder
// resolution and as extra process environment for notify_cmd scripts.
type Options struct {
	Title    string
	Subtitle string
	Message  string
	Env      map[string]string
}

// NotifyWithOptions sends a desktop notification using the configured
// notify_cmd. It reads notify_cmd from config (with the platform default when
// unset; an explicitly empty value disables notifications), substitutes
// {{title}}, {{subtitle}}, {{message}}, {{agent}}, and {{type}} placeholders,
// and starts the command. The env map entries are appended to the process
// environment so notify_cmd scripts can reference them directly. No-ops when
// notify_cmd resolves to an empty string.
//
// The command is fire-and-forget: it runs detached in its own process group
// with no inherited stdio, and NotifyWithOptions returns right after Start
// without waiting. Notifiers such as alerter stay alive until the toast times
// out or is clicked, and a hook must never block on that. Consequently there
// is no exit status to log (only Start failures are), and there is no
// timeout: a hung custom notify_cmd is the user's problem.
func NotifyWithOptions(opts Options) {
	cfg := config.Load()
	notifyCmd, set := cfg.EffectiveNotifyCmd()
	if !set {
		if testing.Testing() {
			// A test must never fall back to the developer's real
			// desktop notifier.
			return
		}
		notifyCmd = DefaultCmd()
	}
	if notifyCmd == "" {
		logging.Log("notify: no notify_cmd (set=%v), %q not shown", set, opts.Title)
		return
	}

	// Resolve {{agent}} and {{type}} values from env, with fallbacks.
	agentVal := opts.Env["AGENTCTL_AGENT_NAME"]
	if agentVal == "" {
		agentVal = opts.Title
	}
	typeVal := opts.Env["AGENTCTL_NOTIFICATION_TYPE"]

	cmdStr := expandNotifyCmd(notifyCmd, opts.Title, opts.Subtitle, opts.Message, agentVal, typeVal)

	if testing.Testing() && invokesDesktopNotifier(cmdStr) {
		logging.Log("notify: refusing to run desktop notifier from a test binary: %q", cmdStr)
		return
	}

	// Build process environment: inherit OS env then overlay the provided map.
	procEnv := os.Environ()
	for k, v := range opts.Env {
		procEnv = append(procEnv, fmt.Sprintf("%s=%s", k, v))
	}
	// AGENTCTL_TITLE is the title as shown, for notify_cmd scripts that group
	// or replace notifications by it (it does not vary across repeat firings).
	if _, ok := opts.Env["AGENTCTL_TITLE"]; !ok {
		procEnv = append(procEnv, "AGENTCTL_TITLE="+opts.Title)
	}

	c := exec.Command("sh", "-c", cmdStr)
	c.Env = procEnv
	c.Stdin, c.Stdout, c.Stderr = nil, nil, nil
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		logging.Error("notify: start: %v", err)
		return
	}
	if err := c.Process.Release(); err != nil {
		logging.Log("notify: release: %v", err)
	}
}

// expandNotifyCmd substitutes the {{...}} placeholders in notifyCmd with
// shell-quoted values in a single pass. Sequential ReplaceAll calls would
// re-scan already substituted text, so a message containing "{{agent}}"
// would have that placeholder replaced inside its quoted value -- and the
// replacement's own quotes would then close the surrounding ones.
func expandNotifyCmd(notifyCmd, title, subtitle, message, agentVal, typeVal string) string {
	return strings.NewReplacer(
		"{{title}}", shellquote.Quote(title),
		"{{subtitle}}", shellquote.Quote(subtitle),
		"{{message}}", shellquote.Quote(message),
		"{{agent}}", shellquote.Quote(agentVal),
		"{{type}}", shellquote.Quote(typeVal),
	).Replace(notifyCmd)
}
