// hubactivity.go holds the tmux primitives for the hub's activity pane: a
// pane at the right edge of the hub list window running `agentctl hud
// --follow-hub`, which shows the tool activity of whichever agent the hub
// list has selected. Like the live viewer pane it is found statelessly by a
// pane user option, never by index, and only panes carrying that option are
// ever resized or killed.
package tmux

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/shellquote"
)

// activityPaneOption is the pane user option marking the hub's activity pane.
const activityPaneOption = "@agentctl_activity"

// HubPane is one pane of the hub list window.
type HubPane struct {
	ID       string
	Width    int
	Viewer   bool // the live viewer pane
	Activity bool // the activity pane
}

// hubPanesFormat lists a pane's ID, width and role flags.
const hubPanesFormat = "#{pane_id} #{pane_width} v#{" + viewerPaneOption + "} a#{" + activityPaneOption + "}"

// parseHubPanes parses `list-panes -F hubPanesFormat` output. Malformed
// lines are skipped.
func parseHubPanes(out string) []HubPane {
	var panes []HubPane
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 4 || !strings.HasPrefix(f[0], "%") {
			continue
		}
		w, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		panes = append(panes, HubPane{ID: f[0], Width: w, Viewer: f[2] == "v1", Activity: f[3] == "a1"})
	}
	return panes
}

// HubPanes lists the panes of the hub list window, left to right.
func HubPanes() ([]HubPane, error) {
	out, err := tmuxRun("list-panes", "-t", sessionWindowTarget(HubSessionName, FleetListWindowName), "-F", hubPanesFormat)
	if err != nil {
		return nil, err
	}
	return parseHubPanes(out), nil
}

// activityCommand is the shell command the activity pane runs.
func activityCommand(exe string) string {
	return shellquote.Quote(exe) + " hud --follow-hub"
}

// activitySplitArgs builds the split-window args creating the activity pane:
// a full-height pane (-f) at the right edge of the hub list window, width
// columns wide, not focused (-d), printing its pane ID.
func activitySplitArgs(width int, cmd string) []string {
	return []string{"split-window", "-h", "-f", "-d", "-P", "-F", "#{pane_id}",
		"-t", sessionWindowTarget(HubSessionName, FleetListWindowName), "-l", strconv.Itoa(width), cmd}
}

// OpenActivityPane creates the activity pane width columns wide and returns
// its pane ID. The caller widens it afterwards (the slide-in).
func OpenActivityPane(width int) (string, error) {
	id, err := tmuxRun(activitySplitArgs(width, activityCommand(selfpath.SpawnableOrFallback()))...)
	if err != nil {
		return "", err
	}
	if _, err := tmuxRun("set-option", "-p", "-t", id, activityPaneOption, "1"); err != nil {
		_, _ = tmuxRun("kill-pane", "-t", id) // never leave an unmarked pane behind
		return "", fmt.Errorf("marking activity pane: %w", err)
	}
	return id, nil
}

// paneWidthArgs builds the resize args pinning a pane to width columns.
func paneWidthArgs(pane string, width int) []string {
	return []string{"resize-pane", "-t", pane, "-x", strconv.Itoa(width)}
}

// ResizeHubPane pins a hub pane to width columns. Resizing the rightmost
// (activity) pane or the leftmost (list) pane only moves its one divider, so
// the live viewer between them absorbs the change.
func ResizeHubPane(pane string, width int) error {
	_, err := tmuxRun(paneWidthArgs(pane, width)...)
	return err
}

// HubListPane returns the hub list pane's ID, or "" when it cannot be
// resolved.
func HubListPane() string { return hubListPaneID() }

// activityPaneIDs returns the IDs of every activity pane among panes.
func activityPaneIDs(panes []HubPane) []string {
	var ids []string
	for _, p := range panes {
		if p.Activity {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

// KillHubActivity kills every activity pane in the hub list window. Panes
// without the activity option are never touched.
func KillHubActivity() error {
	panes, err := HubPanes()
	if err != nil {
		return err
	}
	for _, id := range activityPaneIDs(panes) {
		if _, err := tmuxRun("kill-pane", "-t", id); err != nil {
			return err
		}
	}
	return nil
}
