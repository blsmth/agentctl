// hub.go holds the tmux primitives for the herd hub: a dedicated, persistent
// tmux session (agentctl-hub) that runs the cross-session aggregator view.
// Unlike per-project agentctl-<dir> sessions, the hub owns no agents, so its
// lifecycle deliberately bypasses applyConfig (no monitor, no restore, no
// chrome bars, no after-new-window hook).
package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/selfpath"
)

// HubSessionName is the fixed tmux session name for the herd hub. Unlike
// per-project sessions it does not vary with the launching directory.
const HubSessionName = "agentctl-hub"

// HerdListWindowName is the stable tmux window name of the hub session's
// sole home-grown window — the one running `agentctl hub-view` (the
// HerdModel aggregator). It's given a fixed name (rather than left to
// default to the running command) so it can be targeted reliably by
// SelectHerdListWindow once other windows — linked-in agent windows — start
// sharing the session.
const HerdListWindowName = "herd-list"

// ListSessions returns the names of every tmux session on the local server.
// Returns an empty slice (not an error) when no tmux server is running, since
// this is the liveness input for registry.Prune and "no server" simply means
// "no live sessions" — never abort a caller's whole aggregation over it.
func ListSessions() []string {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil
	}
	return parseSessionList(string(out))
}

// parseSessionList parses the output of `tmux list-sessions -F '#{session_name}'`
// into a slice of session names, skipping blank lines.
func parseSessionList(raw string) []string {
	var sessions []string
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !IsAnyViewerSession(line) {
			sessions = append(sessions, line)
		}
	}
	return sessions
}

// SessionInfo holds a tmux session's name and its working directory
// (#{session_path}). This is the herd hub's primary discovery source: tmux
// exposes the project directory directly, so no registry lookup is needed to
// find where a session's `.agentctl/state.json` lives (see
// docs/herd-view-design.md).
type SessionInfo struct {
	Name string
	Path string
}

// ListSessionsWithPaths returns the name and working directory of every tmux
// session on the local server. Returns an empty slice (not an error) when no
// tmux server is running or no sessions are open — mirrors ListSessions, so
// callers never have to special-case "no server" as an error.
func ListSessionsWithPaths() []SessionInfo {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}|#{session_path}").Output()
	if err != nil {
		return nil
	}
	return parseSessionsWithPaths(string(out))
}

// parseSessionsWithPaths parses the output of
// `tmux list-sessions -F '#{session_name}|#{session_path}'`. It splits on
// the first "|" only, so a session path that happens to contain "|" doesn't
// corrupt the split (a path containing a newline is out of scope — tmux's
// own -F output is newline-delimited per session).
func parseSessionsWithPaths(raw string) []SessionInfo {
	var sessions []SessionInfo
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 || parts[0] == "" || IsAnyViewerSession(parts[0]) {
			continue
		}
		sessions = append(sessions, SessionInfo{Name: parts[0], Path: parts[1]})
	}
	return sessions
}

// sessionWindowTarget builds a session-qualified tmux target ("session:@id")
// for commands that must address a window in a session other than the
// caller's own — SwitchToWindow only ever targets the package-global
// SessionName, which isn't good enough for a hub jumping across sessions.
func sessionWindowTarget(session, windowID string) string {
	return session + ":" + windowID
}

// SwitchToSessionWindow switches the attached client to a specific window in
// a specific (possibly different) tmux session. This is the hub's jump
// primitive: existing SwitchToWindow only targets the package-global
// SessionName, so it can't reach a window living in another session.
func SwitchToSessionWindow(session, windowID string) error {
	target := sessionWindowTarget(session, windowID)
	cmd := exec.Command("tmux", "switch-client", "-t", target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("switch-client %s: %s: %w", target, string(out), err)
	}
	return nil
}

// CapturePane returns the last `lines` lines of a tmux pane's content via
// `tmux capture-pane -p -t <session>:<windowID>`, trimmed of trailing blank
// lines. Plain text only: sweep, status and pane classification pattern-match
// on it. Read-only and non-destructive.
//
// Returns an error if the pane is gone (e.g. the window closed between
// polls) — callers must render a placeholder, never propagate a crash.
func CapturePane(session, windowID string, lines int) (string, error) {
	target := sessionWindowTarget(session, windowID)
	args := capturePaneArgs(target, lines)
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return "", fmt.Errorf("capture-pane %s: %w", target, err)
	}
	return trimTrailingBlankLines(string(out)), nil
}

