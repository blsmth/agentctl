// chromegate.go gates the per-window chrome bar behind the global
// `chrome_bar` config key (off by default) and supplies the native tmux
// status line that replaces it when it is off.
//
// The switch is read when a bar would be spawned (LaunchChromeBar, so the
// after-new-window hook, applyConfig's shell window and the HUD toggle all
// follow it), so flipping it applies to new windows without a restart.
// `agentctl apply-config` (ApplyConfigLive) switches existing windows both
// ways through syncChromeBars.
package tmux

import (
	"fmt"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// chromeBarEnabled reports the global chrome_bar switch. A seam so tests
// decide it without a config file.
var chromeBarEnabled = func() bool { return config.Load().ChromeBarEnabled() }

// chromeBarRun runs LaunchChromeBar's list-panes and split-window; a seam so
// tests see whether a bar would be created without a tmux server.
var chromeBarRun = tmuxRun

// statusLineOptions returns the session options that set up the native tmux
// status line. With the chrome bar on it is switched off (the bar replaces
// it). With the bar off it shows the session name and a plain window list:
// window names already carry the state glyphs (internal/winname), so
// #{window_name} per tab is enough. The list is drawn by a session-scoped
// status-format[0] rather than the window-status-format window options,
// which are only settable server-wide (-gw) and would restyle the user's
// other tmux sessions. Each tab is a mouse range, so clicking selects it.
//
// Only the project session gets this: the hub, mobileview and hub viewer
// sessions set status off on their own sessions.
func statusLineOptions(chromeBar bool, th theme.Theme) [][2]string {
	if chromeBar {
		return [][2]string{{"status", "off"}}
	}
	// No commas inside the #{W:...} arguments: they would split the loop's
	// two formats, so every style attribute gets its own #[...].
	tab := "#[range=window|#{window_index}]#[fg=" + th.Subtle + "] #{window_name} #[norange]"
	current := "#[range=window|#{window_index}]#[fg=" + th.Bright + "]#[bg=" + th.Surface + "]#[bold] #{window_name} #[nobold]#[bg=" + th.Base + "]#[norange]"
	format := "#[align=left]#[fg=" + th.Accent + "]#[bold] #{session_name} #[nobold]" +
		"#{W:" + tab + "," + current + "}"
	return [][2]string{
		{"status", "on"},
		{"status-position", "bottom"},
		{"status-style", "fg=" + th.Subtle + ",bg=" + th.Base},
		{"status-format[0]", format},
	}
}

// applyStatusLine sets statusLineOptions on this project's session.
func applyStatusLine(chromeBar bool) {
	for _, o := range statusLineOptions(chromeBar, theme.Current()) {
		set(o[0], o[1])
	}
}

// recordMainPane records a window's main pane when it has none yet. The
// chrome bar launch used to be the only writer of main_pane for new windows,
// and the worker preamble and cron rely on it, so a window that gets no bar
// still has it recorded.
func recordMainPane(windowID string, ws windowstate.WindowState) {
	if ws.MainPane != "" {
		return
	}
	mainPane := findMainPane(windowID)
	if mainPane == "" {
		return
	}
	windowstate.Update(windowID, func(wst *windowstate.WindowState) { //nolint:errcheck
		if wst.MainPane == "" {
			wst.MainPane = mainPane
		}
	})
}

// isChromeBarCommand reports whether a pane start command runs the chrome
// bar (LaunchChromeBar runs `<exe> chrome-bar <window>`).
func isChromeBarCommand(cmd string) bool {
	return strings.Contains(cmd, " chrome-bar ")
}

// parsePaneCommands parses `list-panes -F "#{pane_id}\t#{pane_start_command}"`
// output into pane ID -> start command.
func parsePaneCommands(out string) map[string]string {
	panes := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		id, cmd, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if strings.HasPrefix(id, "%") {
			panes[id] = cmd
		}
	}
	return panes
}

// chromeAction is what syncChromeBars does to one window.
type chromeAction struct {
	kill   string // chrome bar pane to kill ("" = none)
	clear  string // recorded chrome_bar_pane value to clear ("" = none)
	launch bool   // launch a chrome bar
}

