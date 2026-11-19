package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// stubAutoPollRunner installs r as the browser's autopilot runner.
func stubAutoPollRunner(t *testing.T, r autoPollRunner) {
	t.Helper()
	prev := tkAutoPollRunner
	tkAutoPollRunner = r
	t.Cleanup(func() { tkAutoPollRunner = prev })
}

// fakeAutopilotConfig stands in for the global config: writes flip stored
// unless writeErr is set; resolve maps the stored value to what Load returns.
type fakeAutopilotConfig struct {
	stored   bool
	writes   []bool
	writeErr error
	resolve  func(stored bool) bool
}

func stubAutopilotConfig(t *testing.T, f *fakeAutopilotConfig) {
	t.Helper()
	prevLoad, prevWrite := tkLoadAutoPoll, tkWriteAutoPoll
	tkLoadAutoPoll = func() (bool, time.Duration) {
		if f.resolve != nil {
			return f.resolve(f.stored), 5 * time.Minute
		}
		return f.stored, 5 * time.Minute
	}
	tkWriteAutoPoll = func(want bool) error {
		f.writes = append(f.writes, want)
		if f.writeErr != nil {
			return f.writeErr
		}
		f.stored = want
		return nil
	}
	t.Cleanup(func() { tkLoadAutoPoll, tkWriteAutoPoll = prevLoad, prevWrite })
}

// autopilotOnModel is tkMouseModel with autopilot on and its loop running.
func autopilotOnModel(t *testing.T) TicketsModel {
	t.Helper()
	m := tkMouseModel(t, 130, 40)
	m.autopilot = tkAutopilot{on: true, interval: 5 * time.Minute}
	m.autopilot.startLoop(tkNow())
	return m
}

// autopilotMsgs keeps only the autopilot's own messages.
func autopilotMsgs(msgs []tea.Msg) []tea.Msg {
	var out []tea.Msg
	for _, msg := range msgs {
		switch msg.(type) {
		case autoPollListedMsg, autoPollDispatchedMsg:
			out = append(out, msg)
		}
	}
	return out
}

// runPollCycle fires the current loop's tick and feeds the cycle's messages
// back until it finishes.
func runPollCycle(t *testing.T, m TicketsModel) TicketsModel {
	t.Helper()
	next, cmd := m.Update(autoPollTickMsg{gen: m.autopilot.gen})
	m = next.(TicketsModel)
	queue := autopilotMsgs(runCmds(cmd))
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		next, cmd = m.Update(msg)
		m = next.(TicketsModel)
		queue = append(queue, autopilotMsgs(runCmds(cmd))...)
	}
	return m
}

func choiceContent(t *testing.T, m TicketsModel) tkChoiceModel {
	t.Helper()
	c, ok := m.choiceModal.Content.(tkChoiceModel)
	if !ok || !m.choiceModal.Visible {
		t.Fatalf("choice modal not open (visible=%v, %T)", m.choiceModal.Visible, m.choiceModal.Content)
	}
	return c
}

func TestAutopilotTurnOnConfirms(t *testing.T) {
	cfg := &fakeAutopilotConfig{}
	stubAutopilotConfig(t, cfg)
	m := tkMouseModel(t, 130, 40)
	m.allIssues[0].Labels = append(m.allIssues[0].Labels, ghLabel{Name: "ready"})

	m = tkSendKey(t, m, charKey('P'))
	c := choiceContent(t, m)
	if c.kind != "autopilot" || c.choices[c.focus].label != "Cancel" {
		t.Fatalf("want the autopilot confirmation focused on Cancel, got %+v", c)
	}
	body := ansi.Strip(m.View().Content)
	for _, want := range []string{"Global setting", "First poll in 5m", "#1", "would dispatch", `labelled "ready" (not "in-progress")`} {
		if !strings.Contains(body, want) {
			t.Errorf("confirmation should mention %q", want)
		}
	}
	if len(cfg.writes) != 0 {
		t.Fatal("opening the confirmation must not write the config")
	}

	// enter on the default (Cancel) does nothing.
	m = tkSendKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.choiceModal.Visible || m.autopilot.on || len(cfg.writes) != 0 {
		t.Fatalf("Cancel turned autopilot on (on=%v writes=%v)", m.autopilot.on, cfg.writes)
	}

	t.Run("y confirms", func(t *testing.T) {
		got := tkSendKey(t, tkSendKey(t, m, charKey('P')), charKey('y'))
		if !got.autopilot.on || !got.autopilot.ticker {
			t.Errorf("y should turn autopilot on with a running loop: %+v", got.autopilot)
		}
	})
	t.Run("Turn on button confirms", func(t *testing.T) {
		got := tkSendKey(t, m, charKey('P'))
		got, _ = tkClickZone(t, got, "mkey:y@choice")
		if !got.autopilot.on {
			t.Error("[ Turn on ] should turn autopilot on")
		}
	})
	t.Run("click outside cancels", func(t *testing.T) {
		before := len(cfg.writes)
		got := tkSendKey(t, m, charKey('P'))
		got, _ = tkPressRelease(t, got, 0, 0, 0, 0)
		if got.choiceModal.Visible || len(cfg.writes) != before {
			t.Error("outside click should cancel without writing")
		}
	})
	t.Run("pill click is P", func(t *testing.T) {
		got, _ := tkClickZone(t, m, "key:P@pill")
		if choiceContent(t, got).kind != "autopilot" {
			t.Error("pill click should open the same confirmation")
		}
	})
}

