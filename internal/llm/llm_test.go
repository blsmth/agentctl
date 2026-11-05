package llm

import (
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

func TestNew(t *testing.T) {
	t.Run("ollama with model succeeds", func(t *testing.T) {
		c, err := New(config.LLMConfig{
			Provider: "ollama",
			Ollama:   config.OllamaConfig{Model: "llama3"},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c == nil {
			t.Fatal("expected non-nil Completer")
		}
	})

	t.Run("ollama without model fails", func(t *testing.T) {
		_, err := New(config.LLMConfig{Provider: "ollama"})
		if err == nil {
			t.Fatal("expected error for missing model")
		}
	})

	t.Run("unknown provider fails", func(t *testing.T) {
		_, err := New(config.LLMConfig{Provider: "openai"})
		if err == nil {
			t.Fatal("expected error for unknown provider")
		}
	})

	t.Run("empty provider fails", func(t *testing.T) {
		_, err := New(config.LLMConfig{})
		if err == nil {
			t.Fatal("expected error for empty provider")
		}
	})
}
