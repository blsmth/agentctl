package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/detach"
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/metricsproc"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/pidfile"
	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/shellquote"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// SessionName is the tmux session that agentctl manages.
// It defaults to "agentctl" and is updated by SetSessionName to include
// the project directory name (e.g. "agentctl-myproject").
var SessionName = "agentctl"

// tmuxSafeRe strips characters that are invalid in tmux session names.
var tmuxSafeRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// SetSessionName derives the tmux session name from the project directory.
// The session name becomes "agentctl-<dirname>" where dirname is sanitized
// for tmux (no dots, spaces, or special characters). If dirname is empty
// the session name stays as "agentctl".
func SetSessionName() {
	SessionName = SessionNameForDirName(paths.DirName())
}

// SessionNameForDirName returns the session name for a project directory's
// basename: "agentctl-<sanitized>", or plain "agentctl" when the basename is
// empty or sanitizes to nothing. It is the single source of truth for the
// name, shared by SetSessionName, the hook watchdog and relaunch-prep.
func SessionNameForDirName(dirName string) string {
	if dirName == "" || dirName == "." || dirName == "/" {
		return "agentctl"
	}
	safe := strings.Trim(tmuxSafeRe.ReplaceAllString(dirName, "-"), "-")
	if safe == "" {
		return "agentctl"
	}
	return "agentctl-" + safe
}

// SessionNameForProjectRoot returns the session name for a project root.
func SessionNameForProjectRoot(root string) string {
	return SessionNameForDirName(filepath.Base(root))
}

// ExactSession returns a tmux session target that matches name exactly. A bare
// name in -t is resolved by prefix, so "agentctl-foo" would match a live
// "agentctl-foobar"; the "=" prefix disables that. Use it for commands whose
// target is a session (has-session, kill-session, attach-session,
// list-windows, set-environment, list-clients).
func ExactSession(name string) string { return "=" + name }

// ExactSessionTarget is ExactSession with a trailing ":", the form for
// commands whose target is a window or pane (set-option, set-hook,
// new-window, switch-client, display-message, list-panes -s). With a bare
// "=name" those commands fail ("no such session"), so the colon is required.
func ExactSessionTarget(name string) string { return "=" + name + ":" }

// HasSessionNamed reports whether a tmux session with exactly this name exists.
func HasSessionNamed(name string) bool {
	return exec.Command("tmux", "has-session", "-t", ExactSession(name)).Run() == nil
}

// SessionExists checks if the agentctl tmux session is running.
func SessionExists() bool {
	cmd := exec.Command("tmux", "has-session", "-t", ExactSession(SessionName))
	return cmd.Run() == nil
}

// SessionID returns the tmux session_id (e.g. "$3") of the session named
// SessionName, or "" when it does not exist. Ids are unique for the life of
// the tmux server, so they distinguish a session from a later one that reuses
// its name.
func SessionID() string {
	out, err := exec.Command("tmux", sessionIDArgs(SessionName)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sessionIDArgs builds the display-message argv for SessionID. display-message
// -t takes a target-pane, so a bare "=name" resolves to nothing (empty output,
// exit 0) on tmux 3.x; the trailing colon makes it an explicit session target.
func sessionIDArgs(name string) []string {
	return []string{"display-message", "-p", "-t", ExactSessionTarget(name), "#{session_id}"}
}

// StartSession creates the agentctl tmux session with a shell as window 0,
// configures the status bar and keybindings, then attaches.
func StartSession(defaultAgent string) error {
	if err := StartSessionDetached(defaultAgent); err != nil {
		return err
	}
	logging.Log("attaching session")
	return AttachSession()
}

// StartSessionDetached is StartSession without the attach: it creates and
// configures the session and returns. `agentctl cron tick` uses it to
// dispatch into a project whose session is not running.
func StartSessionDetached(defaultAgent string) error {
	done := logging.Timing("tmux new-session")
	args := []string{
		"new-session",
		"-d",
		"-s", SessionName,
		"-n", "shell",
		"-e", "AGENTCTL=1",
	}
	// Propagate instance name into the tmux environment so that keybinding-
	// spawned subcommands (monitor, popups, etc.) automatically inherit it.
	instance := instanceName()
	if instance != "" {
		args = append(args, "-e", "AGENTCTL_INSTANCE="+instance)
	}
	cmd := exec.Command("tmux", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("creating session: %s: %w", string(out), err)
	}
	done()

	// Preserve what restore needs before the records are wiped: after a tmux
	// server crash or reboot the windows are gone but the records survive.
	// Writes nothing when there are no agent records.
	SaveRestoreAtStart()

	// Clear stale window state from any previous session — pane/window IDs
	// are not stable across tmux session restarts.
	windowstate.RemoveAll() //nolint:errcheck

	done = logging.Timing("applyConfig (total)")
	applyConfig()
	done()

	// Metrics come from the per-machine `agentctl metrics serve`, not the
	// monitor: start it (detached, no-op unless metrics are enabled and none
	// is alive). Works with monitor:false.
	metricsproc.EnsureStarted()

	// Create persistent tickets window at index 1.
	exe := selfpath.SpawnableOrFallback()
	ticketsCmd := exec.Command("tmux", "new-window",
		"-t", ExactSessionTarget(SessionName),
		"-n", "tickets",
		"-e", "AGENTCTL=1",
		exe, "tickets",
	)
	_ = ticketsCmd.Run()

	// Switch back to shell window so the user lands there on attach.
	_ = exec.Command("tmux", "select-window", "-t", ExactSessionTarget(SessionName)+"shell").Run()
	return nil
}

// instanceName extracts the instance suffix from SessionName (e.g. "foo" from
// "agentctl-foo"). Returns "" for the default "agentctl" session.
// Note: this is used only for propagating AGENTCTL_INSTANCE into the new
// session/window's tmux environment (StartSession, buildLaunchEnv) as
// informational metadata. It is NOT read by anything to make behavioral
// decisions — session naming is derived from paths.DirName() via
// SetSessionName, and per-command runtime-dir/cwd pinning is handled
// explicitly (see chromeBarHookArgs, applyConfig's keybindings). A `--instance`
// CLI flag used to exist for this purpose; it was a no-op (SetInstance) and
// was removed rather than left as a trap — main.go still strips a stray
// `--instance X` from argv for backwards compatibility with any stale
// tmux hooks/keybindings from an older agentctl binary.
func instanceName() string {
	const prefix = "agentctl-"
	if len(SessionName) > len(prefix) && SessionName[:len(prefix)] == prefix {
		return SessionName[len(prefix):]
	}
	return ""
}

// EnsureSessionEnv writes this project's runtime dir into the tmux SESSION
// environment of SessionName.
//
// This is the per-session channel that makes the server-global keybindings
// correct. `bind-key` is server-global, so every agentctl session rebinds the
// same keys and only one binding string can exist per key machine-wide; it
// therefore cannot name a project. The session environment can, and tmux
// propagates it to exactly the two spawn mechanisms the keybindings use:
// `run-shell` jobs and `display-popup` panes both inherit it (verified on
// tmux 3.6a). A keypress in session A thus resolves A's runtime dir even
// though session B bound the key.
//
// Called from applyConfig (session start) and from AttachSession, so a
// session created by an older agentctl binary repairs itself the next time
// `agentctl` attaches to it rather than waiting for a restart.
func EnsureSessionEnv() {
	rd := paths.RuntimeDir()
	if rd == "" {
		return
	}
	logging.Debug("tmux: set-environment -t %s AGENTCTL_RUNTIME_DIR=%s", SessionName, rd)
	_ = exec.Command("tmux", "set-environment", "-t", ExactSession(SessionName), "AGENTCTL_RUNTIME_DIR", rd).Run()
}

// AttachSession attaches to an existing agentctl session.
func AttachSession() error {
	EnsureSessionEnv()
	cmd := exec.Command("tmux", "attach-session", "-t", ExactSession(SessionName))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// launchDirName is the subdirectory of the runtime dir that holds launch
// scripts, prompt files, and launch logs.
const launchDirName = "launch"

// LaunchDir returns the per-user directory for launch scripts, prompt files,
// and launch diagnostics logs, creating it with mode 0700 if needed.
//
// Everything written here can contain the full agent prompt (issue bodies,
// context store entries), so it must never live in a shared temp dir: it is
// <runtime dir>/launch when paths.Init has run, and
// <user cache dir>/agentctl/launch otherwise. An existing directory with
// looser permissions is tightened to 0700; a symlink, a non-directory, or a
// directory owned by another user is an error rather than something to
// write through.
func LaunchDir() (string, error) {
	base := paths.RuntimeDir()
	if base == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("resolving launch dir: %w", err)
		}
		base = filepath.Join(cache, "agentctl")
	}
	dir := filepath.Join(base, launchDirName)
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// ensurePrivateDir creates dir (mode 0700) if needed and verifies it is a
// real directory owned by the current user with no group/other access.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (mode %s)", dir, fi.Mode())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is owned by uid %d, not the current user", dir, st.Uid)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("restricting %s to 0700: %w", dir, err)
		}
	}
	return nil
}

