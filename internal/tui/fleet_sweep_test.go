package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/sweeprun"
	"github.com/blsmth/agentctl/internal/tmux"
)

// sweepCall is one stubbed sweep subprocess run.
type sweepCall struct {
	target sweeprun.Target
	dryRun bool
}

// stubSweep replaces the sweep subprocess seam. preview and apply are the
// reports (or errors) the stub returns.
type stubSweep struct {
	mu       sync.Mutex
	calls    []sweepCall
	preview  sweeprun.Report
	apply    sweeprun.Report
	errPrev  error
	errApply error
}

func installSweepStub(t *testing.T) *stubSweep {
	t.Helper()
	s := &stubSweep{}
	orig := fleetSweep
	t.Cleanup(func() { fleetSweep = orig })
	fleetSweep = func(_ context.Context, tg sweeprun.Target, dryRun bool) (sweeprun.Report, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, sweepCall{tg, dryRun})
		if dryRun {
			r := s.preview
			r.Session, r.DryRun = tg.Session, true
			return r, s.errPrev
		}
		r := s.apply
		r.Session = tg.Session
		return r, s.errApply
	}
	return s
}

func (s *stubSweep) all() []sweepCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sweepCall(nil), s.calls...)
}

func rows(names ...string) []sweeprun.Row {
	out := make([]sweeprun.Row, len(names))
	for i, n := range names {
		out[i] = sweeprun.Row{Name: n, WindowID: "@" + n}
	}
	return out
}

// runSweepCmd runs a cmd that must be a sweep and feeds its message back.
func runSweepCmd(t *testing.T, m FleetModel, cmd tea.Cmd) (FleetModel, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a sweep command")
	}
	msg, ok := cmd().(fleetSweptMsg)
	if !ok {
		t.Fatal("command did not produce a fleetSweptMsg")
	}
	next, after := m.Update(msg)
	return next.(FleetModel), after
}