// SendKeyToWindow sends one tmux key name (e.g. "1", "Escape") to
// <session>:<windowID>, the same target CapturePane and CapturePaneANSI
// read, so a caller that classified the captured pane types into exactly the
// pane it checked. The key is passed without -l, so tmux interprets key names;
// callers must allowlist key before calling.
func SendKeyToWindow(session, windowID, key string) error {
	return sendKeyFn(sessionWindowTarget(session, windowID), key)
}

// sendKeyFn runs `tmux send-keys -t target key`; a seam for tests.
var sendKeyFn = func(target, key string) error {
	if out, err := exec.Command("tmux", sendKeyArgs(target, key)...).CombinedOutput(); err != nil {
		return fmt.Errorf("send-keys %s: %s: %w", target, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// sendKeyArgs builds the send-keys argument list. "--" ends option parsing,
// so a key can never be read as a flag.
func sendKeyArgs(target, key string) []string {
	return []string{"send-keys", "-t", target, "--", key}
}

// CapturePaneANSI is CapturePane with `-e`: colour and style escape
// sequences are kept so a client can render the pane faithfully. Classify it
// only after panestate.StripANSI.
func CapturePaneANSI(session, windowID string, lines int) (string, error) {
	target := sessionWindowTarget(session, windowID)
	args := capturePaneArgs(target, lines)
	args = append(args[:2], append([]string{"-e"}, args[2:]...)...)
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return "", fmt.Errorf("capture-pane %s: %w", target, err)
	}
	return trimTrailingBlankLines(string(out)), nil
}

// capturePaneArgs builds the capture-pane argument list.
func capturePaneArgs(target string, lines int) []string {
	args := []string{"capture-pane", "-p", "-t", target}
	if lines > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(lines))
	}
	return args
}

// trimTrailingBlankLines removes trailing blank (whitespace-only) lines from
// captured pane output. tmux's capture-pane pads its result to the pane's
// full height with blank lines, which would otherwise push the actual
// content off the top of a shorter render area.
func trimTrailingBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(ansi.Strip(lines[end-1])) == "" {
		end--
	}
	return strings.Join(lines[:end], "\n")
}

// SwitchToLastSession switches the attached client back to whichever session
// it was previously attached to (`tmux switch-client -l`). Used to return from
// the hub to wherever the user came from.
func SwitchToLastSession() error { return SwitchClientToLast("") }