// launchLogPath returns the diagnostics log path for windowName inside dir.
// Path separators in windowName are replaced so a hostile name cannot
// escape dir.
func launchLogPath(dir, windowName string) string {
	safe := strings.NewReplacer("/", "_", `\`, "_").Replace(windowName)
	return filepath.Join(dir, "launch-"+safe+".log")
}

// openLaunchLog opens launchLog for appending, creating it with mode 0600.
// O_NOFOLLOW makes the open fail rather than write through a symlink
// planted at the (predictable) log path, and an existing file is tightened
// to 0600 since it may predate the private launch dir.
func openLaunchLog(launchLog string) (*os.File, error) {
	f, err := os.OpenFile(launchLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening launch log: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, fmt.Errorf("restricting launch log: %w", err)
	}
	return f, nil
}

// writeLaunchLogHeader appends the launch diagnostics header (start marker,
// PWD, and the command being launched) directly to launchLog from Go,
// truncating shellCmd the same way the old inline echo did.
//
// This exists so the generated launch script never has to transcribe
// shellCmd into a shell string at all. shellCmd is influenced by
// user-supplied content (adhoc prompts, ticket titles, custom prompts, etc.)
// and can contain anything, including an odd number of backticks. The script
// used to echo it inside a DOUBLE-quoted string
// (`echo "... Command: %s" >> ...`), and inside double quotes the shell
// treats backticks as command substitution regardless of any single quotes
// the value happens to carry -- an unmatched backtick left the shell waiting
// for a closing backtick that never came, so it consumed the rest of the
// script (including the real `claude` invocation on the next line) as
// unterminated input and the agent silently never started. Go already has
// the string in hand and needs no shell to transcribe it, so writing it with
// a plain file write sidesteps shell interpretation entirely -- there is
// nothing left to misparse.
//
// This must APPEND, not truncate: launch-<windowName>.log accumulates every
// launch for that window name over time (the script's own exit-code line
// further down uses >> for the same reason), and that history is
// load-bearing -- the original bug in this file was diagnosed by noticing
// one launch's entry stopped after "PWD" with no "Command" line, which was
// only visible by comparing it against several earlier healthy launches
// sitting in the same file. Truncating on every launch would erase exactly
// the evidence needed to catch the next occurrence of this class of bug.
func writeLaunchLogHeader(launchLog, workDir, shellCmd string) error {
	ts := time.Now().Format("15:04:05")
	header := fmt.Sprintf(
		"[%s] Launch script starting\n[%s] PWD: %s\n[%s] Command: %s\n",
		ts, ts, workDir, ts, truncateStr(shellCmd, 500),
	)
	f, err := openLaunchLog(launchLog)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(header)
	return err
}

// buildLaunchScript returns the POSIX sh launch script run by LaunchAgent.
// launchLog is the diagnostics log path (already pre-populated by
// writeLaunchLogHeader before this script ever runs); scriptPath is this
// script's own path, removed by its final line.
//
// The script must stay POSIX sh: it is executed by /bin/sh (see
// launchWindowArgv), which is dash on many Linux systems. In particular
// there is no process substitution, so instead of `2> >(tee ...)` the
// command's stderr is piped through tee while its stdout is pointed back at
// the terminal (fd 4), and its exit status travels out of the pipeline over
// fd 3 into a command substitution.
//
// The invocation line interpolates shellCmd verbatim on its own line inside
// a subshell (so an `exit` inside it cannot skip the status report).
// shellCmd is not a single opaque token but a full command line built by the
// caller (harness.Harness.Command and friends), and every value it embeds is
// already quoted by the caller with shellquote.Quote.
// Single quotes disable ALL shell expansion, including backticks and $(), so
// this line is safe to interpolate raw. Values this function interpolates
// itself (the log and script paths) are quoted here.
//
// unsetLine, when non-empty (see unsetLineFor), is emitted just before
// shellCmd inside the subshell so the listed variables are gone from the
// claude process's environment.
func buildLaunchScript(shellCmd, launchLog, scriptPath, unsetLine string) string {
	if unsetLine != "" {
		unsetLine += "\n"
	}
	return fmt.Sprintf(`#!/bin/sh
# agentctl launch script (POSIX sh)
AGENTCTL_LAUNCH_LOG=%s
export AGENTCTL_LAUNCH_LOG

exec 4>&1
exit_code=$( { { (
%s%s
) 2>&1 1>&4 4>&- 3>&-; echo "$?" >&3; } | tee -a "$AGENTCTL_LAUNCH_LOG" >&2 3>&- 4>&-; } 3>&1 )
exec 4>&-

echo "[$(date '+%%H:%%M:%%S')] Exit code: $exit_code" >> "$AGENTCTL_LAUNCH_LOG"
if [ "$exit_code" != 0 ]; then
    echo "[$(date '+%%H:%%M:%%S')] ERROR: Command failed with exit code $exit_code" >> "$AGENTCTL_LAUNCH_LOG"
    echo "Launch failed with exit code $exit_code. Check $AGENTCTL_LAUNCH_LOG for details."
    echo "Press Enter to close..."
    read -r _ || true
fi
rm -f %s
`, shellquote.Quote(launchLog), unsetLine, shellCmd, shellquote.Quote(scriptPath))
}

// EnvUnset is the extraEnv key naming variables to remove from the launched
// process's environment (space-separated). tmux -e can only set variables,
// and an inherited ANTHROPIC_API_KEY would shadow a backend's apiKeyHelper
// and be sent to the backend.
const EnvUnset = "AGENTCTL_UNSET_ENV"

var unsetNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// accountCredentialEnv are inherited variables that outrank a config dir's
// own login (Claude Code auth precedence) and would silently bill another
// account or API key.
var accountCredentialEnv = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}

// AddUnsetEnv appends names to env[EnvUnset], keeping the names already
// listed there, so independent launch steps (account, backend) can each ask
// for variables to be removed without clobbering one another.
func AddUnsetEnv(env map[string]string, names ...string) {
	have := strings.Fields(env[EnvUnset])
	for _, n := range names {
		if !slices.Contains(have, n) {
			have = append(have, n)
		}
	}
	if len(have) > 0 {
		env[EnvUnset] = strings.Join(have, " ")
	}
}

// RemoveUnsetEnv drops names from env[EnvUnset] (deleting the key when the
// list becomes empty).
func RemoveUnsetEnv(env map[string]string, names ...string) {
	have := strings.Fields(env[EnvUnset])
	if len(have) == 0 {
		return
	}
	kept := have[:0]
	for _, n := range have {
		if !slices.Contains(names, n) {
			kept = append(kept, n)
		}
	}
	if len(kept) == 0 {
		delete(env, EnvUnset)
		return
	}
	env[EnvUnset] = strings.Join(kept, " ")
}

// launchUnsetEnv returns the space-separated variables the launch script
// unsets: extraEnv[EnvUnset], plus, when the launch selects an account
// (CLAUDE_CONFIG_DIR set in extraEnv, or listed in EnvUnset because the
// account lives in Claude Code's default dir), every accountCredentialEnv
// name the launch does not set itself (a backend may set
// ANTHROPIC_AUTH_TOKEN, and tmux -e values must survive).
func launchUnsetEnv(extraEnv map[string]string) string {
	names := strings.Fields(extraEnv[EnvUnset])
	if extraEnv[claudehome.EnvConfigDir] == "" && !slices.Contains(names, claudehome.EnvConfigDir) {
		return strings.Join(names, " ")
	}
	for _, n := range accountCredentialEnv {
		if _, set := extraEnv[n]; set || slices.Contains(names, n) {
			continue
		}
		names = append(names, n)
	}
	return strings.Join(names, " ")
}

// unsetLineFor turns a space-separated list of variable names into an
// `unset` command line. Every name is validated against unsetNameRe, since
// the result is interpolated into a shell script unquoted. An empty list
// yields "".
func unsetLineFor(names string) (string, error) {
	fields := strings.Fields(names)
	if len(fields) == 0 {
		return "", nil
	}
	for _, n := range fields {
		if !unsetNameRe.MatchString(n) {
			return "", fmt.Errorf("invalid variable name %q in %s", n, EnvUnset)
		}
	}
	return "unset " + strings.Join(fields, " "), nil
}

// loginCapableShells are the shells known to accept `-l -c <cmd>` (login
// shell running a command string). Others -- notably csh/tcsh, which reject
// `-l` combined with any other flag -- would kill the window before the
// launch script ever ran, with nothing logged, so they fall back to /bin/sh.
var loginCapableShells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true,
	"dash": true, "ksh": true, "mksh": true,
}

// launchWindowArgv returns the argv tmux new-window runs for a launch
// script. The user's $SHELL is started as a login shell so the agent sees
// the same PATH and environment as an interactive terminal (where `claude`
// is usually installed), and it immediately execs /bin/sh on the script, so
// the script itself never depends on which shell the user prefers. When
// $SHELL is unset or not in loginCapableShells it falls back to
// `/bin/sh -l`. The script path is quoted with shellquote.Quote, whose
// single-quote form is also valid in fish.
func launchWindowArgv(userShell, scriptPath string) []string {
	if userShell == "" || !loginCapableShells[filepath.Base(userShell)] {
		userShell = "/bin/sh"
	}
	return []string{userShell, "-l", "-c", "exec /bin/sh " + shellquote.Quote(scriptPath)}
}

// writeLaunchScript creates a new 0600 launch script in dir containing
// buildLaunchScript's output and returns its path and content.
//
// unsetEnv is the space-separated AGENTCTL_UNSET_ENV value ("" for none).
func writeLaunchScript(dir, shellCmd, launchLog, unsetEnv string) (string, string, error) {
	unsetLine, err := unsetLineFor(unsetEnv)
	if err != nil {
		return "", "", err
	}
	sf, err := os.CreateTemp(dir, "launch-*.sh") // CreateTemp uses mode 0600
	if err != nil {
		return "", "", fmt.Errorf("creating launch script: %w", err)
	}
	scriptPath := sf.Name()
	content := buildLaunchScript(shellCmd, launchLog, scriptPath, unsetLine)
	if _, err := sf.WriteString(content); err != nil {
		sf.Close()
		os.Remove(scriptPath)
		return "", "", fmt.Errorf("writing launch script: %w", err)
	}
	if err := sf.Close(); err != nil {
		os.Remove(scriptPath)
		return "", "", fmt.Errorf("closing launch script: %w", err)
	}
	return scriptPath, content, nil
}

// launchTmux runs the tmux commands LaunchAgent issues (new-window and the
// window options that follow it). A var so launch-path tests can capture the
// exact argv without a tmux server; see SetLaunchTmuxForTest.
var launchTmux = func(args ...string) ([]byte, error) {
	return exec.Command("tmux", args...).CombinedOutput()
}

// SetLaunchTmuxForTest replaces the tmux runner LaunchAgent uses and returns
// a func restoring the previous one. Tests only.
func SetLaunchTmuxForTest(fn func(args ...string) ([]byte, error)) (restore func()) {
	prev := launchTmux
	launchTmux = fn
	return func() { launchTmux = prev }
}

// LaunchAgent creates a new tmux window for the given agent.
// windowName is the tmux window name (may include a ~N instance suffix).
// logPath, if non-empty, is set as the @agentctl_log window option so the
// monitor can find the JSONL file without scanning the filesystem.
// agentName is the base agent name (e.g. "go-engineer"); sessionID is a UUID
// for log correlation. Both are injected as env vars into the window.
// Returns the stable tmux window ID (e.g. "@5") that can be used for all
// subsequent targeting, regardless of window name changes.
//
// focus selects whether the new window becomes the current window; when false
// the window is created detached (-d) so the user stays where they were.
// The insertion position follows the global new_window_position config.
func LaunchAgent(windowName string, workDir string, shellCmd string, logPath string, agentName string, sessionID string, extraEnv map[string]string, focus bool) (string, error) {
	done := logging.Timing("LaunchAgent: tmux new-window")
	// Use "session:" (trailing colon) to force tmux to interpret this as a
	// session target with auto-assigned window index. Without the colon,
	// tmux may resolve "agentctl" as a window reference (especially inside
	// display-popup) and fail with "index N in use".
	// Resolve agentctl binary path for shell-prefix and hooks.
	agentctlBin := selfpath.SpawnableOrFallback()

	// Harness-specific env (Claude Code's CLAUDE_CODE_* vars, including the
	// shell prefix that runs audit logging) arrives in extraEnv: callers add
	// it from their harness (harness.Harness.LaunchEnv).
	envArgs := buildLaunchEnv(windowName, workDir, agentName, sessionID, agentctlBin, instanceName(), extraEnv)
	left := config.Load().EffectiveNewWindowPosition() == config.NewWindowLeft
	newWindowArgs := buildNewWindowArgs(SessionName, windowName, workDir, focus, left, envArgs)

	// The SessionStart hook (agentctl hook-session-start) handles dynamic
	// identity var injection (AGENTCTL_WINDOW_ID, AGENTCTL_WINDOW_INDEX) via
	// $CLAUDE_ENV_FILE, so no shell wrapper is needed here.
	//
	// Write shellCmd to a script to avoid quoting issues with nested single
	// quotes and $(cat ...) expansion inside a `sh -c '...'` argument.
	// shellCmd may end with the user prompt (issue text, ad-hoc spec), so it
	// is only logged at debug level; callers log an elided form.
	logging.Log("LaunchAgent: window=%s shellCmd len=%d", windowName, len(shellCmd))
	logging.Debug("LaunchAgent: shellCmd=%s", truncateStr(shellCmd, 500))
	scriptDir, err := LaunchDir()
	if err != nil {
		return "", fmt.Errorf("launch dir: %w", err)
	}
	cleanupLegacyLaunchDirOnce(scriptDir)
	launchLog := launchLogPath(scriptDir, windowName)

	// Write the launch diagnostics header (start marker, PWD, and the
	// command being launched) directly from Go rather than having the
	// generated script echo it -- see writeLaunchLogHeader's doc comment
	// for why. A failure here (e.g. a symlink planted at the log path) is
	// fatal: the script appends to the same path with plain shell
	// redirection, which would follow it.
	if err := writeLaunchLogHeader(launchLog, workDir, shellCmd); err != nil {
		return "", fmt.Errorf("launch log %s: %w", launchLog, err)
	}

	scriptPath, scriptContent, err := writeLaunchScript(scriptDir, shellCmd, launchLog, launchUnsetEnv(extraEnv))
	if err != nil {
		return "", err
	}
	logging.Log("LaunchAgent: launch log at %s", launchLog)
	logging.Log("LaunchAgent: wrote launch script %s (%d bytes)", scriptPath, len(scriptContent))
	logging.Debug("LaunchAgent: launch script first 300 chars: %s", truncateStr(scriptContent, 300))
	newWindowArgs = append(newWindowArgs, launchWindowArgv(os.Getenv("SHELL"), scriptPath)...)
	out, err := launchTmux(newWindowArgs...)
	if err != nil {
		return "", fmt.Errorf("new-window: %s: %w", string(out), err)
	}
	done()

	windowID := strings.TrimSpace(string(out))

	// Enable silence monitoring — after 2s of no output, tab shows idle indicator.
	// Use windowID as the target so renames don't break these option sets.
	done = logging.Timing("LaunchAgent: window options")
	idTarget := sessionWindowTarget(SessionName, windowID)
	_, _ = launchTmux("set-option", "-w", "-t", idTarget, "monitor-silence", "2")
	_, _ = launchTmux("set-option", "-w", "-t", idTarget, "monitor-activity", "off")

	// Inject the stable window ID as an env var so the process inside can read it.
	if windowID != "" {
		_, _ = launchTmux("set-environment", "-t", ExactSession(SessionName), "AGENTCTL_WINDOW_ID", windowID)
	}

	// Bootstrap the windowstate file with launch-time metadata so the monitor
	// and TUI can find the JSONL file and dispatch context immediately without
	// scanning the filesystem. The hook-session-start hook will Update() on top
	// of this shortly after with session_id.
	//
	// Use Update() (locked read-modify-write), not a raw Write(). The monitor
	// discovers brand-new windows on its very next 500ms tick and immediately
	// calls windowstate.Update() on them too (applyVisuals writes ctx_pct for
	// every known window every tick). A raw, unlocked Write() here can lose
	// that race: if the monitor's locked Update() reads the (still
	// nonexistent) file, applies its own small delta, and writes back AFTER
	// this bootstrap's raw Write() has already landed, its stale read
	// clobbers everything this call just set -- including SpawnedBy, which
	// nothing else ever re-populates. Routing through Update() puts both
	// writers under the same flock, so whichever runs second always starts
	// from what the first left instead of overwriting it wholesale.
	if windowID != "" {
		// A NEW agent starts in this window: clear any tombstone left by a
		// previous window that held the same (recycled) @N ID.
		windowstate.Revive(windowID)
		windowstate.Update(windowID, func(ws *windowstate.WindowState) { //nolint:errcheck
			applyLaunchBootstrap(ws, windowName, agentName, logPath, extraEnv)
		})
	}
	done()

	return windowID, nil
}

// applyLaunchBootstrap stamps a freshly launched agent's metadata onto its
// window record. A recycled @N may carry a previous window's fields, so
// anything that belongs to the previous agent's work is reset here.
func applyLaunchBootstrap(ws *windowstate.WindowState, windowName, agentName, logPath string, extraEnv map[string]string) {
	ws.Instance = windowName
	// The window was just created with this name; start the glyph
	// bookkeeping consistent with it so the first state writer
	// renames from a known baseline. A recycled @N may carry a
	// previous window's Seen/Done.
	ws.RenderedName = windowName
	ws.Seen = false
	ws.AgentName = agentName
	ws.LogPath = logPath
	ws.Ticket = extraEnv["AGENTCTL_TICKET"]
	ws.TicketTitle = extraEnv["AGENTCTL_TICKET_TITLE"]
	ws.Mode = extraEnv["AGENTCTL_MODE"]
	ws.Backend = extraEnv["AGENTCTL_BACKEND"]
	ws.Account = extraEnv[claudehome.EnvAccount]
	ws.Harness = extraEnv[harness.EnvHarness]
	ws.SessionName = extraEnv["AGENTCTL_CLAUDE_NAME"]
	ws.SpawnedBy = extraEnv["AGENTCTL_SPAWNED_BY"]
	ws.SpawnedBySession = extraEnv["AGENTCTL_SPAWNED_BY_SESSION"]
	// The PR belongs to the previous work: dispatch completion records the
	// new one, and restore re-applies a restored window's after launch.
	// Leaving it would make the next done push announce an old PR.
	ws.PRNumber = 0
	ws.PRURL = ""
}

// buildNewWindowArgs returns the tmux new-window argv (without the trailing
// shell command) for an agent window. Without focus it adds -d so the window
// is not selected. With left it adds -b and targets "<session>:^" (the first
// window) so the new window is inserted before it; otherwise the target is
// "<session>:" (trailing colon: force a session target with an auto-assigned
// index; see the LaunchAgent history -- without it tmux may resolve the bare
// name as a window reference and fail with "index N in use").
// -n and -c are format-expanded by tmux, so they are escaped (FormatEscape).
func buildNewWindowArgs(session, windowName, workDir string, focus, left bool, envArgs []string) []string {
	args := []string{"new-window"}
	if !focus {
		args = append(args, "-d")
	}
	target := ExactSessionTarget(session)
	if left {
		args = append(args, "-b")
		target += "^"
	}
	args = append(args, "-t", target, "-n", FormatEscape(windowName), "-c", FormatEscape(workDir))
	args = append(args, envArgs...)
	return append(args, "-P", "-F", "#{window_id}")
}

// ShellPrefixScriptPath is the shell-prefix.sh EnsureShellPrefixScript writes
// into the runtime dir (monitor start, init, dispatch). Claude windows point
// CLAUDE_CODE_SHELL_PREFIX at it so audit logging runs for every command.
func ShellPrefixScriptPath() string {
	return filepath.Join(paths.RuntimeDir(), "shell-prefix.sh")
}

// buildLaunchEnv returns the -e KEY=VALUE arguments for tmux new-window.
// It is extracted as a helper to allow unit testing the env-var construction
// without a live tmux session.
func buildLaunchEnv(windowName, workDir, agentName, sessionID, agentctlBin, inst string, extraEnv map[string]string) []string {
	args := []string{
		"-e", "AGENTCTL=1",
		"-e", "AGENTCTL_WINDOW_NAME=" + windowName,
		"-e", "AGENTCTL_AGENT_NAME=" + agentName,
		"-e", "AGENTCTL_INSTANCE_NAME=" + windowName,
		"-e", "AGENTCTL_SESSION_ID=" + sessionID,
		"-e", "AGENTCTL_BIN=" + agentctlBin,
		"-e", "AGENTCTL_WORKING_DIR=" + workDir,
	}
	if inst != "" {
		args = append(args, "-e", "AGENTCTL_INSTANCE="+inst)
	}

	// AGENTCTL_RUNTIME_DIR: default it here so every LaunchAgent caller --
	// not just dispatch -- launches its window with a runtime dir pinned,
	// rather than letting the launched process resolve one from its own CWD
	// (a split-brain where state landed in the wrong project).
	//
	// Absent vs. present-but-empty matters: a caller that already put a key
	// (even "") in extraEnv gets it forwarded verbatim by the loop below --
	// dispatch's deliberately different target-repo value must never be
	// overwritten, and hub.go's deliberate empty-string clear (so the hub
	// session doesn't inherit a stray project runtime dir) must not be
	// replaced by a default either. Only a caller that never mentions the
	// key at all (nil extraEnv, or a map lacking it) gets defaulted, and only
	// when paths.RuntimeDir() is non-empty -- an empty default would be
	// indistinguishable from the deliberate hub.go clear.
	if _, ok := extraEnv["AGENTCTL_RUNTIME_DIR"]; !ok {
		if rd := paths.RuntimeDir(); rd != "" {
			args = append(args, "-e", "AGENTCTL_RUNTIME_DIR="+rd)
		}
	}

	for k, v := range extraEnv {
		args = append(args, "-e", k+"="+v)
	}
	return args
}

// SwitchToWindow selects the window identified by its tmux window ID (e.g. "@3").
// Using a window ID is unambiguous and stable across renames.
func SwitchToWindow(windowID string) error {
	target := sessionWindowTarget(SessionName, windowID)
	cmd := exec.Command("tmux", "select-window", "-t", target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("select-window: %s: %w", string(out), err)
	}
	return nil
}

// SwitchToAgent selects the window for the given agent by name.
// Deprecated: prefer SwitchToWindow when a window ID is available.
func SwitchToAgent(agentName string) error {
	target := sessionWindowTarget(SessionName, agentName)
	cmd := exec.Command("tmux", "select-window", "-t", target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("select-window: %s: %w", string(out), err)
	}
	return nil
}

// WindowIndex returns the tmux index of windowID, or 0 when unknown.
func WindowIndex(windowID string) int {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", windowID, "#{window_index}").Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

// SaveRestore writes restore.json for the current project from the per-window
// hook state, ordered by the live tmux windows where they still exist. It
// writes nothing when there are no agent entries, so a clean start never
// clobbers a good file from an earlier quit. Failures are logged, never
// returned: they must not block starting or killing a session. It returns the
// number of entries saved.
func SaveRestore() int {
	return saveRestoreFor(SessionName, filepath.Dir(state.FilePath()), windowstate.All(), restoreOpts{filterLive: true})
}

// SaveRestoreAtStart is SaveRestore for StartSession, run after new-session
// and before windowstate.RemoveAll. Only the fresh shell window is live then,
// so records are not filtered by liveness (that is the crash/reboot case), and
// an existing restore.json is left alone: RunRestore clears it once consumed,
// so a surviving file is an unconsumed save from a kill point and is
// authoritative over what the leftover records can reconstruct.
func SaveRestoreAtStart() int {
	return saveRestoreFor(SessionName, filepath.Dir(state.FilePath()), windowstate.All(), restoreOpts{keepExisting: true})
}

// SaveRestoreIn is SaveRestore for another project (session name and runtime
// dir), used by relaunch-prep which runs outside the project's own session.
func SaveRestoreIn(session, runtimeDir string) int {
	return saveRestoreFor(session, runtimeDir, windowstate.AllFrom(windowstate.WindowsDirFor(runtimeDir)), restoreOpts{filterLive: true})
}

type restoreOpts struct {
	// filterLive drops records whose window is not in the live tmux session
	// (closed windows whose record was never removed).
	filterLive bool
	// keepExisting skips the write when restore.json already exists.
	keepExisting bool
}

// listRestoreWindows lists the session's windows with their real tmux
// #{window_index}. A var so tests can stub tmux.
var listRestoreWindows = func(session string) (map[string]state.LiveWindow, error) {
	out, err := exec.Command("tmux", "list-windows", "-t", ExactSession(session),
		"-F", "#{window_id}\t#{window_index}\t#{pane_current_path}").Output()
	if err != nil {
		return nil, fmt.Errorf("tmux list-windows -t %s: %w", session, err)
	}
	live := map[string]state.LiveWindow{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || parts[0] == "" {
			continue
		}
		idx, _ := strconv.Atoi(parts[1])
		live[parts[0]] = state.LiveWindow{Index: idx, Path: parts[2]}
	}
	return live, nil
}

func saveRestoreFor(session, runtimeDir string, records map[string]windowstate.WindowState, opts restoreOpts) int {
	if runtimeDir == "" || runtimeDir == "." {
		return 0
	}
	if opts.keepExisting {
		if _, err := os.Lstat(filepath.Join(runtimeDir, state.RestoreFileName)); err == nil {
			logging.Log("save-restore: keeping existing %s", state.RestoreFileName)
			return 0
		}
	}
	live, err := listRestoreWindows(session)
	if err != nil {
		if opts.filterLive {
			// Unknown, not empty: without the live set we cannot tell closed
			// windows from open ones, so keep whatever is already saved.
			logging.Log("save-restore: skipping, cannot list windows: %v", err)
			return 0
		}
		live = nil
	}
	if opts.filterLive && len(live) > 0 {
		kept := make(map[string]windowstate.WindowState, len(records))
		for id, ws := range records {
			if _, ok := live[id]; ok {
				kept[id] = ws
			}
		}
		records = kept
	}
	agents := state.BuildRestoreAgents(records, live)
	if len(agents) == 0 {
		return 0
	}
	if err := state.SaveRestoreAgents(runtimeDir, agents); err != nil {
		logging.Log("save-restore: %v", err)
		return 0
	}
	logging.Log("save-restore: saved %d agents", len(agents))
	return len(agents)
}

// KillSession destroys the entire agentctl tmux session.
// It mirrors KillWindow/KillAgent: pane PIDs are collected across the whole
// session BEFORE the kill, and sigTermPaneGroups is called AFTER a successful
// kill to SIGTERM every pane's process group. This reaches headless Claude
// subprocesses that would not receive SIGHUP on pane destruction.
func KillSession() error {
	// Windows are still alive here: record what restore needs first.
	SaveRestore()
	// Collect pane PIDs across the entire session before destroying it.
	paneOut, _ := exec.Command("tmux", "list-panes",
		"-s", "-t", ExactSessionTarget(SessionName), "-F", "#{pane_pid}").Output()
	cmd := exec.Command("tmux", "kill-session", "-t", ExactSession(SessionName))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kill-session: %s: %w", string(out), err)
	}
	// SIGTERM each pane's process group to clean up shells and children,
	// including headless Claude subprocesses with no controlling TTY.
	sigTermPaneGroups(string(paneOut))
	return nil
}

// KillAgent closes the window for the given agent.
// It collects pane PIDs before killing and sends SIGTERM to their process
// groups to prevent orphaned shell processes from lingering after the window
// is destroyed.
func KillAgent(agentName string) error {
	target := sessionWindowTarget(SessionName, agentName)
	// Collect pane PIDs before destroying the window.
	paneOut, _ := exec.Command("tmux", "list-panes", "-t", target, "-F", "#{pane_pid}").Output()
	cmd := exec.Command("tmux", "kill-window", "-t", target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kill-window: %s: %w", string(out), err)
	}
	// SIGTERM each pane's process group to clean up shells and children.
	sigTermPaneGroups(string(paneOut))
	return nil
}

// KillWindow closes the window identified by its tmux window ID (e.g. "@3").
// Prefer this over KillAgent when a window ID is available, as it is unambiguous.
// It collects pane PIDs before killing and sends SIGTERM to their process
// groups to prevent orphaned shell processes from lingering after the window
// is destroyed.
func KillWindow(windowID string) error {
	target := sessionWindowTarget(SessionName, windowID)
	// Collect pane PIDs before destroying the window.
	paneOut, _ := exec.Command("tmux", "list-panes", "-t", target, "-F", "#{pane_pid}").Output()
	cmd := exec.Command("tmux", "kill-window", "-t", target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kill-window: %s: %w", string(out), err)
	}
	// SIGTERM each pane's process group to clean up shells and children.
	sigTermPaneGroups(string(paneOut))
	SpawnReconcile()
	return nil
}

// KillWindowIn is KillWindow for a window in any agentctl session: it kills
// windowID in its home session (KillSessionWindow, which SIGTERMs the pane
// process groups) and spawns a reconcile pinned to that session's runtimeDir,
// so the window's own Gone handling (and its own cascade) runs in the right
// project. With an empty runtimeDir no reconcile is spawned.
func KillWindowIn(session, runtimeDir, windowID string) error {
	if err := KillSessionWindow(session, windowID); err != nil {
		return err
	}
	if runtimeDir == "" {
		return nil
	}
	if err := spawnReconcileFn(runtimeDir); err != nil {
		logging.Log("tmux: spawning reconcile in %s: %v", runtimeDir, err)
	}
	return nil
}

// spawnReconcileFn starts the detached reconcile process; a seam for tests.
var spawnReconcileFn = func(runtimeDir string) error {
	return detach.StartSelfPinned([]string{"reconcile"}, runtimeDir)
}

// SpawnReconcile starts a detached `agentctl reconcile` so a window this
// process just closed gets its Gone handling without a monitor. It never
// waits and never fails the caller; with no runtime dir it does nothing.
func SpawnReconcile() {
	rd := paths.RuntimeDir()
	if rd == "" {
		return
	}
	if err := spawnReconcileFn(rd); err != nil {
		logging.Log("tmux: spawning reconcile: %v", err)
	}
}

// KillAllAgents closes all agent windows, leaving only the "shell" window.
// Returns the number of windows killed and any error from the last failed kill.
func KillAllAgents() (int, error) {
	agents := RunningAgents()
	var killed int
	var lastErr error
	for id := range agents {
		if err := KillWindow(id); err != nil {
			lastErr = err
			continue
		}
		killed++
	}
	return killed, lastErr
}

// StripAgentPrefix removes spinner/status prefixes from a tmux window name.
// The monitor renames windows with prefixes like "⠋ go-engineer" (working)
// or "⏸ go-engineer" (blocked). Only UI prefixes are stripped here; semantic
// prefixes like "improve~" are left intact for call sites to handle explicitly.
// Spinner frames are detected via the braille Unicode block (U+2800–U+28FF).
func StripAgentPrefix(name string) string {
	runes := []rune(name)
	if len(runes) >= 2 && runes[0] >= 0x2800 && runes[0] <= 0x28FF && runes[1] == ' ' {
		name = string(runes[2:])
	}
	name = strings.TrimPrefix(name, "⏸ ")
	name = strings.TrimPrefix(name, "✓ ")
	name = strings.TrimPrefix(name, "● ")
	return name
}

// WindowInfo holds the ID, name, and current pane path of a single tmux
// window.
type WindowInfo struct {
	ID   string
	Name string
	// Path is the active pane's #{pane_current_path} at the time of the
	// list-windows call. For a window with multiple panes, tmux resolves
	// this to the *active* pane's cwd.
	Path string
}

// ListWindows returns an ordered slice of all windows in the agentctl session.
// The order matches tmux display order (by window index). No filtering is
// applied — the caller decides which windows to act on.
func ListWindows() []WindowInfo {
	return ListWindowsIn(SessionName)
}

// ListWindowsIn lists the windows of the named session. It is ListWindows
// parameterised so callers that must be scoped to the session they were
// invoked from (e.g. `agentctl sweep`) never depend on the global.
func ListWindowsIn(session string) []WindowInfo {
	windows, _ := ListWindowsInErr(session)
	return windows
}

// ListWindowsInErr is ListWindowsIn that distinguishes a failed tmux call
// (error) from a session with no windows (nil, nil). Callers that act on a
// window's absence must use it.
func ListWindowsInErr(session string) ([]WindowInfo, error) {
	// Tab-delimited: window names and pane paths may both contain spaces
	// (e.g. spinner-prefixed names, or a path under "My Projects"), so a
	// space delimiter would corrupt both fields. Neither field can contain a
	// literal tab.
	cmd := exec.Command("tmux", "list-windows", "-t", ExactSession(session),
		"-F", "#{window_id}\t#{pane_current_path}\t#{window_name}")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tmux list-windows -t %s: %w", session, err)
	}
	var windows []WindowInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || parts[0] == "" {
			continue
		}
		windows = append(windows, WindowInfo{ID: parts[0], Path: parts[1], Name: parts[2]})
	}
	return windows, nil
}

// ListWindowsErr is ListWindows that reports a failed tmux call.
func ListWindowsErr() ([]WindowInfo, error) {
	return ListWindowsInErr(SessionName)
}

// ActiveWindowIDFast returns the currently active tmux window ID. It prefers
// the hook-written file at filePath (populated by the after-select-window hook)
// to avoid racing with tmux display-message when a tick fires just before a
// window switch. Falls back to tmux display-message when the file is absent,
// empty, or filePath is "".
func ActiveWindowIDFast(filePath string) string {
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err == nil {
			id := strings.TrimSpace(string(data))
			if id != "" {
				return id
			}
		}
	}
	// Fallback for first start before any window switch triggers the hook.
	return CurrentWindowID()
}

// PrefixActive returns true when the tmux prefix key (ctrl+b by default) has
// been pressed and not yet released. Used by the monitor to suppress window
// renames during prefix-key navigation.
func PrefixActive() bool {
	out, err := exec.Command("tmux", "display-message", "-p", "#{client_prefix}").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

// RenameWindow renames the tmux window identified by windowID to name.
// windowID should be a stable window ID (e.g. "@3"). This is a best-effort
// operation; errors are silently ignored.
func RenameWindow(windowID, name string) {
	target := sessionWindowTarget(SessionName, windowID)
	exec.Command("tmux", "rename-window", "-t", target, FormatEscape(name)).Run() //nolint:errcheck
}

// BaseAgentName strips the :N instance suffix from an instance name to
// recover the base agent definition name. E.g. "go-engineer:2" → "go-engineer".
// Names without a numeric :N suffix are returned unchanged.
func BaseAgentName(instanceName string) string {
	idx := strings.LastIndex(instanceName, ":")
	if idx == -1 || idx == len(instanceName)-1 {
		return instanceName
	}
	suffix := instanceName[idx+1:]
	if _, err := strconv.Atoi(suffix); err != nil {
		return instanceName
	}
	return instanceName[:idx]
}

// RunningInstances returns all running instance names for the given base agent,
// sorted alphabetically. E.g. for "go-engineer" it might return
// ["go-engineer", "go-engineer:2"].
func RunningInstances(baseName string) []string {
	running := RunningAgents()
	var instances []string
	for _, name := range running {
		if BaseAgentName(name) == baseName {
			instances = append(instances, name)
		}
	}
	sort.Strings(instances)
	return instances
}

// NextInstanceName returns the next available window name for a new instance
// of the given base agent. If no instances are running, returns the base name.
// Otherwise returns baseName:N where N is one higher than the current max.
func NextInstanceName(baseName string) string {
	instances := RunningInstances(baseName)
	if len(instances) == 0 {
		return baseName
	}
	maxN := 1 // the bare name counts as instance 1
	for _, inst := range instances {
		idx := strings.LastIndex(inst, ":")
		if idx == -1 {
			continue
		}
		if n, err := strconv.Atoi(inst[idx+1:]); err == nil && n > maxN {
			maxN = n
		}
	}
	return fmt.Sprintf("%s:%d", baseName, maxN+1)
}

// RunningAgents returns a map of window ID → stripped instance name for all
// active agent windows. Window IDs (e.g. "@3") are stable across renames,
// making them safe to use as tmux targets. Names are stripped of spinner/status
// prefixes (e.g. "⠋ go-engineer" → "go-engineer").
func RunningAgents() map[string]string {
	running := make(map[string]string)
	if !SessionExists() {
		return running
	}
	for _, w := range ListWindows() {
		if w.ID == "" || w.Name == "" || w.Name == "shell" {
			continue
		}
		clean := StripAgentPrefix(w.Name)
		if clean != "" && clean != "shell" {
			running[w.ID] = clean
		}
	}
	return running
}

// DisplayMessage shows a brief message in the tmux status line.
func DisplayMessage(msg string) { DisplayMessageTo("", msg) }

// DisplayMessageTo is DisplayMessage aimed at a specific client (-c) when
// client is non-empty; needed when running detached from any client (popups).
func DisplayMessageTo(client, msg string) {
	args := []string{"display-message"}
	if client != "" {
		args = append(args, "-c", client)
	}
	args = append(args, FormatEscape(msg))
	_ = exec.Command("tmux", args...).Run()
}

// NewTempWindow opens a temporary tmux window that closes when the command exits.
func NewTempWindow(name string, shellCmd string) {
	_ = exec.Command("tmux", "new-window", "-t", ExactSessionTarget(SessionName), "-n", FormatEscape(name), shellCmd).Run()
}

// PaneWorkDir returns the current working directory of the pane in the given window.
func PaneWorkDir(windowID string) string {
	target := sessionWindowTarget(SessionName, windowID)
	cmd := exec.Command("tmux", "display-message", "-p", "-t", target, "#{pane_current_path}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CurrentWindowName returns the name of the currently active tmux window.
func CurrentWindowName() string {
	cmd := exec.Command("tmux", "display-message", "-p", "#{window_name}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CurrentWindowID returns the tmux window ID (e.g. "@3") of the window the
// tmux client is currently focused on. Returns "" if tmux cannot be queried.
//
// This is deliberately NOT signal.ResolveWindowID: that answers "which window
// is this process running in" (session ID, then $TMUX_PANE, then
// AGENTCTL_WINDOW_ID), whereas an untargeted display-message answers "which
// window is the user looking at" -- the right question for popups and
// keybinding-spawned commands that act on the focused window.
func CurrentWindowID() string {
	cmd := exec.Command("tmux", "display-message", "-p", "#{window_id}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// WindowNameByID returns the current window name for the given window ID (e.g., "@3").
// This queries tmux directly with an explicit target, so it works from any context
// (popups, background processes, etc.).
func WindowNameByID(windowID string) string {
	target := sessionWindowTarget(SessionName, windowID)
	cmd := exec.Command("tmux", "display-message", "-p", "-t", target, "#{window_name}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ResolveAgentByWindowID returns the base agent name for a tmux window ID.
// It fetches the window name, strips spinner/status prefixes, and returns the base name.
// Returns "" if the window doesn't exist or is the shell window.
func ResolveAgentByWindowID(windowID string) string {
	name := WindowNameByID(windowID)
	if name == "" || name == "shell" {
		return ""
	}
	return BaseAgentName(StripAgentPrefix(name))
}

// utilityWindowNames is the set of window names that must not receive a chrome
// bar. These windows run full-screen alt-screen TUI applications and use the
// entire window area; adding a chrome bar pane would steal rows from the TUI
// and corrupt the layout.
var utilityWindowNames = map[string]bool{
	"tickets":  true,
	"sessions": true,
	"agents":   true,
}

// FormatEscape escapes s for a tmux string that tmux format-expands, by
// doubling every '#' ("##" expands to a literal '#'). Without it, a value
// such as a project path containing "#(cmd)" or "#{...}" is expanded by tmux
// -- the former runs cmd -- before any shell quoting applies.
//
// Format-expanded positions include a run-shell command, display-message
// text, and new-window/new-session -n and -c and rename-window's name
// (verified on tmux 3.6a). It must NOT be applied to strings tmux does not
// expand (display-popup commands, new-window/split-window shell commands, -e
// values): there "##" would stay literal.
func FormatEscape(s string) string {
	return strings.ReplaceAll(s, "#", "##")
}

// ActiveWindowFileName is the runtime-dir file the after-select-window hook
// keeps updated with the active window ID.
const ActiveWindowFileName = "active-window"

// ActiveWindowPath returns this project's active-window file path.
func ActiveWindowPath() string {
	return filepath.Join(paths.RuntimeDir(), ActiveWindowFileName)
}

var windowIDRe = regexp.MustCompile(`^@[0-9]+$`)

// WriteActiveWindowFile records windowID in the active-window file at path.
// It writes a temp file in the same directory and renames it over path, so a
// symlink planted at path (e.g. committed into a repo's .agentctl/) is
// replaced rather than followed. windowID must look like a tmux window ID.
func WriteActiveWindowFile(path, windowID string) error {
	if !windowIDRe.MatchString(windowID) {
		return fmt.Errorf("invalid window id %q", windowID)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".active-window-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(windowID + "\n"); err != nil {
		tmp.Close()        //nolint:errcheck
		os.Remove(tmpName) //nolint:errcheck
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName) //nolint:errcheck
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName) //nolint:errcheck
		return fmt.Errorf("renaming into place: %w", err)
	}
	return nil
}

// hookAgentctlCmd returns the shell command a session hook's run-shell uses
// to invoke agentctl with args, pinned to runtimeDir. Both paths are quoted
// for sh and format-escaped for tmux, which expands the run-shell command
// before sh sees it. args is emitted verbatim (it may carry #{window_id}).
func hookAgentctlCmd(exe, runtimeDir, args string) string {
	return fmt.Sprintf("AGENTCTL_RUNTIME_DIR=%s %s %s",
		FormatEscape(shellquote.Quote(runtimeDir)), FormatEscape(shellquote.Quote(exe)), args)
}

// chromeBarHookArgs returns the tmux args to set the session-scoped
// after-new-window hook that launches a chrome bar in every new window.
// Utility windows (tickets, sessions, agents) are skipped by
// LaunchChromeBar itself.
//
// Only #{window_id} is interpolated into the shell command. The window NAME
// must never be: tmux expands formats before sh runs, so a window named
// x$(cmd) -- names derive from agent names, which a cloned repo can choose --
// would execute cmd regardless of any shell quoting around the format.
//
// The hook fires via `run-shell`, which executes with the tmux SERVER's
// environment (not the session's or the new window's) and the ACTIVE PANE's
// cwd -- neither of which reliably points at this project once bare
// sessions run in git worktrees. Rather than let the spawned
// `chrome-bar-pane` process fall back to paths.Init(cwd) and potentially
// resolve a worktree's runtime dir (and therefore the wrong tmux
// SessionName), runtimeDir and projectRoot are baked into the command
// explicitly: an `AGENTCTL_RUNTIME_DIR=` assignment (env beats cwd in
// paths.Init) and `run-shell -c <projectRoot>` as a second line of defense.
func chromeBarHookArgs(session, exe, runtimeDir, projectRoot string) []string {
	shellCmd := hookAgentctlCmd(exe, runtimeDir, "chrome-bar-pane #{window_id}")
	// The whole shell command is one argument to run-shell, quoted once more
	// for tmux's own command parser (which, like sh, takes '\'' as a literal
	// quote outside single quotes). Wrapping it in bare '...' instead would
	// let the single quotes already inside it terminate the wrap early.
	return []string{"set-hook", "-t", ExactSessionTarget(session), "after-new-window",
		fmt.Sprintf("run-shell -b -c %s %s", shellquote.Quote(projectRoot), shellquote.Quote(shellCmd))}
}

// activeWindowHookArgs returns the tmux args to set the session-scoped
// after-select-window hook that records the active window ID in
// <runtimeDir>/active-window.
//
// The write is done by the hidden `agentctl set-active-window` subcommand
// (WriteActiveWindowFile: temp file + rename) rather than a shell `>`
// redirect, which would follow a symlink a cloned repo could commit at
// .agentctl/active-window. runtimeDir is pinned the same way as in
// chromeBarHookArgs, and every path is quoted and format-escaped.
func activeWindowHookArgs(session, exe, runtimeDir string) []string {
	shellCmd := hookAgentctlCmd(exe, runtimeDir, "set-active-window #{window_id}")
	return []string{"set-hook", "-t", ExactSessionTarget(session), "after-select-window",
		"run-shell -b " + shellquote.Quote(shellCmd)}
}

// chromeSizeHookEvents are the client events whose session hooks run
// enforce-chrome: the ones that distort chrome pane sizes, plus
// client-session-changed so switching into a session undoes hub zooms.
var chromeSizeHookEvents = []string{"client-resized", "client-attached", "client-session-changed"}

// chromeSizeHookArgs returns the tmux args to set a session-scoped hook for
// event (one of chromeSizeHookEvents) that re-enforces chrome bar pane sizes
// through the hidden `agentctl enforce-chrome`, pinned like
// activeWindowHookArgs.
func chromeSizeHookArgs(session, event, exe, runtimeDir string) []string {
	shellCmd := hookAgentctlCmd(exe, runtimeDir, "enforce-chrome")
	return []string{"set-hook", "-t", ExactSessionTarget(session), event,
		"run-shell -b " + shellquote.Quote(shellCmd)}
}

// reconcileHookArgs returns the tmux args to set the session-scoped
// window-unlinked hook that runs the hidden `agentctl reconcile`. tmux fires
// it when a window is destroyed, whether killed or because its last process
// exited (not on kill-session), so Gone handling needs no monitor.
// Reconcile re-lists windows itself; the hook carries no window ID.
func reconcileHookArgs(session, exe, runtimeDir string) []string {
	shellCmd := hookAgentctlCmd(exe, runtimeDir, "reconcile")
	return []string{"set-hook", "-t", ExactSessionTarget(session), "window-unlinked",
		"run-shell -b " + shellquote.Quote(shellCmd)}
}

// keybindingPinArgs returns the fragments applyConfig splices into every
// keybinding that runs agentctl, so the spawned process resolves THIS
// project's runtime dir and cwd rather than inheriting whatever the active
// pane happens to be (a git worktree, for any dispatched agent).
//
// The fragments are deliberately CONSTANT: they name no project. That is the
// whole fix. `bind-key` is SERVER-global, so every agentctl session on the
// machine rebinds the same keys and only one binding string can exist per key
// machine-wide -- whichever session started last wins for all of them. The
// previous version baked this project's runtime dir and project root into the
// binding, which meant a keypress in ANY session dispatched into whichever
// project happened to bind last: prefix+A pressed in agentctl-acme launched
// agents into agentctl-other-project, twice.
//
// Both values are now resolved at KEYPRESS time from the session the key was
// actually pressed in:
//
//   - AGENTCTL_RUNTIME_DIR comes from the tmux SESSION environment (see
//     EnsureSessionEnv). `run-shell` jobs and `display-popup` panes both
//     inherit it, and env beats cwd in paths.Init.
//   - cwd comes from the AGENTCTL_PIN_CWD flag, which makes the spawned
//     agentctl chdir to paths.ProjectDir() once its runtime dir is resolved
//     (paths.HonorCwdPin). A static `run-shell -c` / `display-popup -d` is
//     what the old version used and is exactly what cannot be made dynamic:
//     tmux format-expands a run-shell COMMAND but not its -c flag, and
//     display-popup expands neither its command nor its -d/-e values
//     (verified on tmux 3.6a). Only the run-shell command string can carry a
//     #{...} format, which is why #{window_id} works in the bindings below
//     but #{session_name} could not be threaded through to a cwd flag.
//
// popupPin goes on a `display-popup`; envPrefix prefixes a `run-shell`
// command that execs the agentctl binary directly.
func keybindingPinArgs() (popupPin, envPrefix string) {
	popupPin = fmt.Sprintf("-e %s=1", paths.CwdPinEnv)
	envPrefix = fmt.Sprintf("%s=1 ", paths.CwdPinEnv)
	return popupPin, envPrefix
}

// keyBinding is one prefix-table binding installed by applyConfig.
type keyBinding struct {
	key     string
	command string // tmux command string passed to bind-key
}

// agentctlKeybindings returns the prefix bindings that invoke the agentctl
// binary at exe.
//
// Every layer is quoted with shellquote.Quote, the same way chromeBarHookArgs
// does: exe is quoted for the shell that runs it, and the whole shell command
// is quoted once more as a single argument for tmux's command parser (which,
// shares sh's single-quote escaping rules). A binary path containing spaces,
// quotes, or $ therefore stays one word at every level.
//
// A nested `run-shell 'tmux display-popup ...'` needs popupPin applied to the
// INNER display-popup, not the outer run-shell -- the outer run-shell only
// invokes the `tmux` client binary, which just forwards the display-popup
// command to the server; it never execs agentctl itself, so pinning it would
// be a no-op. See keybindingPinArgs for why the pins are needed at all.
//
// The nested forms also target the inner popup at `-t #{window_id}`. A direct
// `display-popup` binding is resolved against the pane the key was pressed in
// and so picks up the right session environment for free, but the inner
// `tmux` of a nested form is a fresh client invocation that would otherwise
// fall back to the server's notion of the "current" session. The outer
// run-shell format-expands #{window_id} before the shell sees it.
//
// Note: the popup -w/-h sizes are fixed here because tmux needs them when the
// key is pressed, before the popup's model exists. Keep them in sync with the
// content widths in internal/tui/styles.go (each popup model reports its
// natural size via PopupDimensions).
func agentctlKeybindings(exe string) []keyBinding {
	popupPin, envPrefix := keybindingPinArgs()
	qexe := shellquote.Quote(exe)
	// invokeRS is for anything that passes through a run-shell, which
	// format-expands, so the binary path is format-escaped (see FormatEscape).
	invokeRS := func(args string) string { return FormatEscape(qexe) + " " + args }

	// nested opens the popup via run-shell so #{window_id} is expanded. The
	// outer run-shell expands the whole inner command once (unescaping the
	// path); the inner display-popup then runs it unexpanded. The window ID is
	// also handed to the popup as KeyWindowEnv (display-popup itself expands
	// neither its command nor its -e values, so only this form can carry it):
	// hubtarget.Apply uses it to redirect hub keypresses to the viewed agent.
	nested := func(w, h int, args string) string {
		inner := fmt.Sprintf("tmux display-popup -E -t #{window_id} %s -e %s=#{window_id} -e %s=#{client_name} -w %d -h %d %s",
			popupPin, KeyWindowEnv, KeyClientEnv, w, h, shellquote.Quote(invokeRS(args)))
		return "run-shell " + shellquote.Quote(inner)
	}
	popup := func(w, h int, args string) string { return nested(w, h, args) }
	// run execs agentctl args via run-shell with the cwd pin and KeyWindowEnv.
	run := func(flags, args string) string {
		return "run-shell " + flags + shellquote.Quote(envPrefix+KeyWindowEnv+"=#{window_id} "+KeyClientEnv+"=#{client_name} "+invokeRS(args))
	}

	return []keyBinding{
		{"a", popup(100, 40, "menu")},
		{"x", nested(50, 11, "kill-menu #{window_id}")},
		{"o", run("", "logs #{window_id}")},
		{"s", nested(68, 18, "settings-menu #{window_id}")},
		{"i", nested(50, 12, "improve-menu #{window_id}")},
		{"t", run("-b ", "open-tickets-window")},
		{"T", popup(80, 30, "ticket-launch")},
		{"Q", popup(84, 30, "ticket-menu")},
		{"c", popup(60, 26, "create-menu")},
		{"P", popup(82, 34, "permissions-menu")},
		{"S", run("-b ", "open-sessions-window")},
		{"q", popup(50, 14, "quit-menu")},
		{"R", popup(50, 14, "restart-menu")},
		{"m", run("-b ", "open-agents-window")},
		{"A", run("-b ", "launch")},
		{"p", popup(62, 24, "theme-picker")},
		{"K", popup(50, 11, "kill-all-menu")},
		{"h", run("", "hud-toggle #{window_id}")},
		{"H", nested(80, 30, "hud #{window_id}")},
		{"C", nested(60, 24, "context-push-menu #{window_id}")},
		{"X", run("-b ", "open-context-window")},
		// Toggle agentctl serve (the phone web app) and report the outcome as
		// a tmux message on the pressing client. Machine-wide, so not
		// redirected in the hub (hubtarget scopes it ScopeOther).
		{"W", run("-b ", "serve toggle --message-client '#{client_name}'")},
		// Fleet hub: summon the cross-session dashboard, and jump straight to
		// the next blocked agent anywhere. bind-key is server-global — these
		// two keys end up live in every agentctl session, including the hub
		// itself, which is what we want (see StartHubSession's docs for why
		// the hub skips the rest of applyConfig).
		//
		// Deliberately left unpinned: both are cross-project by design.
		// StartHubSession explicitly clears AGENTCTL_RUNTIME_DIR and pins the
		// hub session's own cwd to hubDir(), and jump-next-blocked resolves
		// each candidate's RuntimeDir from the fleet registry (internal/fleet),
		// not from paths.RuntimeDir() of the invoking process.
		{"g", "run-shell -b " + shellquote.Quote(invokeRS("hub"))},
		{"b", "run-shell -b " + shellquote.Quote(invokeRS("jump-next-blocked --client-session '#{client_session}'"))},
	}
}

// StartMonitor launches `<exe> monitor` detached (via tmux run-shell -b) with
// projectRoot as its cwd, so the monitor resolves the right runtime dir
// regardless of which pane is active. run-shell hands its command to sh, so
// the binary path is quoted, and tmux format-expands that command first, so it
// is format-escaped too. (run-shell's -c is not format-expanded.)
func StartMonitor(exe, projectRoot string) error {
	if testing.Testing() {
		return selfpath.ErrTestBinary
	}
	qexe := FormatEscape(shellquote.Quote(exe))
	return exec.Command("tmux", "run-shell", "-b", "-c", projectRoot, qexe+" monitor").Run()
}

// restartMonitor replaces this project's monitor with a fresh one, unless the
// monitor kill switch (config `monitor: false`) is off. When disabled it does
// nothing at all: an already running monitor is left alone (flipping the flag
// never kills one), and the next relaunch simply does not start another.
// It reports whether a monitor was started.
func restartMonitor(enabled bool, pidDir, exe, projectRoot string, kill func(dir, name string), start func(exe, projectRoot string) error) bool {
	if !enabled {
		logging.Log("applyConfig: monitor disabled (config monitor:false), not starting one")
		return false
	}
	kill(pidDir, "monitor.pid")
	_ = start(exe, projectRoot)
	return true
}

// applyConfig sets up the status bar, keybindings, and styling.
func applyConfig() {
	exe := selfpath.SpawnableOrFallback()

	// Publish this project's runtime dir into the session environment before
	// anything else: it is what makes the server-global keybindings below
	// resolve per-session rather than per-whoever-started-last.
	done := logging.Timing("applyConfig: session environment")
	EnsureSessionEnv()
	done()

	// Both tmux status rows are now replaced by Bubble Tea panes, so we disable
	// the native tmux status bar entirely.
	done = logging.Timing("applyConfig: status bar off")
	set("status", "off")
	done()

	// Kill any existing monitor for this instance before launching a
	// replacement.  Without this, every applyConfig call (e.g. on restart or
	// re-attach) spawns additional background processes that never get cleaned
	// up, wasting CPU and potentially causing concurrent state.json writes.
	done = logging.Timing("applyConfig: start monitor")
	pidDir := paths.RuntimeDir()
	// projectRoot is the parent of .agentctl/ — pass it explicitly via -c so
	// the monitor process always resolves paths.Init(cwd) to the correct
	// directory regardless of which pane is active when applyConfig runs.
	projectRoot := filepath.Dir(pidDir)
	restartMonitor(config.Load().MonitorEnabled(), pidDir, exe, projectRoot, pidfile.Kill, StartMonitor)
	// Restore agents from previous session in the background
	qexe := FormatEscape(shellquote.Quote(exe))
	_ = exec.Command("tmux", "run-shell", "-b", "-c", projectRoot, qexe+" restore").Run()
	done()

	installSessionHooks(exe, pidDir, projectRoot)

	// Launch the chrome bar in the initial shell window.
	done = logging.Timing("applyConfig: LaunchChromeBar shell window")
	if shellID := windowIDByName("shell"); shellID != "" {
		_ = LaunchChromeBar(shellID) // best-effort at startup; failure is non-fatal
	}
	done()

	applyStylingAndKeybindings(exe)
}

// ApplyConfigLive re-applies the session hooks, options and keybindings to the
// running session so it picks up changes from a newer binary without a
// relaunch. Unlike applyConfig it never touches the monitor, restores nothing
// and launches no windows.
func ApplyConfigLive() {
	exe := selfpath.SpawnableOrFallback()
	EnsureSessionEnv()
	set("status", "off")
	pidDir := paths.RuntimeDir()
	installSessionHooks(exe, pidDir, filepath.Dir(pidDir))
	applyStylingAndKeybindings(exe)
}

// installSessionHooks sets every session-scoped hook agentctl relies on.
func installSessionHooks(exe, pidDir, projectRoot string) {
	// The ChromeBarRows-row chrome bar replaces the separate status-bar and
	// tab-bar panes, eliminating a tmux pane-border line. Set the
	// after-new-window hook so every new window gets the chrome bar
	// automatically.
	done := logging.Timing("applyConfig: session hooks")

	// Remove stale global hooks from older agentctl versions that used -g.
	// Session-scoped hooks set below will take over for this session.
	_ = exec.Command("tmux", "set-hook", "-ug", "after-new-window").Run()
	_ = exec.Command("tmux", "set-hook", "-ug", "after-select-window").Run()

	hookArgs := chromeBarHookArgs(SessionName, exe, pidDir, projectRoot)
	_ = exec.Command("tmux", hookArgs...).Run() // args built internally, not user input

	// Instant active-window detection: write the window ID to a file on every
	// window switch. Chrome-bars pick this up via fsnotify (same directory as
	// state.json) for near-instant tab highlight updates.
	selectArgs := activeWindowHookArgs(SessionName, exe, pidDir)
	_ = exec.Command("tmux", selectArgs...).Run() // args built internally, not user input

	// Chrome pane sizes are re-enforced when a client attaches or resizes,
	// the events that distort them, instead of on a monitor timer. Switching
	// a client into this session also sweeps leftover hub zooms, so leaving
	// the hub for this session brings its chrome bar back at once.
	for _, ev := range chromeSizeHookEvents {
		_ = exec.Command("tmux", chromeSizeHookArgs(SessionName, ev, exe, pidDir)...).Run()
	}

	// Gone handling when a window is destroyed, with no monitor involved.
	_ = exec.Command("tmux", reconcileHookArgs(SessionName, exe, pidDir)...).Run()
	done()
}

// applyStylingAndKeybindings sets the misc options, pane borders and
// keybindings.
func applyStylingAndKeybindings(exe string) {
	// Misc options
	done := logging.Timing("applyConfig: misc options (7 calls)")
	set("visual-silence", "off")
	set("visual-activity", "off")
	set("renumber-windows", "on")
	set("mouse", "on")
	set("history-limit", "200000")
	// Terminal window/tab title: show "agentctl · <project> · <window>" so the
	// OS title bar reflects which project and agent are active. set-titles is
	// session-scoped (-t SessionName) and does not affect other tmux sessions.
	set("set-titles", "on")
	set("set-titles-string", titleString(paths.DirName()))
	applyExtendedKeys()
	// Invisible pane borders — match terminal background so chrome panes blend in.
	// Uses the theme's Base color so it adapts to the active theme.
	borderColor := theme.Current().Base
	setw("pane-border-style", "fg="+borderColor)
	setw("pane-active-border-style", "fg="+borderColor)
	setw("pane-border-lines", "single")
	done()

	// Keybindings: prefix (ctrl-b) then key
	done = logging.Timing("applyConfig: keybindings (6 calls)")
	for _, kb := range agentctlKeybindings(exe) {
		bind(kb.key, kb.command)
	}
	bindRepeat("<", "resize-pane -L 5")
	bindRepeat(">", "resize-pane -R 5")
	bind("[", "previous-window")
	bind("]", "next-window")

	// Cmd+[ / Cmd+] tab switching (Ghostty sends Alt+Left/Right CSI sequences)
	bindRoot("M-Left", "previous-window")
	bindRoot("M-Right", "next-window")

	// Cmd+< / Cmd+> pane switching (Ghostty sends Alt+Up/Down CSI sequences)
	// Main pane is left, HUD pane is right.
	bindRoot("M-Up", "select-pane -L")
	bindRoot("M-Down", "select-pane -R")
	done()
}

// SplitPaneBelow splits a new pane below the target pane.
// targetPaneID is a tmux pane ID (e.g. "%3"). rows is an absolute row count or percentage
// (e.g. "1", "2", "30%"). Returns the new pane ID.
func SplitPaneBelow(targetPaneID, rows, command string) (string, error) {
	logging.Debug("tmux: split-pane %s direction=below rows=%s", targetPaneID, rows)
	args := []string{
		"split-window",
		"-v",
		"-t", targetPaneID,
		"-l", rows,
		"-P", "-F", "#{pane_id}",
		command,
	}
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("split-window-below: %s: %w", string(out), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// DisablePaneInput disables keyboard input to a pane.
func DisablePaneInput(paneID string) error {
	return exec.Command("tmux", "select-pane", "-t", paneID, "-d").Run()
}

// SelectPane makes the given pane the active (focused) pane.
func SelectPane(paneID string) error {
	return exec.Command("tmux", "select-pane", "-t", paneID).Run()
}

// FindMainPane exposes findMainPane to callers outside this package (e.g.
// the monitor's chrome-bar backfill, which needs to resolve a window's main
// pane without duplicating the "everything except chrome/HUD" logic).
func FindMainPane(windowID string) string {
	return findMainPane(windowID)
}

// findMainPane returns the pane ID of the main (terminal) pane in a window.
// This is the pane that is NOT a chrome bar or HUD pane.
func findMainPane(windowID string) string {
	ws, _ := windowstate.Read(windowID)
	id, _ := MainPaneOf(SessionName, windowID, ws.ChromeBarPane, ws.HudPane)
	return id
}

// MainPaneOf returns the main pane of <session>:<windowID> given the
// window's recorded chrome bar and HUD panes: findMainPane with every input
// explicit, so a caller that serves many sessions (agentctl serve) resolves
// the same pane InjectPromptToWindow types into without binding the
// package-global session.
func MainPaneOf(session, windowID, chromeBarPane, hudPane string) (string, error) {
	target := sessionWindowTarget(session, windowID)
	out, err := exec.Command("tmux", "list-panes", "-t", target, "-F", "#{pane_id}").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("list-panes %s: %s: %w", target, strings.TrimSpace(string(out)), err)
	}
	if id := pickMainPane(string(out), chromeBarPane, hudPane); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("no main pane in %s", target)
}

// pickMainPane returns the first pane ID in list-panes output that is
// neither the chrome bar nor the HUD pane.
func pickMainPane(listPanes, chromeBarPane, hudPane string) string {
	for _, id := range strings.Split(strings.TrimSpace(listPanes), "\n") {
		if id != chromeBarPane && id != hudPane && id != "" {
			return id
		}
	}
	return ""
}

// WindowIDByName returns the tmux window ID for the first window with the given
// name in the agentctl session, or "" if not found.
func WindowIDByName(name string) string {
	return windowIDByName(name)
}

// windowIDByName returns the tmux window ID for the first window with the given
// name in the agentctl session, or "" if not found.
func windowIDByName(name string) string {
	out, err := exec.Command("tmux", "list-windows", "-t", ExactSession(SessionName),
		"-F", "#{window_id} #{window_name}").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) == name {
			return strings.TrimSpace(parts[0])
		}
	}
	return ""
}

// ChromeBarRows is the fixed height of the chrome bar pane docked at the
// bottom of every agentctl window: keybind hints + agent tabs. Rate limits and
// per-window metrics live in the "rich" Claude Code status line instead. LaunchChromeBar (pane creation) and
// EnforceMainPaneSize (main pane sizing) both derive from this single
// constant so they can never disagree about the chrome bar's height.
const ChromeBarRows = 2

// chromeRowsForLayout returns the total chrome-bar row count EnforceMainPaneSize
// should subtract from the main pane's target height, given whether a chrome
// bar pane is present. Derived from ChromeBarRows so this can never disagree
// with chromeBarSplitArgs/LaunchChromeBar about the bar's actual height.
func chromeRowsForLayout(hasChromeBar bool) int {
	if hasChromeBar {
		return ChromeBarRows
	}
	return 0
}

// chromeBarSplitArgs returns the tmux split-window args used by
// LaunchChromeBar to create the chrome bar pane at exactly ChromeBarRows
// rows, anchored to lastPane and running chromeBarCmd. Split out from
// LaunchChromeBar so the args (and their dependence on ChromeBarRows) are
// unit-testable without a live tmux server.
func chromeBarSplitArgs(lastPane, chromeBarCmd string) []string {
	return []string{
		"split-window",
		"-v", "-f",
		"-t", lastPane,
		"-l", strconv.Itoa(ChromeBarRows),
		"-P", "-F", "#{pane_id}",
		chromeBarCmd,
	}
}

// LaunchChromeBar creates a ChromeBarRows-row pane at the bottom of the given
// window running the agentctl chrome-bar command: keybind hints and agent
// tabs, eliminating the tmux pane-border line that would otherwise appear
// between separate panes.
//
// The pane is created with split-window -v -f -l <ChromeBarRows> so it
// steals rows from the whole layout rather than from a single adjacent pane.
// The pane ID is stored as @agentctl_chrome_bar to prevent duplicate bars.
//
// Utility windows (tickets, sessions, agents) are skipped because they run
// full-screen TUI applications that must fill the entire window.
func LaunchChromeBar(windowID string) error {
	// The hub window gets no chrome: the agent's own chrome bar, inside the
	// live pane, is the only tab bar.
	if IsHubWindow(windowID) {
		return nil
	}
	// Skip utility windows — they use the full screen.
	if winName := WindowNameByID(windowID); utilityWindowNames[winName] {
		return nil
	}

	// Avoid creating a duplicate chrome bar.
	ws, _ := windowstate.Read(windowID)
	if ws.ChromeBarPane != "" {
		return nil
	}

	exe := selfpath.SpawnableOrFallback()
	chromeBarCmd := shellquote.Quote(exe) + " chrome-bar " + windowID

	// Find the last pane so the bar appears at the absolute bottom.
	target := sessionWindowTarget(SessionName, windowID)
	out, err := exec.Command("tmux", "list-panes", "-t", target, "-F", "#{pane_id}").Output()
	if err != nil {
		return fmt.Errorf("list-panes for chrome bar: %w", err)
	}
	panes := strings.Fields(strings.TrimSpace(string(out)))
	if len(panes) == 0 {
		return fmt.Errorf("no panes found in window %s", windowID)
	}
	lastPane := panes[len(panes)-1]

	// Use -f (full-window split) with an absolute row count so the bar steals
	// from the whole layout, not just the adjacent pane.
	args := chromeBarSplitArgs(lastPane, chromeBarCmd)
	rawOut, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("chrome bar split: %s: %w", string(rawOut), err)
	}
	paneID := strings.TrimSpace(string(rawOut))
	// Input stays enabled: tmux drops mouse events for input-off panes, and the
	// chrome bar handles tab clicks (see tui.ChromeBarModel).
	mainPane := findMainPane(windowID)
	windowstate.Update(windowID, func(wst *windowstate.WindowState) { //nolint:errcheck
		wst.ChromeBarPane = paneID
		wst.MainPane = mainPane
	})
	// Refocus the main content pane; split-window leaves focus on the new pane.
	if mainPane != "" {
		_ = SelectPane(mainPane)
	}
	return nil
}

