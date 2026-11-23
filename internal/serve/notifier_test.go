package serve

import (
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
)

type notifyEnv struct {
	n     *Notifier
	clock *fakeClock
	got   []Message
}

func newNotifyEnv(cfg config.ServePushConfig) *notifyEnv {
	e := &notifyEnv{clock: newClock()}
	e.n = NewNotifier(cfg, e.clock.Now, func(m Message) { e.got = append(e.got, m) })
	return e
}

func (e *notifyEnv) take() []Message {
	out := e.got
	e.got = nil
	return out
}

func win(state string, done bool) fleet.Window {
	return fleet.Window{Session: "agentctl-proj", Project: "proj", WindowID: "@5", Label: "go-engineer:3", Agent: "go-engineer:3",
		State: state, Done: done, Mode: "bare"}
}

func (e *notifyEnv) transition(w fleet.Window) {
	e.n.Handle(fleetwatch.Event{Kind: fleetwatch.KindTransition, Window: w})
}

func TestNotifierTriggers(t *testing.T) {
	blocked := win("blocked", false)
	blocked.PendingTool = "Bash"
	prDone := win("idle", true)
	prDone.PRNumber, prDone.PRURL = 61, "https://github.com/o/r/pull/61"

	tests := []struct {
		name      string
		cfg       config.ServePushConfig
		w         fleet.Window
		wantTitle string
		wantBody  string
		urgent    bool
	}{
		{"blocked", config.ServePushConfig{}, blocked, "go-engineer:3", "Needs permission · proj · Bash", true},
		{"done", config.ServePushConfig{}, win("idle", true), "go-engineer:3", "Finished · proj", false},
		{"done with PR", config.ServePushConfig{}, prDone, "go-engineer:3", "PR #61 ready · proj", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifyEnv(tt.cfg)
			e.transition(tt.w)
			got := e.take()
			if len(got) != 1 {
				t.Fatalf("messages = %+v, want 1", got)
			}
			m := got[0]
			if m.Title != tt.wantTitle || m.Body != tt.wantBody || m.Urgent != tt.urgent {
				t.Errorf("message = %+v", m)
			}
			if m.URL != "/#/w/agentctl-proj/%405" || m.Tag != "agentctl-proj:@5" {
				t.Errorf("url/tag = %q %q", m.URL, m.Tag)
			}
		})
	}

	t.Run("idle and working do not push", func(t *testing.T) {
		e := newNotifyEnv(config.ServePushConfig{})
		e.transition(win("working", false))
		e.transition(win("idle", false))
		if got := e.take(); len(got) != 0 {
			t.Errorf("messages = %+v", got)
		}
	})

	f := false
	t.Run("blocked disabled", func(t *testing.T) {
		e := newNotifyEnv(config.ServePushConfig{Blocked: &f})
		e.transition(blocked)
		e.clock.Advance(time.Hour)
		e.n.Tick()
		if got := e.take(); len(got) != 0 {
			t.Errorf("messages = %+v", got)
		}
	})
	t.Run("done disabled", func(t *testing.T) {
		e := newNotifyEnv(config.ServePushConfig{Done: &f})
		e.transition(win("idle", true))
		if got := e.take(); len(got) != 0 {
			t.Errorf("messages = %+v", got)
		}
	})
}

func TestNotifierPayloadHasNoPaneText(t *testing.T) {
	w := win("blocked", false)
	w.Activity = "SECRET pane text"
	w.Ticket = "SECRET ticket"
	e := newNotifyEnv(config.ServePushConfig{})
	e.transition(w)
	for _, m := range e.take() {
		if strings.Contains(string(m.payload()), "SECRET") {
			t.Errorf("payload leaks text: %s", m.payload())
		}
	}
}

