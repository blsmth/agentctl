package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
)

func orderModel(t *testing.T) (FleetModel, string) {
	t.Helper()
	m := foldModel(t)
	m.orderPath = filepath.Join(t.TempDir(), "group-order.json")
	return m, m.orderPath
}

func groupSessions(m FleetModel) []string {
	var out []string
	for _, g := range m.groups {
		out = append(out, g.Session)
	}
	return out
}

// lineScreenY is the screen row of line i (no scrolling in these fixtures).
func lineScreenY(m FleetModel, i int) int {
	y := m.fleetListTop()
	for j := 0; j < i; j++ {
		y += m.lineHeight(j, m.listColWidth())
	}
	return y
}

func headerLine(t *testing.T, m FleetModel, session string) int {
	t.Helper()
	for i, l := range m.lines {
		if l.kind == fleetLineHeader && l.group.Session == session {
			return i
		}
	}
	t.Fatalf("no header for %s", session)
	return -1
}

func selectHeader(m FleetModel, session string) FleetModel {
	m.selSession, m.selWindowID, m.selIsHeader = session, "", true
	m.resolveSelection()
	return m
}

func orderKey(m FleetModel, k tea.KeyPressMsg) FleetModel {
	next, _ := m.Update(k)
	return next.(FleetModel)
}

func orderSend(m FleetModel, msg tea.Msg) FleetModel {
	next, _ := m.Update(msg)
	return next.(FleetModel)
}

func TestShiftKeysMoveGroupAndPersist(t *testing.T) {
	m, path := orderModel(t)
	m = selectHeader(m, "s2")
	m = orderKey(m, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	if got := groupSessions(m); !slices.Equal(got, []string{"s2", "s1", "s3", "s4"}) {
		t.Fatalf("after shift+up: %v", got)
	}
	if m.selSession != "s2" || !m.selIsHeader {
		t.Errorf("selection = %q header=%v, want s2 header", m.selSession, m.selIsHeader)
	}
	if m.lines[m.splitPane.Cursor()].group.Session != "s2" {
		t.Errorf("cursor not on moved group")
	}
	if got := fleet.ReadGroupOrder(path); !slices.Equal(got, []string{"/src/beta", "/src/alpha", "/src/gamma", "/src/delta"}) {
		t.Errorf("saved %v", got)
	}

	// No-op at the top: nothing changes.
	m = orderKey(m, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	if got := groupSessions(m); !slices.Equal(got, []string{"s2", "s1", "s3", "s4"}) {
		t.Errorf("shift+up at top moved: %v", got)
	}

	m = orderKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
	if got := groupSessions(m); !slices.Equal(got, []string{"s1", "s2", "s3", "s4"}) {
		t.Errorf("after shift+down: %v", got)
	}
}

func TestShiftMoveFromRowKeepsRowSelected(t *testing.T) {
	m, _ := orderModel(t)
	m.selSession, m.selWindowID, m.selIsHeader = "s1", "@3", false
	m.resolveSelection()
	m = orderKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
	if got := groupSessions(m); !slices.Equal(got, []string{"s2", "s1", "s3", "s4"}) {
		t.Fatalf("order %v", got)
	}
	if m.selSession != "s1" || m.selWindowID != "@3" || m.selIsHeader {
		t.Errorf("selection = %q %q header=%v, want row @3", m.selSession, m.selWindowID, m.selIsHeader)
	}
}

func TestMoveGroupNoOpsWriteNothing(t *testing.T) {
	m, path := orderModel(t)
	m.groups = m.groups[:1]
	m.rebuildLines()
	if m.moveGroup(fleet.GroupKey(m.groups[0]), 0) {
		t.Error("single group moved")
	}
	m = foldModel(t)
	m.orderPath = path
	if m.moveGroup("/nope", 0) || m.moveGroup("/src/alpha", 0) {
		t.Error("unknown key or same slot moved")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("no-op wrote the order file")
	}
	m.groups = nil
	m.rebuildLines()
	if m.moveGroup("/src/alpha", 1) {
		t.Error("moved with zero groups")
	}
}

func TestMoveGroupWithFilterAndStaleKeys(t *testing.T) {
	m, path := orderModel(t)
	if err := fleet.WriteGroupOrder(path, []string{"/src/alpha", "/src/away", "/src/beta"}); err != nil {
		t.Fatal(err)
	}
	m.filter = fleetFilterNeeds
	m.relayout()
	if !m.moveGroup("/src/gamma", 0) {
		t.Fatal("move under filter failed")
	}
	if got := groupSessions(m); !slices.Equal(got, []string{"s3", "s1", "s2", "s4"}) {
		t.Errorf("order %v", got)
	}
	got := fleet.ReadGroupOrder(path)
	if i, j := slices.Index(got, "/src/alpha"), slices.Index(got, "/src/away"); j != i+1 {
		t.Errorf("stale key lost its slot: %v", got)
	}
}

func TestDragReorders(t *testing.T) {
	m, path := orderModel(t)
	y1 := lineScreenY(m, headerLine(t, m, "s1"))
	y3 := lineScreenY(m, headerLine(t, m, "s3"))

	m = orderSend(m, tea.MouseClickMsg{Button: tea.MouseLeft, X: 20, Y: y1})
	if m.drag == nil || m.dragging() {
		t.Fatal("press should arm a pending, inactive drag")
	}
	m = orderSend(m, tea.MouseMotionMsg{Button: tea.MouseLeft, X: 20, Y: y1})
	if m.dragging() {
		t.Fatal("motion on the same row must not start a drag")
	}
	m = orderSend(m, tea.MouseMotionMsg{Button: tea.MouseLeft, X: 20, Y: y3})
	if !m.dragging() || m.drag.slot != 2 {
		t.Fatalf("drag = %+v, want active over slot 2", m.drag)
	}
	at, ok := m.dragIndicatorLine()
	if !ok || !strings.Contains(ansi.Strip(m.renderLine(at, m.listColWidth())), "──") {
		t.Errorf("indicator line = %d,%v: %q", at, ok, ansi.Strip(m.renderLine(max(at, 0), m.listColWidth())))
	}
	if got := ansi.Strip(m.renderLine(headerLine(t, m, "s1"), m.listColWidth())); !strings.Contains(got, "alpha") {
		t.Errorf("dragged header should still render its name: %q", got)
	}
	m = orderSend(m, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 20, Y: y3})
	if got := groupSessions(m); !slices.Equal(got, []string{"s2", "s3", "s1", "s4"}) {
		t.Fatalf("order after drag %v", got)
	}
	if m.drag != nil {
		t.Error("drag state not cleared")
	}
	if got := fleet.ReadGroupOrder(path); len(got) != 4 || got[2] != "/src/alpha" {
		t.Errorf("saved %v", got)
	}
}

func TestDragUpOntoBody(t *testing.T) {
	m, _ := orderModel(t)
	yFrom := lineScreenY(m, headerLine(t, m, "s3"))
	// A row inside s1's body.
	var rowLine int
	for i, l := range m.lines {
		if l.kind == fleetLineRow && l.row.Session == "s1" {
			rowLine = i
			break
		}
	}
	m = orderSend(m, tea.MouseClickMsg{Button: tea.MouseLeft, X: 20, Y: yFrom})
	m = orderSend(m, tea.MouseMotionMsg{Button: tea.MouseLeft, X: 20, Y: lineScreenY(m, rowLine)})
	m = orderSend(m, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 20, Y: lineScreenY(m, rowLine)})
	if got := groupSessions(m); !slices.Equal(got, []string{"s3", "s1", "s2", "s4"}) {
		t.Errorf("order %v", got)
	}
}