// enforceChromeSizes pins chrome panes to their configured height by growing
// the main pane to consume all remaining space. tmux may inflate chrome panes
// during window resize or attach; growing the main pane forces all chrome
// panes to their minimum without triggering the ping-pong effect that occurs
// when chrome panes are resized individually.
func enforceChromeSizes(windowID string) {
	EnforceMainPaneSize(windowID)
}

// EnforceAllChromeSizes calls EnforceMainPaneSize for every window in the
// agentctl session that has a ChromeBarPane set in its windowstate. This
// covers windows (such as the shell window) that are skipped by the monitor's
// per-agent processing loop. It should be called periodically from the monitor
// tick (e.g. every ~5 ticks / 2.5 s) to correct any size inflation caused by
// terminal resizes or tmux attach from a different terminal size.
func EnforceAllChromeSizes() {
	if !SessionExists() {
		return
	}
	out, err := exec.Command("tmux", "list-windows", "-t", ExactSession(SessionName),
		"-F", "#{window_id}").Output()
	if err != nil {
		return
	}
	for _, id := range strings.Fields(strings.TrimSpace(string(out))) {
		ws, _ := windowstate.Read(id)
		if ws.ChromeBarPane != "" {
			EnforceMainPaneSize(id)
		}
	}
}

// windowSizeFormat is the display-message format EnforceMainPaneSize reads
// (parsed by parseWindowHeightZoom).
const windowSizeFormat = "#{window_height} #{window_zoomed_flag}"

