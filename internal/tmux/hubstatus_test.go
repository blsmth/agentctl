package tmux

import (
	"reflect"
	"testing"
)

func TestStatusSplitArgs(t *testing.T) {
	cmd := statusCommand("/opt/agent ctl/agentctl")
	if cmd != "'/opt/agent ctl/agentctl' hub-status" {
		t.Errorf("statusCommand = %q", cmd)
	}
	got := statusSplitArgs("%2", 6, cmd)
	want := []string{"split-window", "-v", "-b", "-d", "-P", "-F", "#{pane_id}", "-t", "%2", "-l", "6", cmd}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("statusSplitArgs = %v, want %v", got, want)
	}
}

func TestPaneHeightArgs(t *testing.T) {
	if got, want := paneHeightArgs("%9", 6), []string{"resize-pane", "-t", "%9", "-y", "6"}; !reflect.DeepEqual(got, want) {
		t.Errorf("paneHeightArgs = %v, want %v", got, want)
	}
}

func TestPlanHubStatus(t *testing.T) {
	list := HubPane{ID: "%1", Width: 56}
	viewer := HubPane{ID: "%2", Width: 72, Viewer: true}
	status := HubPane{ID: "%9", Width: 72, Status: true}
	extra := HubPane{ID: "%10", Width: 72, Status: true}
	activity := HubPane{ID: "%7", Width: 70, Activity: true}
	tests := []struct {
		name  string
		panes []HubPane
		want  bool
		plan  statusPlan
	}{
		{"viewer, no status: split from the viewer", []HubPane{list, viewer, activity}, true, statusPlan{split: "%2", open: true}},
		{"existing status is reused (hot reload)", []HubPane{list, status, viewer}, true, statusPlan{open: true}},
		{"duplicates beyond the first are killed", []HubPane{list, status, extra, viewer}, true, statusPlan{kill: []string{"%10"}, open: true}},
		{"no viewer: kill the orphan", []HubPane{list, status, activity}, true, statusPlan{kill: []string{"%9"}}},
		{"no viewer, no status: nothing", []HubPane{list}, true, statusPlan{}},
		{"not wanted: kill", []HubPane{list, status, viewer}, false, statusPlan{kill: []string{"%9"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := planHubStatus(tt.panes, tt.want); !reflect.DeepEqual(got, tt.plan) {
				t.Errorf("planHubStatus = %+v, want %+v", got, tt.plan)
			}
		})
	}
}

func TestStatusPaneNeverTakenForViewerOrActivity(t *testing.T) {
	panes := parseHubPanes("%1 56 v a s\n%9 72 v a s1\n%2 72 v1 a s\n%7 70 v a1 s\n")
	if got := viewerPaneIn(panes); got != "%2" {
		t.Errorf("viewerPaneIn = %q, want %%2", got)
	}
	if got := activityPaneIDs(panes); !reflect.DeepEqual(got, []string{"%7"}) {
		t.Errorf("activityPaneIDs = %v", got)
	}
	if got := statusPaneIDs(panes); !reflect.DeepEqual(got, []string{"%9"}) {
		t.Errorf("statusPaneIDs = %v", got)
	}
}

func TestFocusToggleTarget(t *testing.T) {
	tests := []struct {
		name, active, list, viewer, want string
	}{
		{"list active: viewer, never the status pane", "%1", "%1", "%2", "%2"},
		{"viewer active: list", "%2", "%1", "%2", "%1"},
		{"status active: list", "%9", "%1", "%2", "%1"},
		{"no viewer: fall back", "%1", "%1", "", ""},
		{"no list: fall back", "%1", "", "%2", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := focusToggleTarget(tt.active, tt.list, tt.viewer); got != tt.want {
				t.Errorf("focusToggleTarget = %q, want %q", got, tt.want)
			}
		})
	}
}
