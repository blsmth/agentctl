package serve

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
	"github.com/blsmth/agentctl/internal/tmux"
)

// fakeLaptop is a LaptopIdleFunc whose answer tests set.
type fakeLaptop struct {
	mu    sync.Mutex
	idle  time.Duration
	err   error
	calls int
}

func (f *fakeLaptop) probe(context.Context) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.idle, f.err
}

func (f *fakeLaptop) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeLaptop) setActive(active bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = nil
	f.idle = time.Second
	if !active {
		f.idle = time.Hour
	}
}

func TestParseHIDIdle(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    time.Duration
		wantErr bool
	}{
		{"ioreg line", "+-o IOHIDSystem  <class IOHIDSystem>\n  |   \"HIDIdleTime\" = 249638250\n", 249638250, false},
		{"nested tree", "    | | |   \"HIDIdleTime\" = 120000000000\n", 2 * time.Minute, false},
		{"first valid wins", "\"HIDIdleTime\" = junk\n\"HIDIdleTime\" = 5\n\"HIDIdleTime\" = 9\n", 5, false},
		{"missing", "+-o IOHIDSystem\n  |   \"Other\" = 1\n", 0, true},
		{"empty", "", 0, true},
		{"negative", "\"HIDIdleTime\" = -1\n", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHIDIdle(tt.out)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("parseHIDIdle = %v, %v; want %v, err %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestTmuxIdle(t *testing.T) {
	now := time.Unix(10_000, 0)
	laptop := func(session string, act int64) tmux.Client {
		return tmux.Client{Name: "/dev/ttys001", Session: session, Activity: act}
	}
	tests := []struct {
		name    string
		clients []tmux.Client
		want    time.Duration
		wantErr bool
	}{
		{"no tmux clients", nil, 0, true},
		{"project session", []tmux.Client{laptop("agentctl-proj", 9_970)}, 30 * time.Second, false},
		{"hub and linked viewer sessions", []tmux.Client{laptop("agentctl-hub", 9_000), laptop("agentctl-proj-viewer", 9_990)}, 10 * time.Second, false},
		{"most recent of several sessions", []tmux.Client{laptop("agentctl-a", 9_000), laptop("agentctl-b", 9_995), laptop("agentctl-c", 9_500)}, 5 * time.Second, false},
		{"phone viewer is not the laptop", []tmux.Client{laptop(tmux.MobileViewerSessionPrefix+"abc", 9_999)}, 0, true},
		{"control mode is not the laptop", []tmux.Client{{Session: "agentctl-proj", Activity: 9_999, Flags: "attached,control-mode"}}, 0, true},
		{"non-agentctl session ignored", []tmux.Client{laptop("work", 9_999)}, 0, true},
		{"zero activity ignored", []tmux.Client{laptop("agentctl-proj", 0)}, 0, true},
		{"clock skew is zero idle", []tmux.Client{laptop("agentctl-proj", 10_005)}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tmuxIdle(tt.clients, now)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("tmuxIdle = %v, %v; want %v, err %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestSystemIdleProbe(t *testing.T) {
	now := time.Unix(10_000, 0)
	ioregOK := func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`  |   "HIDIdleTime" = 3000000000` + "\n"), nil
	}
	ioregMissing := func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New(`exec: "ioreg": executable file not found in $PATH`)
	}
	tmuxActive := func(time.Duration) ([]tmux.Client, error) {
		return []tmux.Client{{Session: "agentctl-proj", Activity: 9_990}}, nil
	}
	noServer := func(time.Duration) ([]tmux.Client, error) {
		return nil, errors.New("no server running on /tmp/tmux-501/default")
	}
	tests := []struct {
		name      string
		goos      string
		ioreg     func(context.Context, string, ...string) ([]byte, error)
		clients   func(time.Duration) ([]tmux.Client, error)
		want      time.Duration
		wantErr   bool
		wantIoreg bool
		wantTmux  bool
	}{
		{"darwin uses HID idle only", "darwin", ioregOK, tmuxActive, 3 * time.Second, false, true, false},
		{"darwin without ioreg falls back to tmux", "darwin", ioregMissing, tmuxActive, 10 * time.Second, false, true, true},
		{"linux never runs ioreg", "linux", ioregOK, tmuxActive, 10 * time.Second, false, false, true},
		{"linux with no tmux server", "linux", ioregOK, noServer, 0, true, false, true},
		{"headless darwin, nothing works", "darwin", ioregMissing, noServer, 0, true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ranIoreg, ranTmux bool
			run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				ranIoreg = true
				if name != "ioreg" {
					t.Errorf("ran %q", name)
				}
				return tt.ioreg(ctx, name, args...)
			}
			list := func(d time.Duration) ([]tmux.Client, error) {
				ranTmux = true
				return tt.clients(d)
			}
			probe := newSystemIdleProbe(tt.goos, run, list, func() time.Time { return now })
			got, err := probe(context.Background())
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("probe = %v, %v; want %v, err %v", got, err, tt.want, tt.wantErr)
			}
			if ranIoreg != tt.wantIoreg || ranTmux != tt.wantTmux {
				t.Errorf("ran ioreg=%v tmux=%v, want %v %v", ranIoreg, ranTmux, tt.wantIoreg, tt.wantTmux)
			}
		})
	}
}