func sweepKey(m FleetModel, key string) (FleetModel, tea.Cmd) {
	var msg tea.KeyPressMsg
	if len(key) == 1 {
		msg = tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	} else if key == "esc" {
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	next, cmd := m.Update(msg)
	return next.(FleetModel), cmd
}

func footerText(m FleetModel) string {
	return ansi.Strip(m.footerBody(m.contentWidth()))
}

func TestSweepHubSessionMatchesTmux(t *testing.T) {
	if sweeprun.HubSession != tmux.HubSessionName {
		t.Fatalf("sweeprun.HubSession = %q, tmux.HubSessionName = %q", sweeprun.HubSession, tmux.HubSessionName)
	}
}

func TestGroupCanSweep(t *testing.T) {
	m := buttonTestModel(t)
	if !groupCanSweep(m.groups[0]) || !groupCanSweep(m.groups[1]) {
		t.Error("groups with a runtime dir should be sweepable")
	}
	g := m.groups[0]
	g.RuntimeDir = ""
	if groupCanSweep(g) {
		t.Error("group without runtime dir is sweepable")
	}
	g = m.groups[0]
	g.Session = tmux.HubSessionName
	if groupCanSweep(g) {
		t.Error("the hub session is sweepable")
	}
}

// The sweep button renders where it hit-tests, at every width, and never
// overlaps the fold toggle or "+ new"; narrow headers drop it.
func TestSweepButtonLayoutAndHitColumns(t *testing.T) {
	for _, width := range []int{20, 28, 36, 44, 56, 80, 120} {
		t.Run(fmt.Sprintf("width%d", width), func(t *testing.T) {
			m := mouseTestModel(t, width, 40)
			m.groups[0].ProjectDir, m.groups[0].RuntimeDir = "/p/one", "/p/one/.agentctl"
			m.groups[0].Project = "project-one"
			m.groups[0].Rows[1].Done = true // one sweepable row: "sweep 1"
			m.rebuildLines()
			m.resolveSelection()
			cw := m.listColWidth()
			g := m.lines[0].group
			// Hit testing is for the header under the pointer, drawn hovered.
			m.hover = fleetHover{line: 0, reg: fleetHoverHeader}
			hdr := []rune(ansi.Strip(m.renderLine(0, cw)))
			span := m.headerSweepSpan(g, cw)
			if span == [2]int{} {
				if strings.Contains(string(hdr), fleetSweepLabel) {
					t.Fatalf("cw=%d: no sweep span but header shows it: %q", cw, string(hdr))
				}
				if _, a := m.buttonDecision(cw-fleetSidebarRightGutter-len(fleetNewLabel)-2, 2, tea.MouseLeft); a == fleetClickSweep {
					t.Fatalf("cw=%d: hidden sweep button is clickable", cw)
				}
				if cw >= 44 {
					t.Fatalf("cw=%d: sweep button should fit", cw)
				}
				return
			}
			if got := string(hdr[span[0]:span[1]]); got != sweepLabel(1) {
				t.Fatalf("cw=%d: span %v holds %q in %q", cw, span, got, string(hdr))
			}
			name := m.headerNameSpan(g, cw)
			if name[1] > span[0]-1 {
				t.Errorf("cw=%d: name span %v reaches the sweep slack (sweep %v)", cw, name, span)
			}
			for x := span[0] - 1; x < span[1]; x++ {
				if _, a := m.buttonDecision(x, 2, tea.MouseLeft); a != fleetClickSweep {
					t.Errorf("cw=%d x=%d: action %v, want sweep", cw, x, a)
				}
				if h := m.hoverAt(x, 2); h.reg != fleetHoverSweep {
					t.Errorf("cw=%d x=%d: hover %v, want sweep", cw, x, h.reg)
				}
			}
			if _, a := m.buttonDecision(span[1]+fleetSweepSep, 2, tea.MouseLeft); a != fleetClickNew {
				t.Errorf("cw=%d: \"+ new\" after sweep is %v, want new", cw, a)
			}
			if _, a := m.buttonDecision(span[0]-2, 2, tea.MouseLeft); a == fleetClickSweep {
				t.Errorf("cw=%d: column before the slack still sweeps", cw)
			}
			// Rows never carry the button.
			if _, a := m.buttonDecision(span[0], 4, tea.MouseLeft); a == fleetClickSweep {
				t.Errorf("cw=%d: row hit-tests as sweep", cw)
			}
		})
	}
}

// markSweepable makes the first group's row b done, so its header offers
// "sweep 1".
func markSweepable(m *FleetModel) {
	m.groups[0].Rows[1].Done = true
	m.rebuildLines()
	m.resolveSelection()
}

func TestSweepButtonOnlyWithSweepableRows(t *testing.T) {
	tests := []struct {
		name string
		done int
		want string // "" = no button
	}{
		{"nothing to sweep", 0, ""},
		{"one done row", 1, "sweep 1"},
		{"two done rows", 2, "sweep 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := buttonTestModel(t)
			for i := 0; i < tt.done; i++ {
				m.groups[0].Rows[i].Done = true
			}
			m.rebuildLines()
			cw := m.listColWidth()
			g := m.lines[0].group
			m.hover = fleetHover{line: 0, reg: fleetHoverHeader}
			hdr := ansi.Strip(m.renderLine(0, cw))
			span := m.headerSweepSpan(g, cw)
			if tt.want == "" {
				if strings.Contains(hdr, fleetSweepLabel) || span != [2]int{} {
					t.Errorf("header offers sweep with nothing to sweep: %q span %v", hdr, span)
				}
				// The counts sit flush right when no button is drawn.
				m.hover = fleetHover{}
				if plain := ansi.Strip(m.renderLine(0, cw)); !strings.HasSuffix(strings.TrimRight(plain, " "), "○ 2") {
					t.Errorf("counts not flush right: %q", plain)
				}
				return
			}
			if !strings.Contains(hdr, tt.want) || string([]rune(hdr)[span[0]:span[1]]) != tt.want {
				t.Errorf("header %q span %v, want %q", hdr, span, tt.want)
			}
		})
	}
}

func TestHeaderNewOnlySelectedOrHovered(t *testing.T) {
	m := buttonTestModel(t)
	m.selectLine(3) // a row: the header is neither selected nor hovered
	cw := m.listColWidth()
	g := m.lines[0].group
	tests := []struct {
		name     string
		selected bool
		hover    fleetHoverRegion
		want     bool
	}{
		{"plain", false, fleetHoverNone, false},
		{"selected", true, fleetHoverNone, true},
		{"hovered", false, fleetHoverHeader, true},
		{"hovering the name", false, fleetHoverFold, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hdr := ansi.Strip(m.renderGroupHeaderHover(g, tt.selected, tt.hover, cw))
			if got := strings.Contains(hdr, fleetNewLabel); got != tt.want {
				t.Errorf("shows + new = %v, want %v: %q", got, tt.want, hdr)
			}
		})
	}
	// Hit testing lays the header out as drawn under the pointer: "+ new"
	// is there, so its columns click.
	span := m.headerNewSpan(g, cw)
	if span[1] != cw-fleetSidebarRightGutter || span[1]-span[0] != len(fleetNewLabel) {
		t.Errorf("new span = %v", span)
	}
	if _, a := m.buttonDecision(span[0], 2, tea.MouseLeft); a != fleetClickNew {
		t.Errorf("click on + new = %v", a)
	}
	if h := m.hoverAt(span[0]-5, 2); h.reg != fleetHoverHeader || h.line != 0 {
		t.Errorf("hover on the header body = %+v", h)
	}
}