// parseWindowHeightZoom parses windowSizeFormat output into the window height and
// whether the window is zoomed. EnforceMainPaneSize skips zoomed windows.
func parseWindowHeightZoom(out string) (height int, zoomed bool, err error) {
	f := strings.Fields(out)
	if len(f) == 0 {
		return 0, false, fmt.Errorf("empty window size %q", out)
	}
	height, err = strconv.Atoi(f[0])
	if err != nil || height <= 0 {
		return 0, false, fmt.Errorf("invalid window_height %q", f[0])
	}
	return height, len(f) > 1 && f[1] == "1", nil
}

// EnforceMainPaneSize grows the main (terminal) pane to fill all available
// window space, which forces every chrome pane (header, footer, status bar)
// to its minimum height of 1 row. This avoids the ping-pong inflation that
// occurs when adjacent chrome panes are shrunk individually: tmux redistributes
// freed rows to the nearest adjacent pane rather than to the main pane.
//
// The target height is:
//
//	windowHeight - chromePaneCount×1 - borderCount(numPanes-1)
//
// A zoomed window is skipped: resize-pane would unzoom it.
func EnforceMainPaneSize(windowID string) {
	mainPane := findMainPane(windowID)
	if mainPane == "" {
		logging.Log("tmux: EnforceMainPaneSize: no main pane found for window %s", windowID)
		return
	}

	// Query window height and zoom state.
	target := sessionWindowTarget(SessionName, windowID)
	out, err := exec.Command("tmux", "display-message", "-t", target, "-p", windowSizeFormat).Output()
	if err != nil {
		logging.Log("tmux: EnforceMainPaneSize: display-message failed: %v", err)
		return
	}
	windowHeight, zoomed, err := parseWindowHeightZoom(string(out))
	if err != nil {
		logging.Log("tmux: EnforceMainPaneSize: %v", err)
		return
	}
	if zoomed {
		// resize-pane unzooms a zoomed window: leave it to whoever zoomed
		// it (the user, or the hub hiding the chrome bar, see hubzoom.go).
		return
	}

	// Count chrome panes and their total row height.
	chromePaneCount := 0
	chromeRowTotal := 0

	wsLayout, _ := windowstate.Read(windowID)
	if wsLayout.ChromeBarPane != "" {
		chromePaneCount++
		chromeRowTotal += chromeRowsForLayout(true)
	}

	if chromePaneCount == 0 {
		return // nothing to enforce
	}

	// Account for HUD pane if present — read its current height and preserve it.
	hudPaneHeight := 0
	hudPaneBorder := 0
	if wsLayout.HudPane != "" {
		hudPaneHeight = PaneHeight(wsLayout.HudPane)
		hudPaneBorder = 1 // border between main and HUD
	}

	// Total panes = main + chrome panes (+ HUD if present).
	// Borders = numPanes - 1 (one border line between each adjacent pair).
	numPanes := 1 + chromePaneCount
	borderCount := numPanes - 1
	targetHeight := windowHeight - chromeRowTotal - borderCount - hudPaneHeight - hudPaneBorder
	if targetHeight < 1 {
		targetHeight = 1
	}

	if err := ResizePaneHeight(mainPane, targetHeight); err != nil {
		logging.Log("tmux: EnforceMainPaneSize: resize main pane %s to %d: %v", mainPane, targetHeight, err)
	}
}

