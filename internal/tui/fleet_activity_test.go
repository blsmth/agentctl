package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/tmux"
)

func TestDecideHubLayout(t *testing.T) {
	tests := []struct {
		name               string
		win, list, userAct int
		want               hubLayout
		wantErr            bool
	}{
		{name: "wide: default 35%", win: 200, list: 56, want: hubLayout{list: 56, viewer: 72, activity: 70}},
		{name: "activity gives way to the viewer's 60", win: 160, list: 50, want: hubLayout{list: 50, viewer: 60, activity: 48}},
		{name: "exact fit at the minimums", win: 146, list: 44, want: hubLayout{list: 44, viewer: 60, activity: 40}},
		{name: "user width kept", win: 200, list: 56, userAct: 50, want: hubLayout{list: 56, viewer: 92, activity: 50}},
		{name: "user width clamped for the viewer", win: 200, list: 56, userAct: 120, want: hubLayout{list: 56, viewer: 60, activity: 82}},
		{name: "user width below the minimum", win: 200, list: 56, userAct: 10, want: hubLayout{list: 56, viewer: 102, activity: 40}},
		{name: "narrow: replaces the viewer", win: 145, list: 44, want: hubLayout{list: 44, activity: 100, hideViewer: true}},
		{name: "narrow, user width ignored", win: 120, list: 44, userAct: 50, want: hubLayout{list: 44, activity: 75, hideViewer: true}},
		{name: "too narrow for anything", win: 60, list: 44, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideHubLayout(tt.win, tt.list, tt.userAct)
			if (got.err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", got.err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			got.err = nil
			if got != tt.want {
				t.Errorf("decideHubLayout(%d, %d, %d) = %+v, want %+v", tt.win, tt.list, tt.userAct, got, tt.want)
			}
			// Every column is accounted for: panes plus one divider each.
			used := got.list + got.activity + 1
			if !got.hideViewer {
				used += got.viewer + 1
			}
			if used != tt.win {
				t.Errorf("columns used = %d, want %d", used, tt.win)
			}
		})
	}
}

func TestSlideWidths(t *testing.T) {
	if got, want := slideWidths(70), []int{26, 48, 70}; !reflect.DeepEqual(got, want) {
		t.Errorf("slideWidths(70) = %v, want %v", got, want)
	}
	if got := slideWidths(activitySlideStart); got[len(got)-1] != activitySlideStart {
		t.Errorf("slideWidths(start) = %v", got)
	}
}

// activityStub records the activity pane's tmux calls.
type activityStub struct {
	win      int
	opened   []int
	resizes  []string
	kills    int
	hides    int
	shows    []string
	panes    []tmux.HubPane
	openErr  error
	panesErr error
}

func installActivityStub(t *testing.T, win int) *activityStub {
	t.Helper()
	s := &activityStub{win: win}
	saved := []any{hubWindowWidth, activityOpenPane, activityResize, activityKillPanes, activityListPane, activityHideViewer, activityPanes, showLiveViewer, hubZoomSync, hubSelectionPath}
	t.Cleanup(func() {
		hubWindowWidth = saved[0].(func() (int, error))
		activityOpenPane = saved[1].(func(int) (string, error))
		activityResize = saved[2].(func(string, int) error)
		activityKillPanes = saved[3].(func() error)
		activityListPane = saved[4].(func() string)
		activityHideViewer = saved[5].(func() error)
		activityPanes = saved[6].(func() ([]tmux.HubPane, error))
		showLiveViewer = saved[7].(func(string, string, int) error)
		hubZoomSync = saved[8].(func(bool))
		hubSelectionPath = saved[9].(func() (string, error))
	})
	hubWindowWidth = func() (int, error) { return s.win, nil }
	activityOpenPane = func(w int) (string, error) {
		s.opened = append(s.opened, w)
		if s.openErr != nil {
			return "", s.openErr
		}
		return "%7", nil
	}
	activityResize = func(p string, w int) error {
		s.resizes = append(s.resizes, fmt.Sprintf("%s=%d", p, w))
		return nil
	}
	activityKillPanes = func() error { s.kills++; return nil }
	activityListPane = func() string { return "%1" }
	activityHideViewer = func() error { s.hides++; return nil }
	activityPanes = func() ([]tmux.HubPane, error) { return s.panes, s.panesErr }
	showLiveViewer = func(sess, win string, w int) error {
		s.shows = append(s.shows, fmt.Sprintf("%s|%s=%d", sess, win, w))
		return nil
	}
	hubZoomSync = func(bool) {}
	path := filepath.Join(t.TempDir(), "hub-selection.json")
	hubSelectionPath = func() (string, error) { return path, nil }
	return s
}

