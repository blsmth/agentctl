package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestNewToast_ReturnsValidCmd verifies that NewToast returns a tea.Cmd that
// produces a ToastMsg with the expected field values.
func TestNewToast_ReturnsValidCmd(t *testing.T) {
	t.Run("default options", func(t *testing.T) {
		cmd := NewToast("hello")
		if cmd == nil {
			t.Fatal("expected non-nil tea.Cmd")
		}
		msg := cmd()
		tm, ok := msg.(ToastMsg)
		if !ok {
			t.Fatalf("expected ToastMsg, got %T", msg)
		}
		if tm.msg != "hello" {
			t.Errorf("msg = %q, want %q", tm.msg, "hello")
		}
		if tm.level != ToastInfo {
			t.Errorf("level = %v, want ToastInfo", tm.level)
		}
		if tm.duration != toastDefaultHold {
			t.Errorf("duration = %v, want %v", tm.duration, toastDefaultHold)
		}
	})

	t.Run("with level warn", func(t *testing.T) {
		cmd := NewToast("caution", WithToastLevel(ToastWarn))
		tm := cmd().(ToastMsg)
		if tm.level != ToastWarn {
			t.Errorf("level = %v, want ToastWarn", tm.level)
		}
	})

	t.Run("with level error", func(t *testing.T) {
		cmd := NewToast("boom", WithToastLevel(ToastError))
		tm := cmd().(ToastMsg)
		if tm.level != ToastError {
			t.Errorf("level = %v, want ToastError", tm.level)
		}
	})

	t.Run("with custom duration", func(t *testing.T) {
		d := 10 * time.Second
		cmd := NewToast("long", WithToastDuration(d))
		tm := cmd().(ToastMsg)
		if tm.duration != d {
			t.Errorf("duration = %v, want %v", tm.duration, d)
		}
	})
}

// TestToastModel_Update_ToastMsg_HiddenToSlidingIn verifies the state machine
// transition from hidden → sliding_in when a ToastMsg is received.
func TestToastModel_Update_ToastMsg_HiddenToSlidingIn(t *testing.T) {
	m := newToastModel()
	if m.state != toastStateHidden {
		t.Fatalf("initial state = %v, want hidden", m.state)
	}

	msg := ToastMsg{msg: "hi", level: ToastInfo, duration: toastDefaultHold}
	m2, cmd := m.Update(msg)

	if m2.state != toastStateSlidingIn {
		t.Errorf("state = %v, want sliding_in", m2.state)
	}
	if m2.frame != 0 {
		t.Errorf("frame = %d, want 0", m2.frame)
	}
	if m2.current == nil {
		t.Fatal("current should not be nil after first ToastMsg")
	}
	if m2.current.msg != "hi" {
		t.Errorf("current.msg = %q, want %q", m2.current.msg, "hi")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd (animation tick) after transition")
	}
}

// TestToastModel_Queue_SecondToastQueued verifies that a second toast is added
// to the queue when the model is already in an active state.
func TestToastModel_Queue_SecondToastQueued(t *testing.T) {
	m := newToastModel()

	// First toast starts slide-in.
	first := ToastMsg{msg: "first", level: ToastInfo, duration: toastDefaultHold}
	m, _ = m.Update(first)

	if m.state != toastStateSlidingIn {
		t.Fatalf("expected sliding_in after first toast, got %v", m.state)
	}
	if len(m.queue) != 0 {
		t.Fatalf("queue length = %d, want 0 before second toast", len(m.queue))
	}

	// Second toast while first is animating — should be queued.
	second := ToastMsg{msg: "second", level: ToastWarn, duration: toastDefaultHold}
	m, cmd := m.Update(second)

	if len(m.queue) != 1 {
		t.Errorf("queue length = %d, want 1", len(m.queue))
	}
	if m.queue[0].msg != "second" {
		t.Errorf("queued msg = %q, want %q", m.queue[0].msg, "second")
	}
	if cmd != nil {
		t.Error("expected nil cmd when toast is queued (animation already running)")
	}
}