// ResizePaneHeight sets the absolute height of a tmux pane.
func ResizePaneHeight(paneID string, height int) error {
	out, err := exec.Command("tmux", "resize-pane", "-t", paneID, "-y", fmt.Sprintf("%d", height)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("resize-pane: %s: %w", string(out), err)
	}
	return nil
}

// ToggleHud toggles the HUD pane for the given agent window.
// If no HUD pane exists, it ensures chrome panes exist and opens the HUD below the terminal.
// If a HUD pane exists and is expanded, it collapses it to 2 rows (metrics only).
// If a HUD pane exists and is collapsed, it expands it back to 30% of the window height.
func ToggleHud(windowID string) error {
	if IsHubWindow(windowID) {
		return fmt.Errorf("no HUD in the hub; select an agent first")
	}
	wsHud, _ := windowstate.Read(windowID)
	if wsHud.HudPane != "" {
		if wsHud.HudCollapsed {
			// Expand back to 30% of window height.
			out, _ := exec.Command("tmux", "display-message", "-t",
				sessionWindowTarget(SessionName, windowID), "-p", "#{window_height}").CombinedOutput()
			totalH, _ := strconv.Atoi(strings.TrimSpace(string(out)))
			targetH := totalH * 30 / 100
			if targetH < 5 {
				targetH = 5
			}
			_ = ResizePaneHeight(wsHud.HudPane, targetH)
			windowstate.Update(windowID, func(ws *windowstate.WindowState) { //nolint:errcheck
				ws.HudCollapsed = false
			})
			logging.Log("tmux: HUD expand → %d rows window=%s", targetH, windowID)
			enforceChromeSizes(windowID)
		} else {
			// Collapse to 2 rows (just the metrics bar).
			_ = ResizePaneHeight(wsHud.HudPane, 2)
			windowstate.Update(windowID, func(ws *windowstate.WindowState) { //nolint:errcheck
				ws.HudCollapsed = true
			})
			logging.Log("tmux: HUD collapse → 2 rows window=%s", windowID)
			enforceChromeSizes(windowID)
		}
		return nil
	}

	// Ensure the chrome bar exists — if not, launch it before opening the HUD.
	if wsHud.ChromeBarPane == "" {
		_ = LaunchChromeBar(windowID)
	}

	exe := selfpath.SpawnableOrFallback()
	cmd := fmt.Sprintf("%s hud %s", shellquote.Quote(exe), windowID)

	// Split the main (terminal) pane to open the HUD below it.
	mainPane := findMainPane(windowID)
	if mainPane == "" {
		return fmt.Errorf("no main pane found")
	}
	paneID, err := SplitPaneBelow(mainPane, "30%", cmd)
	if err != nil {
		return err
	}
	windowstate.Update(windowID, func(ws *windowstate.WindowState) { //nolint:errcheck
		ws.HudPane = paneID
		ws.HudCollapsed = false
	})
	logging.Log("tmux: HUD open → pane=%s window=%s", paneID, windowID)
	return nil
}

