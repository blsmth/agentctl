package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// drawerClock stubs fleetNow with a clock the test advances.
func drawerClock(t *testing.T) func(time.Duration) {
	t.Helper()
	saved := fleetNow
	now := schedNow
	fleetNow = func() time.Time { return now }
	t.Cleanup(func() { fleetNow = saved })
	return func(d time.Duration) { now = now.Add(d) }
}

// selectSchedRaw selects a job line and runs the post-update sync without
// driving any command, so the slide-in is left in flight.
func selectSchedRaw(t *testing.T, m FleetModel, name string) (FleetModel, bool) {
	t.Helper()
	for i, l := range m.lines {
		if schedLineKey(l) == name && l.kind == fleetLineSchedule {
			m.selectLine(i)
			next, cmd := m.Update(nil)
			return next.(FleetModel), cmd != nil
		}
	}
	t.Fatalf("no schedule line %q", name)
	return m, false
}

func TestDrawerSlidesInAndStopsTicking(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	advance := drawerClock(t)
	m := showSchedules(t, f.model(t))
	m, _ = selectSchedRaw(t, m, "nightly")
	if !m.schedPaneOn || !m.schedAnimOn || m.schedDrawerOffset() != m.schedDrawerWidth() {
		t.Fatalf("open: on=%v anim=%v offset=%d width=%d", m.schedPaneOn, m.schedAnimOn, m.schedDrawerOffset(), m.schedDrawerWidth())
	}
	gen := m.schedAnimGen

	advance(60 * time.Millisecond)
	next, cmd := m.Update(schedDrawerTickMsg{gen: gen})
	m = next.(FleetModel)
	if cmd == nil || !m.schedAnimOn {
		t.Fatal("mid-slide: the next frame should be scheduled")
	}
	if off := m.schedDrawerOffset(); off <= 0 || off >= m.schedDrawerWidth() {
		t.Errorf("mid-slide offset %d of %d", off, m.schedDrawerWidth())
	}
	// Ease-out: a third of the time covers more than a third of the way.
	if m.schedAnimP <= 1.0/3 {
		t.Errorf("progress %.2f after a third of the duration is not eased out", m.schedAnimP)
	}
	for _, l := range strings.Split(m.render(), "\n") {
		if w := ansi.StringWidth(l); w > m.contentWidth() {
			t.Errorf("sliding frame line is %d wide: %q", w, ansi.Strip(l))
		}
	}

	advance(time.Second)
	next, cmd = m.Update(schedDrawerTickMsg{gen: gen})
	m = next.(FleetModel)
	if cmd != nil || m.schedAnimOn || m.schedDrawerOffset() != 0 {
		t.Errorf("settled: cmd=%v anim=%v offset=%d", cmd != nil, m.schedAnimOn, m.schedDrawerOffset())
	}
	if !strings.Contains(ansi.Strip(m.render()), "Last runs") {
		t.Error("settled drawer does not show the card")
	}
	// A late tick after settling does nothing and does not tick again.
	next, cmd = m.Update(schedDrawerTickMsg{gen: gen})
	if cmd != nil || next.(FleetModel).schedAnimOn {
		t.Error("a tick after settling restarted the animation")
	}
}

func TestDrawerRowMovesDoNotReplayAnimation(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	advance := drawerClock(t)
	m := showSchedules(t, f.model(t))
	m, _ = selectSchedRaw(t, m, "nightly")
	gen := m.schedAnimGen
	advance(time.Second)
	next, _ := m.Update(schedDrawerTickMsg{gen: gen})
	m = next.(FleetModel)
	if m.schedAnimOn {
		t.Fatal("did not settle")
	}
	for _, name := range []string{"babysit", "upcoming", "nightly"} {
		m, _ = selectSchedRaw(t, m, name)
		if m.schedAnimOn || m.schedAnimGen != gen || m.schedDrawerOffset() != 0 {
			t.Fatalf("moving to %s replayed the slide: anim=%v gen %d→%d", name, m.schedAnimOn, gen, m.schedAnimGen)
		}
	}
	// Opening the form over the card is not an open either.
	m = schedPress(t, m, "e")
	if m.schedForm == nil || m.schedAnimOn || m.schedAnimGen != gen {
		t.Errorf("form over the open card replayed the slide: anim=%v gen %d→%d", m.schedAnimOn, gen, m.schedAnimGen)
	}
}

