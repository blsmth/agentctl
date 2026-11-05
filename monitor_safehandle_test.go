package main

import (
	"github.com/blsmth/agentctl/internal/metrics"
	"github.com/blsmth/agentctl/internal/watcher"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/prometheus/client_golang/prometheus"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/state"
)

func TestSafeHandleRecoversAndContinues(t *testing.T) {
	tr := state.Transition{Agent: "a", WindowID: "@1"}
	safeHandle("boom", tr, func() { panic("kaboom") })
	ran := 0
	safeHandle("ok", tr, func() { ran++ })
	if ran != 1 {
		t.Fatalf("ran = %d, want 1", ran)
	}
}

func TestSafeHandleRunsOnce(t *testing.T) {
	ran := 0
	safeHandle("ok", state.Transition{}, func() { ran++ })
	if ran != 1 {
		t.Fatalf("ran = %d, want 1", ran)
	}
}

func TestFireHandlersSurvivesHandleCompletePanic(t *testing.T) {
	orig := handleCompleteFn
	defer func() { handleCompleteFn = orig }()
	calls := 0
	handleCompleteFn = func(state.Transition, config.AgentConfig) {
		calls++
		panic("handler panic")
	}
	m := &monitorModel{}
	tr := state.Transition{Agent: "a", WindowID: "@1", To: state.StatusGone}
	m.fireHandlers(tr, config.AgentConfig{}, true) // must not panic; improve handler still runs
	m.fireHandlers(tr, config.AgentConfig{}, false)
	if calls != 2 {
		t.Fatalf("handleCompleteFn calls = %d, want 2", calls)
	}
}

// TestDetectAndFireTransitionsSurvivesHandlerPanic drives the real monitor
// call path with a panicking HandleComplete and asserts the tick completes,
// RecordTransition still counted the edge, and prev state advanced.
func TestDetectAndFireTransitionsSurvivesHandlerPanic(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := &monitorModel{windows: map[string]*instanceState{}, transitionTracker: metrics.NewTransitionTracker(reg)}
	inst := &instanceState{instance: "go-engineer", base: "go-engineer", hasPrev: true, prevState: state.StatusWorking}

	orig := handleCompleteFn
	t.Cleanup(func() { handleCompleteFn = orig })
	calls := 0
	handleCompleteFn = func(state.Transition, config.AgentConfig) { calls++; panic("boom") }

	m.detectAndFireTransitions(inst, "@1", state.StatusIdle, windowstate.WindowState{State: "idle"}, &config.Config{}, "")

	if calls != 1 {
		t.Fatalf("handleCompleteFn calls = %d, want 1", calls)
	}
	if got := gatherCounterTotal(t, reg, "agentctl_state_transitions_total"); got != 1 {
		t.Errorf("transitions counter = %v, want 1", got)
	}
	if inst.prevState != state.StatusIdle {
		t.Errorf("prevState = %v, want idle (tick must finish after panic)", inst.prevState)
	}
}

// TestReconcileWindowsGonePathSurvivesHandlerPanic covers the Gone path.
func TestReconcileWindowsGonePathSurvivesHandlerPanic(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := &monitorModel{
		windows:           map[string]*instanceState{"@9": {instance: "go-engineer", base: "go-engineer", hasPrev: true, prevState: state.StatusWorking}},
		transitionTracker: metrics.NewTransitionTracker(reg),
		watcher:           watcher.New(),
	}
	orig := handleCompleteFn
	t.Cleanup(func() { handleCompleteFn = orig })
	calls := 0
	handleCompleteFn = func(state.Transition, config.AgentConfig) { calls++; panic("boom") }

	m.reconcileWindows(&config.Config{}, nil, 0)

	if calls != 1 {
		t.Fatalf("handleCompleteFn calls = %d, want 1", calls)
	}
	if got := gatherCounterTotal(t, reg, "agentctl_state_transitions_total"); got != 1 {
		t.Errorf("transitions counter = %v, want 1", got)
	}
	if _, ok := m.windows["@9"]; ok {
		t.Error("gone window not cleaned up after handler panic")
	}
}
