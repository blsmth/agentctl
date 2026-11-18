package tui

import "testing"

func TestFleetLeaverMessageSetsJumpErr(t *testing.T) {
	m := &FleetModel{}
	l := fleetLeaver{m: m, home: "agentctl-x"}
	l.Message("boom")
	if m.jumpErr != "boom" {
		t.Errorf("jumpErr = %q", m.jumpErr)
	}
	if l.ViewedHome() != "agentctl-x" {
		t.Errorf("ViewedHome = %q", l.ViewedHome())
	}
}