// planChromeWindow decides how to bring one window in line with the chrome
// bar switch, given its name, its windowstate record and its live panes.
//
// Off: only the recorded chrome_bar_pane is ever killed, and only when it is
// a live pane of this window running the chrome bar, is not the recorded
// main pane and is not the window's last pane. A recorded pane that is gone
// or is something else is cleared from the record without killing anything.
//
// On: a utility window is left alone (it runs full screen); a window that
// already shows a chrome bar keeps it; a stale record (the pane is gone) is
// cleared so LaunchChromeBar does not refuse; everything else gets a bar.
func planChromeWindow(enabled bool, name string, ws windowstate.WindowState, panes map[string]string) chromeAction {
	bar := ws.ChromeBarPane
	cmd, live := panes[bar]
	liveBar := bar != "" && live && isChromeBarCommand(cmd)
	if !enabled {
		if bar == "" {
			return chromeAction{}
		}
		a := chromeAction{clear: bar}
		if liveBar && bar != ws.MainPane && len(panes) > 1 {
			a.kill = bar
		}
		return a
	}
	if utilityWindowNames[name] || liveBar {
		return chromeAction{}
	}
	for _, c := range panes {
		if isChromeBarCommand(c) {
			return chromeAction{} // an unrecorded bar: never add a second one
		}
	}
	a := chromeAction{launch: true}
	if bar != "" {
		a.clear = bar
	}
	return a
}

// chromeSync holds the tmux and windowstate calls syncChromeBars makes, so
// tests run it without a tmux server.
type chromeSync struct {
	listWindows func() ([]WindowInfo, error)
	listPanes   func(windowID string) (string, error)
	read        func(windowID string) windowstate.WindowState
	killPane    func(paneID string) error
	clear       func(windowID, paneID string)
	launch      func(windowID string) error
}

// liveChromeSync is the real chromeSync for this project's session.
func liveChromeSync() chromeSync {
	return chromeSync{
		listWindows: func() ([]WindowInfo, error) { return ListWindowsInErr(SessionName) },
		listPanes: func(windowID string) (string, error) {
			return tmuxRun("list-panes", "-t", sessionWindowTarget(SessionName, windowID),
				"-F", "#{pane_id}\t#{pane_start_command}")
		},
		read: func(windowID string) windowstate.WindowState {
			ws, _ := windowstate.Read(windowID)
			return ws
		},
		killPane: func(paneID string) error {
			_, err := tmuxRun("kill-pane", "-t", paneID)
			return err
		},
		clear: func(windowID, paneID string) {
			windowstate.Update(windowID, func(ws *windowstate.WindowState) { //nolint:errcheck
				if ws.ChromeBarPane == paneID {
					ws.ChromeBarPane = ""
				}
			})
		},
		launch: LaunchChromeBar,
	}
}

// chromeSyncDeps builds the chromeSync ApplyConfigLive uses; a seam for tests.
var chromeSyncDeps = liveChromeSync

// syncChromeBars brings every window of this project's session in line with
// the chrome bar switch: off kills the recorded bars, on launches bars into
// windows missing one. No session or no windows is a no-op. Best effort per
// window: one failure does not stop the rest.
func syncChromeBars(enabled bool, d chromeSync) {
	wins, err := d.listWindows()
	if err != nil {
		logging.Log("tmux: chrome bar sync: %v", err)
		return
	}
	for _, w := range wins {
		if err := syncChromeWindow(enabled, w, d); err != nil {
			logging.Log("tmux: chrome bar sync %s: %v", w.ID, err)
		}
	}
}

// syncChromeWindow applies planChromeWindow to one window.
func syncChromeWindow(enabled bool, w WindowInfo, d chromeSync) error {
	ws := d.read(w.ID)
	if !enabled && ws.ChromeBarPane == "" {
		return nil // nothing recorded: no tmux call
	}
	out, err := d.listPanes(w.ID)
	if err != nil {
		return fmt.Errorf("listing panes: %w", err)
	}
	a := planChromeWindow(enabled, w.Name, ws, parsePaneCommands(out))
	if a.kill != "" {
		if err := d.killPane(a.kill); err != nil {
			return fmt.Errorf("killing chrome bar %s: %w", a.kill, err)
		}
	}
	if a.clear != "" {
		d.clear(w.ID, a.clear)
	}
	if a.launch {
		if err := d.launch(w.ID); err != nil {
			return fmt.Errorf("launching chrome bar: %w", err)
		}
	}
	return nil
}
