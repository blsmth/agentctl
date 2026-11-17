// Package notify implements the desktop-notification command-substitution
// logic behind internal/hooks' desktop notifications. It is its own package
// so the substitution and notify_cmd execution can be tested in isolation
// from the hook handlers.
package notify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

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

// DefaultCmd returns the platform-appropriate default notification command.
// On macOS it uses alerter, and on Linux notify-send, each only when that
// binary is on PATH; otherwise it returns an empty string (no-op).
func DefaultCmd() string {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("alerter"); err != nil {
			return ""
		}
		return "alerter --title {{title}} --subtitle {{subtitle}} --message {{message}} --sound default --timeout 10"
	case "linux":
		if _, err := exec.LookPath("notify-send"); err == nil {
			return "notify-send {{title}} {{message}}"
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
// unset; an explicitly empty value disables notifications), substitutes {{title}}, {{subtitle}}, {{message}}, {{agent}},
// and {{type}} placeholders, and runs the command with a 30s timeout. The
// env map entries are appended to the process environment so notify_cmd
// scripts can reference them directly. No-ops when notify_cmd resolves to an
// empty string.
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	c.Env = procEnv
	if err := c.Start(); err != nil {
		logging.Error("notify: start: %v", err)
		return
	}
	if err := c.Wait(); err != nil {
		logging.Log("notify: process exited: %v", err)
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