// titleString returns the set-titles-string value for the given project
// directory name. The #W token is expanded by tmux at render time to the
// active window name, giving "agentctl · <project> · <agent>" in the OS
// window/tab title. When project is empty or a bare path component ("." or
// "/"), "agentctl" is used as a safe fallback.
func titleString(project string) string {
	if project == "" || project == "." || project == "/" {
		project = "agentctl"
	}
	return "agentctl · " + project + " · #W"
}

func set(key, value string) {
	_ = exec.Command("tmux", "set-option", "-t", ExactSessionTarget(SessionName), key, value).Run()
}

// setw sets a global window-level tmux option so it applies to all windows
// (existing and future). Some options (e.g. pane-border-style) are
// window-scoped and are silently ignored by set() which uses session scope.
func setw(key, value string) {
	_ = exec.Command("tmux", "set-option", "-gw", key, value).Run()
}

func bind(key, command string) {
	_ = exec.Command("tmux", "bind-key", key, command).Run()
}

func bindRepeat(key, command string) {
	_ = exec.Command("tmux", "bind-key", "-r", key, command).Run()
}

func bindRoot(key, command string) {
	_ = exec.Command("tmux", "bind-key", "-n", key, command).Run()
}

// OpenOrFocusTicketsWindow focuses the persistent "tickets" window in the
// agentctl session. The window is created at session startup and should always
// exist; if it has been killed, this function recreates it.
func OpenOrFocusTicketsWindow() error {
	target := sessionWindowTarget(SessionName, "tickets")

	// Fast path: window exists — just focus it.
	if err := exec.Command("tmux", "select-window", "-t", target).Run(); err == nil {
		return nil
	}

	// Window died — recreate it.
	exe := selfpath.SpawnableOrFallback()

	// Capture the currently active window ID so the tickets process can return
	// focus to it on exit (best-effort; empty string is handled gracefully).
	originID := CurrentWindowID()

	cmd := exec.Command("tmux", "new-window",
		"-t", ExactSessionTarget(SessionName),
		"-n", "tickets",
		"-e", "AGENTCTL=1",
		"-e", "AGENTCTL_ORIGIN_WINDOW="+originID,
		exe, "tickets", "--tui",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("new-window tickets: %s: %w", string(out), err)
	}
	return nil
}