func TestLaptopActivity(t *testing.T) {
	t.Run("threshold", func(t *testing.T) {
		for _, tt := range []struct {
			idle time.Duration
			err  error
			want bool
		}{
			{0, nil, true},
			{119 * time.Second, nil, true},
			{120 * time.Second, nil, false},
			{time.Hour, nil, false},
			{0, errNoLaptopSignal, false},
		} {
			f := &fakeLaptop{idle: tt.idle, err: tt.err}
			if got := newLaptopActivity(f.probe, 2*time.Minute, newClock().Now).Active(); got != tt.want {
				t.Errorf("idle=%v err=%v: Active = %v, want %v", tt.idle, tt.err, got, tt.want)
			}
		}
	})
	t.Run("cached for a few seconds", func(t *testing.T) {
		clock := newClock()
		f := &fakeLaptop{}
		f.setActive(true)
		l := newLaptopActivity(f.probe, 2*time.Minute, clock.Now)
		for range 50 {
			l.Active()
		}
		if f.calls != 1 {
			t.Fatalf("probe calls = %d, want 1", f.calls)
		}
		f.setActive(false)
		clock.Advance(laptopCacheTTL - time.Millisecond)
		if !l.Active() {
			t.Error("cache not used inside TTL")
		}
		clock.Advance(time.Millisecond)
		if l.Active() || f.calls != 2 {
			t.Errorf("after TTL: active=%v calls=%d", l.Active(), f.calls)
		}
	})
	t.Run("nil is never active", func(t *testing.T) {
		var l *laptopActivity
		if l.Active() {
			t.Error("nil laptopActivity active")
		}
	})
}

// laptopNotifyEnv is a notifier env with a fake laptop wired in.
type laptopNotifyEnv struct {
	*notifyEnv
	laptop  *fakeLaptop
	act     *laptopActivity
	pending map[string]bool
}

func newLaptopNotifyEnv(cfg config.ServePushConfig, active bool) *laptopNotifyEnv {
	e := &laptopNotifyEnv{notifyEnv: newNotifyEnv(cfg), laptop: &fakeLaptop{}, pending: map[string]bool{}}
	e.laptop.setActive(active)
	e.act = newLaptopActivity(e.laptop.probe, 2*time.Minute, e.clock.Now)
	e.act.Active() // prime the cache, as a running server has one
	e.n.SetLaptop(e.act, func(id string) bool { return e.pending[id] })
	return e
}

// setActive changes the laptop state and moves past the probe cache.
func (e *laptopNotifyEnv) setActive(active bool) {
	e.act.wait()
	e.laptop.setActive(active)
	e.clock.Advance(laptopCacheTTL)
	e.act.Active() // Notifier only reads the cache; refresh it here
}

func approvalMsg(id string) Message {
	return Message{Title: "Approval needed", Tag: "approval:" + id, Kind: "approval", Urgent: true, TTL: 540}
}

