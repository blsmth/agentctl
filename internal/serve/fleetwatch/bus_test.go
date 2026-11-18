package fleetwatch

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
)

type fakeSource struct {
	mu     sync.Mutex
	groups []Group
	err    error
}

func (f *fakeSource) Fleet() ([]Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.groups, f.err
}

func (f *fakeSource) set(g ...Group) {
	f.mu.Lock()
	f.groups = g
	f.mu.Unlock()
}

func win(id, st string, done bool) fleet.Window {
	return fleet.Window{Session: "agentctl-p", Project: "p", WindowID: id, Agent: "a", State: st, Done: done}
}

func grp(ws ...fleet.Window) Group {
	return Group{Session: "agentctl-p", Project: "p", Windows: ws}
}

func next(t *testing.T, s *Subscription) Event {
	t.Helper()
	select {
	case ev := <-s.C:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

func none(t *testing.T, s *Subscription) {
	t.Helper()
	select {
	case ev := <-s.C:
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestBusWindowAndTransition(t *testing.T) {
	src := &fakeSource{}
	src.set(grp(win("@1", "idle", false)))
	b := NewBus(src, Options{})
	snap, sub := b.Subscribe()
	defer sub.Close()
	if len(snap) != 1 || len(snap[0].Windows) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}

	src.set(grp(win("@1", "working", false), win("@2", "idle", false)))
	b.Refresh()
	// @1 changed: window + transition idle->working; @2 new: window + transition ""->idle.
	want := []struct {
		kind     Kind
		id       string
		from, to string
	}{
		{KindWindow, "@1", "", ""},
		{KindTransition, "@1", "idle", "working"},
		{KindWindow, "@2", "", ""},
		{KindTransition, "@2", "", "idle"},
	}
	for _, w := range want {
		ev := next(t, sub)
		if ev.Kind != w.kind || ev.Window.WindowID != w.id || ev.From != w.from || ev.To != w.to {
			t.Errorf("got %+v, want %+v", ev, w)
		}
	}
}

func TestBusIgnoresNonRenderedChanges(t *testing.T) {
	row := func(turns int, sid string) fleet.Row {
		return fleet.Row{Session: "agentctl-p", Project: "p", AgentState: state.AgentState{
			Name: "a", WindowID: "@1", State: "working", TurnCount: turns, SessionID: sid, InputTokens: turns * 10,
		}}
	}
	mk := func(r fleet.Row) Group {
		return Group{Session: "agentctl-p", Project: "p", Windows: []fleet.Window{fleet.ToWindow(r, fleet.Meta{})}}
	}
	src := &fakeSource{}
	src.set(mk(row(1, "s1")))
	b := NewBus(src, Options{})
	_, sub := b.Subscribe()
	defer sub.Close()

	// Turn count, session id and token totals are not rendered.
	src.set(mk(row(2, "s2")))
	b.Refresh()
	none(t, sub)

	// Activity is rendered.
	r := row(2, "s2")
	r.LastToolDetail = "Editing x.go"
	src.set(mk(r))
	b.Refresh()
	if ev := next(t, sub); ev.Kind != KindWindow || ev.Window.Activity != "Editing x.go" {
		t.Errorf("got %+v", ev)
	}
}

func TestBusGone(t *testing.T) {
	src := &fakeSource{}
	src.set(grp(win("@1", "idle", false), win("@2", "idle", false)))
	b := NewBus(src, Options{})
	_, sub := b.Subscribe()
	defer sub.Close()

	src.set(grp(win("@1", "idle", false)))
	b.Refresh()
	none(t, sub) // one miss is not enough
	if got := len(b.Snapshot()[0].Windows); got != 2 {
		t.Errorf("window dropped from snapshot after one miss: %d", got)
	}
	b.Refresh()
	ev := next(t, sub)
	if ev.Kind != KindGone || ev.Session != "agentctl-p" || ev.WindowID != "@2" {
		t.Errorf("got %+v", ev)
	}
}

func TestBusFleetErrorChangesNothing(t *testing.T) {
	src := &fakeSource{}
	src.set(grp(win("@1", "idle", false)))
	b := NewBus(src, Options{})
	_, sub := b.Subscribe()
	defer sub.Close()
	src.mu.Lock()
	src.err = os.ErrDeadlineExceeded
	src.groups = nil
	src.mu.Unlock()
	b.Refresh()
	b.Refresh()
	none(t, sub)
}

func TestBusSlowSubscriberResyncs(t *testing.T) {
	src := &fakeSource{}
	src.set(grp(win("@0", "idle", false)))
	b := NewBus(src, Options{})
	_, slow := b.Subscribe()
	defer slow.Close()
	_, fast := b.Subscribe()
	defer fast.Close()

	// Flood well past the buffer without the slow subscriber reading, while
	// the fast one keeps up. Publishing must never block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= subBuffer*2; i++ {
			st := "working"
			if i%2 == 0 {
				st = "idle"
			}
			src.set(grp(win("@0", st, false)))
			b.Refresh()
			drain(fast.c)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked on a slow subscriber")
	}

	var sawFleet bool
	for {
		select {
		case ev := <-slow.C:
			if ev.Kind == KindFleet {
				sawFleet = true
				if len(ev.Fleet) != 1 {
					t.Errorf("resync fleet = %+v", ev.Fleet)
				}
			}
			continue
		default:
		}
		break
	}
	if !sawFleet {
		t.Error("slow subscriber never got a full fleet resync")
	}
}

func TestBusRunRefreshesOnFileChange(t *testing.T) {
	rt := t.TempDir()
	winDir := filepath.Join(rt, "windows")
	if err := os.MkdirAll(winDir, 0o700); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{}
	g := grp(win("@1", "idle", false))
	g.RuntimeDir = rt
	src.set(g)
	b := NewBus(src, Options{Debounce: 10 * time.Millisecond, Rediscover: 20 * time.Millisecond})
	_, sub := b.Subscribe()
	defer sub.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	g2 := grp(win("@1", "blocked", false))
	g2.RuntimeDir = rt
	src.set(g2)
	if err := os.WriteFile(filepath.Join(winDir, "@1.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		ev := next(t, sub)
		if ev.Kind == KindTransition && ev.To == "blocked" {
			return
		}
	}
}

func TestActive(t *testing.T) {
	tests := []struct {
		name string
		ws   []fleet.Window
		want bool
	}{
		{"empty", nil, false},
		{"idle and done", []fleet.Window{win("@1", "idle", false), win("@2", "idle", true)}, false},
		{"working", []fleet.Window{win("@1", "working", false)}, true},
		{"blocked", []fleet.Window{win("@1", "idle", false), win("@2", "blocked", false)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Active([]Group{grp(tt.ws...)}); got != tt.want {
				t.Errorf("Active = %v", got)
			}
		})
	}
}