func TestAutopilotTurnOffIsInstant(t *testing.T) {
	cfg := &fakeAutopilotConfig{stored: true}
	stubAutopilotConfig(t, cfg)
	m := autopilotOnModel(t)

	m = tkSendKey(t, m, charKey('P'))
	if m.choiceModal.Visible {
		t.Fatal("turning off must not ask")
	}
	if m.autopilot.on || m.autopilot.ticker {
		t.Errorf("autopilot still on: %+v", m.autopilot)
	}
	if len(cfg.writes) != 1 || cfg.writes[0] {
		t.Errorf("writes = %v, want [false]", cfg.writes)
	}
	if m.flash != "Autopilot off" {
		t.Errorf("flash = %q, want the off toast", m.flash)
	}
}

func TestAutopilotToggleErrors(t *testing.T) {
	tests := []struct {
		name    string
		cfg     fakeAutopilotConfig
		start   bool
		key     []rune
		wantOn  bool
		wantMsg string
	}{
		{
			name:    "write error turning on",
			cfg:     fakeAutopilotConfig{writeErr: errors.New("permission denied")},
			key:     []rune{'P', 'y'},
			wantOn:  false,
			wantMsg: "permission denied",
		},
		{
			name:    "write error turning off",
			cfg:     fakeAutopilotConfig{stored: true, writeErr: errors.New("disk full")},
			start:   true,
			key:     []rune{'P'},
			wantOn:  true,
			wantMsg: "disk full",
		},
		{
			name:    "config resolves off after the write",
			cfg:     fakeAutopilotConfig{resolve: func(bool) bool { return false }},
			key:     []rune{'P', 'y'},
			wantOn:  false,
			wantMsg: "still off",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			stubAutopilotConfig(t, &cfg)
			m := tkMouseModel(t, 130, 40)
			if tt.start {
				m = autopilotOnModel(t)
			}
			for _, k := range tt.key {
				m = tkSendKey(t, m, charKey(k))
			}
			if m.autopilot.on != tt.wantOn {
				t.Errorf("on = %v, want %v (what the config resolves)", m.autopilot.on, tt.wantOn)
			}
			if !m.flashErr || !strings.Contains(m.flash, tt.wantMsg) {
				t.Errorf("flash = %q (err=%v), want an error mentioning %q", m.flash, m.flashErr, tt.wantMsg)
			}
			if !strings.Contains(ansi.Strip(m.renderPill(false)), tt.wantMsg) {
				t.Errorf("pill = %q, want it to show the error", ansi.Strip(m.renderPill(false)))
			}
		})
	}
}

func TestAutopilotTickLoopDoesNotDouble(t *testing.T) {
	cfg := &fakeAutopilotConfig{stored: true}
	stubAutopilotConfig(t, cfg)
	m := autopilotOnModel(t)
	oldGen := m.autopilot.gen

	// A second start while the loop runs is refused.
	if cmd := m.autopilot.startLoop(tkNow()); cmd != nil {
		t.Fatal("startLoop started a second loop while one was running")
	}

	// off → on quickly.
	m = tkSendKey(t, m, charKey('P'))
	m = tkSendKey(t, tkSendKey(t, m, charKey('P')), charKey('y'))
	if !m.autopilot.on || m.autopilot.gen == oldGen {
		t.Fatalf("want a fresh loop after off/on, got %+v", m.autopilot)
	}

	// The old loop's tick is dropped: no poll, no reschedule.
	next, cmd := m.Update(autoPollTickMsg{gen: oldGen})
	if cmd != nil || next.(TicketsModel).autopilot.phase != pollIdle {
		t.Error("a stale tick polled or rescheduled: two loops would run")
	}
	_, cmd = m.Update(autopilotClockMsg{gen: oldGen})
	if cmd != nil {
		t.Error("a stale countdown tick rescheduled")
	}

	// The current loop's tick polls and reschedules exactly once.
	next, cmd = m.Update(autoPollTickMsg{gen: m.autopilot.gen})
	if cmd == nil || next.(TicketsModel).autopilot.phase != pollListing {
		t.Error("the live tick should start a poll")
	}
}