func TestNotifierReminder(t *testing.T) {
	ten := 10
	e := newNotifyEnv(config.ServePushConfig{BlockedReminderMinutes: &ten})
	e.transition(win("blocked", false))
	if got := e.take(); len(got) != 1 {
		t.Fatalf("initial = %+v", got)
	}

	e.clock.Advance(9 * time.Minute)
	e.n.Tick()
	if got := e.take(); len(got) != 0 {
		t.Fatalf("reminder too early: %+v", got)
	}
	e.clock.Advance(time.Minute)
	e.n.Tick()
	got := e.take()
	if len(got) != 1 || got[0].Kind != "reminder" || !got[0].Urgent || !strings.HasPrefix(got[0].Body, "Still blocked 10 min") {
		t.Fatalf("reminder = %+v", got)
	}

	// Once per episode.
	e.clock.Advance(time.Hour)
	e.n.Tick()
	if got := e.take(); len(got) != 0 {
		t.Fatalf("second reminder: %+v", got)
	}

	// Unblock, then block again after the flap window: a new episode with a
	// fresh push and a fresh reminder.
	e.transition(win("working", false))
	e.clock.Advance(time.Minute)
	e.transition(win("blocked", false))
	if got := e.take(); len(got) != 1 || got[0].Kind != "blocked" {
		t.Fatalf("re-block = %+v", got)
	}
	e.clock.Advance(10 * time.Minute)
	e.n.Tick()
	if got := e.take(); len(got) != 1 || got[0].Kind != "reminder" {
		t.Fatalf("re-armed reminder = %+v", got)
	}
}

func TestNotifierReminderDisabledAndCancelled(t *testing.T) {
	zero := 0
	e := newNotifyEnv(config.ServePushConfig{BlockedReminderMinutes: &zero})
	e.transition(win("blocked", false))
	e.take()
	e.clock.Advance(time.Hour)
	e.n.Tick()
	if got := e.take(); len(got) != 0 {
		t.Errorf("reminder with 0 minutes: %+v", got)
	}

	// Answered before the reminder: no reminder.
	e = newNotifyEnv(config.ServePushConfig{})
	e.transition(win("blocked", false))
	e.take()
	e.clock.Advance(5 * time.Minute)
	e.transition(win("working", false))
	e.clock.Advance(time.Hour)
	e.n.Tick()
	if got := e.take(); len(got) != 0 {
		t.Errorf("reminder after unblock: %+v", got)
	}
}

func TestNotifierFlappingDoesNotSpam(t *testing.T) {
	e := newNotifyEnv(config.ServePushConfig{})
	for i := 0; i < 6; i++ {
		e.transition(win("blocked", false))
		e.clock.Advance(2 * time.Second)
		e.transition(win("working", false))
		e.clock.Advance(2 * time.Second)
	}
	if got := e.take(); len(got) != 1 {
		t.Errorf("flapping produced %d pushes, want 1", len(got))
	}

	// Repeated events for the same blocked state (rendered-field changes)
	// are not new episodes either.
	e = newNotifyEnv(config.ServePushConfig{})
	e.transition(win("blocked", false))
	for i := 0; i < 5; i++ {
		e.n.Handle(fleetwatch.Event{Kind: fleetwatch.KindWindow, Window: win("blocked", false)})
	}
	if got := e.take(); len(got) != 1 {
		t.Errorf("window events produced %d pushes, want 1", len(got))
	}
}

func TestNotifierDoneWaitsForPR(t *testing.T) {
	dispatched := func(pr int) fleet.Window {
		w := win("idle", true)
		w.Mode = "dispatch"
		if pr > 0 {
			w.PRNumber, w.PRURL = pr, "https://github.com/o/r/pull/9"
		}
		return w
	}

	t.Run("PR arrives within grace", func(t *testing.T) {
		e := newNotifyEnv(config.ServePushConfig{})
		e.transition(dispatched(0))
		if got := e.take(); len(got) != 0 {
			t.Fatalf("pushed before grace: %+v", got)
		}
		e.clock.Advance(5 * time.Second)
		e.n.Handle(fleetwatch.Event{Kind: fleetwatch.KindWindow, Window: dispatched(9)})
		got := e.take()
		if len(got) != 1 || got[0].Body != "PR #9 ready · proj" {
			t.Fatalf("got %+v", got)
		}
		e.clock.Advance(time.Minute)
		e.n.Tick()
		if got := e.take(); len(got) != 0 {
			t.Errorf("duplicate done push: %+v", got)
		}
	})

	t.Run("no PR: pushed when grace expires", func(t *testing.T) {
		e := newNotifyEnv(config.ServePushConfig{})
		e.transition(dispatched(0))
		e.clock.Advance(prGrace - time.Second)
		e.n.Tick()
		if got := e.take(); len(got) != 0 {
			t.Fatalf("early: %+v", got)
		}
		e.clock.Advance(time.Second)
		e.n.Tick()
		got := e.take()
		if len(got) != 1 || got[0].Body != "Finished · proj" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("resumed work cancels the pending push", func(t *testing.T) {
		e := newNotifyEnv(config.ServePushConfig{})
		e.transition(dispatched(0))
		w := win("working", false)
		w.Mode = "dispatch"
		e.transition(w)
		e.clock.Advance(time.Minute)
		e.n.Tick()
		if got := e.take(); len(got) != 0 {
			t.Errorf("got %+v", got)
		}
	})
}

func TestQuietHours(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 16, h, m, 0, 0, time.Local) }
	tests := []struct {
		spec string
		t    time.Time
		want bool
	}{
		{"", at(3, 0), false},
		{"garbage", at(3, 0), false},
		{"23:00-07:00", at(23, 0), true},
		{"23:00-07:00", at(23, 59), true},
		{"23:00-07:00", at(0, 30), true},
		{"23:00-07:00", at(6, 59), true},
		{"23:00-07:00", at(7, 0), false},
		{"23:00-07:00", at(12, 0), false},
		{"23:00-07:00", at(22, 59), false},
		{"09:00-17:00", at(9, 0), true},
		{"09:00-17:00", at(16, 59), true},
		{"09:00-17:00", at(17, 0), false},
		{"09:00-17:00", at(3, 0), false},
		{"10:00-10:00", at(10, 0), false},
	}
	for _, tt := range tests {
		if got := InQuietHours(tt.spec, tt.t); got != tt.want {
			t.Errorf("InQuietHours(%q, %s) = %v, want %v", tt.spec, tt.t.Format("15:04"), got, tt.want)
		}
	}
}

