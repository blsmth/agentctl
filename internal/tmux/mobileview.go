// mobileview.go holds the tmux primitives for the phone web app's live
// terminal (agentctl serve). It is the serve counterpart of hubview.go: a
// GROUPED viewer session shares the agent's windows, and a nested client
// attached to it inside a PTY streams the window to the phone. Agent panes
// are never joined or moved, so killing the viewer leaves the agent alone.
//
// The nested client attaches with -f ignore-size, so the phone-sized client
// never resizes the shared window under the laptop's client.
package tmux

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// MobileViewerSessionPrefix starts the name of every phone viewer session.
// Discovery filters it out (IsAnyViewerSession), so viewers never show up in
// the fleet, hub, status, sweep, restore or reconcile.
const MobileViewerSessionPrefix = "agentctl-mobileview-"

// mobileKeyTable is the key table of a phone viewer session. It has no
// bindings, so every key the phone sends reaches the pane: with the prefix
// also off, the phone cannot reach tmux commands (command prompt, run-shell,
// new-window) through the viewer.
const mobileKeyTable = "agentctl-mobile"

// IsMobileViewerSession reports whether name is a phone viewer session.
func IsMobileViewerSession(name string) bool {
	return strings.HasPrefix(name, MobileViewerSessionPrefix)
}

// IsAnyViewerSession reports whether name is a hub or phone viewer session:
// a grouped session that only shares another session's windows and must be
// excluded wherever agentctl discovers sessions.
func IsAnyViewerSession(name string) bool {
	return IsViewerSession(name) || IsMobileViewerSession(name)
}

// NewMobileViewerName returns a fresh, random phone viewer session name.
func NewMobileViewerName() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating viewer name: %w", err)
	}
	return MobileViewerSessionPrefix + hex.EncodeToString(b[:]), nil
}

// validMobileTarget rejects home sessions and window IDs that could address
// anything other than one window of one agentctl session.
func validMobileTarget(home, windowID string) error {
	if home == "" || strings.ContainsAny(home, ":.") || home == HubSessionName || IsAnyViewerSession(home) {
		return fmt.Errorf("invalid session %q for a phone viewer", home)
	}
	if len(windowID) < 2 || windowID[0] != '@' {
		return fmt.Errorf("invalid window %q for a phone viewer", windowID)
	}
	if _, err := strconv.Atoi(windowID[1:]); err != nil {
		return fmt.Errorf("invalid window %q for a phone viewer", windowID)
	}
	return nil
}

// MobileViewerSetupArgs builds the tmux commands that create a phone viewer
// session grouped with home and showing windowID: the grouped session, its
// pass-through options (no prefix, an empty key table, no status line) and
// the window selection.
func MobileViewerSetupArgs(home, windowID, name string) ([][]string, error) {
	if err := validMobileTarget(home, windowID); err != nil {
		return nil, err
	}
	if !IsMobileViewerSession(name) {
		return nil, fmt.Errorf("invalid phone viewer name %q", name)
	}
	cmds := [][]string{{"new-session", "-d", "-t", "=" + home, "-s", name}}
	for _, o := range [][2]string{{"prefix", "None"}, {"prefix2", "None"}, {"key-table", mobileKeyTable}, {"status", "off"}} {
		cmds = append(cmds, []string{"set-option", "-t", "=" + name + ":", o[0], o[1]})
	}
	return append(cmds, []string{"select-window", "-t", "=" + name + ":" + windowID}), nil
}

// MobileViewerAttachArgs builds the argv (after "tmux") of the nested client
// serve runs in a PTY: an ignore-size client, read-only (-r) unless the
// phone may type, that turns destroy-unattached on once attached so the
// viewer dies with the client even if serve crashes before cleaning up.
func MobileViewerAttachArgs(name string, readOnly bool) []string {
	args := []string{"-u", "attach-session", "-t", "=" + name, "-f", "ignore-size"}
	if readOnly {
		args = append(args, "-r")
	}
	return append(args, ";", "set-option", "-t", "="+name+":", "destroy-unattached", "on")
}

// CreateMobileViewer creates a phone viewer session showing windowID of home
// and returns its name. On failure the partial session is killed.
func CreateMobileViewer(home, windowID string) (string, error) {
	name, err := NewMobileViewerName()
	if err != nil {
		return "", err
	}
	cmds, err := MobileViewerSetupArgs(home, windowID, name)
	if err != nil {
		return "", err
	}
	for _, c := range cmds {
		if _, err := tmuxRun(c...); err != nil {
			_ = KillMobileViewer(name)
			return "", err
		}
	}
	return name, nil
}

// KillMobileViewer kills one phone viewer session. Names outside the prefix
// are refused, so it can never kill an agent session. A viewer that is
// already gone is not an error.
func KillMobileViewer(name string) error {
	if !IsMobileViewerSession(name) {
		return fmt.Errorf("refusing to kill non-viewer session %q", name)
	}
	if _, err := tmuxRun("kill-session", "-t", "="+name); err != nil {
		if strings.Contains(err.Error(), "can't find session") || strings.Contains(err.Error(), "no server running") {
			return nil
		}
		return err
	}
	return nil
}

// MobileViewerSessions lists the live phone viewer sessions.
func MobileViewerSessions() ([]string, error) {
	out, err := tmuxRun("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return nil, nil
		}
		return nil, err
	}
	return parseMobileViewerSessions(out), nil
}

// parseMobileViewerSessions filters a list-sessions name listing to phone
// viewer sessions.
func parseMobileViewerSessions(out string) []string {
	var res []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); IsMobileViewerSession(l) {
			res = append(res, l)
		}
	}
	return res
}

// WindowSize returns windowID's size in cells, as the laptop's client sees it.
func WindowSize(windowID string) (cols, rows int, err error) {
	if _, err := strconv.Atoi(strings.TrimPrefix(windowID, "@")); err != nil || !strings.HasPrefix(windowID, "@") {
		return 0, 0, fmt.Errorf("invalid window %q", windowID)
	}
	out, err := tmuxRun("display-message", "-p", "-t", windowID, "#{window_width} #{window_height}")
	if err != nil {
		return 0, 0, err
	}
	return parseWindowSize(out)
}

// parseWindowSize parses "#{window_width} #{window_height}" output.
func parseWindowSize(out string) (cols, rows int, err error) {
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("parsing window size %q", out)
	}
	cols, err1 := strconv.Atoi(f[0])
	rows, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || cols <= 0 || rows <= 0 {
		return 0, 0, fmt.Errorf("parsing window size %q", out)
	}
	return cols, rows, nil
}