func TestNotifierLaptopDecision(t *testing.T) {
	blocked := win("blocked", false)
	blocked.PendingTool = "Bash"
	tests := []struct {
		name   string
		active bool
		quiet  bool // feature on
		fire   func(e *laptopNotifyEnv)
		want   int
	}{
		{"blocked, laptop active", true, true, func(e *laptopNotifyEnv) { e.transition(blocked) }, 0},
		{"blocked, laptop idle", false, true, func(e *laptopNotifyEnv) { e.transition(blocked) }, 1},
		{"done, laptop active", true, true, func(e *laptopNotifyEnv) { e.transition(win("idle", true)) }, 0},
		{"done, laptop idle", false, true, func(e *laptopNotifyEnv) { e.transition(win("idle", true)) }, 1},
		{"artifact, laptop active", true, true, func(e *laptopNotifyEnv) { e.artifact() }, 0},
		{"artifact, laptop idle", false, true, func(e *laptopNotifyEnv) { e.artifact() }, 1},
		{"approval, laptop active", true, true, func(e *laptopNotifyEnv) { e.n.Approval("a1", approvalMsg("a1"), e.clock.Now().Add(time.Minute)) }, 0},
		{"approval, laptop idle", false, true, func(e *laptopNotifyEnv) { e.n.Approval("a1", approvalMsg("a1"), e.clock.Now().Add(time.Minute)) }, 1},
		{"feature off, laptop active", true, false, func(e *laptopNotifyEnv) { e.transition(blocked) }, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e *laptopNotifyEnv
			if tt.quiet {
				e = newLaptopNotifyEnv(config.ServePushConfig{}, tt.active)
			} else {
				e = &laptopNotifyEnv{notifyEnv: newNotifyEnv(config.ServePushConfig{})}
			}
			tt.fire(e)
			if got := e.take(); len(got) != tt.want {
				t.Errorf("messages = %+v, want %d", got, tt.want)
			}
		})
	}
}

func (e *laptopNotifyEnv) artifact() {
	e.n.Handle(fleetwatch.Event{Kind: fleetwatch.KindArtifact, Artifact: fleetwatch.ArtifactChange{
		Action: fleetwatch.ArtifactAdd, ID: "abcdefghijklmnopqrstuvwxyz", Title: "Report", Project: "proj",
	}})
}

func TestNotifierLaptopIdleBlockedPushOnce(t *testing.T) {
	logs := capturePushLog(t)
	e := newLaptopNotifyEnv(config.ServePushConfig{}, true)
	stuck := win("blocked", false)
	stuck.PendingTool = "Bash"
	other := win("blocked", false)
	other.WindowID = "@6"
	finished := win("idle", true)
	finished.WindowID = "@7"

	// While at the laptop: @5 blocks and stays blocked, @6 blocks and is
	// answered, @7 finishes, and the reminder comes due for @5.
	e.transition(stuck)
	e.transition(other)
	other.State = "working"
	e.transition(other)
	e.transition(finished)
	e.clock.Advance(11 * time.Minute)
	e.n.Tick()
	if got := e.take(); len(got) != 0 {
		t.Fatalf("pushed while laptop active: %+v", got)
	}
	if !strings.Contains(logs(), "kind=blocked tag=agentctl-proj:@5 suppressed, laptop active") {
		t.Errorf("suppression not logged:\n%s", logs())
	}

	e.setActive(false)
	e.n.Tick()
	got := e.take()
	if len(got) != 1 || got[0].Tag != "agentctl-proj:@5" || got[0].Kind != "blocked" || !got[0].Urgent {
		t.Fatalf("on idle got %+v, want one blocked push for @5", got)
	}
	if !strings.HasPrefix(got[0].Body, "Still blocked 11 min") {
		t.Errorf("body = %q", got[0].Body)
	}
	for range 5 {
		e.clock.Advance(time.Minute)
		e.n.Tick()
	}
	if got := e.take(); len(got) != 0 {
		t.Errorf("repeat pushes after idle: %+v", got)
	}

	// Back at the laptop and away again: nothing new is owed.
	e.setActive(true)
	e.n.Tick()
	e.setActive(false)
	e.n.Tick()
	if got := e.take(); len(got) != 0 {
		t.Errorf("pushed again on second idle: %+v", got)
	}
}

