// hubview.go holds the tmux primitives for the hub's LIVE agent pane (spike).
//
// The right-hand pane of the hub's list window runs a nested tmux client
// attached to a GROUPED "viewer" session that shares windows with the agent's
// home session. Agent panes are never joined or moved (see CapturePane for why
// join-pane is forbidden): the viewer only *shares* the window, so killing the
// viewer pane, the viewer session, or the whole hub leaves every agent alone.
package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ViewerSessionName is the grouped session the hub's live pane attaches to.
// Its name starts with ViewerSessionPrefix, which discovery filters out.
const (
	ViewerSessionName   = "agentctl-hubview"
	ViewerSessionPrefix = "agentctl-hubview"
)

// viewerPaneOption is a pane user option marking the hub's live-viewer pane,
// so it can be found statelessly (never by index, never an agent pane).
const viewerPaneOption = "@agentctl_viewer"

// IsViewerSession reports whether name is a hub viewer session.
func IsViewerSession(name string) bool {
	return strings.HasPrefix(name, ViewerSessionPrefix)
}

// viewerNewSessionArgs builds the grouped-session creation args.
func viewerNewSessionArgs(homeSession string) []string {
	return []string{"new-session", "-d", "-t", homeSession, "-s", ViewerSessionName}
}

// viewerOptionArgs builds the per-session option commands that make the
// viewer transparent: no prefix (the outer client's prefix still drives the
// hub, every other key reaches the agent) and no status line. Note
// destroy-unattached is NOT here: setting it on a detached session destroys
// it immediately (verified), so the attach command sets it once attached.
func viewerOptionArgs() [][]string {
	opts := [][2]string{{"prefix", "None"}, {"prefix2", "None"}, {"status", "off"}}
	out := make([][]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, []string{"set-option", "-t", ViewerSessionName, o[0], o[1]})
	}
	return out
}

// viewerSelectWindowArgs selects an agent window inside the viewer session.
func viewerSelectWindowArgs(windowID string) []string {
	return []string{"select-window", "-t", sessionWindowTarget(ViewerSessionName, windowID)}
}

// viewerAttachCommand is the shell command run in the live pane: a nested
// client on the same server (TMUX unset so tmux allows nesting, socket passed
// explicitly), which flips destroy-unattached on once attached.
func viewerAttachCommand(socketPath string) string {
	sock := ""
	if socketPath != "" {
		sock = " -S " + shellQuote(socketPath)
	}
	return fmt.Sprintf("env -u TMUX tmux%s attach -t %s \\; set-option -t %s destroy-unattached on",
		sock, ViewerSessionName, ViewerSessionName)
}

// viewerSplitArgs builds the split-window args creating the live pane to the
// right of the hub list pane, printing the new pane ID.
func viewerSplitArgs(listTarget string, width int, attachCmd string) []string {
	return []string{"split-window", "-h", "-d", "-P", "-F", "#{pane_id}",
		"-t", listTarget, "-l", fmt.Sprint(width), attachCmd}
}