func TestNotifierQuietHoursAndMute(t *testing.T) {
	t.Run("quiet hours suppress", func(t *testing.T) {
		e := newNotifyEnv(config.ServePushConfig{QuietHours: "11:00-13:00"}) // clock is 12:00 UTC
		e.clock.t = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
		e.transition(win("blocked", false))
		e.transition(win("idle", true))
		if got := e.take(); len(got) != 0 {
			t.Errorf("pushed in quiet hours: %+v", got)
		}
	})
	t.Run("quiet hours crossing midnight", func(t *testing.T) {
		e := newNotifyEnv(config.ServePushConfig{QuietHours: "23:00-07:00"})
		e.clock.t = time.Date(2026, 11, 16, 2, 0, 0, 0, time.UTC)
		e.transition(win("blocked", false))
		if got := e.take(); len(got) != 0 {
			t.Errorf("pushed at 02:00: %+v", got)
		}
		e.clock.t = time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
		e.transition(win("working", false))
		e.clock.Advance(time.Minute)
		e.transition(win("blocked", false))
		if got := e.take(); len(got) != 1 {
			t.Errorf("not pushed at 08:00: %+v", got)
		}
	})
	t.Run("muted project", func(t *testing.T) {
		e := newNotifyEnv(config.ServePushConfig{MutedProjects: []string{"proj"}})
		e.transition(win("blocked", false))
		e.transition(win("idle", true))
		e.clock.Advance(time.Hour)
		e.n.Tick()
		if got := e.take(); len(got) != 0 {
			t.Errorf("pushed for muted project: %+v", got)
		}
		w := win("blocked", false)
		w.Project, w.WindowID = "other", "@6"
		e.transition(w)
		if got := e.take(); len(got) != 1 {
			t.Errorf("other project muted too: %+v", got)
		}
	})
}

func TestNotifierSeedAndGone(t *testing.T) {
	e := newNotifyEnv(config.ServePushConfig{})
	g := fleetwatch.Group{Session: "agentctl-proj", Windows: []fleet.Window{win("blocked", false)}}
	e.n.Seed([]fleetwatch.Group{g})
	e.clock.Advance(time.Hour)
	e.n.Tick()
	// A KindWindow for the already-blocked window is not a transition.
	e.n.Handle(fleetwatch.Event{Kind: fleetwatch.KindWindow, Window: win("blocked", false)})
	if got := e.take(); len(got) != 0 {
		t.Fatalf("startup announced existing blocked window: %+v", got)
	}

	// Dropped events: the full fleet reveals a new transition.
	g.Windows = []fleet.Window{win("idle", true)}
	e.n.Handle(fleetwatch.Event{Kind: fleetwatch.KindFleet, Fleet: []fleetwatch.Group{g}})
	if got := e.take(); len(got) != 1 || got[0].Kind != "done" {
		t.Fatalf("fleet resync = %+v", got)
	}

	e.n.Handle(fleetwatch.Event{Kind: fleetwatch.KindGone, Session: "agentctl-proj", WindowID: "@5"})
	if len(e.n.episodes) != 0 {
		t.Errorf("episodes not cleared: %v", e.n.episodes)
	}
}

