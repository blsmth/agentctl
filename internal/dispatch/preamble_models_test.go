package dispatch

import (
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

func TestModelsSectionBackends(t *testing.T) {
	nous := config.BackendConfig{Kind: config.BackendKindNous, Model: "qwen/qwen3-coder-next"}
	ollama := config.BackendConfig{BaseURL: "http://localhost:11434", Model: "qwen3"}
	tests := []struct {
		name     string
		backends map[string]config.BackendConfig
		want     []string
		notWant  []string
	}{
		{"none", nil, nil, []string{"--backend", "Nous Portal"}},
		{"nous", map[string]config.BackendConfig{"nous": nous},
			[]string{"`--backend nous` (Nous Portal, default model qwen/qwen3-coder-next): billed per token in real money", "Use it only when you want a second opinion from a non-Anthropic model", "not as a fallback when plan usage is high."},
			[]string{"Ollama"}},
		{"ollama", map[string]config.BackendConfig{"local": ollama},
			[]string{"`--backend local` (Ollama, local", "free and local"},
			[]string{"Nous Portal", "per token"}},
		{"both", map[string]config.BackendConfig{"nous": nous, "local": ollama},
			[]string{"--backend nous", "--backend local"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := backendGuidance(tt.backends)
			for _, mode := range []string{config.PRReviewAlways, config.PRReviewOptional, config.PRReviewOff} {
				for _, messaging := range []bool{true, false} {
					p := preambleForHarnessBackends(RoleOrchestrator, messaging, mode, lines)
					if !strings.Contains(p, "### Models\n") || !strings.Contains(p, "cheapest model") {
						t.Fatalf("mode %q messaging %v: no Models section", mode, messaging)
					}
					for _, w := range tt.want {
						if !strings.Contains(p, w) {
							t.Errorf("missing %q", w)
						}
					}
					for _, w := range tt.notWant {
						if strings.Contains(p, w) {
							t.Errorf("unexpected %q", w)
						}
					}
				}
			}
		})
	}
}

func TestModelsSectionNotInWorker(t *testing.T) {
	lines := backendGuidance(map[string]config.BackendConfig{"nous": {Kind: config.BackendKindNous, Model: "m"}})
	for _, p := range []string{
		preambleForHarnessBackends(RoleWorker, true, "always", lines),
		preambleWithOrchestratorBackends(RoleWorker, prReviewOrch, "always", lines),
	} {
		if strings.Contains(p, "### Models") || strings.Contains(p, "--backend") {
			t.Error("worker preamble got the Models section")
		}
	}
}

func TestReviewModelWording(t *testing.T) {
	const want = "`--model sonnet` by default, Opus only for hooks/concurrency/auth/security-sensitive PRs"
	for _, mode := range []string{config.PRReviewAlways, config.PRReviewOptional} {
		if p := preambleForHarnessFor(RoleOrchestrator, true, mode); !strings.Contains(p, want) || strings.Contains(p, "for small PRs") {
			t.Errorf("mode %q: review wording wrong", mode)
		}
	}
}