func TestSweepButtonHiddenWhileSelectorArmed(t *testing.T) {
	m := armedHeaderModel(t)
	if span := m.headerSweepSpan(m.lines[0].group, m.listColWidth()); span != [2]int{} {
		t.Errorf("armed selector header still has a sweep span %v", span)
	}
	if strings.Contains(ansi.Strip(m.renderLine(0, m.listColWidth())), fleetSweepLabel) {
		t.Error("armed selector header still shows sweep")
	}
}

func TestSweepHoverStyle(t *testing.T) {
	m := buttonTestModel(t)
	markSweepable(&m)
	cw := m.listColWidth()
	g := m.lines[0].group
	span := m.headerSweepSpan(g, cw)
	plain := m.renderGroupHeaderHover(g, false, fleetHoverHeader, cw)
	hovered := m.renderGroupHeaderHover(g, false, fleetHoverSweep, cw)
	if ansi.Strip(plain) != ansi.Strip(hovered) {
		t.Error("hover changed the header text")
	}
	if span == [2]int{} || m.sweepButtonStyle(g, fleetHoverSweep).Render("x") != fleetNewHoverStyle.Render("x") {
		t.Error("hovered sweep should use the + new hover style")
	}
}

func TestSweepKeyPreviewThenApply(t *testing.T) {
	stub := installSweepStub(t)
	stub.preview = sweeprun.Report{Closed: rows("a", "b"), Attention: rows("c"), InProgress: rows("d")}
	stub.apply = sweeprun.Report{Closed: rows("a")}
	m := buttonTestModel(t)
	m.selectLine(0)

	m, cmd := sweepKey(m, fleetSweepKey)
	if m.sweepBusy != "s1" || !strings.Contains(m.infoMsg, "checking what to sweep") {
		t.Fatalf("busy=%q info=%q", m.sweepBusy, m.infoMsg)
	}
	// A second press while the preview runs starts nothing.
	m2, again := sweepKey(m, fleetSweepKey)
	if again != nil {
		t.Error("second sweep started while one is running")
	}
	m = m2
	m, _ = runSweepCmd(t, m, cmd)
	if !m.sweepArmed() || m.sweepBusy != "" {
		t.Fatalf("preview should arm the confirm: armed=%v busy=%q", m.sweepArmed(), m.sweepBusy)
	}
	want := "sweep s1: close 2 (a, b), 1 needs attention, 1 in progress  y/n"
	if got := m.sweepPrompt(200); got != want {
		t.Errorf("prompt %q, want %q", got, want)
	}
	if f := footerText(m); !strings.Contains(f, "sweep s1: close 2") || !strings.Contains(f, "  y/n") {
		t.Errorf("footer %q lacks the prompt", f)
	}
	if got := m.sweepPrompt(20); lipgloss.Width(got) > 20 || !strings.HasSuffix(got, "y/n") {
		t.Errorf("narrow prompt %q", got)
	}
	if f := footerText(m); !strings.Contains(f, "y sweep") || !strings.Contains(f, "n cancel") {
		t.Errorf("footer hints %q", f)
	}
	if !strings.Contains(m.renderLine(0, m.listColWidth()), fleetBlockedStyle.Render(fleetSweepLabel)) {
		t.Error("armed sweep button should be highlighted")
	}

	m, cmd = sweepKey(m, "y")
	if m.sweepArmed() || m.sweepBusy != "s1" || !strings.Contains(m.infoMsg, "sweeping s1") {
		t.Fatalf("after y: armed=%v busy=%q info=%q", m.sweepArmed(), m.sweepBusy, m.infoMsg)
	}
	m, reload := runSweepCmd(t, m, cmd)
	if !m.infoOK || m.infoMsg != "swept 1 in s1" || reload == nil {
		t.Errorf("after apply: info=%q ok=%v reload=%v", m.infoMsg, m.infoOK, reload != nil)
	}
	calls := stub.all()
	if len(calls) != 2 || !calls[0].dryRun || calls[1].dryRun {
		t.Fatalf("calls = %+v", calls)
	}
	wantT := sweeprun.Target{Session: "s1", RuntimeDir: "/p/one/.agentctl", ProjectDir: "/p/one"}
	if calls[0].target != wantT || calls[1].target != wantT {
		t.Errorf("targets = %+v, want %+v", calls, wantT)
	}
}

