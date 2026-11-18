package tmux

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/testenv"
)

func TestViewerArgBuilders(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"new-session", viewerNewSessionArgs("agentctl-proj", "agentctl-hubview-2"),
			[]string{"new-session", "-d", "-t", "agentctl-proj", "-s", "agentctl-hubview-2"}},
		{"select-window", viewerSelectWindowArgs("agentctl-hubview-2", "@7"),
			[]string{"select-window", "-t", "agentctl-hubview-2:@7"}},
		{"switch-client", viewerSwitchClientArgs("/dev/ttys004", "agentctl-hubview-2"),
			[]string{"switch-client", "-c", "/dev/ttys004", "-t", "agentctl-hubview-2"}},
		{"destroy-unattached", viewerDestroyUnattachedArgs("agentctl-hubview"),
			[]string{"set-option", "-t", "agentctl-hubview", "destroy-unattached", "on"}},
		{"split", viewerSplitArgs("agentctl-hub:fleet-list", 80, "cmd"),
			[]string{"split-window", "-h", "-d", "-P", "-F", "#{pane_id}", "-t", "agentctl-hub:fleet-list", "-l", "80", "cmd"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !equalStrings(tt.got, tt.want) {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

func TestNextViewerName(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		want     string
	}{
		{"none", nil, "agentctl-hubview"},
		{"base taken", []string{"agentctl-hubview"}, "agentctl-hubview-2"},
		{"-2 taken", []string{"agentctl-hubview-2"}, "agentctl-hubview"},
		{"both taken", []string{"agentctl-hubview", "agentctl-hubview-2"}, "agentctl-hubview-3"},
		{"unrelated", []string{"agentctl-a"}, "agentctl-hubview"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextViewerName(tt.existing)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			if !IsViewerSession(got) {
				t.Errorf("%q does not match viewer prefix", got)
			}
		})
	}
}

func TestParseViewerSessionsAndClients(t *testing.T) {
	if got := parseViewerSessions("agentctl-a\nagentctl-hubview-2\n\nagentctl-hubview\nagentctl-hub\n"); !equalStrings(got, []string{"agentctl-hubview-2", "agentctl-hubview"}) {
		t.Errorf("parseViewerSessions = %v", got)
	}
	if got := parseViewerSessions(""); len(got) != 0 {
		t.Errorf("empty = %v", got)
	}
	if got := parseClientNames("/dev/ttys001\n /dev/ttys002 \n\n"); !equalStrings(got, []string{"/dev/ttys001", "/dev/ttys002"}) {
		t.Errorf("parseClientNames = %v", got)
	}
	if got := parseClientNames(""); len(got) != 0 {
		t.Errorf("empty = %v", got)
	}
}

func TestViewerOptionArgs(t *testing.T) {
	got := map[string]string{}
	for _, a := range viewerOptionArgs("agentctl-hubview-2") {
		if len(a) != 5 || a[0] != "set-option" || a[2] != "agentctl-hubview-2" {
			t.Fatalf("unexpected option args %v", a)
		}
		got[a[3]] = a[4]
	}
	want := map[string]string{"prefix": "None", "prefix2": "None", "status": "off", "mouse": "on"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("option %s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["destroy-unattached"]; ok {
		t.Error("destroy-unattached must be set after attach, not on a detached session")
	}
}

func TestViewerAttachCommand(t *testing.T) {
	tests := []struct {
		name, sock, want string
	}{
		{"no socket", "", `env -u TMUX tmux attach -t agentctl-hubview-2 \; set-option -t agentctl-hubview-2 destroy-unattached on`},
		{"socket", "/tmp/tmux-1/default", `env -u TMUX tmux -S '/tmp/tmux-1/default' attach -t agentctl-hubview-2 \; set-option -t agentctl-hubview-2 destroy-unattached on`},
		{"quote", "/a b/it's", `env -u TMUX tmux -S '/a b/it'\''s' attach -t agentctl-hubview-2 \; set-option -t agentctl-hubview-2 destroy-unattached on`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := viewerAttachCommand(tt.sock, "agentctl-hubview-2"); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestViewerNeverUsesJoinOrMove(t *testing.T) {
	all := [][]string{viewerNewSessionArgs("h", "v"), viewerSelectWindowArgs("v", "@1"),
		viewerSwitchClientArgs("c", "v"), viewerSplitArgs("t", 1, "c")}
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
// private tmux server. Opt in with AGENTCTL_SPIKE_TMUX_SOCKET=<socket name>.
// The server needs agentctl-hub:fleet-list (attached by a client, so the pane
// has a size) and sessions "homea" and "homeb", each with a window running a
// long sleep; pass their window IDs via AGENTCTL_SPIKE_WIN_A / _B.
func TestShowHideLiveViewer_Integration(t *testing.T) {
	name := os.Getenv("AGENTCTL_SPIKE_TMUX_SOCKET")
	if name == "" {
		t.Skip("set AGENTCTL_SPIKE_TMUX_SOCKET to run against a private tmux server")
	}
	winA, winB := os.Getenv("AGENTCTL_SPIKE_WIN_A"), os.Getenv("AGENTCTL_SPIKE_WIN_B")
	if winA == "" || winB == "" {
		t.Skip("set AGENTCTL_SPIKE_WIN_A and AGENTCTL_SPIKE_WIN_B")
	}
	// The developer started this server by hand under the real socket dir;
	// testenv moved TMUX_TMPDIR into the sandbox, so put it back.
	orig, _ := testenv.Original("TMUX_TMPDIR")
	t.Setenv("TMUX_TMPDIR", orig)
	out, err := exec.Command("tmux", "-L", name, "display-message", "-p", "#{socket_path},1,0").Output()
	if err != nil {
		t.Skipf("no private server: %v", err)
	}
	t.Setenv("TMUX", strings.TrimSpace(string(out)))

	panePIDs := func(win string) string {
		o, _ := exec.Command("tmux", "-L", name, "list-panes", "-t", win, "-F", "#{pane_pid}").Output()
		return strings.TrimSpace(string(o))
	}
	pidsA, pidsB := panePIDs(winA), panePIDs(winB)
	if pidsA == "" || pidsB == "" {
		t.Fatal("agent windows missing")
	}

	if err := ShowLiveViewer("homea", winA, 60); err != nil {
		t.Fatal(err)
	}
	pane := viewerPaneID()
	if pane == "" || !viewerSessionExists() {
		t.Fatal("viewer pane/session missing after show")
	}
	// Same session again, then a different project: pane must survive.
	for _, step := range []struct{ home, win string }{{"homea", winA}, {"homeb", winB}, {"homea", winA}} {
		if err := ShowLiveViewer(step.home, step.win, 60); err != nil {
			t.Fatalf("show %s/%s: %v", step.home, step.win, err)
		}
		if got := viewerPaneID(); got != pane {
			t.Fatalf("viewer pane changed: %q -> %q", pane, got)
		}
		if n := len(viewerSessions()); n != 1 {
			t.Fatalf("viewer sessions = %d, want 1", n)
		}
	}
	if panePIDs(winA) != pidsA || panePIDs(winB) != pidsB {
		t.Fatal("agent panes changed")
	}
	if err := HideLiveViewer(); err != nil {
		t.Fatal(err)
	}
	if viewerPaneID() != "" || viewerSessionExists() {
		t.Fatal("viewer leaked after hide")
	}
	if panePIDs(winA) != pidsA || panePIDs(winB) != pidsB {
		t.Fatal("agent panes killed by hide")
	}
}

func TestViewerResizeArgs(t *testing.T) {
	got := strings.Join(viewerResizeArgs("%7", 120), " ")
	if got != "resize-pane -t %7 -x 120" {
		t.Errorf("viewerResizeArgs = %q", got)
	}
}

func TestPickHomeSession(t *testing.T) {
	tests := []struct {
		name    string
		members []string
		want    string
	}{
		{"viewer first", []string{"agentctl-hubview", "agentctl-acme"}, "agentctl-acme"},
		{"home only", []string{"agentctl-acme"}, "agentctl-acme"},
		{"only viewers", []string{"agentctl-hubview", "agentctl-hubview-2"}, ""},
		{"empty", []string{""}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickHomeSession(tt.members); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseEnvValue(t *testing.T) {
	tests := []struct{ out, want string }{
		{"AGENTCTL_RUNTIME_DIR=/p/.agentctl\n", "/p/.agentctl"},
		{"-AGENTCTL_RUNTIME_DIR", ""},
		{"AGENTCTL_RUNTIME_DIR=", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := parseEnvValue("AGENTCTL_RUNTIME_DIR", tt.out); got != tt.want {
			t.Errorf("parseEnvValue(%q) = %q, want %q", tt.out, got, tt.want)
		}
	}
}

// Every binding that runs agentctl must carry the key window so the hub
// redirect can tell where the key was pressed (except the cross-project ones).
func TestKeybindingsCarryKeyWindow(t *testing.T) {
	unflagged := map[string]bool{"g": true, "b": true, "p": true}
	for _, kb := range agentctlKeybindings("/x/agentctl") {
		has := strings.Contains(kb.command, KeyWindowEnv+"=#{window_id}")
		if unflagged[kb.key] {
			continue
		}
		if !has {
			t.Errorf("binding %q lacks %s: %s", kb.key, KeyWindowEnv, kb.command)
		}
		if !strings.Contains(kb.command, KeyClientEnv+"=#{client_name}") {
			t.Errorf("binding %q lacks %s: %s", kb.key, KeyClientEnv, kb.command)
		}
	}
}
