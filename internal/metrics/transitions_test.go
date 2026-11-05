package metrics

import (
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/state"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// newTestTransitionTracker creates a TransitionTracker backed by an isolated
// registry without starting an HTTP server.
func newTestTransitionTracker() (*TransitionTracker, *prometheus.Registry) {
	reg := prometheus.NewRegistry()
	tr := NewTransitionTracker(reg)
	return tr, reg
}

func TestTransitionTracker_StateTransition(t *testing.T) {
	tr, _ := newTestTransitionTracker()

	tr.RecordTransition(state.Transition{
		Agent:    "go-engineer",
		WindowID: "win1",
		From:     state.StatusWorking,
		To:       state.StatusIdle,
	})

	count := testutil.ToFloat64(tr.stateTransitions.WithLabelValues("working", "idle", "go-engineer"))
	if count != 1 {
		t.Errorf("stateTransitions{working,idle,go-engineer} = %v, want 1", count)
	}
}

func TestTransitionTracker_GoneCleanup(t *testing.T) {
	tr, _ := newTestTransitionTracker()

	// Seed some per-window state via a Working transition.
	tr.RecordTransition(state.Transition{
		Agent:    "go-engineer",
		WindowID: "win2",
		From:     state.StatusIdle,
		To:       state.StatusWorking,
	})

	// Now mark the window gone.
	tr.RecordTransition(state.Transition{
		Agent:    "go-engineer",
		WindowID: "win2",
		From:     state.StatusWorking,
		To:       state.StatusGone,
	})

	tr.mu.Lock()
	defer tr.mu.Unlock()

	if tr.seenWindows["win2"] {
		t.Error("seenWindows should be cleaned up after Gone")
	}
	if _, ok := tr.spawnTimes["win2"]; ok {
		t.Error("spawnTimes should be cleaned up after Gone")
	}
	if _, ok := tr.workingStart["win2"]; ok {
		t.Error("workingStart should be cleaned up after Gone")
	}
}

func TestTransitionTracker_WorkingDuration(t *testing.T) {
	tr, _ := newTestTransitionTracker()

	// Transition into Working to start the clock.
	tr.RecordTransition(state.Transition{
		Agent:    "go-engineer",
		WindowID: "win3",
		From:     state.StatusIdle,
		To:       state.StatusWorking,
	})

	// Small sleep so the observed duration is positive.
	time.Sleep(5 * time.Millisecond)

	// Transition out of Working to record the duration.
	tr.RecordTransition(state.Transition{
		Agent:    "go-engineer",
		WindowID: "win3",
		From:     state.StatusWorking,
		To:       state.StatusIdle,
	})

	// The histogram should have exactly one observation.
	count := testutil.CollectAndCount(tr.workingDuration)
	if count == 0 {
		t.Error("workingDuration histogram should have at least one series after Working->Idle")
	}
}

func TestTransitionTracker_FailureDetection(t *testing.T) {
	tr, _ := newTestTransitionTracker()

	// Working->Gone is treated as a likely crash/failure.
	tr.RecordTransition(state.Transition{
		Agent:    "go-engineer",
		WindowID: "win4",
		From:     state.StatusWorking,
		To:       state.StatusGone,
	})

	count := testutil.ToFloat64(tr.failures.WithLabelValues("go-engineer"))
	if count != 1 {
		t.Errorf("failures{go-engineer} = %v, want 1", count)
	}
}

func TestTransitionTracker_ActiveGaugeNeverNegative(t *testing.T) {
	W, I, B, G := state.StatusWorking, state.StatusIdle, state.StatusBlocked, state.StatusGone
	type step struct {
		win      string
		from, to state.AgentStatus
	}
	tests := []struct {
		name                   string
		steps                  []step
		working, idle, blocked float64
	}{
		{"first seen working->idle", []step{{"@1", W, I}}, 0, 1, 0},
		{"idle->gone returns to zero", []step{{"@1", W, I}, {"@1", I, G}}, 0, 0, 0},
		{"two windows interleaved", []step{{"@1", W, I}, {"@2", I, B}, {"@1", I, W}, {"@2", B, W}}, 2, 0, 0},
		{"gone for never-counted window", []step{{"@1", I, G}}, 0, 0, 0},
		{"repeated same-state no-op", []step{{"@1", W, I}, {"@1", I, I}, {"@1", I, I}}, 0, 1, 0},
		{"double gone", []step{{"@1", W, I}, {"@1", I, G}, {"@1", I, G}}, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, _ := newTestTransitionTracker()
			for _, s := range tt.steps {
				tr.RecordTransition(state.Transition{WindowID: s.win, Agent: "a", From: s.from, To: s.to})
			}
			for st, want := range map[state.AgentStatus]float64{W: tt.working, I: tt.idle, B: tt.blocked} {
				if got := testutil.ToFloat64(tr.agentsActive.WithLabelValues(st.String())); got != want {
					t.Errorf("%s = %v, want %v", st, got, want)
				}
			}
			if len(tr.counted) > 2 {
				t.Errorf("counted map leaked: %v", tr.counted)
			}
		})
	}
}