func TestClickWithoutMotionKeepsClickBehavior(t *testing.T) {
	m, path := orderModel(t)
	h := headerLine(t, m, "s1")
	y := lineScreenY(m, h)
	span := m.headerNameSpan(m.groups[0], m.listColWidth())
	m = orderSend(m, tea.MouseClickMsg{Button: tea.MouseLeft, X: span[0], Y: y})
	m = orderSend(m, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: span[0], Y: y})
	if !m.isFolded("s1") {
		t.Error("click on the name should still fold the group")
	}
	if got := groupSessions(m); !slices.Equal(got, []string{"s1", "s2", "s3", "s4"}) {
		t.Errorf("order changed: %v", got)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("click wrote the order file")
	}
}

func TestDragFromNameRestoresFold(t *testing.T) {
	m, _ := orderModel(t)
	y1 := lineScreenY(m, headerLine(t, m, "s1"))
	span := m.headerNameSpan(m.groups[0], m.listColWidth())
	m = orderSend(m, tea.MouseClickMsg{Button: tea.MouseLeft, X: span[0], Y: y1})
	if !m.isFolded("s1") {
		t.Fatal("press on the name folds")
	}
	y2 := lineScreenY(m, headerLine(t, m, "s2"))
	m = orderSend(m, tea.MouseMotionMsg{Button: tea.MouseLeft, X: span[0], Y: y2})
	if m.isFolded("s1") {
		t.Error("starting a drag should undo the press's fold toggle")
	}
}

func TestDragCancel(t *testing.T) {
	setup := func(t *testing.T) (FleetModel, string) {
		m, path := orderModel(t)
		y1 := lineScreenY(m, headerLine(t, m, "s1"))
		m = orderSend(m, tea.MouseClickMsg{Button: tea.MouseLeft, X: 20, Y: y1})
		m = orderSend(m, tea.MouseMotionMsg{Button: tea.MouseLeft, X: 20, Y: lineScreenY(m, headerLine(t, m, "s3"))})
		if !m.dragging() {
			t.Fatal("not dragging")
		}
		return m, path
	}
	want := []string{"s1", "s2", "s3", "s4"}

	t.Run("esc", func(t *testing.T) {
		m, path := setup(t)
		m = orderKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
		m = orderSend(m, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 20, Y: lineScreenY(m, headerLine(t, m, "s3"))})
		if got := groupSessions(m); !slices.Equal(got, want) {
			t.Errorf("order %v", got)
		}
		if _, err := os.Stat(path); err == nil {
			t.Error("cancelled drag wrote the file")
		}
	})
	t.Run("release outside the list", func(t *testing.T) {
		m, _ := setup(t)
		m = orderSend(m, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 20, Y: 0})
		if got := groupSessions(m); !slices.Equal(got, want) {
			t.Errorf("order %v", got)
		}
		if m.drag != nil {
			t.Error("drag not cleared")
		}
	})
}

func TestPressOnRowIsNotADrag(t *testing.T) {
	m, _ := orderModel(t)
	for i, l := range m.lines {
		if l.kind == fleetLineRow {
			m = orderSend(m, tea.MouseClickMsg{Button: tea.MouseLeft, X: 10, Y: lineScreenY(m, i)})
			break
		}
	}
	if m.drag != nil {
		t.Error("press on an agent row started a drag")
	}
}