// OpenOrFocusSessionsWindow opens a dedicated "sessions" window in the agentctl
// session, or focuses it if it already exists. The window runs `agentctl sessions`
// in full-screen alt-screen mode.
func OpenOrFocusSessionsWindow() error {
	exe := selfpath.SpawnableOrFallback()
	windowName := "sessions"
	target := sessionWindowTarget(SessionName, windowName)

	// Check if the window already exists.
	err := exec.Command("tmux", "select-window", "-t", target).Run()
	if err == nil {
		// Window exists and is now focused.
		return nil
	}

	// Capture the currently active window ID so the child process can return focus to it on exit.
	originID := CurrentWindowID()

	// Window doesn't exist — create it.
	sessionTarget := ExactSessionTarget(SessionName)
	cmd := exec.Command("tmux", "new-window",
		"-t", sessionTarget,
		"-n", windowName,
		"-e", "AGENTCTL=1",
		"-e", "AGENTCTL_ORIGIN_WINDOW="+originID,
		exe, "sessions", "--tui",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("new-window sessions: %s: %w", string(out), err)
	}
	return nil
}

// OpenOrFocusAgentsWindow opens a dedicated "agents" window in the agentctl
// session, or focuses it if it already exists. The window runs `agentctl agents`
// in full-screen alt-screen mode.
func OpenOrFocusAgentsWindow() error {
	exe := selfpath.SpawnableOrFallback()
	windowName := "agents"
	target := sessionWindowTarget(SessionName, windowName)

	// Check if the window already exists.
	err := exec.Command("tmux", "select-window", "-t", target).Run()
	if err == nil {
		return nil
	}

	originID := CurrentWindowID()

	sessionTarget := ExactSessionTarget(SessionName)
	cmd := exec.Command("tmux", "new-window",
		"-t", sessionTarget,
		"-n", windowName,
		"-e", "AGENTCTL=1",
		"-e", "AGENTCTL_ORIGIN_WINDOW="+originID,
		exe, "agents", "--tui",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("new-window agents: %s: %w", string(out), err)
	}
	return nil
}

