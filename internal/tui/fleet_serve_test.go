package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/servectl"
)

// stubServe replaces the probe and toggle for one test and counts toggles.
func stubServe(t *testing.T, state servectl.State, msg string, err error) *int {
	t.Helper()
	origP, origT := fleetServeProbe, fleetServeToggle
	t.Cleanup(func() { fleetServeProbe, fleetServeToggle = origP, origT })
	n := 0
	fleetServeProbe = func() servectl.State { return state }
	fleetServeToggle = func() (string, error) { n++; return msg, err }
	return &n
}

// runCmd runs cmd and feeds every resulting message back into m, one level
// deep (batches included).
func runCmd(t *testing.T, m FleetModel, cmd tea.Cmd) FleetModel {
	t.Helper()
	if cmd == nil {
		return m
	}
	msgs := []tea.Msg{cmd()}
	if b, ok := msgs[0].(tea.BatchMsg); ok {
		msgs = msgs[:0]
		for _, c := range b {
			if c != nil {
				msgs = append(msgs, c())
			}
		}
	}
	for _, msg := range msgs {
		switch msg.(type) {
		case fleetServeProbedMsg, fleetServeToggledMsg:
			next, _ := m.Update(msg)
			m = next.(FleetModel)
		}
	}
	return m
}

func TestServeBadgeStates(t *testing.T) {
	tests := []struct {
		name  string
		known bool
		state servectl.State
		want  string
	}{
		{"before the first probe", false, servectl.StateRunning, ""},
		{"phone on", true, servectl.StateRunning, "📱 on"},
		{"phone off", true, servectl.StatePhoneOff, "📱 off"},
		{"process down", true, servectl.StateOff, "📱 down"},
		{"pid alive, socket dead", true, servectl.StateUnresponsive, "📱 stuck"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := foldModel(t)
			m.serveKnown, m.serveState = tt.known, tt.state
			title := ansi.Strip(m.renderTitle(m.contentWidth()))
			if tt.want == "" {
				if strings.Contains(title, "📱") {
					t.Errorf("title shows a badge before any probe: %q", title)
				}
				return
			}
			if !strings.Contains(title, "agentctl hub"+fleetServeGap+tt.want) {
				t.Errorf("title = %q, want badge %q after the title", title, tt.want)
			}
		})
	}
}

func TestServeBadgeDistinctStyles(t *testing.T) {
	m := foldModel(t)
	m.serveKnown = true
	seen := map[string]servectl.State{}
	for _, st := range []servectl.State{servectl.StateRunning, servectl.StatePhoneOff, servectl.StateOff, servectl.StateUnresponsive} {
		m.serveState = st
		b := m.serveBadge()
		if prev, dup := seen[b]; dup {
			t.Errorf("%v renders like %v: %q", st, prev, b)
		}
		seen[b] = st
	}
}

func TestServeBadgeNarrowTitle(t *testing.T) {
	m := foldModel(t)
	m.serveKnown, m.serveState = true, servectl.StateRunning
	m.termWidth = 40 // contentWidth's floor
	m.liveLeft = 40
	title := ansi.Strip(m.renderTitle(m.contentWidth()))
	if !strings.Contains(title, "📱 on") {
		t.Errorf("narrow title dropped the badge: %q", title)
	}
	if w := ansi.StringWidth(title); w > m.contentWidth() {
		t.Errorf("title is %d wide, over %d", w, m.contentWidth())
	}
}

func TestServeProbeOnTick(t *testing.T) {
	stubServe(t, servectl.StateUnresponsive, "", nil)
	m := foldModel(t)
	next, cmd := m.Update(fleetTickMsg{})
	m = next.(FleetModel)
	if !m.serveProbing {
		t.Fatal("tick did not start a serve probe")
	}
	// A second tick while the probe is in flight does not start another.
	if c := m.serveProbeCmd(); c != nil {
		t.Error("second probe started while one is in flight")
	}
	m = runCmd(t, m, cmd)
	if !m.serveKnown || m.serveState != servectl.StateUnresponsive || m.serveProbing {
		t.Errorf("after probe: known=%v state=%v probing=%v", m.serveKnown, m.serveState, m.serveProbing)
	}
}