// activityPump feeds msgs to m, running every command they return (batches and
// sequences flattened, in order) and feeding the results back, until no
// message is left. Poll ticks are dropped so the loop ends.
func activityPump(t *testing.T, m FleetModel, msgs ...tea.Msg) FleetModel {
	t.Helper()
	for i := 0; len(msgs) > 0; i++ {
		if i > 100 {
			t.Fatal("activityPump: message loop does not settle")
		}
		msg := msgs[0]
		msgs = msgs[1:]
		next, cmd := m.Update(msg)
		m = next.(FleetModel)
		msgs = append(msgs, activityRun(cmd)...)
	}
	return m
}

// activityRun runs cmd and returns the messages it produces, flattening batches
// and sequences and dropping the hub's own poll and load messages.
func activityRun(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		var out []tea.Msg
		for i := 0; i < v.Len(); i++ {
			c, _ := v.Index(i).Interface().(tea.Cmd)
			out = append(out, activityRun(c)...)
		}
		return out
	}
	switch msg.(type) {
	case fleetTickMsg, fleetLoadedMsg, fleetViewerMsg:
		return nil
	}
	return []tea.Msg{msg}
}

func activityKey(t *testing.T, m FleetModel, key rune) FleetModel {
	t.Helper()
	next, cmd := m.Update(tea.KeyPressMsg{Code: key, Text: string(key)})
	return activityPump(t, next.(FleetModel), activityRun(cmd)...)
}

func TestFleetActivityOpenSlidesAndCloses(t *testing.T) {
	s := installActivityStub(t, 200)
	m := liveTestModel(t, 56, 40)
	m.selectLine(m.firstRowLine())

	m = activityKey(t, m, 'v')
	if m.activity.pane != "%7" || m.activity.busy || m.activity.slide != nil {
		t.Fatalf("activity = %+v", m.activity)
	}
	if !reflect.DeepEqual(s.opened, []int{activitySlideStart}) {
		t.Errorf("opened at %v, want [%d]", s.opened, activitySlideStart)
	}
	// Three widening steps, then the list is pinned with the last one.
	want := []string{"%7=26", "%7=48", "%1=56", "%7=70"}
	if !reflect.DeepEqual(s.resizes, want) {
		t.Errorf("resizes = %v, want %v", s.resizes, want)
	}
	if s.hides != 0 || m.activity.hidesViewer {
		t.Errorf("viewer hidden on a wide window")
	}
	path, _ := hubSelectionPath()
	if got, err := readHubSelection(path); err != nil || got.WindowID == "" {
		t.Errorf("selection not published on open: %+v %v", got, err)
	}

	m = activityKey(t, m, 'v')
	if m.activity.open() || s.kills != 1 {
		t.Errorf("close: pane=%q kills=%d", m.activity.pane, s.kills)
	}
	if len(s.shows) != 0 {
		t.Errorf("viewer re-shown though it was never hidden: %v", s.shows)
	}
}

func TestFleetActivityNarrowReplacesViewer(t *testing.T) {
	s := installActivityStub(t, 140)
	m := liveTestModel(t, 44, 40)
	m.selectLine(m.firstRowLine())
	key := m.selSession + "|" + m.selWindowID
	m.liveShown = key

	m = activityKey(t, m, 'v')
	if !m.activity.hidesViewer || s.hides != 1 {
		t.Fatalf("viewer not replaced: hides=%d activity=%+v", s.hides, m.activity)
	}
	if got := s.resizes[len(s.resizes)-1]; got != "%7=95" {
		t.Errorf("final width %s, want %%7=95", got)
	}
	for _, r := range s.resizes {
		if strings.HasPrefix(r, "%1=") {
			t.Errorf("list pane resized while the viewer is hidden: %v", s.resizes)
		}
	}
	// Selection changes do not bring the viewer back while it is replaced.
	m.moveCursor(1)
	if cmd := m.maybeShowCmd(); cmd != nil {
		t.Error("live viewer shown while the activity pane replaces it")
	}

	m = activityKey(t, m, 'v')
	if m.activity.open() || m.activity.hidesViewer || s.kills != 1 {
		t.Fatalf("close: %+v kills=%d", m.activity, s.kills)
	}
	if len(s.shows) != 1 {
		t.Fatalf("viewer not restored on close: %v", s.shows)
	}
	if !strings.HasSuffix(s.shows[0], fmt.Sprintf("=%d", 140-44-1)) {
		t.Errorf("viewer restored at %s, want width %d", s.shows[0], 140-44-1)
	}
}

