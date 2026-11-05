package llm

import (
	"context"
	"fmt"

	"github.com/blsmth/agentctl/internal/claude"
)

// claudeCompleter adapts the internal/claude subprocess client to Completer.
// The subprocess is started lazily on the first Complete call (via the
// claude package's singleton), so selecting this provider in New never spawns
// anything by itself and a missing `claude` binary surfaces as a Complete
// error the caller can log, not a construction failure.
//
// This provider is retained deliberately and is never selected by default;
// see the internal/claude package doc.
type claudeCompleter struct {
	// client is injectable for tests; production uses claude.DefaultClient.
	client func() (completionClient, error)
}

// completionClient is the subset of *claude.Client the adapter needs.
type completionClient interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

var _ completionClient = (*claude.Client)(nil)

func newClaudeCompleter() Completer {
	return &claudeCompleter{
		client: func() (completionClient, error) { return claude.DefaultClient() },
	}
}

// Complete implements Completer.
func (c *claudeCompleter) Complete(ctx context.Context, prompt string) (string, error) {
	cl, err := c.client()
	if err != nil {
		return "", fmt.Errorf("llm: starting claude client: %w", err)
	}
	out, err := cl.Complete(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("llm: claude completion: %w", err)
	}
	return out, nil
}