// TestToastModel_Advance_PopsNextOrHides verifies advance() behaviour.
func TestToastModel_Advance_PopsNextOrHides(t *testing.T) {
	t.Run("queue empty → hidden", func(t *testing.T) {
		m := newToastModel()
		first := ToastMsg{msg: "only", level: ToastInfo, duration: toastDefaultHold}
		m, _ = m.Update(first)

		// Advance with empty queue should go hidden.
		m2, cmd := m.advance()
		if m2.state != toastStateHidden {
			t.Errorf("state = %v, want hidden", m2.state)
		}
		if m2.current != nil {
			t.Errorf("current should be nil after advance to hidden")
		}
		if cmd != nil {
			t.Errorf("expected nil cmd when hiding, got non-nil")
		}
	})

	t.Run("queue has next → sliding_in", func(t *testing.T) {
		m := newToastModel()
		// Send two toasts to populate the queue.
		m, _ = m.Update(ToastMsg{msg: "first", level: ToastInfo, duration: toastDefaultHold})
		m, _ = m.Update(ToastMsg{msg: "second", level: ToastWarn, duration: toastDefaultHold})

		if len(m.queue) != 1 {
			t.Fatalf("queue length = %d before advance, want 1", len(m.queue))
		}

		m2, cmd := m.advance()
		if m2.state != toastStateSlidingIn {
			t.Errorf("state = %v, want sliding_in", m2.state)
		}
		if m2.current == nil || m2.current.msg != "second" {
			t.Errorf("current.msg = %v, want second", m2.current)
		}
		if len(m2.queue) != 0 {
			t.Errorf("queue length = %d after advance, want 0", len(m2.queue))
		}
		if cmd == nil {
			t.Error("expected anim tick cmd after advancing to next toast")
		}
	})
}

// TestToastModel_Active returns the correct bool for each state.
func TestToastModel_Active(t *testing.T) {
	tests := []struct {
		state toastState
		want  bool
	}{
		{toastStateHidden, false},
		{toastStateSlidingIn, true},
		{toastStateVisible, true},
		{toastStateSlidingOut, true},
	}
	for _, tc := range tests {
		m := ToastModel{state: tc.state}
		if got := m.Active(); got != tc.want {
			t.Errorf("state %v: Active() = %v, want %v", tc.state, got, tc.want)
		}
	}
}

// TestToastModel_View_EmptyWhenHidden verifies that View returns "" when hidden.
func TestToastModel_View_EmptyWhenHidden(t *testing.T) {
	m := newToastModel()
	if v := m.View(80); v != "" {
		t.Errorf("View(80) = %q, want empty string when hidden", v)
	}
}

// TestToastModel_View_NonEmptyWhenActive verifies that View returns a non-empty
// string when a toast is active (sliding_in, visible, sliding_out).
func TestToastModel_View_NonEmptyWhenActive(t *testing.T) {
	states := []toastState{toastStateSlidingIn, toastStateVisible, toastStateSlidingOut}
	for _, st := range states {
		entry := &toastEntry{msg: "test", level: ToastInfo, duration: toastDefaultHold}
		m := ToastModel{state: st, current: entry}
		v := m.View(80)
		if v == "" {
			t.Errorf("state %v: View(80) returned empty string, want non-empty", st)
		}
	}
}

// TestToastModel_AnimFrameProgression verifies that receiving toastAnimFrames
// animation ticks during sliding_in transitions the model to visible.
func TestToastModel_AnimFrameProgression(t *testing.T) {
	m := newToastModel()
	m, _ = m.Update(ToastMsg{msg: "anim", level: ToastInfo, duration: toastDefaultHold})

	if m.state != toastStateSlidingIn {
		t.Fatalf("expected sliding_in, got %v", m.state)
	}

	// Feed toastAnimFrames animation ticks; the last one should complete the slide-in.
	for i := 0; i < toastAnimFrames; i++ {
		var cmd tea.Cmd
		m, cmd = m.Update(toastAnimMsg{})
		_ = cmd
	}

	if m.state != toastStateVisible {
		t.Errorf("after %d anim frames: state = %v, want visible", toastAnimFrames, m.state)
	}
	if m.frame != 0 {
		t.Errorf("frame = %d, want 0 after entering visible state", m.frame)
	}
}

// TestToastModel_SlideOutProgression verifies sliding_out completes and the
// model returns to hidden (with no queued toasts).
func TestToastModel_SlideOutProgression(t *testing.T) {
	// Build a model that is already in visible state.
	entry := &toastEntry{msg: "bye", level: ToastInfo, duration: toastDefaultHold}
	m := ToastModel{state: toastStateVisible, current: entry}

	// Hold-done triggers slide-out.
	m, _ = m.Update(toastHoldDoneMsg{})
	if m.state != toastStateSlidingOut {
		t.Fatalf("expected sliding_out after hold-done, got %v", m.state)
	}

	// Feed toastAnimFrames ticks to complete slide-out.
	for i := 0; i < toastAnimFrames; i++ {
		m, _ = m.Update(toastAnimMsg{})
	}

	if m.state != toastStateHidden {
		t.Errorf("after slide-out: state = %v, want hidden", m.state)
	}
}
