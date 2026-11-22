package tmux

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseHubPanes(t *testing.T) {
	out := "%1 56 v a s\n%9 72 v a s1\n%2 72 v1 a s\n%7 70 v a1 s\ngarbage\n%3 notanumber v a s\n%4 10 v a\n"
	got := parseHubPanes(out)
	want := []HubPane{
		{ID: "%1", Width: 56},
		{ID: "%9", Width: 72, Status: true},
		{ID: "%2", Width: 72, Viewer: true},
		{ID: "%7", Width: 70, Activity: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseHubPanes = %+v, want %+v", got, want)
	}
	if ids := activityPaneIDs(got); !reflect.DeepEqual(ids, []string{"%7"}) {
		t.Errorf("activityPaneIDs = %v", ids)
	}
}

func TestActivitySplitArgs(t *testing.T) {
	cmd := activityCommand("/opt/agent ctl/agentctl")
	if cmd != "'/opt/agent ctl/agentctl' hud --follow-hub" {
		t.Errorf("activityCommand = %q", cmd)
	}
	args := activitySplitArgs(4, cmd)
	joined := strings.Join(args, " ")
	for _, want := range []string{"split-window -h -f -d", "-l 4", "-t " + sessionWindowTarget(HubSessionName, FleetListWindowName)} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	if args[len(args)-1] != cmd {
		t.Errorf("command not last: %v", args)
	}
}

func TestPaneWidthArgs(t *testing.T) {
	if got, want := paneWidthArgs("%7", 70), []string{"resize-pane", "-t", "%7", "-x", "70"}; !reflect.DeepEqual(got, want) {
		t.Errorf("paneWidthArgs = %v, want %v", got, want)
	}
}

func TestViewerSplitTarget(t *testing.T) {
	// Split from the list pane, never the window (whose active pane may be
	// the activity pane).
	if got := viewerSplitTarget("%1"); got != "%1" {
		t.Errorf("viewerSplitTarget(%%1) = %q", got)
	}
	if got, want := viewerSplitTarget(""), sessionWindowTarget(HubSessionName, FleetListWindowName); got != want {
		t.Errorf("fallback = %q, want %q", got, want)
	}
}

func TestParseListPaneSkipsActivity(t *testing.T) {
	// The activity pane (rightmost) is flagged too, so it is never taken for
	// the list.
	if got := parseListPane("%7 1\n%9 1\n%1 \n%2 1\n"); got != "%1" {
		t.Errorf("got %q, want %%1", got)
	}
}