// viewerRespawnArgs builds the respawn args for the VIEWER pane only.
func viewerRespawnArgs(viewerPane, attachCmd string) []string {
	return []string{"respawn-pane", "-k", "-t", viewerPane, attachCmd}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// outerSocketPath extracts the socket path from $TMUX ("path,pid,idx").
func outerSocketPath() string {
	t := os.Getenv("TMUX")
	if i := strings.Index(t, ","); i >= 0 {
		return t[:i]
	}
	return ""
}

func tmuxRun(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %s: %w", args[0], strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// viewerPaneID returns the live pane's ID in the hub list window, or "".
func viewerPaneID() string {
	out, err := tmuxRun("list-panes", "-t", sessionWindowTarget(HubSessionName, HerdListWindowName),
		"-F", "#{pane_id} #{"+viewerPaneOption+"}")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == "1" {
			return f[0]
		}
	}
	return ""
}

func viewerSessionHome() string {
	out, err := tmuxRun("display-message", "-p", "-t", ViewerSessionName, "#{session_group}")
	if err != nil {
		return ""
	}
	return out
}

// ShowLiveViewer shows windowID (living in homeSession) in the hub's live
// pane, creating the pane/viewer session as needed. rightWidth is the width of
// the live pane when it has to be created. Only the viewer pane/session are
// ever created, respawned or killed; agent panes are never touched.
func ShowLiveViewer(homeSession, windowID string, rightWidth int) error {
	if homeSession == HubSessionName || IsViewerSession(homeSession) || windowID == "" {
		return fmt.Errorf("cannot view %q/%q live", homeSession, windowID)
	}
	pane := viewerPaneID()
	sameGroup := pane != "" && viewerSessionExists() && viewerSessionHome() == homeSession
	if sameGroup {
		_, err := tmuxRun(viewerSelectWindowArgs(windowID)...)
		return err
	}
	// New or different home session: rebuild the viewer session. Killing the
	// viewer session only drops a group member; windows survive in home.
	_ = exec.Command("tmux", "kill-session", "-t", ViewerSessionName).Run()
	if _, err := tmuxRun(viewerNewSessionArgs(homeSession)...); err != nil {
		return err
	}
	for _, a := range viewerOptionArgs() {
		if _, err := tmuxRun(a...); err != nil {
			return err
		}
	}
	if _, err := tmuxRun(viewerSelectWindowArgs(windowID)...); err != nil {
		return err
	}
	cmd := viewerAttachCommand(outerSocketPath())
	if pane != "" {
		_, err := tmuxRun(viewerRespawnArgs(pane, cmd)...)
		return err
	}
	list := sessionWindowTarget(HubSessionName, HerdListWindowName)
	id, err := tmuxRun(viewerSplitArgs(list, rightWidth, cmd)...)
	if err != nil {
		return err
	}
	_, err = tmuxRun("set-option", "-p", "-t", id, viewerPaneOption, "1")
	return err
}

func viewerSessionExists() bool {
	return exec.Command("tmux", "has-session", "-t", "="+ViewerSessionName).Run() == nil
}

// HideLiveViewer kills the viewer pane and viewer session. Agent windows are
// shared, not owned, by the viewer, so they survive.
func HideLiveViewer() error {
	if pane := viewerPaneID(); pane != "" {
		_ = exec.Command("tmux", "kill-pane", "-t", pane).Run()
	}
	if viewerSessionExists() {
		_, err := tmuxRun("kill-session", "-t", "="+ViewerSessionName)
		return err
	}
	return nil
}

// FocusLivePane toggles focus between the hub list pane and the live pane
// (same as the outer-prefix `o`, but without needing the prefix).
func FocusLivePane() error {
	_, err := tmuxRun("select-pane", "-t", sessionWindowTarget(HubSessionName, HerdListWindowName)+".+")
	return err
}

// HubWindowWidth returns the full width of the hub list window. Once the live
// pane exists the hub TUI only sees its own (narrow) pane, so the real
// window width has to come from tmux.
func HubWindowWidth() (int, error) {
	out, err := tmuxRun("display-message", "-p", "-t",
		sessionWindowTarget(HubSessionName, HerdListWindowName), "#{window_width}")
	if err != nil {
		return 0, err
	}
	var w int
	if _, err := fmt.Sscanf(out, "%d", &w); err != nil {
		return 0, fmt.Errorf("parsing window width %q: %w", out, err)
	}
	return w, nil
}

// viewerResizeArgs builds the resize args pinning the live pane to width
// columns (the list pane takes whatever remains).
func viewerResizeArgs(viewerPane string, width int) []string {
	return []string{"resize-pane", "-t", viewerPane, "-x", fmt.Sprint(width)}
}

// ResizeLiveViewer pins the live pane to rightWidth columns. A no-op when
// there is no live pane.
func ResizeLiveViewer(rightWidth int) error {
	pane := viewerPaneID()
	if pane == "" {
		return nil
	}
	_, err := tmuxRun(viewerResizeArgs(pane, rightWidth)...)
	return err
}
