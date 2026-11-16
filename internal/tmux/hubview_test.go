package tmux

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestViewerArgBuilders(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"new-session", viewerNewSessionArgs("agentctl-proj"),
			[]string{"new-session", "-d", "-t", "agentctl-proj", "-s", "agentctl-hubview"}},
		{"select-window", viewerSelectWindowArgs("@7"),
			[]string{"select-window", "-t", "agentctl-hubview:@7"}},
		{"respawn", viewerRespawnArgs("%9", "cmd"),
			[]string{"respawn-pane", "-k", "-t", "%9", "cmd"}},
		{"split", viewerSplitArgs("agentctl-hub:herd-list", 80, "cmd"),
			[]string{"split-window", "-h", "-d", "-P", "-F", "#{pane_id}", "-t", "agentctl-hub:herd-list", "-l", "80", "cmd"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !equalStrings(tt.got, tt.want) {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

func TestViewerOptionArgs(t *testing.T) {
	got := map[string]string{}
	for _, a := range viewerOptionArgs() {
		if len(a) != 5 || a[0] != "set-option" || a[2] != ViewerSessionName {
			t.Fatalf("unexpected option args %v", a)
		}
		got[a[3]] = a[4]
	}
	want := map[string]string{"prefix": "None", "prefix2": "None", "status": "off"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("option %s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["destroy-unattached"]; ok {
		t.Error("destroy-unattached must be set at attach time, not on a detached session")
	}
}

func TestViewerAttachCommand(t *testing.T) {
	tests := []struct {
		name, sock, want string
	}{
		{"no socket", "", `env -u TMUX tmux attach -t agentctl-hubview \; set-option -t agentctl-hubview destroy-unattached on`},
		{"socket", "/tmp/tmux-1/default", `env -u TMUX tmux -S '/tmp/tmux-1/default' attach -t agentctl-hubview \; set-option -t agentctl-hubview destroy-unattached on`},
		{"quote", "/a b/it's", `env -u TMUX tmux -S '/a b/it'\''s' attach -t agentctl-hubview \; set-option -t agentctl-hubview destroy-unattached on`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := viewerAttachCommand(tt.sock); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestViewerNeverUsesJoinOrMove(t *testing.T) {
	all := [][]string{viewerNewSessionArgs("h"), viewerSelectWindowArgs("@1"),
		viewerRespawnArgs("%1", "c"), viewerSplitArgs("t", 1, "c")}
	for _, a := range all {
		if strings.Contains(a[0], "join-pane") || strings.Contains(a[0], "move-pane") {
			t.Errorf("forbidden command %v", a)
		}
	}
}

func TestViewerSessionsFiltered(t *testing.T) {
	got := parseSessionList("agentctl-a\nagentctl-hubview\nagentctl-hubview-2\n")
	if len(got) != 1 || got[0] != "agentctl-a" {
		t.Errorf("parseSessionList = %v", got)
	}
	info := parseSessionsWithPaths("agentctl-a|/p\nagentctl-hubview|/p\n")
	if len(info) != 1 || info[0].Name != "agentctl-a" {
		t.Errorf("parseSessionsWithPaths = %v", info)
	}
}

func TestShowLiveViewer_Refusals(t *testing.T) {
	for _, s := range []string{HubSessionName, ViewerSessionName} {
		if err := ShowLiveViewer(s, "@1", 50); err == nil {
			t.Errorf("expected refusal for home %q", s)
		}
	}
	if err := ShowLiveViewer("agentctl-x", "", 50); err == nil {
		t.Error("expected refusal for empty window id")
	}
}

// TestShowHideLiveViewer_Integration drives the real functions against a
// private tmux server. Opt in with AGENTCTL_SPIKE_TMUX_SOCKET=<socket name>
// (a server that already has agentctl-hub:herd-list and a session "home").
func TestShowHideLiveViewer_Integration(t *testing.T) {
	name := os.Getenv("AGENTCTL_SPIKE_TMUX_SOCKET")
	if name == "" {
		t.Skip("set AGENTCTL_SPIKE_TMUX_SOCKET to run against a private tmux server")
	}
	out, err := exec.Command("tmux", "-L", name, "display-message", "-p", "#{socket_path},1,0").Output()
	if err != nil {
		t.Skipf("no private server: %v", err)
	}
	t.Setenv("TMUX", strings.TrimSpace(string(out)))
	if err := ShowLiveViewer("home", "@0", 60); err != nil {
		t.Fatal(err)
	}
	if viewerPaneID() == "" || !viewerSessionExists() {
		t.Fatal("viewer pane/session missing after show")
	}
	if err := ShowLiveViewer("home", "@0", 60); err != nil {
		t.Fatal(err)
	}
	if err := HideLiveViewer(); err != nil {
		t.Fatal(err)
	}
	if viewerPaneID() != "" || viewerSessionExists() {
		t.Fatal("viewer leaked after hide")
	}
}
