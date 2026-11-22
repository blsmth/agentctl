package tmux

import (
	"slices"
	"strings"
	"testing"
)

func TestHubZoomArgBuilders(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"list", hubZoomListArgs(),
			[]string{"list-windows", "-a", "-F", "#{window_id} #{window_zoomed_flag} #{@agentctl_hub_zoom}"}},
		{"panes", hubZoomPanesArgs("@7"),
			[]string{"list-panes", "-t", "@7", "-F", "#{pane_id}\t#{pane_start_command}"}},
		{"zoom", hubZoomArgs("%12"), []string{"resize-pane", "-Z", "-t", "%12"}},
		{"unzoom", hubUnzoomArgs("@7"), []string{"resize-pane", "-Z", "-t", "@7"}},
		{"mark", hubZoomMarkArgs("@7"), []string{"set-option", "-w", "-t", "@7", "@agentctl_hub_zoom", "1"}},
		{"clear", hubZoomClearArgs("@7"), []string{"set-option", "-w", "-u", "-t", "@7", "@agentctl_hub_zoom"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !equalStrings(tt.got, tt.want) {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestParseZoomWindows(t *testing.T) {
	out := "@1 0 \n@2 1 1\n@2 1 1\n@3 1 \n@4 0 1\ngarbage\n\n"
	got := parseZoomWindows(out)
	want := []zoomWindow{
		{id: "@1"},
		{id: "@2", zoomed: true, marked: true},
		{id: "@3", zoomed: true},
		{id: "@4", marked: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestPlanHubZoom(t *testing.T) {
	plain := zoomWindow{id: "@1"}
	userZoom := zoomWindow{id: "@1", zoomed: true}
	hubZoom := zoomWindow{id: "@1", zoomed: true, marked: true}
	userUnzoomed := zoomWindow{id: "@1", marked: true}
	otherHub := zoomWindow{id: "@2", zoomed: true, marked: true}
	otherUser := zoomWindow{id: "@3", zoomed: true}
	otherStale := zoomWindow{id: "@4", marked: true}

	tests := []struct {
		name   string
		wins   []zoomWindow
		viewed string
		hide   bool
		want   zoomPlan
	}{
		{"zooms viewed window", []zoomWindow{plain}, "@1", true, zoomPlan{zoom: "@1"}},
		{"chrome shown: no zoom", []zoomWindow{plain}, "@1", false, zoomPlan{}},
		{"nothing viewed", []zoomWindow{plain}, "", true, zoomPlan{}},
		{"never adopts a user zoom", []zoomWindow{userZoom}, "@1", true, zoomPlan{}},
		{"keeps own zoom on viewed", []zoomWindow{hubZoom}, "@1", true, zoomPlan{}},
		{"respects a user unzoom", []zoomWindow{userUnzoomed}, "@1", true, zoomPlan{}},
		{"chrome shown: unzooms viewed", []zoomWindow{hubZoom}, "@1", false, zoomPlan{unzoom: []string{"@1"}}},
		{"chrome shown: clears unzoomed marker", []zoomWindow{userUnzoomed}, "@1", false, zoomPlan{clear: []string{"@1"}}},
		{
			"switch: unzoom old hub zoom, leave user zoom, zoom new",
			[]zoomWindow{plain, otherHub, otherUser, otherStale}, "@1", true,
			zoomPlan{unzoom: []string{"@2"}, clear: []string{"@4"}, zoom: "@1"},
		},
		{
			"sweep with no viewer undoes only hub zooms",
			[]zoomWindow{otherHub, otherUser, otherStale}, "", true,
			zoomPlan{unzoom: []string{"@2"}, clear: []string{"@4"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planHubZoom(tt.wins, tt.viewed, tt.hide)
			if !slices.Equal(got.unzoom, tt.want.unzoom) || !slices.Equal(got.clear, tt.want.clear) || got.zoom != tt.want.zoom {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPickZoomPane(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{"main first", "%1\tclaude --name x\n%2\t'/bin/agentctl' chrome-bar @3", "%1"},
		{"chrome first", "%2\t'/bin/agentctl' chrome-bar @3\n%1\tzsh", "%1"},
		{"skips hud", "%4\t'/bin/agentctl' hud @3\n%1\tclaude\n%2\t/bin/agentctl chrome-bar @3", "%1"},
		{"only chrome", "%2\t/bin/agentctl chrome-bar @3", ""},
		{"no chrome bar: nothing to hide", "%1\tclaude\n%4\t'/bin/agentctl' hud @3", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickZoomPane(tt.out); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// fakeZoomTmux records tmux calls made through the hub zoom seams.
type fakeZoomTmux struct {
	list     string
	panes    string
	calls    []string
	enforced []string
}

func (f *fakeZoomTmux) install(t *testing.T, viewed string) {
	t.Helper()
	oldRun, oldViewed, oldEnforce := hubZoomRun, hubZoomViewed, hubZoomEnforce
	t.Cleanup(func() { hubZoomRun, hubZoomViewed, hubZoomEnforce = oldRun, oldViewed, oldEnforce })
	hubZoomRun = func(args ...string) (string, error) {
		f.calls = append(f.calls, strings.Join(args, " "))
		switch args[0] {
		case "list-windows":
			return f.list, nil
		case "list-panes":
			return f.panes, nil
		}
		return "", nil
	}
	hubZoomViewed = func() string { return viewed }
	hubZoomEnforce = func(w string) { f.enforced = append(f.enforced, w) }
}

func TestSyncHubZoom(t *testing.T) {
	listCmd := strings.Join(hubZoomListArgs(), " ")
	t.Run("zooms the viewed main pane, marking first", func(t *testing.T) {
		f := &fakeZoomTmux{list: "@1 0 \n@2 1 1", panes: "%9\t/bin/agentctl chrome-bar @1\n%8\tclaude"}
		f.install(t, "@1")
		SyncHubZoom(true)
		want := []string{
			listCmd,
			"resize-pane -Z -t @2",
			"set-option -w -u -t @2 @agentctl_hub_zoom",
			strings.Join(hubZoomPanesArgs("@1"), " "),
			"set-option -w -t @1 @agentctl_hub_zoom 1",
			"resize-pane -Z -t %8",
		}
		if !slices.Equal(f.calls, want) {
			t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
		}
		if !slices.Equal(f.enforced, []string{"@2"}) {
			t.Errorf("enforced = %v, want [@2]", f.enforced)
		}
	})
	t.Run("sweep never zooms", func(t *testing.T) {
		f := &fakeZoomTmux{list: "@1 0 "}
		f.install(t, "@1")
		SweepHubZoom()
		if !slices.Equal(f.calls, []string{listCmd}) {
			t.Errorf("calls = %q", f.calls)
		}
	})
	t.Run("sweep keeps the viewed hub zoom", func(t *testing.T) {
		f := &fakeZoomTmux{list: "@1 1 1"}
		f.install(t, "@1")
		SweepHubZoom()
		if !slices.Equal(f.calls, []string{listCmd}) {
			t.Errorf("calls = %q", f.calls)
		}
	})
	t.Run("clear undoes the viewed hub zoom", func(t *testing.T) {
		f := &fakeZoomTmux{list: "@1 1 1\n@3 1 "}
		f.install(t, "@1")
		ClearHubZoom()
		want := []string{listCmd, "resize-pane -Z -t @1", "set-option -w -u -t @1 @agentctl_hub_zoom"}
		if !slices.Equal(f.calls, want) {
			t.Errorf("calls = %q, want %q", f.calls, want)
		}
	})
}

func TestParseWindowHeightZoom(t *testing.T) {
	tests := []struct {
		out     string
		height  int
		zoomed  bool
		wantErr bool
	}{
		{"40 0\n", 40, false, false},
		{"40 1\n", 40, true, false},
		{"40", 40, false, false},
		{"", 0, false, true},
		{"x 1", 0, false, true},
		{"0 0", 0, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.out, func(t *testing.T) {
			h, z, err := parseWindowHeightZoom(tt.out)
			if (err != nil) != tt.wantErr || h != tt.height || z != tt.zoomed {
				t.Errorf("got (%d, %v, %v), want (%d, %v, err=%v)", h, z, err, tt.height, tt.zoomed, tt.wantErr)
			}
		})
	}
}

func TestChromeSizeHookEventsIncludeSessionChanged(t *testing.T) {
	for _, ev := range []string{"client-resized", "client-attached", "client-session-changed"} {
		if !slices.Contains(chromeSizeHookEvents, ev) {
			t.Errorf("chromeSizeHookEvents missing %s", ev)
		}
	}
}
