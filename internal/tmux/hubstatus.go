// hubstatus.go holds the tmux primitives for the hub's status pane: a short
// pane stacked above the live viewer pane running `agentctl hub-status`,
// which shows the selected row's detail (title, recap, activity, chips).
// Like the viewer and activity panes it is found statelessly by a pane user
// option, never by index, and only panes carrying that option are ever
// resized or killed. It lives and dies with the viewer: ShowLiveViewer's
// callers ensure it once the viewer is up, and HideLiveViewer kills it.
package tmux

import (
	"fmt"
	"strconv"

	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/shellquote"
)

// statusPaneOption is the pane user option marking the hub's status pane.
const statusPaneOption = "@agentctl_status"

// statusCommand is the shell command the status pane runs.
func statusCommand(exe string) string {
	return shellquote.Quote(exe) + " hub-status"
}

// statusSplitArgs builds the split-window args creating the status pane: a
// vertical split of the viewer pane placed before it (-b, so above), height
// rows tall, not focused (-d), printing its pane ID. Splitting the viewer
// pane (not the window) keeps the status pane in the viewer's column, so
// the list and activity panes keep their full height.
func statusSplitArgs(viewerPane string, height int, cmd string) []string {
	return []string{"split-window", "-v", "-b", "-d", "-P", "-F", "#{pane_id}",
		"-t", viewerPane, "-l", strconv.Itoa(height), cmd}
}

// paneHeightArgs builds the resize args pinning a pane to height rows.
func paneHeightArgs(pane string, height int) []string {
	return []string{"resize-pane", "-t", pane, "-y", strconv.Itoa(height)}
}

// statusPaneIDs returns the IDs of every status pane among panes.
func statusPaneIDs(panes []HubPane) []string {
	var ids []string
	for _, p := range panes {
		if p.Status {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

// viewerPaneIn returns the live viewer pane's ID among panes, or "".
func viewerPaneIn(panes []HubPane) string {
	for _, p := range panes {
		if p.Viewer {
			return p.ID
		}
	}
	return ""
}

// statusPlan is what EnsureHubStatusPane does for a pane listing.
type statusPlan struct {
	kill  []string // status panes to kill
	split string   // viewer pane to split a new status pane from ("" = none)
	open  bool     // a status pane is (or will be) open
}

// planHubStatus decides how to bring the status pane in line with want: kill
// every status pane when it is not wanted or there is no viewer to sit
// above; keep exactly one (the first, killing extras a race left behind)
// when one exists; otherwise split one from the viewer.
func planHubStatus(panes []HubPane, want bool) statusPlan {
	ids := statusPaneIDs(panes)
	viewer := viewerPaneIn(panes)
	if !want || viewer == "" {
		return statusPlan{kill: ids}
	}
	if len(ids) > 1 {
		return statusPlan{kill: ids[1:], open: true}
	}
	if len(ids) == 1 {
		return statusPlan{open: true}
	}
	return statusPlan{split: viewer, open: true}
}

// EnsureHubStatusPane brings the hub's status pane in line with want and
// reports whether one is open afterwards. With want and a viewer pane it
// creates the pane height rows tall unless a marked one already exists (a
// hot-reloaded hub finds the pane its previous image made, so it never makes
// a second); without either it kills any status pane.
func EnsureHubStatusPane(want bool, height int) (bool, error) {
	panes, err := HubPanes()
	if err != nil {
		return false, err
	}
	plan := planHubStatus(panes, want)
	for _, id := range plan.kill {
		_, _ = tmuxRun("kill-pane", "-t", id)
	}
	if plan.split == "" {
		return plan.open, nil
	}
	id, err := tmuxRun(statusSplitArgs(plan.split, height, statusCommand(selfpath.SpawnableOrFallback()))...)
	if err != nil {
		return false, err
	}
	if _, err := tmuxRun("set-option", "-p", "-t", id, statusPaneOption, "1"); err != nil {
		_, _ = tmuxRun("kill-pane", "-t", id) // never leave an unmarked pane behind
		return false, fmt.Errorf("marking status pane: %w", err)
	}
	return true, nil
}

// PinHubStatusPane resizes every status pane to height rows. A window resize
// spreads the height change over the viewer column's panes, so the hub pins
// the status pane back after one.
func PinHubStatusPane(height int) error {
	panes, err := HubPanes()
	if err != nil {
		return err
	}
	for _, id := range statusPaneIDs(panes) {
		if _, err := tmuxRun(paneHeightArgs(id, height)...); err != nil {
			return err
		}
	}
	return nil
}

// KillHubStatus kills every status pane in the hub list window. Panes
// without the status option are never touched.
func KillHubStatus() error {
	panes, err := HubPanes()
	if err != nil {
		return err
	}
	for _, id := range statusPaneIDs(panes) {
		if _, err := tmuxRun("kill-pane", "-t", id); err != nil {
			return err
		}
	}
	return nil
}
