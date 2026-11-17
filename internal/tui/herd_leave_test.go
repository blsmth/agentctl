package tui

import "testing"

func TestHerdLeaverMessageSetsJumpErr(t *testing.T) {
	m := &HerdModel{}
	l := herdLeaver{m: m, home: "agentctl-x"}
	l.Message("boom")
	if m.jumpErr != "boom" {
		t.Errorf("jumpErr = %q", m.jumpErr)
	}
	if l.ViewedHome() != "agentctl-x" {
		t.Errorf("ViewedHome = %q", l.ViewedHome())
	}
}