func TestDrawerStaleTickIgnored(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	advance := drawerClock(t)
	m := showSchedules(t, f.model(t))
	m, _ = selectSchedRaw(t, m, "nightly")
	oldGen := m.schedAnimGen
	// Close, then open again: the first open's ticks are stale.
	m = selectAgent(t, m, "@1")
	if m.schedPaneOn || m.schedAnimOn {
		t.Fatalf("closing left the drawer up: on=%v anim=%v", m.schedPaneOn, m.schedAnimOn)
	}
	m, _ = selectSchedRaw(t, m, "nightly")
	if m.schedAnimGen == oldGen {
		t.Fatal("reopening kept the generation")
	}
	advance(time.Second)
	next, cmd := m.Update(schedDrawerTickMsg{gen: oldGen})
	got := next.(FleetModel)
	if cmd != nil || !got.schedAnimOn || got.schedAnimP != 0 {
		t.Errorf("stale tick acted: cmd=%v anim=%v p=%.2f", cmd != nil, got.schedAnimOn, got.schedAnimP)
	}
}

func TestDrawerResizeMidAnimation(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	advance := drawerClock(t)
	m := showSchedules(t, f.model(t))
	m, _ = selectSchedRaw(t, m, "nightly")
	advance(90 * time.Millisecond)
	next, _ := m.Update(schedDrawerTickMsg{gen: m.schedAnimGen})
	m = next.(FleetModel)
	m.liveLeft, m.termWidth = 50, 50
	m.relayout()
	if off := m.schedDrawerOffset(); off < 0 || off > m.schedDrawerWidth() {
		t.Errorf("offset %d outside the resized drawer %d", off, m.schedDrawerWidth())
	}
	for _, l := range strings.Split(m.render(), "\n") {
		if w := ansi.StringWidth(l); w > m.contentWidth() {
			t.Errorf("line %d wide after resize: %q", w, ansi.Strip(l))
		}
	}
}

func TestDrawerWidthBounds(t *testing.T) {
	f := newSchedFixture(t)
	m := f.model(t)
	for _, tc := range []struct{ left, want int }{
		{150, 120},              // 80%
		{62, 49},                // 80% of 62
		{43, fleetSchedPaneMin}, // clamped to the minimum
		{40, 40},                // list left with < a sliver: the whole pane
	} {
		m.liveLeft, m.termWidth = tc.left, max(tc.left, 150)
		if got := m.schedDrawerWidth(); got != tc.want {
			t.Errorf("pane %d: drawer %d, want %d", tc.left, got, tc.want)
		}
	}
}

func TestDrawerOriginHitTestingAndListClicks(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "nightly")
	top := m.fleetListTop()
	dx := m.schedDrawerX()
	if m.schedDrawerCovers(dx-1, top) || !m.schedDrawerCovers(dx, top) {
		t.Error("the drawer starts at its left border")
	}
	// A click on the list to the left of the card still selects that row.
	for i, l := range m.lines {
		if l.kind == fleetLineRow && l.row.WindowID == "@1" {
			y := top
			for j := m.scrollOffset; j < i; j++ {
				y += m.lineHeight(j, m.contentWidth())
			}
			m = schedClickAt(t, m, 4, y)
			if m.selWindowID != "@1" {
				t.Errorf("list click beside the card: selected %q", m.selWindowID)
			}
			break
		}
	}
	// With the form open, the form's origin is the drawer's border + 2.
	m = selectSched(t, m, "nightly")
	m = schedPress(t, m, "e")
	x0, _, _, _, _, ok := m.schedFormArea()
	if !ok || x0 != m.schedDrawerX()+2 {
		t.Errorf("form area x0 %d (ok %v), want %d", x0, ok, m.schedDrawerX()+2)
	}
	x, y := schedFind(t, m, "Schedule", "Hourly")
	if x < x0 {
		t.Errorf("control at x=%d left of the form origin %d", x, x0)
	}
	m = schedClickAt(t, m, x, y)
	if formOf(t, m).kind != schedKindHourly {
		t.Error("click at the control did not hit it")
	}
}