func TestSweepCancel(t *testing.T) {
	for _, key := range []string{"n", "esc", "j", "k"} {
		t.Run(key, func(t *testing.T) {
			stub := installSweepStub(t)
			stub.preview = sweeprun.Report{Closed: rows("a")}
			m := buttonTestModel(t)
			m.selectLine(0)
			m, cmd := sweepKey(m, fleetSweepKey)
			m, _ = runSweepCmd(t, m, cmd)
			if !m.sweepArmed() {
				t.Fatal("not armed")
			}
			m, cmd = sweepKey(m, key)
			if m.sweepArmed() {
				t.Error("still armed")
			}
			if cmd != nil {
				if _, isSweep := cmd().(fleetSweptMsg); isSweep {
					t.Error("cancel key applied the sweep")
				}
			}
			if n := len(stub.all()); n != 1 {
				t.Errorf("%d sweep runs, want only the preview", n)
			}
		})
	}
}

func TestSweepNothingToSweep(t *testing.T) {
	stub := installSweepStub(t)
	stub.preview = sweeprun.Report{Attention: rows("c", "d")}
	m := buttonTestModel(t)
	m.selectLine(0)
	m, cmd := sweepKey(m, fleetSweepKey)
	m, _ = runSweepCmd(t, m, cmd)
	if m.sweepArmed() {
		t.Error("nothing to close armed a confirm")
	}
	if want := "sweep s1: nothing to sweep (2 need attention)"; m.infoMsg != want {
		t.Errorf("info = %q, want %q", m.infoMsg, want)
	}
	// y now does nothing sweep-related.
	m, cmd = sweepKey(m, "y")
	if cmd != nil || len(stub.all()) != 1 {
		t.Error("y without an armed confirm ran a sweep")
	}
}

func TestSweepErrors(t *testing.T) {
	t.Run("preview fails", func(t *testing.T) {
		stub := installSweepStub(t)
		stub.errPrev = errors.New(`sweep s1: no tmux session "s1"`)
		m := buttonTestModel(t)
		m.selectLine(0)
		m, cmd := sweepKey(m, fleetSweepKey)
		m, _ = runSweepCmd(t, m, cmd)
		if m.sweepArmed() || m.sweepBusy != "" || !strings.Contains(m.jumpErr, "no tmux session") || m.infoMsg != "" {
			t.Errorf("armed=%v busy=%q err=%q info=%q", m.sweepArmed(), m.sweepBusy, m.jumpErr, m.infoMsg)
		}
	})
	t.Run("apply fails", func(t *testing.T) {
		stub := installSweepStub(t)
		stub.preview = sweeprun.Report{Closed: rows("a")}
		stub.errApply = errors.New("sweep s1: exit status 1")
		m := buttonTestModel(t)
		m.selectLine(0)
		m, cmd := sweepKey(m, fleetSweepKey)
		m, _ = runSweepCmd(t, m, cmd)
		m, cmd = sweepKey(m, "y")
		m, _ = runSweepCmd(t, m, cmd)
		if m.sweepBusy != "" || !strings.Contains(m.jumpErr, "exit status 1") {
			t.Errorf("busy=%q err=%q", m.sweepBusy, m.jumpErr)
		}
	})
	t.Run("a window fails to close", func(t *testing.T) {
		stub := installSweepStub(t)
		stub.preview = sweeprun.Report{Closed: rows("a", "b")}
		stub.apply = sweeprun.Report{Closed: []sweeprun.Row{{Name: "a"}, {Name: "b", Errors: []string{"kill failed"}}}}
		m := buttonTestModel(t)
		m.selectLine(0)
		m, cmd := sweepKey(m, fleetSweepKey)
		m, _ = runSweepCmd(t, m, cmd)
		m, cmd = sweepKey(m, "y")
		m, _ = runSweepCmd(t, m, cmd)
		if want := "swept 1 in s1; 1 failed: b: kill failed"; m.jumpErr != want {
			t.Errorf("err = %q, want %q", m.jumpErr, want)
		}
	})
	t.Run("session gone before apply", func(t *testing.T) {
		stub := installSweepStub(t)
		stub.preview = sweeprun.Report{Closed: rows("a")}
		m := buttonTestModel(t)
		m.selectLine(0)
		m, cmd := sweepKey(m, fleetSweepKey)
		m, _ = runSweepCmd(t, m, cmd)
		m.groups = m.groups[1:] // the session died; no reload yet
		m, cmd = sweepKey(m, "y")
		if cmd != nil || !strings.Contains(m.jumpErr, "gone") || len(stub.all()) != 1 {
			t.Errorf("cmd=%v err=%q calls=%d", cmd != nil, m.jumpErr, len(stub.all()))
		}
	})
}

