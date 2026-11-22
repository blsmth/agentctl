package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/race"
)

var raceT0 = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

func raceFixture(t *testing.T) *race.Race {
	t.Helper()
	f, err := os.Open("../race/testdata/demo.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	es, err := logstream.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return race.Build(es, race.DefaultParams())
}

// raceAt returns a model sized w x h, ticked to wall offset d in frames.
func raceAt(t *testing.T, r *race.Race, w, h int, d time.Duration) RaceModel {
	t.Helper()
	factor := race.Factor(r.AgentTotal, 30*time.Second, 0)
	var m tea.Model = NewRaceModel(r, "demo", factor, raceT0)
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	for at := raceFrame; at <= d; at += raceFrame {
		m, _ = m.Update(raceTickMsg(raceT0.Add(at)))
	}
	return m.(RaceModel)
}

func plainView(m RaceModel) []string {
	return strings.Split(ansi.Strip(m.View().Content), "\n")
}

func TestRaceViewWidths(t *testing.T) {
	r := raceFixture(t)
	tests := []struct {
		name string
		w, h int
		at   time.Duration
		want []string
	}{
		{"wide", 120, 35, 10 * time.Second, []string{"AGENT", "SPEEDUP", "HUMAN @ 60 WPM", "typing", "lower bound: never pauses, never typos", "space pause"}},
		{"wide typing", 120, 35, 1500 * time.Millisecond, []string{"⌨ typing", "▌"}},
		{"narrow", 80, 30, 10 * time.Second, []string{"AGENT 0:", "HUMAN 0:", "faster", "lower bound"}},
		{"tiny", 30, 8, 10 * time.Second, []string{"AGENT", "HUMAN", "enlarge"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := raceAt(t, r, tt.w, tt.h, tt.at)
			lines := plainView(m)
			if len(lines) > tt.h {
				t.Errorf("view is %d rows, terminal has %d", len(lines), tt.h)
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > tt.w {
					t.Errorf("row %d is %d cells wide (> %d): %q", i, w, tt.w, l)
				}
			}
			all := strings.Join(lines, "\n")
			for _, s := range tt.want {
				if !strings.Contains(all, s) {
					t.Errorf("view lacks %q:\n%s", s, all)
				}
			}
			if testing.Verbose() {
				t.Logf("\n%s", all)
			}
		})
	}
}

func TestRaceEndCardAfterBeat(t *testing.T) {
	r := raceFixture(t)
	// 30s replay: the agent is done at 30s, the card comes one beat later.
	m := raceAt(t, r, 120, 35, 30*time.Second+raceBeat/2)
	if m.endCard {
		t.Fatal("end card shown before the beat passed")
	}
	if !m.numbers().agent.Done || m.numbers().human.Done {
		t.Fatal("want the agent done and the human still going")
	}
	m = raceAt(t, r, 120, 35, 31*time.Second+raceBeat)
	if !m.endCard {
		t.Fatal("no end card a beat after the agent finished")
	}
	all := strings.Join(plainView(m), "\n")
	for _, s := range []string{"FINISH", "Agent: " + race.Friendly(r.AgentTotal), "Human: " + race.Friendly(r.HumanTotal), "faster than by hand", "36 steps", "never typos"} {
		if !strings.Contains(all, s) {
			t.Errorf("end card lacks %q:\n%s", s, all)
		}
	}
	// The frame clock stops on the end card.
	if _, cmd := m.Update(raceTickMsg(raceT0.Add(time.Minute))); cmd != nil {
		t.Error("ticks keep running on the end card")
	}
	if testing.Verbose() {
		t.Logf("\n%s", all)
	}
}

func TestRaceKeys(t *testing.T) {
	r := raceFixture(t)
	m := raceAt(t, r, 120, 35, time.Second)
	pos := m.pb.Pos()

	press := func(m RaceModel, k string) (RaceModel, tea.Cmd) {
		out, cmd := m.Update(tea.KeyPressMsg{Code: rune(k[0]), Text: k})
		return out.(RaceModel), cmd
	}
	m, _ = press(m, " ")
	if !m.pb.Paused() {
		t.Fatal("space did not pause")
	}
	next, _ := m.Update(raceTickMsg(raceT0.Add(5 * time.Second)))
	m = next.(RaceModel)
	if m.pb.Pos() != pos {
		t.Fatalf("paused replay moved from %v to %v", pos, m.pb.Pos())
	}
	if !strings.Contains(strings.Join(plainView(m), "\n"), "paused") {
		t.Error("no paused indicator")
	}
	f := m.pb.Factor()
	m, _ = press(m, "+")
	if m.pb.Factor() != 2*f {
		t.Errorf("+ factor = %v, want %v", m.pb.Factor(), 2*f)
	}
	m, _ = press(m, "-")
	m, _ = press(m, "-")
	if m.pb.Factor() != f/2 {
		t.Errorf("- factor = %v, want %v", m.pb.Factor(), f/2)
	}
	m, _ = press(m, "e")
	if !m.endCard {
		t.Fatal("e did not jump to the end card")
	}
	m.ticking = false
	m, cmd := press(m, "r")
	if m.endCard || m.pb.Pos() != 0 || m.pb.Factor() != m.factor0 || cmd == nil {
		t.Fatalf("r did not restart: card=%v pos=%v factor=%v cmd=%v", m.endCard, m.pb.Pos(), m.pb.Factor(), cmd != nil)
	}
	if _, cmd := press(m, "q"); cmd == nil {
		t.Fatal("q did not quit")
	}
}

func TestRaceHugeAndMultibyteResult(t *testing.T) {
	huge := strings.Repeat("ログ\x1b[31m行\t\x07\n", 200000)
	r := &race.Race{Params: race.DefaultParams(), ToolCalls: map[string]int{"Read": 1}}
	r.Steps = []race.Step{{
		Kind: race.KindTool, Tool: "Read", Label: "/big.log", Typed: "/big.log", TypedRunes: 8,
		ReadRunes: len([]rune(huge)), ResultHead: string([]rune(huge)[:2000]), ResultTail: "ログ行",
		AgentDur: 10 * time.Second, TypeDur: time.Second, ReadDur: time.Hour,
	}}
	r.AgentTotal, r.HumanTotal = 10*time.Second, time.Hour+time.Second
	m := NewRaceModel(r, "big", 1, raceT0)
	m.width, m.height = 100, 30
	next, _ := m.Update(raceTickMsg(raceT0.Add(2 * time.Second)))
	m = next.(RaceModel)
	for i, l := range plainView(m) {
		if w := ansi.StringWidth(l); w > 100 {
			t.Errorf("row %d is %d cells wide: %q", i, w, l)
		}
		if strings.ContainsAny(l, "\x1b\x07\t") {
			t.Errorf("row %d leaks control characters: %q", i, l)
		}
	}
}

func TestBigText(t *testing.T) {
	rows := bigText("1:02:03")
	w := ansi.StringWidth(rows[0])
	for i, r := range rows {
		if ansi.StringWidth(r) != w {
			t.Errorf("row %d width %d, want %d", i, ansi.StringWidth(r), w)
		}
	}
	if got := ansi.StringWidth(bigText("37x")[0]); got != 11 {
		t.Errorf("37x width = %d, want 11", got)
	}
}