func TestPushContent(t *testing.T) {
	titled := func(w fleet.Window) fleet.Window {
		w.Title, w.Agent, w.Label = "Fix hub viewer jump", "go-engineer:3", "go-engineer:3"
		return w
	}
	blocked := func(tool, detail string) fleet.Window {
		w := win("blocked", false)
		w.PendingTool, w.PendingToolDetail = tool, detail
		return w
	}
	done := func(recap, source string, pr int) fleet.Window {
		w := win("idle", true)
		w.Recap, w.RecapSource, w.PRNumber = recap, source, pr
		return w
	}
	long := strings.Repeat("lorem ipsum dolor ", 20)

	tests := []struct {
		name      string
		w         fleet.Window
		wantTitle string
		wantBody  string
	}{
		{"blocked, titled: title leads, agent and project in the body", titled(blocked("Bash", "go test ./...")),
			"Fix hub viewer jump", "Needs permission · go-engineer · proj · Bash: go test ./..."},
		{"blocked, no title yet: display name leads, agent not repeated", blocked("Bash", "ls"),
			"go-engineer:3", "Needs permission · proj · Bash: ls"},
		{"blocked, tool without detail", titled(blocked("Edit", "")),
			"Fix hub viewer jump", "Needs permission · go-engineer · proj · Edit"},
		{"blocked, no tool recorded", titled(blocked("", "")),
			"Fix hub viewer jump", "Needs permission · go-engineer · proj"},
		{"blocked, multi-line command is one line", titled(blocked("Bash", "git commit -m 'a\n\nb'")),
			"Fix hub viewer jump", "Needs permission · go-engineer · proj · Bash: git commit -m 'a b'"},
		{"done with PR", titled(done("", "", 273)),
			"Fix hub viewer jump", "PR #273 ready · go-engineer · proj"},
		{"done with a summary recap", titled(done("Fixed the viewer jump by clamping the offset. More text.", "summary", 0)),
			"Fix hub viewer jump", "Finished · go-engineer · proj · Fixed the viewer jump by clamping the offset"},
		{"done, task recap is not repeated", titled(done("Fix hub viewer jump", "task", 0)),
			"Fix hub viewer jump", "Finished · go-engineer · proj"},
		{"done, bare window without anything", done("", "", 0),
			"go-engineer:3", "Finished · proj"},
		{"no label, no agent: window id", fleet.Window{WindowID: "@9", State: "blocked", Project: "proj"},
			"@9", "Needs permission · proj"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifyEnv(config.ServePushConfig{})
			e.transition(tt.w)
			got := e.take()
			if len(got) != 1 {
				t.Fatalf("messages = %+v, want 1", got)
			}
			if got[0].Title != tt.wantTitle || got[0].Body != tt.wantBody {
				t.Errorf("title/body = %q / %q, want %q / %q", got[0].Title, got[0].Body, tt.wantTitle, tt.wantBody)
			}
			if strings.Contains(got[0].Title, "agentctl:") || got[0].Body == "" {
				t.Errorf("message = %+v", got[0])
			}
		})
	}

	t.Run("a long command is cut at a word within about 120 runes", func(t *testing.T) {
		e := newNotifyEnv(config.ServePushConfig{})
		e.transition(titled(blocked("Bash", long)))
		b := e.take()[0].Body
		if n := len([]rune(b)); n > 125 || !strings.HasSuffix(b, "…") || strings.Contains(b, "\n") {
			t.Errorf("body = %q (%d runes)", b, n)
		}
	})

	t.Run("the reminder keeps the title and names the tool", func(t *testing.T) {
		e := newNotifyEnv(config.ServePushConfig{})
		e.transition(titled(blocked("Bash", "go test ./...")))
		e.take()
		e.clock.Advance(10 * time.Minute)
		e.n.Tick()
		got := e.take()
		if len(got) != 1 || got[0].Title != "Fix hub viewer jump" ||
			got[0].Body != "Still blocked 10 min · go-engineer · proj · Bash: go test ./..." {
			t.Errorf("reminder = %+v", got)
		}
	})
}
