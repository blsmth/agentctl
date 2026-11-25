package tui

import (
	"errors"
	"reflect"
	"testing"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/reconcile"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestFleetCloseCmdClosesChildrenBeforeParent(t *testing.T) {
	var order []string
	oldKill, oldCloser := killSessionWindow, fleetChildCloser
	t.Cleanup(func() { killSessionWindow, fleetChildCloser = oldKill, oldCloser })

	killSessionWindow = func(session, id string) error {
		order = append(order, "parent:"+session+":"+id)
		return nil
	}
	wins := []reconcile.LiveWindow{
		{Session: "s", WindowID: "@2", Record: windowRecord("@1", "sid-1", "sid-2")},
		{Session: "s", WindowID: "@3", Record: windowRecord("@2", "sid-2", "sid-3")},
	}
	fleetChildCloser = reconcile.ChildCloser{
		Enabled: func() bool { return true },
		List:    func() ([]reconcile.LiveWindow, error) { return wins, nil },
		Kill: func(w reconcile.LiveWindow) error {
			order = append(order, "child:"+w.WindowID)
			if w.WindowID == "@3" {
				return errors.New("kill failed") // must not block the parent
			}
			return nil
		},
	}

	r := fleet.Row{Session: "s", AgentState: state.AgentState{Name: "p", WindowID: "@1", SessionID: "sid-1"}}
	msg := fleetCloseCmd(r)().(fleetClosedMsg)
	if msg.err != nil {
		t.Fatalf("err = %v", msg.err)
	}
	want := []string{"child:@3", "child:@2", "parent:s:@1"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestFleetCloseCmdNoSessionIDClosesOnlyWindow(t *testing.T) {
	var order []string
	oldKill, oldCloser := killSessionWindow, fleetChildCloser
	t.Cleanup(func() { killSessionWindow, fleetChildCloser = oldKill, oldCloser })
	killSessionWindow = func(_, id string) error { order = append(order, id); return nil }
	fleetChildCloser = reconcile.ChildCloser{
		Enabled: func() bool { return true },
		List:    func() ([]reconcile.LiveWindow, error) { t.Error("listed"); return nil, nil },
		Kill:    func(reconcile.LiveWindow) error { t.Error("killed child"); return nil },
	}
	fleetCloseCmd(fleet.Row{Session: "s", AgentState: state.AgentState{WindowID: "@1"}})()
	if !reflect.DeepEqual(order, []string{"@1"}) {
		t.Errorf("order = %v", order)
	}
}

func TestDispatchedUnderCountsDescendants(t *testing.T) {
	row := func(id, parent string) fleet.Row {
		return fleet.Row{Session: "s", Parent: parent, AgentState: state.AgentState{WindowID: id}}
	}
	m := &FleetModel{lines: []fleetLine{
		{kind: fleetLineRow, row: row("@1", "")},
		{kind: fleetLineRow, row: row("@2", "@1")},
		{kind: fleetLineRow, row: row("@3", "@2")},
		{kind: fleetLineRow, row: row("@4", "")},
	}}
	if got := m.dispatchedUnder(m.lines[0].row); got != 2 {
		t.Errorf("dispatchedUnder(@1) = %d, want 2", got)
	}
	if got := m.dispatchedUnder(m.lines[3].row); got != 0 {
		t.Errorf("dispatchedUnder(@4) = %d, want 0", got)
	}
}

func windowRecord(spawnedBy, bySession, sid string) windowstate.WindowState {
	return windowstate.WindowState{SpawnedBy: spawnedBy, SpawnedBySession: bySession, SessionID: sid}
}
