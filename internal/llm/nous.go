package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
)

// nousTimeout bounds one chat-completions request, key resolution included.
const nousTimeout = 15 * time.Second

// nousMaxBody bounds how much of a response is read.
const nousMaxBody = 1 << 20

// nousMaxTokens is the completion budget. Reasoning models (gpt-oss) spend
// part of it on their reasoning field before writing content, so it is well
// above what a short answer needs.
const nousMaxTokens = 1024

// nousTemperature keeps short summarizing completions stable.
const nousTemperature = 0.2

// ErrNoKey wraps a failure to resolve a backend's API key, so callers can tell
// "not configured" from "the request failed".
var ErrNoKey = errors.New("llm: no API key")

// KeyFunc resolves the bearer token for a request.
type KeyFunc func(ctx context.Context) (string, error)

// NousClient is a Completer backed by an OpenAI-compatible
// /v1/chat/completions endpoint on a Nous Portal backend.
type NousClient struct {
	name       string // backend name, "" when built for tests; keys the credit marker
	baseURL    string
	model      string
	key        KeyFunc
	timeout    time.Duration
	httpClient *http.Client
}

// NewNousClient builds a client for the nous-kind backend name, sending model.
// The key is resolved through backendauth on every Complete call and never
// stored in the client.
func NewNousClient(name string, b config.BackendConfig, model string) (*NousClient, error) {
	if !b.IsNous() {
		return nil, fmt.Errorf("llm: backend %q is not kind %q", name, config.BackendKindNous)
	}
	base := b.BaseURL
	if base == "" {
		base = config.NousDefaultBaseURL
	}
	c, err := newNousClient(base, model, func(ctx context.Context) (string, error) {
		return backendauth.Resolve(ctx, name, b)
	})
	if err != nil {
		return nil, err
	}
	c.name = name
	return c, nil
}

// newNousClient is the constructor tests use: any base URL, any key source.
func newNousClient(baseURL, model string, key KeyFunc) (*NousClient, error) {
	if model == "" {
		return nil, errors.New("llm: nous model is required")
	}
	if key == nil {
		return nil, errors.New("llm: nous key source is required")
	}
	return &NousClient{
		baseURL:    strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1"),
		model:      model,
		key:        key,
		timeout:    nousTimeout,
		httpClient: &http.Client{},
	}, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
}

// chatResponse is the subset of a chat-completions response used here.
// Content is a pointer because reasoning models may return null content
// alongside a populated reasoning field.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content          *string `json:"content"`
			Reasoning        string  `json:"reasoning"`
			ReasoningContent string  `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// Complete sends prompt as one user message and returns the assistant's
// content, trimmed. Reasoning fields are ignored. Every failure (key,
// network, timeout, non-200, malformed or truncated or blank completion) is an
// error with an empty string; no error carries the key.
func (c *NousClient) Complete(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	key, err := c.key(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoKey, err)
	}

	payload, err := json.Marshal(chatRequest{
		Model:       c.model,
		Messages:    []chatMessage{{Role: "user", Content: prompt}},
		MaxTokens:   nousMaxTokens,
		Temperature: nousTemperature,
	})
	if err != nil {
		return "", fmt.Errorf("llm: marshal nous request: %w", err)
	}
	url := c.baseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("llm: build nous request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	backendauth.SetUserAgent(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm: nous request to %s failed: %w", url, redactErr(err, key))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, nousMaxBody))
	if err != nil {
		return "", fmt.Errorf("llm: read nous response: %w", redactErr(err, key))
	}
	if backendauth.IsNoCredits(resp.StatusCode, body) {
		if c.name != "" {
			backendauth.MarkNoCredits(c.name)
		}
		return "", fmt.Errorf("llm: nous returned HTTP %d: %w", resp.StatusCode, backendauth.ErrNoCredits)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm: nous returned HTTP %d: %s", resp.StatusCode, snippet(string(body), key))
	}

	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return "", fmt.Errorf("llm: decode nous response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return "", errors.New("llm: nous response has no choices")
	}
	ch := cr.Choices[0]
	var content string
	if ch.Message.Content != nil {
		content = strings.TrimSpace(*ch.Message.Content)
	}
	if content == "" || (ch.FinishReason != "" && ch.FinishReason != "stop") {
		return "", fmt.Errorf("llm: invalid nous completion: finish_reason=%q content_len=%d reasoning_len=%d",
			ch.FinishReason, len(content), len(ch.Message.Reasoning)+len(ch.Message.ReasoningContent))
	}
	if c.name != "" {
		backendauth.ClearNoCredits(c.name)
	}
	return content, nil
}

// snippet is the first line of a response body, capped, with the key removed.
func snippet(body, key string) string {
	s := strings.TrimSpace(strings.SplitN(body, "\n", 2)[0])
	if key != "" {
		s = strings.ReplaceAll(s, key, "[redacted]")
	}
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}

// redactErr returns err with any occurrence of key replaced.
func redactErr(err error, key string) error {
	if key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "[redacted]"))
}