func TestServeKeyToggles(t *testing.T) {
	tests := []struct {
		name     string
		state    servectl.State
		msg      string
		err      error
		progress string
		wantInfo string
		wantErr  string
	}{
		{"phone off", servectl.StateRunning, "phone access off", nil, "turning phone access off", "phone access off", ""},
		{"phone on", servectl.StatePhoneOff, "phone access on", nil, "turning phone access on", "phone access on", ""},
		{"start", servectl.StateOff, "serve started at https://h/", nil, "starting", "serve started at https://h/", ""},
		{"not installed", servectl.StateOff, "", servectl.ErrNotInstalled, "starting", "", "serve install"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := stubServe(t, servectl.StateOff, tt.msg, tt.err)
			m := foldModel(t)
			m.serveKnown, m.serveState = true, tt.state
			next, cmd := m.Update(tea.KeyPressMsg{Code: 'S', Text: "S"})
			m = next.(FleetModel)
			if !strings.Contains(m.infoMsg, tt.progress) {
				t.Errorf("progress = %q, want %q", m.infoMsg, tt.progress)
			}
			// A second press while the toggle runs is ignored.
			if _, again := m.Update(tea.KeyPressMsg{Code: 'S', Text: "S"}); again != nil {
				t.Error("second S started another toggle")
			}
			m = runCmd(t, m, cmd)
			if *n != 1 {
				t.Errorf("toggled %d times, want 1", *n)
			}
			if tt.wantErr != "" {
				if !strings.Contains(m.jumpErr, tt.wantErr) {
					t.Errorf("jumpErr = %q, want %q", m.jumpErr, tt.wantErr)
				}
			} else if m.infoMsg != tt.wantInfo || !m.infoOK {
				t.Errorf("info = %q (ok %v), want %q", m.infoMsg, m.infoOK, tt.wantInfo)
			}
			if m.serveBusy {
				t.Error("still busy after the toggle finished")
			}
		})
	}
}

func TestServeBadgeClick(t *testing.T) {
	n := stubServe(t, servectl.StateOff, "serve stopped", nil)
	m := foldModel(t)
	m.serveKnown, m.serveState = true, servectl.StateRunning
	span, ok := m.serveBadgeSpan()
	if !ok {
		t.Fatal("badge not shown")
	}
	// The span matches where the badge renders.
	title := ansi.Strip(m.renderTitle(m.contentWidth()))
	if got := ansi.Cut(title, span[0], span[1]); got != "📱 on" {
		t.Errorf("span %v covers %q", span, got)
	}
	// Clicks beside it, or on another row, do nothing.
	for _, pos := range [][2]int{{span[0] - 1, 0}, {span[1], 0}, {span[0], 1}} {
		if _, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: pos[0], Y: pos[1]}); cmd != nil {
			m2 := runCmd(t, m, cmd)
			_ = m2
		}
	}
	if *n != 0 {
		t.Fatalf("a click off the badge toggled serve")
	}
	next, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: span[0], Y: 0})
	m = runCmd(t, next.(FleetModel), cmd)
	if *n != 1 || m.infoMsg != "serve stopped" {
		t.Errorf("click: toggles=%d info=%q", *n, m.infoMsg)
	}
}

func TestServeToggleErrorIsFlashed(t *testing.T) {
	stubServe(t, servectl.StateOff, "", errors.New("launchctl bootout: boom"))
	m := foldModel(t)
	m = runCmd(t, m, m.toggleServe())
	if m.jumpErr != "serve: launchctl bootout: boom" || m.infoMsg != "" {
		t.Errorf("jumpErr=%q info=%q", m.jumpErr, m.infoMsg)
	}
}

func TestHelpListsServeKey(t *testing.T) {
	for _, it := range fleetHelpItems() {
		if it.keys == fleetServeKey && it.action == fleetServeKey {
			return
		}
	}
	t.Errorf("? reference lacks %q", fleetServeKey)
}