func TestAutopilotStaleTickAfterOffDoesNotDispatch(t *testing.T) {
	cfg := &fakeAutopilotConfig{stored: true}
	stubAutopilotConfig(t, cfg)
	dispatched := 0
	stubAutoPollRunner(t, autoPollRunner{
		list: func(string) ([]byte, error) {
			return []byte(`[{"number":7,"title":"x","labels":[{"name":"ready"}]}]`), nil
		},
		dispatch: func(int, string) error { dispatched++; return nil },
	})
	m := autopilotOnModel(t)
	gen := m.autopilot.gen

	next, cmd := m.Update(autoPollTickMsg{gen: gen})
	m = next.(TicketsModel)
	listed := autopilotMsgs(runCmds(cmd))
	if len(listed) != 1 {
		t.Fatalf("want the list result, got %v", listed)
	}

	m = tkSendKey(t, m, charKey('P')) // off while the list call was in flight
	next, cmd = m.Update(listed[0])
	runCmds(cmd)
	if dispatched != 0 {
		t.Error("a poll that finished after OFF dispatched an issue")
	}
	_, cmd = next.(TicketsModel).Update(autoPollTickMsg{gen: gen})
	if cmd != nil {
		t.Error("a tick after OFF scheduled work")
	}
}

func TestAutopilotPill(t *testing.T) {
	m := tkMouseModel(t, 130, 40)
	m.allIssues[0].Labels = append(m.allIssues[0].Labels, ghLabel{Name: "ready"})
	off := ansi.Strip(m.renderPill(false))
	if !strings.Contains(off, "[ Autopilot ○ off ]") || !strings.Contains(off, "1 ready issue waiting") {
		t.Errorf("off pill = %q", off)
	}

	m.autopilot = tkAutopilot{on: true, interval: 5 * time.Minute, ticker: true, nextPoll: tkNow().Add(192 * time.Second)}
	on := ansi.Strip(m.renderPill(false))
	if !strings.Contains(on, "[ Autopilot ● ON ]") || !strings.Contains(on, "next 3m") {
		t.Errorf("on pill = %q", on)
	}
	m.autopilot.phase, m.autopilot.issue = pollDispatching, 171
	if got := ansi.Strip(m.renderPill(false)); !strings.Contains(got, "dispatching #171…") {
		t.Errorf("dispatching pill = %q", got)
	}
	m.autopilot.phase = pollIdle
	m.autopilot.lastPoll = time.Date(2026, 1, 1, 14, 2, 0, 0, time.Local)
	m.autopilot.status = "✓ dispatched #171"
	if got := ansi.Strip(m.renderPill(false)); !strings.Contains(got, "last 14:02 ✓ dispatched #171") {
		t.Errorf("after-poll pill = %q", got)
	}

	// The header keeps its height and the pill at narrow widths.
	n := tkMouseModel(t, 85, 30)
	n.autopilot = m.autopilot
	if !strings.Contains(ansi.Strip(n.View().Content), "Autopilot ● ON") {
		t.Error("pill missing at 85 cols")
	}
}

func TestCycleStatus(t *testing.T) {
	tests := []struct {
		done    []int
		fails   []string
		want    string
		wantErr bool
	}{
		{nil, nil, "✓ nothing ready", false},
		{[]int{171}, nil, "✓ dispatched #171", false},
		{nil, []string{"#3: boom"}, "✗ #3: boom", true},
		{[]int{1}, []string{"#3: boom"}, "✓ dispatched #1 · ✗ #3: boom", true},
	}
	for _, tt := range tests {
		got, isErr := cycleStatus(tt.done, tt.fails)
		if got != tt.want || isErr != tt.wantErr {
			t.Errorf("cycleStatus(%v, %v) = %q,%v; want %q,%v", tt.done, tt.fails, got, isErr, tt.want, tt.wantErr)
		}
	}
}

func TestFmtCountdown(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{42 * time.Second, "42s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m"},
		{4*time.Minute + 59*time.Second, "4m"},
		{24*time.Hour - time.Minute, "23h 59m"},
		{2 * time.Hour, "2h 0m"},
	}
	for _, tt := range tests {
		if got := fmtCountdown(tt.d); got != tt.want {
			t.Errorf("fmtCountdown(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestAutoPollLabelsIgnoreCase(t *testing.T) {
	if _, ok := autoPollCandidateFor(1, "x", []ghLabel{{Name: "Ready"}}); !ok {
		t.Error(`an issue labelled "Ready" should be picked up`)
	}
	if _, ok := autoPollCandidateFor(1, "x", []ghLabel{{Name: "ready"}, {Name: "In-Progress"}}); ok {
		t.Error(`"In-Progress" should be skipped`)
	}
}
