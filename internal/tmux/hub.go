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

	"github.com/blsmth/agentctl/internal/config"
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
		if line != "" {
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
		if len(parts) != 2 || parts[0] == "" {
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
// lines. This is the herd hub's read-only mirror of an agent's terminal for
// the main panel preview.
//
// Deliberately NOT `tmux join-pane`: an earlier spike joined the live pane
// into the hub and found it destroys the agent — a pane joined into the hub
// session is killed outright when the hub dies (`just relaunch`, `agentctl
// quit`), rather than returning home, and it also wrecks the source window's
// chrome geometry on return. capture-pane is read-only and non-destructive,
// so it's safe to call on every poll tick. See docs/herd-view-design.md.
//
// Returns an error if the pane is gone (e.g. the window closed between
// polls) — callers must render a placeholder, never propagate a crash.
func CapturePane(session, windowID string, lines int) (string, error) {
	target := sessionWindowTarget(session, windowID)
	args := []string{"capture-pane", "-p", "-t", target}
	if lines > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(lines))
	}
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return "", fmt.Errorf("capture-pane %s: %w", target, err)
	}
	return trimTrailingBlankLines(string(out)), nil
}

// trimTrailingBlankLines removes trailing blank (whitespace-only) lines from
// captured pane output. tmux's capture-pane pads its result to the pane's
// full height with blank lines, which would otherwise push the actual
// content off the top of a shorter render area.
func trimTrailingBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return strings.Join(lines[:end], "\n")
}

// SwitchToLastSession switches the attached client back to whichever session
// it was previously attached to (`tmux switch-client -l`). Used to return from
// the hub to wherever the user came from.
func SwitchToLastSession() error {
	cmd := exec.Command("tmux", "switch-client", "-l")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("switch-client -l: %s: %w", string(out), err)
	}
	return nil
}

// HubSessionExists reports whether the hub session is currently running.
func HubSessionExists() bool {
	return exec.Command("tmux", "has-session", "-t", HubSessionName).Run() == nil
}

// ── Link-window: open an agent inside the hub ───────────────────────────────
//
// tmux link-window is the safe primitive for "open this agent without
// leaving the hub": a linked window is *shared* between the two sessions,
// not moved, so killing the hub session (`agentctl quit`, `just relaunch`)
// leaves the agent's process alive and its home window intact — tmux only
// destroys a window when its last link goes away. This was verified on a
// throwaway server (`tmux -L linktest`): linking a window into a second
// session, then killing that session, left the source window and its pane's
// process running untouched. See docs/herd-view-design.md and the rejected
// `join-pane` alternative documented in internal/tmux/hub.go's CapturePane.

// hubWindowTarget builds the session-qualified target for a window inside
// the hub session ("agentctl-hub:@id"). Factored out so link/unlink/select
// all build the exact same target string, and so that construction is
// testable without shelling out to tmux.
func hubWindowTarget(windowID string) string {
	return sessionWindowTarget(HubSessionName, windowID)
}

// linkWindowArgs builds the `tmux link-window` argument list that links a
// window from its home session into the hub session: `-s <home>:<id>` (the
// source, kept alive) `-t <hub>:` (a *session* target with no window index,
// so tmux keeps the window's own ID rather than renumbering it into a slot).
func linkWindowArgs(homeSession, windowID string) []string {
	return []string{"link-window", "-s", sessionWindowTarget(homeSession, windowID), "-t", HubSessionName + ":"}
}

// unlinkWindowArgs builds the `tmux unlink-window` argument list for
// detaching a window from the hub session. Deliberately never includes `-k`:
// unlink-window -k destroys the window outright once it has no other link,
// which would kill a live agent whose only link was the hub's. Plain
// unlink-window instead leaves the window exactly as-is in every session it
// remains linked to (its actual home).
func unlinkWindowArgs(windowID string) []string {
	return []string{"unlink-window", "-t", hubWindowTarget(windowID)}
}

// selectHubWindowArgs builds the `tmux select-window` argument list for
// selecting a window already present in the hub session (used both after a
// successful link and as the idempotent no-op path when the window is
// already linked in).
func selectHubWindowArgs(windowID string) []string {
	return []string{"select-window", "-t", hubWindowTarget(windowID)}
}

