// hubview.go holds the tmux primitives for the hub's live agent pane.
//
// The right-hand pane of the hub's list window runs a nested tmux client
// attached to a GROUPED "viewer" session that shares windows with the agent's
// home session. Agent panes are never joined or moved (join-pane is
// forbidden: a joined pane dies with the hub): the viewer only *shares* the
// window, so killing the viewer pane, the viewer session, or the whole hub
// leaves every agent alone.
package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/selfpath"
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
func viewerNewSessionArgs(homeSession, name string) []string {
	return []string{"new-session", "-d", "-t", ExactSession(homeSession), "-s", name}
}

// nextViewerName picks a viewer session name (matching ViewerSessionPrefix)
// not present in existing, so a new viewer can coexist with the old one while
// the nested client is switched across.
func nextViewerName(existing []string) string {
	taken := make(map[string]bool, len(existing))
	for _, e := range existing {
		taken[e] = true
	}
	if !taken[ViewerSessionName] {
		return ViewerSessionName
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s-%d", ViewerSessionName, i); !taken[n] {
			return n
		}
	}
}

// parseViewerSessions filters a list-sessions name listing to viewer sessions.
func parseViewerSessions(out string) []string {
	var res []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && IsViewerSession(l) {
			res = append(res, l)
		}
	}
	return res
}

// parseClientNames parses `list-clients -F "#{client_name}"` output.
func parseClientNames(out string) []string {
	var res []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			res = append(res, l)
		}
	}
	return res
}

// viewerSwitchClientArgs builds the args moving a nested client to a session.
func viewerSwitchClientArgs(client, session string) []string {
	return []string{"switch-client", "-c", client, "-t", ExactSessionTarget(session)}
}

// viewerDestroyUnattachedArgs enables destroy-unattached on a session. Only
// safe once a client is attached (a detached session is destroyed at once).
func viewerDestroyUnattachedArgs(session string) []string {
	return []string{"set-option", "-t", ExactSessionTarget(session), "destroy-unattached", "on"}
}

// viewerOptionArgs builds the per-session option commands that make the
// viewer transparent: no prefix (the outer client's prefix still drives the
// hub, every other key reaches the agent) and no status line. Note
// destroy-unattached is NOT here: setting it on a detached session destroys
// it immediately (verified), so it is set once a client is attached.
func viewerOptionArgs(session string) [][]string {
	opts := [][2]string{{"prefix", "None"}, {"prefix2", "None"}, {"status", "off"}}
	out := make([][]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, []string{"set-option", "-t", ExactSessionTarget(session), o[0], o[1]})
	}
	return out
}

// viewerSelectWindowArgs selects an agent window inside a viewer session.
func viewerSelectWindowArgs(session, windowID string) []string {
	return []string{"select-window", "-t", sessionWindowTarget(session, windowID)}
}

// viewerAttachCommand is the shell command run in the live pane: a nested
// client on the same server (TMUX unset so tmux allows nesting, socket passed
// explicitly), which flips destroy-unattached on once attached.
func viewerAttachCommand(socketPath, session string) string {
	sock := ""
	if socketPath != "" {
		sock = " -S " + shellQuote(socketPath)
	}
	return fmt.Sprintf("env -u TMUX tmux%s attach -t %s \\; set-option -t %s destroy-unattached on",
		sock, ExactSession(session), ExactSessionTarget(session))
}