func TestSweepClickButton(t *testing.T) {
	stub := installSweepStub(t)
	stub.preview = sweeprun.Report{Closed: rows("a")}
	m := buttonTestModel(t)
	markSweepable(&m)
	m.selectLine(3) // row b; the click selects the header
	span := m.headerSweepSpan(m.lines[0].group, m.listColWidth())
	click := func(m FleetModel, x, y int) (FleetModel, tea.Cmd) {
		next, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
		return next.(FleetModel), cmd
	}
	m, cmd := click(m, span[0], 2)
	if m.splitPane.Cursor() != 0 || m.sweepBusy != "s1" {
		t.Fatalf("cursor=%d busy=%q", m.splitPane.Cursor(), m.sweepBusy)
	}
	if _, again := click(m, span[0], 2); again != nil {
		t.Error("double click started a second sweep")
	}
	m, _ = runSweepCmd(t, m, cmd)
	if !m.sweepArmed() {
		t.Fatal("not armed")
	}
	// Clicking the armed button again neither confirms nor re-previews.
	if m2, again := click(m, span[0], 2); again != nil || !m2.sweepArmed() {
		t.Error("repeat click on armed sweep should be a no-op")
	}
	// Clicking elsewhere (moving off the line) cancels.
	m, _ = click(m, 5, 4)
	if m.sweepArmed() {
		t.Error("click on a row should cancel the sweep confirm")
	}
	if n := len(stub.all()); n != 1 {
		t.Errorf("%d sweep runs, want 1", n)
	}
}

func TestSweepPreviewDroppedWhenSelectionMoved(t *testing.T) {
	stub := installSweepStub(t)
	stub.preview = sweeprun.Report{Closed: rows("a")}
	m := buttonTestModel(t)
	m.selectLine(0)
	m, cmd := sweepKey(m, fleetSweepKey)
	m.selectLine(5) // header s2, while the preview runs
	m, _ = runSweepCmd(t, m, cmd)
	if m.sweepArmed() || m.infoMsg != "" {
		t.Errorf("armed=%v info=%q", m.sweepArmed(), m.infoMsg)
	}
}

func TestSweepContextMenu(t *testing.T) {
	stub := installSweepStub(t)
	m := buttonTestModel(t)
	m.openMenu(0)
	items := m.fleetMenuItems(0)
	found := false
	for _, it := range items {
		if it.label == "sweep" && it.key == fleetSweepKey {
			found = true
		}
	}
	if !found {
		t.Fatalf("header menu %+v lacks sweep", items)
	}
	for _, it := range m.fleetMenuItems(2) {
		if it.label == "sweep" {
			t.Error("row menu offers sweep")
		}
	}
	m2, cmd := sweepKey(m, fleetSweepKey)
	if cmd == nil || m2.menuOpen || m2.sweepBusy != "s1" {
		t.Errorf("menu sweep: cmd=%v menu=%v busy=%q", cmd != nil, m2.menuOpen, m2.sweepBusy)
	}
	if _, _ = runSweepCmd(t, m2, cmd); len(stub.all()) != 1 || !stub.all()[0].dryRun {
		t.Errorf("menu sweep runs = %+v", stub.all())
	}
}

func TestSweepInHelp(t *testing.T) {
	for _, it := range fleetHelpItems() {
		if it.keys == fleetSweepKey && it.action == fleetSweepKey {
			return
		}
	}
	t.Error("key reference lacks the sweep key")
}
