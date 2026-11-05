package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

type fakeClient struct {
	out string
	err error
}

func (f fakeClient) Complete(context.Context, string) (string, error) { return f.out, f.err }

func TestClaudeCompleter(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		client  func() (completionClient, error)
		want    string
		wantErr bool
	}{
		{"success", func() (completionClient, error) { return fakeClient{out: "hi"}, nil }, "hi", false},
		{"start error", func() (completionClient, error) { return nil, boom }, "", true},
		{"complete error", func() (completionClient, error) { return fakeClient{err: boom}, nil }, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := (&claudeCompleter{client: tt.client}).Complete(context.Background(), "p")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewClaudeProvider(t *testing.T) {
	c, err := New(config.LLMConfig{Provider: "claude"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := c.(*claudeCompleter); !ok {
		t.Fatalf("got %T, want *claudeCompleter", c)
	}
}
