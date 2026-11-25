package cmd

import (
	"slices"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
)

func TestBuiltinAgentNames(t *testing.T) {
	got := builtinAgentNames(nil)
	if !slices.Contains(got, "general-purpose") || !slices.Contains(got, "Explore") || !slices.IsSorted(got) {
		t.Fatalf("builtinAgentNames(nil) = %v, want sorted list with the built-ins", got)
	}
	got = builtinAgentNames([]agent.Agent{{Name: "Explore"}})
	if slices.Contains(got, "Explore") {
		t.Errorf("a discovered agent must shadow its built-in, got %v", got)
	}
}
