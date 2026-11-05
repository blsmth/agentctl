package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

// defaultTimeout is used when cfg.Timeout is empty or unparseable. Callers
// normally go through config.Config.EffectiveLLM, which already applies the
// "30s" default, but NewOllamaClient defends against being constructed
// directly from a zero-value config.OllamaConfig.
const defaultTimeout = 30 * time.Second

// ollamaTemperature and ollamaSeed are fixed across every request: this
// package is used for deterministic, fact-summarizing completions (e.g. the
// work journal), not creative generation.
const (
	ollamaTemperature = 0.2
	ollamaSeed        = 42
)

// OllamaClient is a Completer backed by a local (or remote, e.g. over
// Tailscale) Ollama server's /api/chat endpoint.
type OllamaClient struct {
	host       string
	model      string
	numCtx     int
	numPredict int
	keepAlive  string
	timeout    time.Duration
	httpClient *http.Client
}

// NewOllamaClient constructs an OllamaClient from cfg. Returns an error if
// cfg.Model is empty -- guessing a model name that is not pulled produces a
// confusing 404 from Ollama, so we fail fast and clearly instead.
func NewOllamaClient(cfg config.OllamaConfig) (*OllamaClient, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("llm: config key llm.ollama.model is required")
	}

	host := cfg.Host
	if host == "" {
		host = "http://localhost:11434"
	}
	host = strings.TrimSuffix(host, "/")

	numCtx := cfg.NumCtx
	if numCtx == 0 {
		numCtx = 8192
	}
	numPredict := cfg.NumPredict
	if numPredict == 0 {
		numPredict = 512
	}
	keepAlive := cfg.KeepAlive
	if keepAlive == "" {
		keepAlive = "30m"
	}

	timeout := defaultTimeout
	if cfg.Timeout != "" {
		if d, err := time.ParseDuration(cfg.Timeout); err == nil {
			timeout = d
		}
	}

	return &OllamaClient{
		host:       host,
		model:      cfg.Model,
		numCtx:     numCtx,
		numPredict: numPredict,
		keepAlive:  keepAlive,
		timeout:    timeout,
		httpClient: &http.Client{},
	}, nil
}

// ollamaMessage is a single chat message.
type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ollamaOptions is the "options" object in an Ollama /api/chat request.
type ollamaOptions struct {
	NumCtx      int     `json:"num_ctx"`
	NumPredict  int     `json:"num_predict"`
	Temperature float64 `json:"temperature"`
	Seed        int     `json:"seed"`
}

// ollamaChatRequest is the body of a POST to {host}/api/chat.
type ollamaChatRequest struct {
	Model     string          `json:"model"`
	Stream    bool            `json:"stream"`
	Think     bool            `json:"think"`
	KeepAlive string          `json:"keep_alive"`
	Messages  []ollamaMessage `json:"messages"`
	Options   ollamaOptions   `json:"options"`
}

// ollamaChatResponse is the subset of an Ollama /api/chat response this
// package cares about.
type ollamaChatResponse struct {
	Message struct {
		Content  string `json:"content"`
		Thinking string `json:"thinking"`
	} `json:"message"`
	DoneReason string `json:"done_reason"`
	EvalCount  int    `json:"eval_count"`
}

// Complete sends prompt as a single user message to the configured Ollama
// model and returns the model's response text.
//
// think is always false in the request: reasoning models (qwen3,
// deepseek-r1, gemma4) otherwise spend the entire token budget in a separate
// "thinking" field and return an empty content, which silently looks like
// success without this check.
//
// A response is only returned when done_reason == "stop" AND the trimmed
// content is non-empty; every other outcome -- unreachable host, timeout,
// malformed JSON, non-2xx HTTP status, or an invalid completion -- returns a
// non-nil error and an empty string. Callers must treat every error as
// non-fatal: the configured host may simply be powered off.
func (c *OllamaClient) Complete(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	reqBody := ollamaChatRequest{
		Model:     c.model,
		Stream:    false,
		Think:     false,
		KeepAlive: c.keepAlive,
		Messages: []ollamaMessage{
			{Role: "user", Content: prompt},
		},
		Options: ollamaOptions{
			NumCtx:      c.numCtx,
			NumPredict:  c.numPredict,
			Temperature: ollamaTemperature,
			Seed:        ollamaSeed,
		},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("llm: marshal ollama request: %w", err)
	}

	url := c.host + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("llm: build ollama request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("llm: ollama request to %s failed: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("llm: read ollama response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm: ollama returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var chatResp ollamaChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return "", fmt.Errorf("llm: decode ollama response: %w", err)
	}

	content := strings.TrimSpace(chatResp.Message.Content)
	if chatResp.DoneReason != "stop" || content == "" {
		return "", fmt.Errorf(
			"llm: invalid ollama completion: done_reason=%q eval_count=%d thinking_len=%d",
			chatResp.DoneReason, chatResp.EvalCount, len(chatResp.Message.Thinking),
		)
	}

	return content, nil
}
