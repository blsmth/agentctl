// Package notify implements the desktop-notification command-substitution
// logic shared by internal/hooks and internal/cmd. It exists as its own
// package (rather than living in internal/hooks, where it originated)
// because internal/cmd needs to fire the same notification (see
// RunSend's --wait-reply gating in internal/cmd/send.go) and
// internal/hooks' version was unexported -- extracting it here means both
// callers run the exact same command-substitution logic instead of two
// copies drifting apart.
package notify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/shellquote"
)

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

// Notify sends a desktop notification using the configured notify_cmd. It
// is a thin delegate over NotifyWithOptions for callers that have no
// subtitle to report -- {{subtitle}} substitutes to the empty string in
// that case.
func Notify(title, message string, env map[string]string) {
	NotifyWithOptions(Options{Title: title, Message: message, Env: env})
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
