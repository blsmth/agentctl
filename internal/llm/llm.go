// Package llm provides a small, single-purpose abstraction over a local LLM
// provider used for one-shot text completion (currently: work journal
// summaries). It is deliberately not a general-purpose SDK.
package llm

import (
	"context"
	"fmt"

	"github.com/blsmth/agentctl/internal/config"
)

// Completer produces a single-turn text completion.
type Completer interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// New constructs a Completer for cfg.Provider. Returns an error if the
// provider is unknown or missing required configuration -- in particular, an
// "ollama" provider with no Model set, since guessing a model name that is
// not pulled produces a confusing 404 from Ollama rather than a clear error
// here.
func New(cfg config.LLMConfig) (Completer, error) {
	switch cfg.Provider {
	case "ollama":
		return NewOllamaClient(cfg.Ollama)
	case "claude":
		// Explicit opt-in only; never a default. See internal/claude.
		return newClaudeCompleter(), nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
}