// parseWindowIDList parses the output of
// `tmux list-windows -F '#{window_id}'` into a set of window IDs, skipping
// blank lines. Mirrors parseSessionList's shape for the same reason: pure,
// no I/O, directly table-driven testable.
func parseWindowIDList(raw string) map[string]bool {
	ids := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			ids[line] = true
		}
	}
	return ids
}

// ListHubWindowIDs returns the set of tmux window IDs currently present in
// the hub session — the herd-list window itself plus any agent windows
// linked in via LinkWindowIntoHub. This is the tmux-is-source-of-truth check
// used both for LinkWindowIntoHub's idempotency and for the sidebar's
// link-state glyph: a window can be unlinked or vanish at any time, so the
// hub never trusts stored state about what's linked, only this live query.
// Returns an empty (non-nil) set if the hub session is gone or the query
// fails — callers treat "nothing linked" the same as "couldn't tell",
// which is the safe default (never claim something is linked when it isn't
// known to be).
func ListHubWindowIDs() map[string]bool {
	out, err := exec.Command("tmux", "list-windows", "-t", HubSessionName, "-F", "#{window_id}").Output()
	if err != nil {
		return map[string]bool{}
	}
	return parseWindowIDList(string(out))
}

// LinkWindowIntoHub links an agent's window from its home session into the
// hub session and selects it, putting the caller inside the real,
// interactive agent terminal without leaving the hub session.
//
// Idempotent: a linked window keeps the *same* window ID in both sessions,
// so before linking this checks ListHubWindowIDs (tmux itself, not any
// cached state) and just selects the window if it's already there. This
// check is required, not just defensive: verified on a throwaway server
// that tmux's own link-window is NOT idempotent on repeated calls — linking
// the same window a second time silently produces a duplicate entry in the
// destination session's window list rather than erroring or no-op'ing.
//
// Refuses to link a window whose home session is the hub itself (there is
// nothing to "bring in" — the window already belongs to the hub) or an
// empty window ID.
func LinkWindowIntoHub(homeSession, windowID string) error {
	if homeSession == HubSessionName {
		return fmt.Errorf("refusing to link a window from the hub session itself")
	}
	if windowID == "" {
		return fmt.Errorf("empty window id")
	}

	if ListHubWindowIDs()[windowID] {
		return SelectHerdWindow(windowID)
	}

	args := linkWindowArgs(homeSession, windowID)
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("link-window %s -> %s: %s: %w", args[2], args[4], string(out), err)
	}
	return SelectHerdWindow(windowID)
}

// UnlinkWindowFromHub unlinks an agent's window from the hub session,
// leaving it exactly as-is in its home session. Deliberately never passes
// `-k` (see unlinkWindowArgs) — that would destroy the window once the hub's
// link was its last one, killing a live agent.
//
// tmux itself refuses to unlink a window that is only linked to one session
// (message: "window only linked to one session") — verified on a throwaway
// server. Callers must treat that refusal as an expected, non-fatal outcome
// (there was nothing to unlink) rather than a real failure; this function
// still returns the error so the caller can decide how to present it, but
// does no special-casing of the message itself.
func UnlinkWindowFromHub(windowID string) error {
	args := unlinkWindowArgs(windowID)
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("unlink-window %s: %s: %w", args[2], string(out), err)
	}
	return nil
}

// SelectHerdWindow selects a window (by tmux window ID) inside the hub
// session — used both by LinkWindowIntoHub (after linking, or as the
// idempotent no-op path) and directly by anything that already knows a
// window is linked in.
func SelectHerdWindow(windowID string) error {
	args := selectHubWindowArgs(windowID)
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("select-window %s: %s: %w", args[2], string(out), err)
	}
	return nil
}

// SelectHerdListWindow selects the hub's own herd-list window (the one
// running the HerdModel aggregator) by its stable name. This is the "back to
// the list" primitive: `agentctl hub` calls it when the caller is already
// inside the hub session (see SwitchToHub), so the summon key works as a
// consistent "take me to the list" gesture from anywhere — including from
// inside a window linked in via LinkWindowIntoHub.
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
	return name
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

	exe, _ := os.Executable()
	if exe == "" {
		exe = "agentctl"
	}

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
// from inside a window linked in via LinkWindowIntoHub — see
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
