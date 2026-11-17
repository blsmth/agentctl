package tmux

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestMobileViewerNames(t *testing.T) {
	a, err := NewMobileViewerName()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewMobileViewerName()
	if a == b {
		t.Fatalf("names not random: %q", a)
	}
	for _, n := range []string{a, b} {
		if !IsMobileViewerSession(n) || !IsAnyViewerSession(n) {
			t.Errorf("%q not recognised as a phone viewer", n)
		}
		if IsViewerSession(n) {
			t.Errorf("%q matches the hub viewer prefix: the hub would kill it", n)
		}
		if strings.ContainsAny(n, ":.= ") {
			t.Errorf("%q has target metacharacters", n)
		}
	}
}

func TestIsAnyViewerSession(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"agentctl-hubview", true},
		{"agentctl-hubview-2", true},
		{"agentctl-mobileview-0a1b2c3d4e5f", true},
		{"agentctl-proj", false},
		{"agentctl-hub", false},
		{"agentctl-mobileviewer", false},
		{"mobileview", false},
	}
	for _, tt := range tests {
		if got := IsAnyViewerSession(tt.name); got != tt.want {
			t.Errorf("IsAnyViewerSession(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestMobileViewerSetupArgs(t *testing.T) {
	const name = "agentctl-mobileview-abc"
	got, err := MobileViewerSetupArgs("agentctl-proj", "@12", name)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"new-session", "-d", "-t", "=agentctl-proj", "-s", name},
		{"set-option", "-t", "=" + name + ":", "prefix", "None"},
		{"set-option", "-t", "=" + name + ":", "prefix2", "None"},
		{"set-option", "-t", "=" + name + ":", "key-table", mobileKeyTable},
		{"set-option", "-t", "=" + name + ":", "status", "off"},
		{"select-window", "-t", "=" + name + ":@12"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	for _, c := range got {
		if slices.Contains([]string{"join-pane", "move-pane", "break-pane", "move-window"}, c[0]) {
			t.Errorf("viewer setup must never move panes: %q", c)
		}
	}
}

func TestMobileViewerSetupArgsRejects(t *testing.T) {
	tests := []struct{ home, win, name string }{
		{"", "@1", "agentctl-mobileview-a"},
		{"agentctl-proj:1", "@1", "agentctl-mobileview-a"},
		{"agentctl-proj.0", "@1", "agentctl-mobileview-a"},
		{HubSessionName, "@1", "agentctl-mobileview-a"},
		{"agentctl-hubview", "@1", "agentctl-mobileview-a"},
		{"agentctl-mobileview-b", "@1", "agentctl-mobileview-a"},
		{"agentctl-proj", "1", "agentctl-mobileview-a"},
		{"agentctl-proj", "@", "agentctl-mobileview-a"},
		{"agentctl-proj", "@1;kill-server", "agentctl-mobileview-a"},
		{"agentctl-proj", "@1", "agentctl-proj"},
	}
	for _, tt := range tests {
		if _, err := MobileViewerSetupArgs(tt.home, tt.win, tt.name); err == nil {
			t.Errorf("MobileViewerSetupArgs(%q, %q, %q) accepted", tt.home, tt.win, tt.name)
		}
	}
}

func TestMobileViewerAttachArgs(t *testing.T) {
	const name = "agentctl-mobileview-abc"
	rw := MobileViewerAttachArgs(name, false)
	want := []string{"-u", "attach-session", "-t", "=" + name, "-f", "ignore-size",
		";", "set-option", "-t", "=" + name + ":", "destroy-unattached", "on"}
	if !reflect.DeepEqual(rw, want) {
		t.Fatalf("write mode: got %q want %q", rw, want)
	}
	ro := MobileViewerAttachArgs(name, true)
	attach := ro[:slices.Index(ro, ";")]
	if !slices.Contains(attach, "-r") {
		t.Errorf("read-only attach lacks -r: %q", ro)
	}
	if i := slices.Index(attach, "-f"); i < 0 || attach[i+1] != "ignore-size" {
		t.Errorf("read-only attach lacks -f ignore-size: %q", ro)
	}
	if slices.Contains(rw, "-r") {
		t.Errorf("write-mode attach is read-only: %q", rw)
	}
}

func TestKillMobileViewerRefusesOtherSessions(t *testing.T) {
	for _, n := range []string{"agentctl-proj", "agentctl-hubview", "agentctl-hub", ""} {
		if err := KillMobileViewer(n); err == nil {
			t.Errorf("KillMobileViewer(%q) accepted", n)
		}
	}
}

func TestParseMobileViewerSessions(t *testing.T) {
	got := parseMobileViewerSessions("agentctl-proj\nagentctl-mobileview-a\n\nagentctl-hubview\n agentctl-mobileview-b \n")
	if want := []string{"agentctl-mobileview-a", "agentctl-mobileview-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestParseWindowSize(t *testing.T) {
	tests := []struct {
		in         string
		cols, rows int
		ok         bool
	}{
		{"212 54", 212, 54, true},
		{" 80 24\n", 80, 24, true},
		{"80", 0, 0, false},
		{"0 24", 0, 0, false},
		{"a b", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tt := range tests {
		c, r, err := parseWindowSize(tt.in)
		if (err == nil) != tt.ok || c != tt.cols || r != tt.rows {
			t.Errorf("parseWindowSize(%q) = %d, %d, %v", tt.in, c, r, err)
		}
	}
}

func TestWindowSizeRejectsBadIDs(t *testing.T) {
	for _, id := range []string{"", "12", "@", "@x", "agentctl-proj:@1"} {
		if _, _, err := WindowSize(id); err == nil {
			t.Errorf("WindowSize(%q) accepted", id)
		}
	}
}

// Discovery must never surface a phone viewer session.
func TestDiscoveryExcludesMobileViewers(t *testing.T) {
	names := parseSessionList("agentctl-proj\nagentctl-mobileview-0a1b\nagentctl-hubview\n")
	if !reflect.DeepEqual(names, []string{"agentctl-proj"}) {
		t.Errorf("parseSessionList = %q", names)
	}
	infos := parseSessionsWithPaths("agentctl-proj|/code/proj\nagentctl-mobileview-0a1b|/code/proj\n")
	if len(infos) != 1 || infos[0].Name != "agentctl-proj" {
		t.Errorf("parseSessionsWithPaths = %+v", infos)
	}
	if got := pickHomeSession([]string{"agentctl-mobileview-0a1b", "agentctl-hubview", "agentctl-proj"}); got != "agentctl-proj" {
		t.Errorf("pickHomeSession = %q", got)
	}
}

func TestHomeOfSessionPassesThroughNonViewers(t *testing.T) {
	for _, n := range []string{"agentctl-proj", "agentctl-hub", ""} {
		if got := homeOfSession(n); got != n {
			t.Errorf("homeOfSession(%q) = %q", n, got)
		}
	}
}

func TestSessionNameForPaneMapsViewerToHome(t *testing.T) {
	origQ, origH := queryPaneSessionName, homeOfSession
	t.Cleanup(func() { queryPaneSessionName, homeOfSession = origQ, origH })
	queryPaneSessionName = func(string) (string, error) { return "agentctl-mobileview-0a1b", nil }
	homeOfSession = func(n string) string {
		if IsAnyViewerSession(n) {
			return "agentctl-proj"
		}
		return n
	}
	if got := SessionNameForPane("%5"); got != "agentctl-proj" {
		t.Errorf("SessionNameForPane = %q, want the home session", got)
	}
}