func TestNotifierLaptopIdleUnblockedBeforeIdle(t *testing.T) {
	e := newLaptopNotifyEnv(config.ServePushConfig{}, true)
	w := win("blocked", false)
	e.transition(w)
	w.State = "working"
	e.transition(w)
	e.setActive(false)
	e.n.Tick()
	if got := e.take(); len(got) != 0 {
		t.Errorf("pushed for a window no longer blocked: %+v", got)
	}
}

func TestNotifierLaptopIdleRespectsQuietHours(t *testing.T) {
	e := newLaptopNotifyEnv(config.ServePushConfig{}, true)
	e.transition(win("blocked", false))
	e.n.cfg.QuietHours = "00:00-23:59"
	e.setActive(false)
	e.n.Tick()
	if got := e.take(); len(got) != 0 {
		t.Errorf("held blocked push sent in quiet hours: %+v", got)
	}
}

func TestNotifierLaptopHeldApproval(t *testing.T) {
	tests := []struct {
		name     string
		resolve  bool          // answered (on the laptop) before idle
		idleAt   time.Duration // when the laptop goes idle after the request
		deadline time.Duration
		want     int
	}{
		{"still pending at idle", false, time.Minute, 9 * time.Minute, 1},
		{"answered on the laptop", true, time.Minute, 9 * time.Minute, 0},
		{"expired before idle", false, 10 * time.Minute, 9 * time.Minute, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newLaptopNotifyEnv(config.ServePushConfig{}, true)
			e.pending["a1"] = true
			deadline := e.clock.Now().Add(tt.deadline)
			e.n.Approval("a1", approvalMsg("a1"), deadline)
			e.n.Tick()
			if got := e.take(); len(got) != 0 {
				t.Fatalf("approval pushed while laptop active: %+v", got)
			}
			if tt.resolve {
				e.pending["a1"] = false
			}
			e.clock.Advance(tt.idleAt - laptopCacheTTL)
			e.setActive(false)
			for range 3 {
				e.n.Tick()
				e.clock.Advance(time.Second)
			}
			got := e.take()
			if len(got) != tt.want {
				t.Fatalf("messages = %+v, want %d", got, tt.want)
			}
			if tt.want == 1 {
				wantTTL := int(deadline.Sub(e.clock.Now().Add(-3 * time.Second)).Seconds())
				if got[0].Tag != "approval:a1" || got[0].TTL != wantTTL {
					t.Errorf("held approval = %+v, want TTL %d", got[0], wantTTL)
				}
			}
		})
	}
}

func TestNotifierLaptopProbesOnlyWhenHeld(t *testing.T) {
	e := newLaptopNotifyEnv(config.ServePushConfig{}, false)
	base := e.laptop.callCount()
	for range 60 {
		e.clock.Advance(time.Second)
		e.n.Tick()
	}
	e.act.wait()
	if got := e.laptop.callCount() - base; got != 0 {
		t.Errorf("idle ticks probed the laptop %d times", got)
	}
	// Many events inside one cache window: one probe.
	for i := range 20 {
		w := win("idle", true)
		w.WindowID = "@" + string(rune('a'+i))
		e.transition(w)
	}
	e.act.wait()
	if got := e.laptop.callCount() - base; got != 1 {
		t.Errorf("20 events probed %d times, want 1", got)
	}
}

// blockingProbe blocks until release is closed.
type blockingProbe struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingProbe) probe(ctx context.Context) (time.Duration, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return time.Second, nil
}

func TestNotifierNeverProbesUnderLock(t *testing.T) {
	b := &blockingProbe{started: make(chan struct{}), release: make(chan struct{})}
	clock := newClock()
	act := newLaptopActivity(b.probe, 2*time.Minute, clock.Now)
	t.Cleanup(func() { close(b.release); act.wait() })
	e := newNotifyEnv(config.ServePushConfig{})
	e.n.SetLaptop(act, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		blocked := win("blocked", false)
		e.transition(blocked) // Handle: first call, no value yet counts as idle
		e.n.Approval("a1", approvalMsg("a1"), e.clock.Now().Add(time.Minute))
		e.n.Tick()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Notifier calls blocked on a stuck laptop probe")
	}
	<-b.started
	if got := e.take(); len(got) != 2 {
		t.Errorf("messages = %+v, want blocked and approval pushed (no value yet = idle)", got)
	}
}

