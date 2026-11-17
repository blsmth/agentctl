package serve

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
)

type fakeHolder struct {
	mu            sync.Mutex
	starts, stops int
	failStart     bool
}

func (h *fakeHolder) Start() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failStart {
		return context.DeadlineExceeded
	}
	h.starts++
	return nil
}

func (h *fakeHolder) Stop() {
	h.mu.Lock()
	h.stops++
	h.mu.Unlock()
}

func (h *fakeHolder) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.starts, h.stops
}

func TestKeepAwakeSet(t *testing.T) {
	h := &fakeHolder{}
	k := &keepAwake{holder: h}
	steps := []struct {
		active        bool
		starts, stops int
	}{
		{false, 0, 0},
		{true, 1, 0},
		{true, 1, 0}, // idempotent
		{false, 1, 1},
		{false, 1, 1},
		{true, 2, 1},
	}
	for i, s := range steps {
		k.set(s.active)
		if a, b := h.counts(); a != s.starts || b != s.stops {
			t.Errorf("step %d: starts/stops = %d/%d, want %d/%d", i, a, b, s.starts, s.stops)
		}
	}
}

func TestKeepAwakeStartFailureRetries(t *testing.T) {
	h := &fakeHolder{failStart: true}
	k := &keepAwake{holder: h}
	k.set(true)
	if k.held {
		t.Fatal("held despite failed start")
	}
	h.mu.Lock()
	h.failStart = false
	h.mu.Unlock()
	k.set(true)
	if s, _ := h.counts(); s != 1 || !k.held {
		t.Errorf("starts = %d held = %v", s, k.held)
	}
}

func TestRunKeepAwakeFollowsFleet(t *testing.T) {
	win := func(st string, done bool) fleetwatch.Group {
		return fleetwatch.Group{Session: "agentctl-p", Windows: []herd.Window{
			{Session: "agentctl-p", WindowID: "@1", State: st, Done: done},
		}}
	}
	src := &fakeFleet{}
	src.set(win("idle", false))
	bus := fleetwatch.NewBus(src, fleetwatch.Options{})
	h := &fakeHolder{}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { runKeepAwake(ctx, bus, h); close(finished) }()

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out: %s (starts/stops %v)", what, func() [2]int { a, b := h.counts(); return [2]int{a, b} }())
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	started := func(n int) func() bool { return func() bool { s, _ := h.counts(); return s == n } }
	stopped := func(n int) func() bool { return func() bool { _, s := h.counts(); return s == n } }

	time.Sleep(20 * time.Millisecond)
	if s, _ := h.counts(); s != 0 {
		t.Fatal("started for an idle fleet")
	}
	src.set(win("working", false))
	bus.Refresh()
	waitFor("start on working", started(1))

	src.set(win("blocked", false))
	bus.Refresh()
	time.Sleep(20 * time.Millisecond)
	if s, p := h.counts(); s != 1 || p != 0 {
		t.Errorf("working->blocked restarted: %d/%d", s, p)
	}

	src.set(win("idle", true)) // done counts as idle
	bus.Refresh()
	waitFor("stop when all idle/done", stopped(1))

	src.set(win("working", false))
	bus.Refresh()
	waitFor("restart", started(2))

	cancel() // shutdown releases it
	<-finished
	waitFor("stop on shutdown", stopped(2))
}

func TestServerCloseReleasesKeepAwake(t *testing.T) {
	e := newReadEnv(t)
	h := e.holder.(*fakeHolder)
	e.fleet.set(fleetwatch.Group{Session: "agentctl-p", Windows: []herd.Window{{Session: "agentctl-p", WindowID: "@1", State: "working"}}})
	e.Start(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for {
		if s, _ := h.counts(); s == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never started")
		}
		time.Sleep(2 * time.Millisecond)
	}
	e.Close() // what Run does before selfexec re-exec
	if _, p := h.counts(); p == 0 {
		t.Error("Close did not release keep-awake")
	}
	e.Close() // idempotent
}
