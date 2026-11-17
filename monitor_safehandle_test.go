package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
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

func TestFireHandlersSurvivesImprovePanic(t *testing.T) {
	orig := improveHandleFn
	defer func() { improveHandleFn = orig }()
	calls := 0
	improveHandleFn = func(state.Transition, config.AgentConfig) {
		calls++
		panic("handler panic")
	}
	m := &monitorModel{}
	tr := state.Transition{Agent: "a", WindowID: "@1", To: state.StatusGone}
	m.fireHandlers(tr, config.AgentConfig{}) // must not panic
	if calls != 1 {
		t.Fatalf("improveHandleFn calls = %d, want 1", calls)
	}
}

// TestDetectAndFireTransitionsSurvivesHandlerPanic drives the real monitor
// call path with a panicking improve.HandleTransition and asserts the tick completes,
// and prev state advanced.
func TestDetectAndFireTransitionsSurvivesHandlerPanic(t *testing.T) {
	m := &monitorModel{windows: map[string]*instanceState{}}
	inst := &instanceState{instance: "go-engineer", base: "go-engineer", hasPrev: true, prevState: state.StatusWorking}

	orig := improveHandleFn
	t.Cleanup(func() { improveHandleFn = orig })
	calls := 0
	improveHandleFn = func(state.Transition, config.AgentConfig) { calls++; panic("boom") }

	m.detectAndFireTransitions(inst, "@1", state.StatusIdle, windowstate.WindowState{State: "idle"}, &config.Config{}, "")

	if calls != 1 {
		t.Fatalf("improveHandleFn calls = %d, want 1", calls)
	}
	if inst.prevState != state.StatusIdle {
		t.Errorf("prevState = %v, want idle (tick must finish after panic)", inst.prevState)
	}
}

// TestReconcileWindowsGonePathSurvivesHandlerPanic covers the Gone path.
func TestReconcileWindowsGonePathSurvivesHandlerPanic(t *testing.T) {
	seedGoneRecord(t, "@9", "working", "go-engineer")
	m := &monitorModel{
		windows: map[string]*instanceState{"@9": {instance: "go-engineer", base: "go-engineer", hasPrev: true, prevState: state.StatusWorking}},
	}
	orig := improveHandleFn
	t.Cleanup(func() { improveHandleFn = orig })
	calls := 0
	improveHandleFn = func(state.Transition, config.AgentConfig) { calls++; panic("boom") }

	m.reconcileWindows(&config.Config{}, nil, 0)
	m.reconcileWindows(&config.Config{}, nil, 0)

	if calls != 1 {
		t.Fatalf("improveHandleFn calls = %d, want 1", calls)
	}
	if _, ok := m.windows["@9"]; ok {
		t.Error("gone window not cleaned up after handler panic")
	}
}

// seedGoneRecord points windowstate and mailbox at temp dirs and writes the
// record the monitor's shared Gone claim consumes for window id.
func seedGoneRecord(t *testing.T, id, st, instance string) {
	t.Helper()
	windowstate.SetDirForTest(filepath.Join(t.TempDir(), "windows"))
	if err := os.MkdirAll(windowstate.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	mailbox.SetDirForTest(t.TempDir())
	rec := windowstate.WindowState{State: st, Instance: instance, AgentName: tmux.BaseAgentName(instance)}
	if err := windowstate.Write(id, rec); err != nil {
		t.Fatal(err)
	}
}
