// hubzoom.go hides the per-window chrome bar while the hub's live pane shows
// a window.
//
// The live pane is a nested client on a grouped viewer session, and grouped
// sessions share windows (panes belong to windows), so tmux cannot hide the
// chrome bar pane for the hub's client alone. Instead the hub zooms the
// window's main pane (resize-pane -Z) while it views the window, and marks the
// window with hubZoomOption so that only zooms the hub made are ever undone; a
// zoom the user made by hand is left alone.
//
// Known tradeoffs: zoom is a window property, so a client attached directly to
// the window's project session would see the zoom too. The viewed-window test
// (hubViewed, shared with ViewerForWindow) therefore treats a window whose home
// session has a direct client as not viewed: switching to or attaching to that
// session unzooms it (via the hook-driven sweep, so only briefly visible), and
// while such a client stays attached the hub shows that project's windows with
// their chrome bar. A window zoomed by the hub also stays zoomed while the hub
// is left for an unrelated session; nobody sees it until its own session is
// entered, which sweeps it.
//
// Leaking a zoom (hub killed, hot reloaded, viewer destroyed) would leave a
// project window without its chrome bar, so SweepHubZoom runs from every
// enforce-chrome (client-attached, client-resized, client-session-changed
// hooks) and unzooms every marked window the hub is not currently viewing.
package tmux

import (
	"strings"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/selfpath"
)

// hubZoomOption is the window user option marking a zoom the hub made.
const hubZoomOption = "@agentctl_hub_zoom"

// hubZoomListArgs lists every window on the server with its zoom flag and hub
// marker. Windows linked into several sessions appear once per session.
func hubZoomListArgs() []string {
	return []string{"list-windows", "-a", "-F",
		"#{window_id} #{window_zoomed_flag} #{" + hubZoomOption + "}"}
}

// hubZoomPanesArgs lists a window's panes with their start commands, to find
// the main pane without the window's windowstate (the hub process belongs to
// no project, so it cannot read it).
func hubZoomPanesArgs(windowID string) []string {
	return []string{"list-panes", "-t", windowID, "-F", "#{pane_id}\t#{pane_start_command}"}
}

// hubZoomArgs zooms pane. resize-pane -Z toggles, so callers only use it on a
// window that is not zoomed.
func hubZoomArgs(pane string) []string {
	return []string{"resize-pane", "-Z", "-t", pane}
}

// hubUnzoomArgs unzooms windowID. On a zoomed window resize-pane -Z unzooms
// whichever pane it targets; callers only use it on a zoomed window.
func hubUnzoomArgs(windowID string) []string {
	return []string{"resize-pane", "-Z", "-t", windowID}
}

// hubZoomMarkArgs marks windowID as zoomed by the hub.
func hubZoomMarkArgs(windowID string) []string {
	return []string{"set-option", "-w", "-t", windowID, hubZoomOption, "1"}
}

// hubZoomClearArgs removes the hub marker from windowID.
func hubZoomClearArgs(windowID string) []string {
	return []string{"set-option", "-w", "-u", "-t", windowID, hubZoomOption}
}

// zoomWindow is one window's zoom state as hubZoomListArgs reports it.
type zoomWindow struct {
	id     string
	zoomed bool // window_zoomed_flag
	marked bool // hubZoomOption set: the hub zoomed it
}

// parseZoomWindows parses hubZoomListArgs output, one entry per window ID.
func parseZoomWindows(out string) []zoomWindow {
	seen := map[string]bool{}
	var res []zoomWindow
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasPrefix(f[0], "@") || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		res = append(res, zoomWindow{id: f[0], zoomed: f[1] == "1", marked: len(f) > 2 && f[2] == "1"})
	}
	return res
}

// zoomPlan is what to do to bring the zoom state in line with what the hub
// views.
type zoomPlan struct {
	unzoom []string // marked and zoomed: unzoom, then clear the marker
	clear  []string // marked but already unzoomed (by the user): clear only
	zoom   string   // window to zoom and mark ("" = none)
}

// planHubZoom decides the zoom changes. viewed is the window the hub's live
// pane shows ("" when none); hide reports whether the chrome bar is hidden in
// the hub.
//
//   - A marked window that is not the viewed window (or every marked window,
//     when chrome is shown) loses its marker and, if still zoomed, its zoom.
//   - The viewed window is zoomed and marked when chrome is hidden and it is
//     neither zoomed (a hand-made zoom is never adopted) nor already marked
//     (marked but unzoomed means the user unzoomed it: leave it be until the
//     hub moves on).
func planHubZoom(wins []zoomWindow, viewed string, hide bool) zoomPlan {
	var p zoomPlan
	for _, w := range wins {
		keep := hide && viewed != "" && w.id == viewed
		switch {
		case w.marked && !keep && w.zoomed:
			p.unzoom = append(p.unzoom, w.id)
		case w.marked && !keep:
			p.clear = append(p.clear, w.id)
		case !w.marked && !w.zoomed && hide && w.id == viewed && viewed != "":
			p.zoom = w.id
		}
	}
	return p
}

