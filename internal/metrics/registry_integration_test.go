package metrics_test

import (
	"testing"

	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/metrics"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/prometheus/client_golang/prometheus"
)

// TestSharedRegistry_CollectorAndTrackerOnSameRegistry verifies that both
// metrics.Collector and metrics.TransitionTracker can be registered on the same
// *prometheus.Registry and that metrics from both are gathered together.
func TestSharedRegistry_CollectorAndTrackerOnSameRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()

	// Register the TransitionTracker on the shared registry.
	tracker := metrics.NewTransitionTracker(reg)

	// Register the Collector on the same registry.
	collector := metrics.NewCollector(reg)

	// Fire a transition through the tracker.
	tracker.RecordTransition(state.Transition{
		Agent:    "go-engineer",
		WindowID: "w1",
		From:     state.StatusWorking,
		To:       state.StatusIdle,
	})

	// Record a token entry through the collector.
	ac := collector.ForAgent("go-engineer")
	ac.OnEntry(logstream.Entry{
		Type: "assistant",
		Usage: &logstream.Usage{
			InputTokens:  100,
			OutputTokens: 50,
		},
		Model: "claude-3-5-sonnet",
	})

	// Gather all metrics.
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	names := make(map[string]bool, len(mfs))
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}

	// Expect metrics from the tracker (state transitions).
	if !names["agentctl_state_transitions_total"] {
		t.Error("expected agentctl_state_transitions_total from TransitionTracker; not found in gathered metrics")
	}

	// Expect metrics from the collector (tokens).
	if !names["agentctl_tokens_total"] {
		t.Error("expected agentctl_tokens_total from Collector; not found in gathered metrics")
	}
}

// TestSharedRegistry_IsolatedRegistriesDoNotLeak verifies that test helpers
// using isolated registries do not affect each other or the shared singleton.
func TestSharedRegistry_IsolatedRegistriesDoNotLeak(t *testing.T) {
	reg1 := prometheus.NewRegistry()
	reg2 := prometheus.NewRegistry()

	metrics.NewCollector(reg1)
	metrics.NewCollector(reg2)

	// Both registries should gather independently without panicking on
	// double-registration (each has its own metric instances).
	if _, err := reg1.Gather(); err != nil {
		t.Fatalf("reg1 Gather: %v", err)
	}
	if _, err := reg2.Gather(); err != nil {
		t.Fatalf("reg2 Gather: %v", err)
	}
}
