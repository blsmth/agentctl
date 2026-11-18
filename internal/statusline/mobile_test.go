package statusline

import (
	"errors"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/phoneview"
	"github.com/blsmth/agentctl/internal/tmux"
)

func TestEffectiveFormat(t *testing.T) {
	const win = "@7"
	now := time.Unix(1_700_000_000, 0)
	fresh := now.Add(-10 * time.Second).Unix()
	stale := now.Add(-laptopIdleAfter - time.Second).Unix()
	laptop := tmux.Client{Name: "/dev/ttys001", WindowID: win, Session: "agentctl-proj", Activity: fresh}
	laptopIdle := tmux.Client{Name: "/dev/ttys001", WindowID: win, Session: "agentctl-proj", Activity: stale}
	laptopElsewhere := tmux.Client{Name: "/dev/ttys001", WindowID: "@9", Session: "agentctl-proj"}
	phone := tmux.Client{Name: "/dev/ttys099", WindowID: win, Session: tmux.MobileViewerSessionPrefix + "abc"}
	hub := tmux.Client{Name: "/dev/ttys002", WindowID: win, Session: "agentctl-hubview", Activity: fresh}
	control := tmux.Client{Name: "ctl", WindowID: win, Session: "agentctl-proj", Flags: "control-mode", Activity: fresh}

	tests := []struct {
		name       string
		marker     bool
		cfg        config.Config
		clients    []tmux.Client
		listErr    error
		window     string
		want       string
		wantListed bool
	}{
		{name: "no marker", clients: []tmux.Client{phone}, window: win, want: "rich"},
		{name: "marker, laptop viewing", marker: true, clients: []tmux.Client{laptop, phone}, window: win, want: "rich", wantListed: true},
		{name: "marker, idle laptop client on the window", marker: true, clients: []tmux.Client{laptopIdle, phone}, window: win, want: "full", wantListed: true},
		{name: "marker, idle and fresh laptop clients", marker: true, clients: []tmux.Client{laptopIdle, laptop, phone}, window: win, want: "rich", wantListed: true},
		{name: "marker, laptop elsewhere", marker: true, clients: []tmux.Client{laptopElsewhere, phone}, window: win, want: "full", wantListed: true},
		{name: "marker, only the phone client", marker: true, clients: []tmux.Client{phone}, window: win, want: "full", wantListed: true},
		{name: "marker, hub viewer counts as laptop", marker: true, clients: []tmux.Client{hub, phone}, window: win, want: "rich", wantListed: true},
		{name: "marker, control client ignored", marker: true, clients: []tmux.Client{control, phone}, window: win, want: "full", wantListed: true},
		{name: "marker, no clients at all", marker: true, window: win, want: "full", wantListed: true},
		{name: "marker, tmux error", marker: true, listErr: errors.New("boom"), window: win, want: "rich", wantListed: true},
		{name: "unresolved window", marker: true, clients: []tmux.Client{phone}, window: "", want: "rich"},
		{name: "mobile format equal to normal disables the switch", marker: true, cfg: config.Config{StatuslineMobileFormat: "rich"}, clients: []tmux.Client{phone}, window: win, want: "rich"},
		{name: "configured mobile format", marker: true, cfg: config.Config{StatuslineFormat: "complementary", StatuslineMobileFormat: "minimal"}, clients: []tmux.Client{phone}, window: win, want: "minimal", wantListed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.marker {
				if err := phoneview.Add(dir, win, "v1"); err != nil {
					t.Fatal(err)
				}
			}
			listed := false
			oldDir, oldList, oldNow := phoneViewDirFn, listClientsFn, nowFn
			t.Cleanup(func() { phoneViewDirFn, listClientsFn, nowFn = oldDir, oldList, oldNow })
			nowFn = func() time.Time { return now }
			phoneViewDirFn = func() string { return dir }
			listClientsFn = func(time.Duration) ([]tmux.Client, error) {
				listed = true
				return tt.clients, tt.listErr
			}
			if got := effectiveFormat(&tt.cfg, tt.window); got != tt.want {
				t.Errorf("format = %q, want %q", got, tt.want)
			}
			if listed != tt.wantListed {
				t.Errorf("tmux listed = %v, want %v", listed, tt.wantListed)
			}
		})
	}
}