// pickZoomPane returns the main pane from hubZoomPanesArgs output: the first
// pane that is neither the chrome bar nor the HUD.
func pickZoomPane(out string) string {
	for _, line := range strings.Split(out, "\n") {
		id, cmd, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if id == "" {
			continue
		}
		if strings.Contains(cmd, " chrome-bar ") || strings.Contains(cmd, " hud ") {
			continue
		}
		return id
	}
	return ""
}

// Seams so tests never touch a live tmux server.
var (
	hubZoomRun     = tmuxRun
	hubZoomViewed  = hubViewedWindow
	hubZoomEnforce = requestChromeEnforce
)

// applyHubZoom reads every window's zoom state and applies planHubZoom. With
// sweepOnly it only ever undoes zooms, never makes one.
func applyHubZoom(viewed string, hide, sweepOnly bool) {
	out, err := hubZoomRun(hubZoomListArgs()...)
	if err != nil {
		logging.Debug("tmux: hub zoom: %v", err)
		return
	}
	p := planHubZoom(parseZoomWindows(out), viewed, hide)
	for _, w := range p.unzoom {
		if _, err := hubZoomRun(hubUnzoomArgs(w)...); err != nil {
			logging.Debug("tmux: hub unzoom %s: %v", w, err)
		}
		_, _ = hubZoomRun(hubZoomClearArgs(w)...)
		// Window size may have changed while zoomed (enforce skips zoomed
		// windows): re-pin the chrome bar now that it is visible again.
		hubZoomEnforce(w)
	}
	for _, w := range p.clear {
		_, _ = hubZoomRun(hubZoomClearArgs(w)...)
	}
	if p.zoom == "" || sweepOnly {
		return
	}
	panes, err := hubZoomRun(hubZoomPanesArgs(p.zoom)...)
	if err != nil {
		return
	}
	pane := pickZoomPane(panes)
	if pane == "" {
		return
	}
	// Mark first: a sweep racing with the zoom then sees the marker and can
	// undo it, rather than seeing an unmarked zoom it must never touch.
	if _, err := hubZoomRun(hubZoomMarkArgs(p.zoom)...); err != nil {
		return
	}
	if _, err := hubZoomRun(hubZoomArgs(pane)...); err != nil {
		logging.Debug("tmux: hub zoom %s: %v", pane, err)
		_, _ = hubZoomRun(hubZoomClearArgs(p.zoom)...)
	}
}

// SyncHubZoom brings the zoom state in line with the hub: when hide is true
// the window the live pane shows is zoomed (hiding its chrome bar) and every
// other hub-zoomed window is restored; when false every hub zoom is undone.
// Called by the hub on each selection change, poll tick and toggle.
func SyncHubZoom(hide bool) {
	applyHubZoom(hubZoomViewed(), hide, false)
}

// SweepHubZoom unzooms every hub-zoomed window the hub is not viewing right
// now. It never zooms anything, so it is safe from any process (enforce-chrome
// hooks, hub start, viewer changes) whatever the hub's toggle state.
func SweepHubZoom() {
	applyHubZoom(hubZoomViewed(), true, true)
}

// ClearHubZoom undoes every hub zoom, viewed or not. Used when the hub or its
// viewer goes away.
func ClearHubZoom() {
	applyHubZoom("", false, true)
}

// hubViewedWindow returns the window the hub's live pane shows to a human:
// the active window of the viewer session with an attached client, unless
// the window's home session has a direct client (then that client is looking
// at the project session, and its chrome bar must stay visible).
func hubViewedWindow() string {
	_, w := hubViewed()
	return w
}

// requestChromeEnforce re-runs enforce-chrome for windowID's project in the
// background, pinned to that project's runtime dir (this process may belong
// to another project, or to none, like the hub). Best effort.
func requestChromeEnforce(windowID string) {
	home := SessionOfWindow(windowID)
	if home == "" {
		return
	}
	env, _ := tmuxRun("show-environment", "-t", "="+home, "AGENTCTL_RUNTIME_DIR")
	rd := parseEnvValue("AGENTCTL_RUNTIME_DIR", env)
	if rd == "" {
		return
	}
	exe := selfpath.SpawnableOrFallback()
	_, _ = tmuxRun("run-shell", "-b", hookAgentctlCmd(exe, rd, "enforce-chrome"))
}