// SwitchClientToLast is SwitchToLastSession aimed at a specific client (-c)
// when client is non-empty.
func SwitchClientToLast(client string) error {
	args := []string{"switch-client"}
	if client != "" {
		args = append(args, "-c", client)
	}
	args = append(args, "-l")
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("switch-client -l: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// SwitchClientToSession switches client (or the current one when empty) to
// the named session. The target is "=name:" for an exact-match session.
func SwitchClientToSession(client, session string) error {
	args := []string{"switch-client"}
	if client != "" {
		args = append(args, "-c", client)
	}
	args = append(args, "-t", "="+session+":")
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("switch-client %s: %s: %w", session, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// ListSessionActivity returns "name\tactivity" lines for every session
// (including viewers; callers filter).
func ListSessionActivity() []SessionActivity {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}\t#{session_activity}").Output()
	if err != nil {
		return nil
	}
	var res []SessionActivity
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, act, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || name == "" {
			continue
		}
		n, _ := strconv.ParseInt(act, 10, 64)
		res = append(res, SessionActivity{Name: name, Activity: n})
	}
	return res
}

// SessionActivity is a session name with its #{session_activity} epoch.
type SessionActivity struct {
	Name     string
	Activity int64
}

// HubSessionExists reports whether the hub session is currently running.
func HubSessionExists() bool {
	return exec.Command("tmux", "has-session", "-t", HubSessionName).Run() == nil
}

// SelectHerdListWindow selects the hub's own herd-list window (the one
// running the HerdModel aggregator) by its stable name. This is the "back to
// the list" primitive: `agentctl hub` calls it when the caller is already
// inside the hub session (see SwitchToHub), so the summon key works as a
// consistent "take me to the list" gesture from anywhere — including from
// inside the live hub view.
func SelectHerdListWindow() error {
	target := sessionWindowTarget(HubSessionName, HerdListWindowName)
	cmd := exec.Command("tmux", "select-window", "-t", target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("select-window %s: %s: %w", target, string(out), err)
	}
	return nil
}

// CurrentSessionName returns the tmux session name of the currently attached
// client, or "" if the caller isn't running inside a tmux client (no TMUX
// env var) or the query fails. Used by SwitchToHub to detect "already inside
// the hub" so it can select the herd-list window instead of issuing a
// (harmless but unhelpful) switch-client to the session it's already in.
func CurrentSessionName() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	out, err := exec.Command("tmux", "display-message", "-p", "#{session_name}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sessionNameForPaneTimeout bounds SessionNameForPane's tmux query. A wedged
// tmux server must not hang the notification path that calls it.
const sessionNameForPaneTimeout = 2 * time.Second

// queryPaneSessionName runs the actual tmux query for SessionNameForPane.
// Replaced in tests so the fallback cascade is exercisable without a live
// tmux server.
var queryPaneSessionName = func(pane string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sessionNameForPaneTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "-t", pane, "#{session_name}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SessionNameForPane returns the tmux session name owning pane (e.g. "%12"),
// or "" if pane is empty or the query fails.
//
// Unlike CurrentSessionName, which resolves against the attached *client*
// (bare `tmux display-message`), this targets the pane directly with
// `-t <pane>` -- the pane a hook runs in is not necessarily the one the
// attached client is currently looking at. This matters for notifications:
// tmux.SessionName is not usable here (see the Notify caller's doc comment),
// and CurrentSessionName would misattribute an alert to whatever session the
// operator happens to be looking at rather than the one it actually came
// from.
func SessionNameForPane(pane string) string {
	if pane == "" {
		return ""
	}
	name, err := queryPaneSessionName(pane)
	if err != nil {
		return ""
	}
	return homeOfSession(name)
}

// hubDir returns ~/.local/state/agentctl/hub, creating it if necessary. The hub
// session's working directory is pinned here so that paths.Init(cwd) — which
// every agentctl command runs on startup and which creates a .agentctl/
// directory in the cwd — never litters whatever directory the user happened
// to run `agentctl hub` from.
func hubDir() (string, error) {
	base, err := config.StateDir()
	if err != nil {
		return "", fmt.Errorf("resolving state dir: %w", err)
	}
	dir := filepath.Join(base, "hub")
	if cfg, cerr := config.GlobalDir(); cerr == nil {
		dir = config.MigrateLegacy(filepath.Join(cfg, "hub"), dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating hub directory: %w", err)
	}
	return dir, nil
}

// hubNewSessionArgs builds the `tmux new-session` argument list for the hub
// session. Factored out so the env vars it seeds are directly testable
// without a live tmux server.
func hubNewSessionArgs(dir, exe string) []string {
	return []string{
		"new-session",
		"-d",
		"-s", HubSessionName,
		"-n", HerdListWindowName,
		"-c", FormatEscape(dir), // -c is format-expanded
		"-e", "AGENTCTL=1",
		// Explicitly clear AGENTCTL_RUNTIME_DIR. tmux new-session seeds a
		// session's environment from whatever process invoked it (e.g. a
		// keybinding's run-shell, or a dispatched agent's own shell), so
		// without this the hub could silently inherit a project's runtime
		// dir and start writing its own state into that project's
		// .agentctl/ instead of hubDir(). An empty value is indistinguishable
		// from unset to paths.Init, which is exactly what we want here.
		"-e", "AGENTCTL_RUNTIME_DIR=",
		exe, "hub-view",
	}
}

// StartHubSession creates the hub session detached, running the aggregator
// (`agentctl hub-view`) full-screen as its only window, with its working
// directory pinned to hubDir().
//
// Deliberately does NOT call applyConfig(): the hub owns no agents, so it
// gets none of applyConfig's per-project machinery (monitor, restore, chrome
// bars, after-new-window hook). Only the minimal options the aggregator
// itself needs are set directly.
func StartHubSession() error {
	if HubSessionExists() {
		return nil
	}

	dir, err := hubDir()
	if err != nil {
		return err
	}

	exe := selfpath.SpawnableOrFallback()

	args := hubNewSessionArgs(dir, exe)
	cmd := exec.Command("tmux", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("creating hub session: %s: %w", string(out), err)
	}

	_ = exec.Command("tmux", "set-option", "-t", HubSessionName, "status", "off").Run()
	_ = exec.Command("tmux", "set-option", "-t", HubSessionName, "mouse", "on").Run()
	_ = exec.Command("tmux", "set-option", "-t", HubSessionName, "set-titles", "on").Run()
	_ = exec.Command("tmux", "set-option", "-t", HubSessionName, "set-titles-string", "agentctl · hub").Run()
	// Pin the herd-list window's name: nothing re-asserts it every tick (the
	// way the monitor keeps agent windows' state-glyph names sticky), so
	// automatic-rename would otherwise be free to drift it away from
	// HerdListWindowName over time, breaking SelectHerdListWindow's targeting.
	herdListTarget := sessionWindowTarget(HubSessionName, HerdListWindowName)
	_ = exec.Command("tmux", "set-window-option", "-t", herdListTarget, "automatic-rename", "off").Run()

	return nil
}

// SwitchToHub creates the hub session if it doesn't already exist, then
// gets the caller to the herd-list window: `select-window` if the caller is
// already attached to the hub session itself (e.g. the summon key pressed
// from inside the live hub view — see
// SelectHerdListWindow), `switch-client` if attached to a different tmux
// session, or `attach-session` if not inside any tmux client at all (e.g.
// summoning the hub from outside tmux entirely).
func SwitchToHub() error {
	if err := StartHubSession(); err != nil {
		return err
	}

	if os.Getenv("TMUX") != "" {
		if CurrentSessionName() == HubSessionName {
			return SelectHerdListWindow()
		}
		cmd := exec.Command("tmux", "switch-client", "-t", HubSessionName)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("switch-client %s: %s: %w", HubSessionName, string(out), err)
		}
		return nil
	}

	cmd := exec.Command("tmux", "attach-session", "-t", HubSessionName)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// WindowIDForPane returns the tmux window ID that owns a pane (e.g. "%306" →
// "@275"). Empty when tmux cannot be reached or the pane no longer exists.
//
// This is the one identity source that is always correct for the calling
// process: tmux sets TMUX_PANE per pane, so unlike AGENTCTL_WINDOW_ID (which
// is session-global and last-writer-wins) or AGENTCTL_SESSION_ID (which a
// forked or background session inherits from its parent), it cannot silently
// name a different window than the caller's own.
func WindowIDForPane(pane string) string {
	if pane == "" {
		return ""
	}
	out, err := exec.Command("tmux", "display-message", "-t", pane, "-p", "#{window_id}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// KillSessionWindow destroys the window windowID in its home session, which
// also removes any link of it (e.g. into the hub). It refuses the hub session
// itself and anything that is not a "@N" window ID, so the hub can never close
// its own list window. Pane process groups are SIGTERMed like KillWindow.
func KillSessionWindow(session, windowID string) error {
	if session == "" || session == HubSessionName {
		return fmt.Errorf("refusing to close a window in session %q", session)
	}
	if !strings.HasPrefix(windowID, "@") {
		return fmt.Errorf("invalid window id %q", windowID)
	}
	target := sessionWindowTarget(session, windowID)
	paneOut, _ := exec.Command("tmux", "list-panes", "-t", target, "-F", "#{pane_pid}").Output()
	if out, err := exec.Command("tmux", "kill-window", "-t", target).CombinedOutput(); err != nil {
		return fmt.Errorf("kill-window: %s: %w", strings.TrimSpace(string(out)), err)
	}
	sigTermPaneGroups(string(paneOut))
	return nil
}

// IsHubWindow reports whether windowID belongs to the hub session (its
// herd-list window). Chrome bars and HUD panes must never be attached to it.
func IsHubWindow(windowID string) bool {
	return SessionOfWindow(windowID) == HubSessionName
}
