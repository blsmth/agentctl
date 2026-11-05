package llm

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

func newTestClient(t *testing.T, host string, overrides func(*config.OllamaConfig)) *OllamaClient {
	t.Helper()
	cfg := config.OllamaConfig{
		Host:       host,
		Model:      "test-model",
		NumCtx:     16384,
		NumPredict: 256,
		KeepAlive:  "45m",
		Timeout:    "1s",
	}
	if overrides != nil {
		overrides(&cfg)
	}
	c, err := NewOllamaClient(cfg)
	if err != nil {
		t.Fatalf("NewOllamaClient: %v", err)
	}
	return c
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// TestNewOllamaClient_RequiresModel covers the "Model has no default"
// requirement: an ollama provider with no model configured must fail fast
// with a clear error rather than let Ollama 404 on a guessed name.
func TestNewOllamaClient_RequiresModel(t *testing.T) {
	_, err := NewOllamaClient(config.OllamaConfig{Host: "http://localhost:11434"})
	if err == nil {
		t.Fatal("expected error for missing model, got nil")
	}
	if !strings.Contains(err.Error(), "llm.ollama.model") {
		t.Errorf("error should name the config key, got: %v", err)
	}
}

// TestComplete_RequestBody asserts the exact serialized request body,
// specifically the three hard-requirement fields: think:false, num_ctx, and
// keep_alive. A silent regression in any of these is invisible without this
// kind of exact assertion.
func TestComplete_RequestBody(t *testing.T) {
	var gotBody []byte
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		gotBody = body
		writeJSON(t, w, map[string]any{
			"message":     map[string]string{"content": "hello", "thinking": ""},
			"done_reason": "stop",
			"eval_count":  10,
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	out, err := c.Complete(context.Background(), "say hi")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != "hello" {
		t.Errorf("out = %q, want %q", out, "hello")
	}
	if gotPath != "/api/chat" {
		t.Errorf("path = %q, want /api/chat", gotPath)
	}

	var req ollamaChatRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("unmarshal request body: %v (body=%s)", err, gotBody)
	}

	if req.Think {
		t.Error("think must be false")
	}
	if req.Options.NumCtx != 16384 {
		t.Errorf("num_ctx = %d, want 16384", req.Options.NumCtx)
	}
	if req.KeepAlive != "45m" {
		t.Errorf("keep_alive = %q, want 45m", req.KeepAlive)
	}
	if req.Model != "test-model" {
		t.Errorf("model = %q, want test-model", req.Model)
	}
	if req.Stream {
		t.Error("stream must be false")
	}
	if req.Options.NumPredict != 256 {
		t.Errorf("num_predict = %d, want 256", req.Options.NumPredict)
	}
	if req.Options.Temperature != 0.2 {
		t.Errorf("temperature = %v, want 0.2", req.Options.Temperature)
	}
	if req.Options.Seed != 42 {
		t.Errorf("seed = %d, want 42", req.Options.Seed)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Content != "say hi" {
		t.Errorf("messages = %+v, want single user message 'say hi'", req.Messages)
	}

	// Also assert the exact raw JSON bytes for think/num_ctx/keep_alive, since
	// asserting only the decoded struct wouldn't catch e.g. a key rename that
	// happens to decode to the same Go field by coincidence.
	raw := string(gotBody)
	for _, want := range []string{`"think":false`, `"num_ctx":16384`, `"keep_alive":"45m"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("raw request body missing %q; body=%s", want, raw)
		}
	}
}

func TestComplete_InvalidResponses(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "done_reason length",
			handler: func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, map[string]any{
					"message":     map[string]string{"content": "trunc", "thinking": ""},
					"done_reason": "length",
					"eval_count":  600,
				})
			},
		},
		{
			name: "empty content nonempty thinking",
			handler: func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, map[string]any{
					"message":     map[string]string{"content": "", "thinking": "lots of thinking here"},
					"done_reason": "stop",
					"eval_count":  600,
				})
			},
		},
		{
			name: "empty content empty thinking",
			handler: func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, map[string]any{
					"message":     map[string]string{"content": "", "thinking": ""},
					"done_reason": "stop",
					"eval_count":  0,
				})
			},
		},
		{
			name: "malformed json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{not json`))
			},
		},
		{
			name: "http 500",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("internal error"))
			},
		},
		{
			name: "http 404 model not pulled",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"model not found"}`))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			c := newTestClient(t, srv.URL, nil)
			out, err := c.Complete(context.Background(), "prompt")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if out != "" {
				t.Errorf("out = %q, want empty string on error", out)
			}
		})
	}
}

// TestComplete_Timeout ensures a server that sleeps past the context
// deadline returns an error rather than hanging.
func TestComplete_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		writeJSON(t, w, map[string]any{
			"message":     map[string]string{"content": "too late", "thinking": ""},
			"done_reason": "stop",
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, func(cfg *config.OllamaConfig) {
		cfg.Timeout = "50ms"
	})

	start := time.Now()
	out, err := c.Complete(context.Background(), "prompt")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if out != "" {
		t.Errorf("out = %q, want empty string on error", out)
	}
	if elapsed > 400*time.Millisecond {
		t.Errorf("Complete took %v, expected to time out well before the server's 500ms sleep", elapsed)
	}
}

// TestComplete_ConnectionRefused asserts a closed-port target returns an
// error without panicking, since the configured host may be a remote box
// that is simply powered off.
func TestComplete_ConnectionRefused(t *testing.T) {
	// Bind to a port, grab its address, then close it immediately so nothing
	// is listening -- guarantees a real "connection refused" rather than
	// relying on a hardcoded port that might be in use.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("ln.Close: %v", err)
	}

	c := newTestClient(t, "http://"+addr, nil)
	out, err := c.Complete(context.Background(), "prompt")
	if err == nil {
		t.Fatal("expected connection-refused error, got nil")
	}
	if out != "" {
		t.Errorf("out = %q, want empty string on error", out)
	}
}

// TestComplete_HostTrailingSlash ensures a trailing slash on Host doesn't
// produce a double-slash path.
func TestComplete_HostTrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		writeJSON(t, w, map[string]any{
			"message":     map[string]string{"content": "ok", "thinking": ""},
			"done_reason": "stop",
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL+"/", nil)
	if _, err := c.Complete(context.Background(), "prompt"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotPath != "/api/chat" {
		t.Errorf("path = %q, want /api/chat", gotPath)
	}
}
