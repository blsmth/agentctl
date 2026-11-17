package hooks

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestSelectPopupClient(t *testing.T) {
	tests := []struct {
		name        string
		clients     []tmuxClient
		window      string
		wantClient  string
		wantViewing bool
	}{
		{"single client viewing", []tmuxClient{{"c1", "@5", 10, "", ""}}, "@5", "c1", true},
		{"two viewing, higher activity wins", []tmuxClient{{"c1", "@5", 10, "", ""}, {"c2", "@5", 20, "", ""}, {"c3", "@9", 99, "", ""}}, "@5", "c2", true},
		{"viewed only via hubview", []tmuxClient{{"/dev/ttys002", "@450", 50, "", ""}, {"/dev/ttys029", "@526", 5, "", ""}}, "@526", "/dev/ttys029", true},
		{"nobody viewing, most active", []tmuxClient{{"c1", "@1", 10, "", ""}, {"c2", "@2", 30, "", ""}}, "@5", "c2", false},
		{"no clients", nil, "@5", "", false},
		{"control-mode ignored", []tmuxClient{{"ctl", "@5", 99, "attached,control-mode", ""}, {"c1", "@1", 1, "", ""}}, "@5", "c1", false},
		{"phone terminal client ignored", []tmuxClient{{"/dev/ttys090", "@5", 99, "attached,ignore-size", "agentctl-mobileview-0a1b2c"}, {"c1", "@1", 1, "", "agentctl-proj"}}, "@5", "c1", false},
		{"only a phone client", []tmuxClient{{"/dev/ttys090", "@5", 99, "attached", "agentctl-mobileview-0a1b2c"}}, "@5", "", false},
		{"unknown window falls back to most active", []tmuxClient{{"c1", "@1", 1, "", ""}, {"c2", "@2", 2, "", ""}}, "", "c2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectPopupClient(tt.clients, tt.window)
			if got.Client != tt.wantClient || got.Viewing != tt.wantViewing {
				t.Fatalf("got %+v, want client=%q viewing=%v", got, tt.wantClient, tt.wantViewing)
			}
		})
	}
}

func TestParseClients(t *testing.T) {
	got := parseClients("/dev/ttys002\t@450\t1700\tattached,focused\tagentctl-proj\nbad\n\n/dev/ttys070\t@526\t1800\t\n")
	want := []tmuxClient{{"/dev/ttys002", "@450", 1700, "attached,focused", "agentctl-proj"}, {"/dev/ttys070", "@526", 1800, "", ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestPopupTargetArgsAndLabel(t *testing.T) {
	if got := (popupTarget{}).clientArgs(); got != nil {
		t.Fatalf("empty target must add no -c, got %v", got)
	}
	if got := (popupTarget{Client: "c"}).clientArgs(); !reflect.DeepEqual(got, []string{"-c", "c"}) {
		t.Fatalf("got %v", got)
	}
	if got := (popupTarget{Viewing: true, WindowID: "@5"}).label("a"); got != "a" {
		t.Fatalf("got %q", got)
	}
	if got := (popupTarget{WindowID: "@5"}).label("a"); got != "a (@5)" {
		t.Fatalf("got %q", got)
	}
}

func TestTmuxHasAttachedClientHubOnly(t *testing.T) {
	origList := listClients
	t.Cleanup(func() { listClients = origList })
	t.Setenv("TMUX_PANE", "") // resolveCallingWindowID -> "" => false
	listClients = func() []tmuxClient { return []tmuxClient{{"hub", "@526", 1, "", ""}} }
	if tmuxHasAttachedClient() {
		t.Fatal("unresolvable window must be unattached")
	}
	// selection is the unit under the production wrapper.
	if !selectPopupClient(listClients(), "@526").Viewing {
		t.Fatal("hub-only view must count as viewed")
	}
	if selectPopupClient(listClients(), "@7").Viewing {
		t.Fatal("other window must not count as viewed")
	}
}

func TestOpenAndCloseUseSameClient(t *testing.T) {
	origRun, origDL, origClose := dispatchPopupRunner, popupDeadline, popupCloser
	t.Cleanup(func() { dispatchPopupRunner, popupDeadline, popupCloser = origRun, origDL, origClose })
	popupDeadline = 10 * time.Millisecond
	var opened, closed string
	dispatchPopupRunner = func(ctx context.Context, client string, _, _ int, _ string) error {
		opened = client
		<-ctx.Done()
		return ctx.Err()
	}
	popupCloser = func(client string) error { closed = client; return nil }

	target := selectPopupClient([]tmuxClient{{"/dev/ttys029", "@526", 1, "", ""}}, "@526")
	runPopupWithDeadline(target, "dispatch", "test", 10, 10, "f", popupNotice{Agent: "a", Tool: "dispatch", Action: "x"})
	if opened != "/dev/ttys029" || closed != opened {
		t.Fatalf("opened=%q closed=%q", opened, closed)
	}
}
