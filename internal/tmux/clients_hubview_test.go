package tmux

import (
	"testing"
	"time"
)

// The hub's nested viewer client counts as a laptop client, judged by its own
// client_activity. tmux only advances that on real input to the client; the
// hub's switch-client/select-window/refresh-client calls do not (checked
// against a live server), so the hub cannot keep a stale view "active".
func TestLaptopViewingHubViewerUsesItsActivity(t *testing.T) {
	now := time.Unix(10_000, 0)
	hub := func(activity int64) []Client {
		return []Client{
			{Name: "/dev/ttys1", WindowID: "@900", Activity: now.Unix(), Session: "agentctl-hub"},
			{Name: "/dev/ttys2", WindowID: "@5", Activity: activity, Session: ViewerSessionName},
		}
	}
	idle := 2 * time.Minute
	if !LaptopViewing(hub(now.Unix()-5), "@5", now, idle) {
		t.Error("recently active hub viewer should count as a laptop looking")
	}
	if LaptopViewing(hub(now.Unix()-600), "@5", now, idle) {
		t.Error("idle hub viewer must not count, whatever the outer hub client does")
	}
}