func TestNotifierIdleFlushSuppressesReminder(t *testing.T) {
	e := newLaptopNotifyEnv(config.ServePushConfig{}, true)
	blocked := win("blocked", false)
	e.transition(blocked)
	e.clock.Advance(2 * time.Minute)
	e.setActive(false)
	e.n.Tick()
	if got := e.take(); len(got) != 1 || got[0].Kind != "blocked" {
		t.Fatalf("on idle got %+v, want one blocked push", got)
	}
	// Past the 10 minute mark the normal reminder must not add a second push.
	for range 12 {
		e.clock.Advance(time.Minute)
		e.n.Tick()
	}
	if got := e.take(); len(got) != 0 {
		t.Errorf("reminder after flushed push: %+v", got)
	}
}

func TestBroadcastSkipsAllDevicesWhileLaptopActive(t *testing.T) {
	logs := capturePushLog(t)
	ts := newTestServer(t, config.ServeConfig{})
	phone, _ := ts.pairDevice(t, "phone")
	tablet, _ := ts.pairDevice(t, "tablet")
	ts.subs.Set(phone.ID, testSub("phone"), ts.clock.Now())   //nolint:errcheck
	ts.subs.Set(tablet.ID, testSub("tablet"), ts.clock.Now()) //nolint:errcheck
	fp := &fakePusher{}
	s := NewSender(ts.devices, ts.subs, fp)
	f := &fakeLaptop{}
	f.setActive(true)
	s.SetLaptop(newLaptopActivity(f.probe, 2*time.Minute, ts.clock.Now))

	s.Broadcast(context.Background(), Message{Kind: "done", Tag: "agentctl-proj:@5"})
	if len(fp.calls) != 0 {
		t.Errorf("pushed %d while laptop active", len(fp.calls))
	}
	if lp := s.LastPush(); lp == nil || lp.Result != "sent to 0, laptop active" {
		t.Errorf("last push = %+v", lp)
	}
	if !strings.Contains(logs(), "push: kind=done tag=agentctl-proj:@5 suppressed, laptop active") {
		t.Errorf("log:\n%s", logs())
	}

	// The test push is never suppressed.
	if err := s.SendTo(context.Background(), phone.ID, Message{Kind: "test"}); err != nil || len(fp.calls) != 1 {
		t.Errorf("SendTo: err=%v calls=%d", err, len(fp.calls))
	}

	fp.calls = nil
	f.setActive(false)
	ts.clock.Advance(laptopCacheTTL)
	s.Broadcast(context.Background(), Message{Kind: "done"})
	if len(fp.calls) != 2 {
		t.Errorf("calls after idle = %d, want 2", len(fp.calls))
	}
}

func TestQuietWhenLaptopActiveWiring(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	if ts.sender.laptop == nil || ts.notifier.laptop == nil {
		t.Fatal("laptop quiet not wired by default")
	}
	if ts.sender.laptop != ts.notifier.laptop {
		t.Error("sender and notifier use different laptop caches")
	}
	if ts.sender.laptop.threshold != 2*time.Minute {
		t.Errorf("threshold = %v", ts.sender.laptop.threshold)
	}
	if ts.sender.laptop.Active() {
		t.Error("test probe reported the laptop active")
	}
	ts = newTestServer(t, config.ServeConfig{Push: config.ServePushConfig{QuietWhenLaptopActive: new(false)}})
	if ts.sender.laptop != nil || ts.notifier.laptop != nil {
		t.Error("laptop quiet wired despite quiet_when_laptop_active=false")
	}
	ts = newTestServer(t, config.ServeConfig{Push: config.ServePushConfig{LaptopIdleSeconds: new(30)}})
	if ts.sender.laptop.threshold != 30*time.Second {
		t.Errorf("threshold = %v, want 30s", ts.sender.laptop.threshold)
	}
}