func TestFleetActivityOpenFailure(t *testing.T) {
	s := installActivityStub(t, 200)
	s.openErr = errors.New("no space for new pane")
	m := liveTestModel(t, 56, 40)
	m = activityKey(t, m, 'v')
	if m.activity.open() || m.activity.busy {
		t.Errorf("activity = %+v", m.activity)
	}
	if !strings.Contains(m.jumpErr, "no space") {
		t.Errorf("jumpErr = %q", m.jumpErr)
	}
}

func TestFleetActivityPoll(t *testing.T) {
	list := tmux.HubPane{ID: "%1", Width: 56}
	viewer := tmux.HubPane{ID: "%2", Width: 72, Viewer: true}
	act := func(id string, w int) tmux.HubPane { return tmux.HubPane{ID: id, Width: w, Activity: true} }
	open := func(m *FleetModel) {
		m.activity = fleetActivity{pane: "%7", width: 70, listWidth: 56, winWidth: 200, sawViewer: true, checked: true}
	}
	tests := []struct {
		name  string
		setup func(m *FleetModel)
		panes []tmux.HubPane
		win   int
		check func(t *testing.T, m FleetModel, s *activityStub)
	}{
		{
			name:  "pane died on its own: toggle resets",
			setup: open,
			panes: []tmux.HubPane{list, viewer},
			win:   200,
			check: func(t *testing.T, m FleetModel, s *activityStub) {
				if m.activity.open() {
					t.Errorf("still open: %+v", m.activity)
				}
			},
		},
		{
			name: "died while it replaced the viewer: viewer restored",
			setup: func(m *FleetModel) {
				open(m)
				m.activity.hidesViewer, m.activity.sawViewer = true, false
			},
			panes: []tmux.HubPane{list},
			win:   200,
			check: func(t *testing.T, m FleetModel, s *activityStub) {
				if m.activity.open() || m.activity.hidesViewer || len(s.shows) != 1 {
					t.Errorf("activity=%+v shows=%v", m.activity, s.shows)
				}
			},
		},
		{
			name: "hot reload: recorded pane reattached, not duplicated",
			setup: func(m *FleetModel) {
				m.applyViewState(hubViewState{ActivityPane: "%7", ActivityWidth: 50})
			},
			panes: []tmux.HubPane{list, viewer, act("%7", 50)},
			win:   200,
			check: func(t *testing.T, m FleetModel, s *activityStub) {
				if m.activity.pane != "%7" || len(s.opened) != 0 {
					t.Errorf("pane=%q opened=%v", m.activity.pane, s.opened)
				}
				if m.activity.userWidth != 50 || m.activity.width != 50 {
					t.Errorf("user width lost: %+v", m.activity)
				}
			},
		},
		{
			name:  "startup: stray marked pane adopted",
			setup: func(m *FleetModel) {},
			panes: []tmux.HubPane{list, viewer, act("%9", 70)},
			win:   200,
			check: func(t *testing.T, m FleetModel, s *activityStub) {
				if m.activity.pane != "%9" {
					t.Errorf("pane = %q, want %%9", m.activity.pane)
				}
			},
		},
		{
			name: "recorded pane gone after restart: dropped",
			setup: func(m *FleetModel) {
				m.applyViewState(hubViewState{ActivityPane: "%7", ActivityWidth: 50})
			},
			panes: []tmux.HubPane{list, viewer},
			win:   200,
			check: func(t *testing.T, m FleetModel, s *activityStub) {
				if m.activity.open() || m.activity.userWidth != 50 {
					t.Errorf("activity = %+v", m.activity)
				}
			},
		},
		{
			name:  "user drag remembered",
			setup: open,
			panes: []tmux.HubPane{list, {ID: "%2", Width: 62, Viewer: true}, act("%7", 80)},
			win:   200,
			check: func(t *testing.T, m FleetModel, s *activityStub) {
				if m.activity.userWidth != 80 || len(s.resizes) != 0 {
					t.Errorf("userWidth=%d resizes=%v", m.activity.userWidth, s.resizes)
				}
			},
		},
		{
			name:  "drag below the minimum snaps back",
			setup: open,
			panes: []tmux.HubPane{list, viewer, act("%7", 25)},
			win:   200,
			check: func(t *testing.T, m FleetModel, s *activityStub) {
				if m.activity.userWidth != activityMinCols || !reflect.DeepEqual(s.resizes, []string{"%7=40"}) {
					t.Errorf("userWidth=%d resizes=%v", m.activity.userWidth, s.resizes)
				}
			},
		},
		{
			name: "window resize keeps the dragged width",
			setup: func(m *FleetModel) {
				open(m)
				m.activity.userWidth, m.activity.width = 80, 80
			},
			panes: []tmux.HubPane{list, viewer, act("%7", 80)},
			win:   220,
			check: func(t *testing.T, m FleetModel, s *activityStub) {
				// 220 wide: list 56, activity 80, viewer 82.
				if want := []string{"%1=56", "%7=80"}; !reflect.DeepEqual(s.resizes, want) {
					t.Errorf("resizes = %v, want %v", s.resizes, want)
				}
				if m.activity.winWidth != 220 {
					t.Errorf("winWidth = %d", m.activity.winWidth)
				}
			},
		},
		{
			name:  "window shrinks below the fit: viewer hidden",
			setup: open,
			panes: []tmux.HubPane{{ID: "%1", Width: 44}, viewer, act("%7", 60)},
			win:   140,
			check: func(t *testing.T, m FleetModel, s *activityStub) {
				if !m.activity.hidesViewer || s.hides != 1 {
					t.Errorf("hidden=%v hides=%d", m.activity.hidesViewer, s.hides)
				}
				if want := []string{"%7=95"}; !reflect.DeepEqual(s.resizes, want) {
					t.Errorf("resizes = %v, want %v", s.resizes, want)
				}
			},
		},
		{
			name: "window grows back: viewer restored beside the pane",
			setup: func(m *FleetModel) {
				open(m)
				m.activity.hidesViewer, m.activity.sawViewer, m.activity.winWidth = true, false, 140
			},
			panes: []tmux.HubPane{list, act("%7", 143)},
			win:   200,
			check: func(t *testing.T, m FleetModel, s *activityStub) {
				if m.activity.hidesViewer {
					t.Error("viewer still hidden")
				}
				if want := []string{"%7=70"}; !reflect.DeepEqual(s.resizes, want) {
					t.Errorf("resizes = %v, want %v", s.resizes, want)
				}
				if len(s.shows) != 1 || !strings.HasSuffix(s.shows[0], "=72") {
					t.Errorf("shows = %v, want the viewer at 72", s.shows)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := installActivityStub(t, tt.win)
			s.panes = tt.panes
			m := liveTestModel(t, 56, 40)
			m.selectLine(m.firstRowLine())
			m.liveShown = m.selSession + "|" + m.selWindowID
			tt.setup(&m)
			m = activityPump(t, m, activityRun(m.activityPollCmd())...)
			tt.check(t, m, s)
		})
	}
}

func TestFleetActivityPollIdleWhenClosed(t *testing.T) {
	installActivityStub(t, 200)
	m := liveTestModel(t, 56, 40)
	m.activity.checked = true
	if cmd := m.activityPollCmd(); cmd != nil {
		t.Error("polls the hub panes with the activity pane closed")
	}
}

func TestFleetActivityProbeQuiet(t *testing.T) {
	installActivityStub(t, 200)
	m := liveTestModel(t, 56, 40)
	m.liveWinWidth, m.liveLeft = 200, 56
	m.quietPanes()
	next, _ := m.Update(fleetProbeMsg{pane: 51, win: 200})
	if got := next.(FleetModel).userLeft; got != 0 {
		t.Errorf("a reflow during the slide-in was taken for a drag: userLeft=%d", got)
	}
	orig := fleetNow
	t.Cleanup(func() { fleetNow = orig })
	fleetNow = func() time.Time { return orig().Add(2 * activityQuiet) }
	next, _ = m.Update(fleetProbeMsg{pane: 51, win: 200})
	if got := next.(FleetModel).userLeft; got != 51 {
		t.Errorf("after the quiet window a drag is a drag: userLeft=%d", got)
	}
}

func TestFleetActivityLiveShowHidden(t *testing.T) {
	s := installActivityStub(t, 140)
	m := liveTestModel(t, 44, 40)
	m.activity = fleetActivity{pane: "%7", checked: true}
	m.selectLine(m.firstRowLine())
	m.liveShown = ""
	m = activityPump(t, m, activityRun(m.maybeShowCmd())...)
	if len(s.shows) != 0 || !m.activity.hidesViewer || m.liveShown != "" {
		t.Errorf("shows=%v hidden=%v liveShown=%q", s.shows, m.activity.hidesViewer, m.liveShown)
	}
}

func TestFleetActivityLiveShowBesidePane(t *testing.T) {
	s := installActivityStub(t, 200)
	m := liveTestModel(t, 56, 40)
	m.activity = fleetActivity{pane: "%7", checked: true}
	m.selectLine(m.firstRowLine())
	m.liveShown = ""
	m = activityPump(t, m, activityRun(m.maybeShowCmd())...)
	if len(s.shows) != 1 || !strings.HasSuffix(s.shows[0], "=72") {
		t.Errorf("shows = %v, want the viewer created 72 wide", s.shows)
	}
}

func TestFleetActivityButton(t *testing.T) {
	installActivityStub(t, 200)
	m := liveTestModel(t, 80, 40)
	m.liveLeft = 80
	l := m.titleLayout(m.contentWidth())
	if l.activity[1] == 0 {
		t.Fatalf("no [activity] button on an 80-column title: %q", ansi.Strip(l.line))
	}
	plain := ansi.Strip(l.line)
	if got := ansi.Cut(plain, l.activity[0], l.activity[1]); got != fleetActivityButton {
		t.Errorf("button span covers %q", got)
	}
	if !m.onActivityButton(l.activity[0], 0) || m.onActivityButton(l.activity[0], 1) {
		t.Error("hit test wrong")
	}
	next, cmd := m.Update(tea.MouseClickMsg{X: l.activity[0] + 1, Y: 0, Button: tea.MouseLeft})
	m = activityPump(t, next.(FleetModel), activityRun(cmd)...)
	if !m.activity.open() {
		t.Error("click on [activity] did not open the pane")
	}
}

func TestFleetActivityButtonYieldsToCounts(t *testing.T) {
	m := liveTestModel(t, 44, 40)
	m.liveLeft = 44
	l := m.titleLayout(m.contentWidth())
	plain := ansi.Strip(l.line)
	if !strings.Contains(plain, "○") {
		t.Errorf("counts dropped for the button: %q", plain)
	}
	if l.activity[1] > 0 && !strings.Contains(plain, fleetActivityButton) {
		t.Errorf("button span without a button: %q", plain)
	}
}

func TestFleetActivityViewStateRoundTrip(t *testing.T) {
	m := liveTestModel(t, 56, 40)
	m.activity = fleetActivity{pane: "%7", userWidth: 64, width: 64, checked: true}
	data := m.viewEnv()
	_, js, _ := strings.Cut(data, "=")
	s, ok := parseHubView([]byte(js))
	if !ok || s.ActivityPane != "%7" || s.ActivityWidth != 64 {
		t.Fatalf("view state = %+v (%v) from %s", s, ok, js)
	}
	m2 := liveTestModel(t, 56, 40)
	m2.applyViewState(s)
	if m2.activity.pane != "%7" || m2.activity.userWidth != 64 || m2.activity.checked {
		t.Errorf("restored activity = %+v", m2.activity)
	}
}

// firstRowLine is the index of the first agent row in the list.
func (m FleetModel) firstRowLine() int {
	for i, l := range m.lines {
		if l.kind == fleetLineRow {
			return i
		}
	}
	return 0
}

func TestFleetActivityFreshStartIgnoresRecordedPane(t *testing.T) {
	s := installActivityStub(t, 200)
	path := filepath.Join(t.TempDir(), "hub-view.json")
	if err := writeHubView(path, hubViewState{ActivityPane: "%7", ActivityWidth: 50}); err != nil {
		t.Fatal(err)
	}
	orig := hubViewPath
	hubViewPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { hubViewPath = orig })

	m := liveTestModel(t, 56, 40)
	m.loadViewState()
	if m.activity.open() || m.activity.userWidth != 50 {
		t.Fatalf("fresh start: activity = %+v, want closed with width 50", m.activity)
	}
	m.selectLine(m.firstRowLine())
	m.liveShown = ""
	m = activityPump(t, m, activityRun(m.maybeShowCmd())...)
	if want := fmt.Sprintf("=%d", 200-56-1); len(s.shows) != 1 || !strings.HasSuffix(s.shows[0], want) {
		t.Errorf("viewer shown as %v, want the two-pane width %s", s.shows, want)
	}
	path2, _ := hubSelectionPath()
	m.publishSelection()
	if got, _ := readHubSelection(path2); got != (HubSelection{}) {
		t.Errorf("selection published with no activity pane: %+v", got)
	}
}

func TestFleetActivityHotReloadKeepsPane(t *testing.T) {
	installActivityStub(t, 200)
	path := filepath.Join(t.TempDir(), "hub-view.json")
	orig := hubViewPath
	hubViewPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { hubViewPath = orig })
	m := liveTestModel(t, 56, 40)
	t.Setenv(HubViewEnv, `{"activity_pane":"%7","activity_width":50}`) // after the constructor, which consumes it
	m.loadViewState()
	if m.activity.pane != "%7" || m.activity.userWidth != 50 {
		t.Errorf("hot reload: activity = %+v, want pane %%7 width 50", m.activity)
	}
}
