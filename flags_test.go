package main

import (
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
)

func TestAgentEnvOverrides_NoModelOrEffort(t *testing.T) {
	// model and effort_level must NOT appear as env vars — they are passed as CLI flags.
	ac := config.AgentConfig{
		Model:       "opus",
		EffortLevel: "high",
	}
	env := dispatch.AgentEnvOverrides(ac)

	if _, ok := env["ANTHROPIC_MODEL"]; ok {
		t.Error("agentEnvOverrides() set ANTHROPIC_MODEL; model should be passed via --model flag")
	}
	if _, ok := env["CLAUDE_CODE_EFFORT_LEVEL"]; ok {
		t.Error("agentEnvOverrides() set CLAUDE_CODE_EFFORT_LEVEL; effort should be passed via --effort flag")
	}
}

func TestAgentEnvOverrides_MaxThinkingTokens(t *testing.T) {
	ac := config.AgentConfig{MaxThinkingTokens: 8000}
	env := dispatch.AgentEnvOverrides(ac)
	if v, ok := env["MAX_THINKING_TOKENS"]; !ok || v != "8000" {
		t.Errorf("MAX_THINKING_TOKENS = %q, want \"8000\"", v)
	}
}