// OpenOrFocusContextWindow opens a dedicated "context" window in the agentctl
// session, or focuses it if it already exists. The window runs
// `agentctl context --tui` in full-screen alt-screen mode.
func OpenOrFocusContextWindow() error {
	exe := selfpath.SpawnableOrFallback()
	windowName := "context"
	target := sessionWindowTarget(SessionName, windowName)

	if err := exec.Command("tmux", "select-window", "-t", target).Run(); err == nil {
		return nil
	}

	originID := CurrentWindowID()

	cmd := exec.Command("tmux", "new-window",
		"-t", ExactSessionTarget(SessionName),
		"-n", windowName,
		"-e", "AGENTCTL=1",
		"-e", "AGENTCTL_ORIGIN_WINDOW="+originID,
		exe, "context", "--tui",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("new-window context: %s: %w", string(out), err)
	}
	return nil
}

// ReturnFocusAndKillWindow switches tmux focus back to originWindowID (if non-empty)
// and then kills the named window in the agentctl session. It is a best-effort
// operation; individual step failures are silently ignored so that the calling
// process can exit cleanly regardless.
func ReturnFocusAndKillWindow(originWindowID, windowName string) {
	if originWindowID != "" {
		exec.Command("tmux", "select-window", "-t", originWindowID).Run() //nolint:errcheck
	}
	target := sessionWindowTarget(SessionName, windowName)
	exec.Command("tmux", "kill-window", "-t", target).Run() //nolint:errcheck
}

// WriteToPaneDirect overwrites the content of a 1-row pane by sending a
// carriage return + clear-to-EOL escape followed by the new content.
// The -l flag sends literal characters (no tmux key lookup).
func WriteToPaneDirect(paneID, content string) error {
	payload := "\r\033[K" + content
	return exec.Command("tmux", "send-keys", "-t", paneID, "-l", payload).Run()
}

// sigTermPaneGroups sends SIGTERM to the process group of each PID listed in
// the whitespace/newline-separated output from `tmux list-panes -F #{pane_pid}`.
// Using a negative PID signals the entire process group, catching all shells
// and their children. Errors are silently ignored — the processes may have
// already exited by the time we signal them.
func sigTermPaneGroups(panePIDOutput string) {
	for _, line := range strings.Split(strings.TrimSpace(panePIDOutput), "\n") {
		line = strings.TrimSpace(line)
		if pid, err := strconv.Atoi(line); err == nil && pid > 1 {
			syscall.Kill(-pid, syscall.SIGTERM) //nolint:errcheck
		}
	}
}

// PaneHeight returns the height (rows) of the given pane.
func PaneHeight(paneID string) int {
	out, err := exec.Command("tmux", "display-message", "-t", paneID, "-p", "#{pane_height}").Output()
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// PaneWidth returns the width (columns) of the given pane.
func PaneWidth(paneID string) int {
	out, err := exec.Command("tmux", "display-message", "-t", paneID, "-p", "#{pane_width}").Output()
	if err != nil {
		return 80
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n <= 0 {
		return 80
	}
	return n
}

// DisplayTarget returns the trimmed output of `tmux display-message -p -t
// target format`, resolving format against target (a pane ID such as "%3" or
// a window target such as "agentctl:@5") rather than the client's focused
// window. Returns "" on any error.
func DisplayTarget(target, format string) string {
	out, err := exec.Command("tmux", "display-message", "-t", target, "-p", format).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