// viewerSplitArgs builds the split-window args creating the live pane to the
// right of the hub list pane, printing the new pane ID.
func viewerSplitArgs(listTarget string, width int, attachCmd string) []string {
	return []string{"split-window", "-h", "-d", "-P", "-F", "#{pane_id}",
		"-t", listTarget, "-l", fmt.Sprint(width), attachCmd}
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
	out, err := tmuxRun("list-panes", "-t", sessionWindowTarget(HubSessionName, FleetListWindowName),
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

// viewerSessions lists the current viewer sessions (normally zero or one;
// two only transiently while switching).
func viewerSessions() []string {
	out, err := tmuxRun("list-sessions", "-F", "#{session_name}")
	if err != nil {
		return nil
	}
	return parseViewerSessions(out)
}

// viewerClients returns the clients attached to a viewer session.
func viewerClients(session string) []string {
	out, err := tmuxRun("list-clients", "-t", ExactSession(session), "-F", "#{client_name}")
	if err != nil {
		return nil
	}
	return parseClientNames(out)
}

// currentViewer returns the viewer session with an attached client (falling
// back to the first one) and its first client, if any.
func currentViewer(sessions []string) (session, client string) {
	for _, s := range sessions {
		if c := viewerClients(s); len(c) > 0 {
			return s, c[0]
		}
	}
	if len(sessions) > 0 {
		return sessions[0], ""
	}
	return "", ""
}

func viewerSessionHome(session string) string {
	out, err := tmuxRun("display-message", "-p", "-t", ExactSessionTarget(session), "#{session_group}")
	if err != nil {
		return ""
	}
	return out
}

func killViewerSessions(except string) {
	for _, s := range viewerSessions() {
		if s != except {
			_ = exec.Command("tmux", "kill-session", "-t", ExactSession(s)).Run()
		}
	}
}

// ShowLiveViewer shows windowID (living in homeSession) in the hub's live
// pane, creating the pane/viewer session as needed. rightWidth is the width of
// the live pane when it has to be created. When the target is in a different
// home session the existing nested client is switched to a fresh viewer
// session, so the pane never dies. Only viewer panes/sessions are ever
// created or killed; agent panes are never touched.
func ShowLiveViewer(homeSession, windowID string, rightWidth int) error {
	if homeSession == HubSessionName || IsAnyViewerSession(homeSession) || windowID == "" {
		return fmt.Errorf("cannot view %q/%q live", homeSession, windowID)
	}
	pane := viewerPaneID()
	sessions := viewerSessions()
	old, client := currentViewer(sessions)
	if pane != "" && client != "" && viewerSessionHome(old) == homeSession {
		_, err := tmuxRun(viewerSelectWindowArgs(old, windowID)...)
		return err
	}

	name := nextViewerName(sessions)
	if err := createViewerSession(viewerDeps{run: tmuxRun, hasSession: HasSessionNamed, kill: killSession},
		homeSession, name, windowID); err != nil {
		return err
	}

	if pane != "" && client != "" {
		if _, err := tmuxRun(viewerSwitchClientArgs(client, name)...); err == nil {
			_, err := tmuxRun(viewerDestroyUnattachedArgs(name)...)
			killViewerSessions(name)
			return err
		}
	}

	// Fallback: no pane or no attached client. Drop the stale pane (never
	// respawn a pane ID captured before a kill) and recreate it.
	if pane != "" {
		_ = exec.Command("tmux", "kill-pane", "-t", pane).Run()
	}
	killViewerSessions(name)
	list := sessionWindowTarget(HubSessionName, FleetListWindowName)
	id, err := tmuxRun(viewerSplitArgs(list, rightWidth, viewerAttachCommand(outerSocketPath(), name))...)
	if err != nil {
		return err
	}
	_, err = tmuxRun("set-option", "-p", "-t", id, viewerPaneOption, "1")
	return err
}

// viewerDeps are the tmux operations createViewerSession needs, as seams.
type viewerDeps struct {
	run        func(args ...string) (string, error)
	hasSession func(name string) bool
	kill       func(name string)
}

func killSession(name string) {
	_ = exec.Command("tmux", "kill-session", "-t", ExactSession(name)).Run()
}

// createViewerSession creates the grouped viewer session for homeSession and
// selects windowID in it. The home session is checked first: if it vanished,
// `new-session -t =home` would create a session in a group literally named
// "=home". Any failure after the viewer exists kills it so it cannot leak.
func createViewerSession(d viewerDeps, homeSession, name, windowID string) error {
	if !d.hasSession(homeSession) {
		return fmt.Errorf("home session %q not found", homeSession)
	}
	if _, err := d.run(viewerNewSessionArgs(homeSession, name)...); err != nil {
		return err
	}
	steps := append(viewerOptionArgs(name), viewerSelectWindowArgs(name, windowID))
	for _, a := range steps {
		if _, err := d.run(a...); err != nil {
			d.kill(name)
			return err
		}
	}
	return nil
}

func viewerSessionExists() bool {
	return len(viewerSessions()) > 0
}

// HideLiveViewer kills the viewer pane and all viewer sessions. Agent windows
// are shared, not owned, by the viewer, so they survive.
func HideLiveViewer() error {
	if pane := viewerPaneID(); pane != "" {
		_ = exec.Command("tmux", "kill-pane", "-t", pane).Run()
	}
	killViewerSessions("")
	ClearHubZoom()
	return nil
}

// FocusLivePane toggles focus between the hub list pane and the live pane
// (same as the outer-prefix `o`, but without needing the prefix).
func FocusLivePane() error {
	_, err := tmuxRun("select-pane", "-t", sessionWindowTarget(HubSessionName, FleetListWindowName)+".+")
	return err
}

// hubListPaneID returns the hub list pane's ID: the pane in the fleet-list
// window that is not the live viewer pane. Empty when it cannot be resolved.
func hubListPaneID() string {
	out, err := tmuxRun("list-panes", "-t", sessionWindowTarget(HubSessionName, FleetListWindowName),
		"-F", "#{pane_id} #{"+viewerPaneOption+"}")
	if err != nil {
		return ""
	}
	return parseListPane(out)
}

// parseListPane picks the first pane not flagged as the viewer from
// `#{pane_id} #{@agentctl_viewer}` lines.
func parseListPane(out string) string {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 1 {
			return f[0]
		}
	}
	return ""
}

// SendKeyToHubList sends a key to the hub's list pane (not the live pane), so
// the hub TUI handles it as if typed. Used by prefix+b inside the hub.
func SendKeyToHubList(key string) error {
	pane := hubListPaneID()
	if pane == "" {
		return fmt.Errorf("hub list pane not found")
	}
	_, err := tmuxRun("send-keys", "-t", pane, key)
	return err
}

// HubWindowWidth returns the full width of the hub list window. Once the live
// pane exists the hub TUI only sees its own (narrow) pane, so the real
// window width has to come from tmux.
func HubWindowWidth() (int, error) {
	out, err := tmuxRun("display-message", "-p", "-t",
		sessionWindowTarget(HubSessionName, FleetListWindowName), "#{window_width}")
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

// KeyWindowEnv is set by every agentctl prefix binding to #{window_id} of the
// window the key was pressed in. hubtarget.Apply uses it to tell whether the
// key was pressed in the hub (and so must be redirected to the viewed agent).
// Only the window ID is interpolated: names are attacker-influenced and tmux
// format-expands before sh runs.
const KeyWindowEnv = "AGENTCTL_KEY_WINDOW"

// KeyClientEnv is set next to KeyWindowEnv to #{client_name} of the client
// that pressed the key. Bindings run in a popup/run-shell child with no
// controlling client, so client-targeted commands need it for an explicit -c.
const KeyClientEnv = "AGENTCTL_KEY_CLIENT"

// SessionOfWindow returns the session owning windowID ("@N"), or "".
func SessionOfWindow(windowID string) string {
	if !strings.HasPrefix(windowID, "@") {
		return ""
	}
	out, err := tmuxRun("display-message", "-p", "-t", windowID, "#{session_name}")
	if err != nil {
		return ""
	}
	return homeOfSession(out)
}

// homeOfSession maps a viewer session (hub or phone) to the home session of
// its group and returns any other name unchanged. tmux resolves a pane or
// window target to the most recently active session holding it, which is a
// viewer while someone watches through the hub or the phone; callers want
// the agent's own session. A seam for tests.
var homeOfSession = func(name string) string {
	if !IsAnyViewerSession(name) {
		return name
	}
	if home := homeOfViewer(name); home != "" {
		return home
	}
	return name
}

// ViewedAgent describes the agent currently shown in the hub's live viewer.
type ViewedAgent struct {
	Viewer     string // viewer session name
	Window     string // active window id in the viewer
	Home       string // home (non-viewer) session of the viewer's group
	RuntimeDir string // AGENTCTL_RUNTIME_DIR of the home session
}

// pickHomeSession chooses the home session from a group's member names: the
// first one that is not a viewer session.
func pickHomeSession(members []string) string {
	for _, m := range members {
		if m != "" && !IsAnyViewerSession(m) && m != HubSessionName {
			return m
		}
	}
	return ""
}

// parseEnvValue parses `show-environment` output ("NAME=value"); "" for the
// unset ("-NAME") or empty cases.
func parseEnvValue(name, out string) string {
	out = strings.TrimSpace(out)
	if v, ok := strings.CutPrefix(out, name+"="); ok {
		return v
	}
	return ""
}

// homeOfViewer returns the home session of a viewer session's group.
func homeOfViewer(viewer string) string {
	group := viewerSessionHome(viewer)
	if group == "" {
		return ""
	}
	out, _ := tmuxRun("list-sessions", "-F", "#{session_name}",
		"-f", "#{==:#{session_group},"+FormatEscape(group)+"}")
	return pickHomeSession(strings.Split(out, "\n"))
}

// HubViewedAgent resolves the agent shown in the live viewer: the current
// viewer session, its active window, the home session of its group and that
// session's runtime dir. Fields are empty when no agent is viewed.
func HubViewedAgent() ViewedAgent {
	viewer, _ := currentViewer(viewerSessions())
	if viewer == "" {
		return ViewedAgent{}
	}
	v := ViewedAgent{Viewer: viewer}
	v.Window, _ = tmuxRun("display-message", "-p", "-t", ExactSessionTarget(viewer), "#{window_id}")
	v.Home = homeOfViewer(viewer)
	if v.Home == "" {
		return ViewedAgent{Viewer: viewer, Window: v.Window}
	}
	env, _ := tmuxRun("show-environment", "-t", ExactSession(v.Home), "AGENTCTL_RUNTIME_DIR")
	v.RuntimeDir = parseEnvValue("AGENTCTL_RUNTIME_DIR", env)
	return v
}

// ViewerActiveWindow returns the home session and active window id the live
// viewer currently shows ("" when there is no viewer). The hub polls it to keep
// its list selection in step when the viewer window changes by other means
// (e.g. a tab click in the agent's chrome bar).
func ViewerActiveWindow() (home, windowID string) {
	viewer, _ := currentViewer(viewerSessions())
	if viewer == "" {
		return "", ""
	}
	windowID, _ = tmuxRun("display-message", "-p", "-t", ExactSessionTarget(viewer), "#{window_id}")
	return homeOfViewer(viewer), windowID
}

// SelectViewerWindow switches the live viewer session containing windowID to
// it. Used by chrome-bar tab clicks made from inside a viewer.
func SelectViewerWindow(viewer, windowID string) error {
	_, err := tmuxRun(viewerSelectWindowArgs(viewer, windowID)...)
	// Give the window the viewer just left its chrome bar back at once; the
	// hub zooms the new one on its next poll.
	SweepHubZoom()
	return err
}

// ViewerForWindow returns the hub viewer session showing windowID right now
// (it is the viewer's active window and the viewer has an attached client),
// or "" when the window is not being viewed through the hub. A window also
// watched directly (a non-viewer client attached to its home session) counts
// as not viewed: there the click most likely came from that client, and tab
// switches keep their normal home-session meaning.
func ViewerForWindow(windowID string) string {
	if windowID == "" {
		return ""
	}
	viewer, w := hubViewed()
	if w != windowID {
		return ""
	}
	return viewer
}

// hubViewed returns the viewer session with an attached client and its active
// window, or empty strings when there is none or when the home session of the
// viewer's group has a direct (non-viewer) client of its own.
func hubViewed() (viewer, windowID string) {
	viewer, client := currentViewer(viewerSessions())
	if viewer == "" || client == "" {
		return "", ""
	}
	w, _ := tmuxRun("display-message", "-p", "-t", ExactSessionTarget(viewer), "#{window_id}")
	if w == "" {
		return "", ""
	}
	if home := homeOfViewer(viewer); home != "" && len(viewerClients(home)) > 0 {
		return "", ""
	}
	return viewer, w
}

// HubCloseCommands builds the tmux commands that tear the hub down: detach the
// client that asked (when known) so its terminal returns to the shell, then
// kill each viewer session and finally the hub. Project sessions are never
// named. Viewers are matched exactly (=name) so no prefix match can leak.
func HubCloseCommands(client string, viewers []string) [][]string {
	var cmds [][]string
	if client != "" {
		cmds = append(cmds, []string{"detach-client", "-t", client})
	}
	for _, v := range viewers {
		if IsViewerSession(v) {
			cmds = append(cmds, []string{"kill-session", "-t", "=" + v})
		}
	}
	return append(cmds, []string{"kill-session", "-t", "=" + HubSessionName})
}

// hubCloseShell joins commands into one shell line of tmux invocations.
func hubCloseShell(cmds [][]string) string {
	parts := make([]string, 0, len(cmds))
	for _, c := range cmds {
		q := make([]string, 0, len(c)+1)
		q = append(q, "tmux")
		for _, a := range c {
			q = append(q, shellQuote(a))
		}
		parts = append(parts, strings.Join(q, " "))
	}
	return strings.Join(parts, "; ")
}

// CloseHub detaches client and destroys the viewer sessions and the hub. The
// popup that calls it lives inside the hub, so the teardown runs via
// `run-shell -b` (owned by the tmux server) and outlives the popup. Agents in
// project sessions are untouched.
func CloseHub(client string) error {
	ClearHubZoom()
	cmds := HubCloseCommands(client, viewerSessions())
	shell := hubCloseShell(cmds)
	// The hub process may re-zoom on a last poll tick before it dies, so
	// sweep once more after the viewers are gone. Pinned to a runtime dir so
	// enforce-chrome never initialises one in whatever cwd run-shell has.
	if rd := paths.RuntimeDir(); rd != "" {
		shell += "; " + hookAgentctlCmd(selfpath.SpawnableOrFallback(), rd, "enforce-chrome")
	}
	_, err := tmuxRun("run-shell", "-b", shell)
	return err
}
