package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// escNoQuit presses esc and fails if the hub asks to quit.
func escNoQuit(t *testing.T, m FleetModel) FleetModel {
	t.Helper()
	next, cmd := m.Update(schedKeyMsg("esc"))
	nm := next.(FleetModel)
	if nm.quitting {
		t.Fatal("esc set quitting")
	}
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("esc returned tea.Quit")
		}
	}
	return schedDrive(t, nm, cmd)
}

func TestEscClosesCardBackToList(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := selectSched(t, showSchedules(t, f.model(t)), "upcoming")
	if !m.schedPaneActive() {
		t.Fatal("card should be open on a selected job")
	}
	m = escNoQuit(t, m)
	if m.schedPaneActive() || m.schedForm != nil {
		t.Fatal("esc should close the card")
	}
	if m.selSchedule != "upcoming" || m.jumpErr != "" {
		t.Errorf("selection %q jumpErr %q: the cursor should stay on the job and the hub stay open", m.selSchedule, m.jumpErr)
	}
	// Moving to another job brings its card back.
	m = selectSched(t, m, "weekly")
	if !m.schedPaneActive() {
		t.Error("selecting another job should show its card")
	}
}

func TestEscFormCleanReturnsToCard(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := selectSched(t, showSchedules(t, f.model(t)), "upcoming")
	m = schedPress(t, m, "e")
	m = escNoQuit(t, m)
	if m.schedForm != nil || !m.schedPaneActive() || m.selSchedule != "upcoming" {
		t.Fatalf("clean esc should land on the card: form %v active %v sel %q", m.schedForm != nil, m.schedPaneActive(), m.selSchedule)
	}
	// A card dismissed earlier comes back once the form closes.
	m = escNoQuit(t, m)
	m = schedPress(t, m, "e")
	m = escNoQuit(t, m)
	if !m.schedPaneActive() || m.schedForm != nil {
		t.Error("closing the form should show the card")
	}
}

func TestEscFormDirtyAsksThenStays(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := selectSched(t, showSchedules(t, f.model(t)), "upcoming")
	m = schedPress(t, m, "e")
	m = focusStopT(t, m, stopAgent)
	m = schedType(t, m, "x")
	m = schedPress(t, m, "tab")
	m = escNoQuit(t, m)
	if m.schedForm == nil || !m.schedForm.discard {
		t.Fatal("dirty esc should open the discard confirm")
	}
	m = escNoQuit(t, m) // any key but y keeps editing
	if m.schedForm == nil || m.schedForm.discard {
		t.Fatal("esc on the confirm should keep editing")
	}
}

func TestEscFromPickersClosesOneLayer(t *testing.T) {
	withSchedTemplates(t, schedTriage)
	f := newSchedFixture(t)
	f.seedFour(t)
	m := selectSched(t, showSchedules(t, f.model(t)), "upcoming")
	m = schedPress(t, m, "e")

	// Schedule picker with a filter: esc clears the filter, form stays.
	m = focusStopT(t, m, stopAgent)
	m = schedType(t, m, "q") // q types, never quits
	if c := m.schedForm.cyclerFor(stopAgent); c == nil || c.filter != "q" {
		t.Fatalf("q should type into the picker filter")
	}
	m = escNoQuit(t, m)
	if m.schedForm == nil || m.schedForm.cyclerFor(stopAgent).filter != "" {
		t.Fatal("esc should clear the picker filter and keep the form")
	}

	// Template picker the same way.
	m = focusStopT(t, m, stopSource)
	m = schedPress(t, m, "right")
	m = focusStopT(t, m, stopTemplate)
	m = schedType(t, m, "t")
	m = escNoQuit(t, m)
	if m.schedForm == nil {
		t.Fatal("esc on a filtered template picker closed the form")
	}
	m = escNoQuit(t, m)
	if m.schedForm != nil && !m.schedForm.dirty() {
		t.Fatal("esc on a clean picker should close the form")
	}
}

func TestEscConfirmCancelsBeforeCard(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := selectSched(t, showSchedules(t, f.model(t)), "upcoming")
	m = schedPress(t, m, "x")
	if m.schedConfirm == nil {
		t.Fatal("x should arm delete")
	}
	m = escNoQuit(t, m)
	if m.schedConfirm != nil || !m.schedPaneActive() {
		t.Error("esc should cancel the confirm and leave the card open")
	}
}
