package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

const testKey = "sk-test-SECRET-123"

func staticKey(ctx context.Context) (string, error) { return testKey, nil }

func newTestNous(t *testing.T, url string) *NousClient {
	t.Helper()
	c, err := newNousClient(url, "openai/gpt-oss-120b", staticKey)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNousCompleteRequest(t *testing.T) {
	var gotPath, gotAuth, gotUA string
	var got chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotUA = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":" Organizing your Photos \n"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	// A trailing /v1 on the base URL must not double up.
	out, err := newTestNous(t, srv.URL+"/v1/").Complete(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Organizing your Photos" {
		t.Errorf("content = %q", out)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer "+testKey {
		t.Errorf("auth header = %q", gotAuth)
	}
	if !strings.HasPrefix(gotUA, "agentctl/") {
		t.Errorf("user agent = %q", gotUA)
	}
	if got.Model != "openai/gpt-oss-120b" || got.Stream || len(got.Messages) != 1 ||
		got.Messages[0].Role != "user" || got.Messages[0].Content != "hello" || got.MaxTokens <= 0 {
		t.Errorf("request = %+v", got)
	}
}

func TestNousCompleteResponses(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    string
		wantErr string
	}{
		{
			name: "gpt-oss content plus reasoning uses content only",
			body: `{"choices":[{"message":{"role":"assistant","content":"Fixing Hub Sort Order","reasoning":"The user wants a title. Let me think...","reasoning_content":"more thoughts"},"finish_reason":"stop"}]}`,
			want: "Fixing Hub Sort Order",
		},
		{
			name:    "reasoning only, null content",
			body:    `{"choices":[{"message":{"role":"assistant","content":null,"reasoning":"thinking forever"},"finish_reason":"length"}]}`,
			wantErr: "invalid nous completion",
		},
		{
			name:    "blank content",
			body:    `{"choices":[{"message":{"content":"   "},"finish_reason":"stop"}]}`,
			wantErr: "invalid nous completion",
		},
		{
			name:    "truncated",
			body:    `{"choices":[{"message":{"content":"Organizing your"},"finish_reason":"length"}]}`,
			wantErr: "finish_reason=\"length\"",
		},
		{name: "no choices", body: `{"choices":[]}`, wantErr: "no choices"},
		{name: "malformed", body: `not json`, wantErr: "decode"},
		{
			name:    "http error never echoes the key",
			status:  http.StatusUnauthorized,
			body:    `{"error":"bad key ` + testKey + `"}`,
			wantErr: "HTTP 401",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			out, err := newTestNous(t, srv.URL).Complete(context.Background(), "p")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), testKey) {
					t.Fatalf("error leaks the key: %v", err)
				}
				if out != "" {
					t.Fatalf("out = %q on error", out)
				}
				return
			}
			if err != nil || out != tt.want {
				t.Fatalf("Complete = %q, %v; want %q", out, err, tt.want)
			}
		})
	}
}

func TestNousCompleteKeyError(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	c, err := newNousClient(srv.URL, "m", func(context.Context) (string, error) {
		return "", errors.New("no API key stored")
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), "p"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
	if called {
		t.Fatal("request sent without a key")
	}
}

func TestNousCompleteTimeout(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(done)
	c := newTestNous(t, srv.URL)
	c.timeout = 50 * time.Millisecond
	if _, err := c.Complete(context.Background(), "p"); err == nil {
		t.Fatal("expected a timeout error")
	}
}

func TestNewNousClient(t *testing.T) {
	if _, err := NewNousClient("ol", config.BackendConfig{Kind: "ollama", Model: "m"}, "m"); err == nil {
		t.Error("non-nous backend accepted")
	}
	c, err := NewNousClient("nous", config.BackendConfig{Kind: config.BackendKindNous, Model: "m"}, "x/y")
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != config.NousDefaultBaseURL || c.model != "x/y" || c.timeout != nousTimeout {
		t.Errorf("client = %+v", c)
	}
	if _, err := NewNousClient("nous", config.BackendConfig{Kind: config.BackendKindNous}, ""); err == nil {
		t.Error("empty model accepted")
	}
}
