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
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/llmusage"
	"github.com/blsmth/agentctl/internal/tokens"
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

func TestSnippetCapsRunes(t *testing.T) {
	got := snippet(strings.Repeat("é", 300)+"\nsecond line", "")
	if n := len([]rune(got)); n != 200 || !utf8.ValidString(got) {
		t.Fatalf("snippet = %d runes, valid=%v", n, utf8.ValidString(got))
	}
	if got := snippet("bad key "+testKey, testKey); strings.Contains(got, testKey) {
		t.Fatalf("snippet leaks the key: %q", got)
	}
}

func TestNousComplete402IsNoCredits(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	status := 402
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 200 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"status":402,"message":"Insufficient available credits for this inference request."}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Fixing Things"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	c := newTestNous(t, srv.URL)
	c.name = "nous-test"

	_, err := c.Complete(context.Background(), "p")
	if !errors.Is(err, backendauth.ErrNoCredits) {
		t.Fatalf("err = %v, want ErrNoCredits", err)
	}
	if _, fresh := backendauth.NoCreditsFresh("nous-test", time.Now()); !fresh {
		t.Error("marker not set")
	}
	status = 200
	if _, err := c.Complete(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	if _, fresh := backendauth.NoCreditsFresh("nous-test", time.Now()); fresh {
		t.Error("marker not cleared by success")
	}
}

// ledgerNous is a client wired to a recorder instead of the real ledger.
func ledgerNous(t *testing.T, url string) (*NousClient, *[]llmusage.Record) {
	t.Helper()
	c := newTestNous(t, url)
	var recs []llmusage.Record
	c.record = func(r llmusage.Record) error { recs = append(recs, r); return nil }
	return c, &recs
}

func TestNousUsageReportedAndLedgered(t *testing.T) {
	tokens.SetOverrides(map[string]tokens.ModelOverride{"openai/gpt-oss-120b": {InputPerMTok: 1, OutputPerMTok: 4, CacheReadPerMTok: 0.1}}, 0)
	t.Cleanup(func() { tokens.SetOverrides(nil, 0) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"A Title"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1000,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":400}}}`)
	}))
	defer srv.Close()
	c, recs := ledgerNous(t, srv.URL)
	c.clock = func() time.Time { return time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC) }
	c.name = "nous"

	var comp Completer = c
	ctx := llmusage.WithMeta(context.Background(), llmusage.Meta{Feature: "titles", Window: "@7", Project: "agentctl"})
	if _, err := comp.Complete(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	ur, ok := comp.(UsageReporter)
	if !ok {
		t.Fatal("NousClient should implement UsageReporter")
	}
	if u, have := ur.LastUsage(); !have || u != (Usage{PromptTokens: 1000, CompletionTokens: 50, CachedTokens: 400}) {
		t.Errorf("usage = %+v, %v", u, have)
	}
	if len(*recs) != 1 {
		t.Fatalf("records = %d", len(*recs))
	}
	r := (*recs)[0]
	if !r.OK || r.Feature != "titles" || r.Backend != "nous" || r.Model != "openai/gpt-oss-120b" ||
		r.Window != "@7" || r.Project != "agentctl" || r.PromptTokens != 1000 || r.CompletionTokens != 50 || r.CachedTokens != 400 {
		t.Errorf("record = %+v", r)
	}
	if r.CostUSD == nil || *r.CostUSD < 0.00083999 || *r.CostUSD > 0.00084001 {
		t.Errorf("cost = %v", r.CostUSD)
	}
}

func TestNousLedgerFailuresAndMissingUsage(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantKind string
		wantCost bool
	}{
		{"http error", 500, `oops`, "http_500", false},
		{"no credits", 402, `{"error":{"message":"insufficient credits"}}`, "no_credits", false},
		{"bad json", 200, `not json`, "decode", false},
		{"no choices", 200, `{"choices":[]}`, "no_choices", false},
		{"truncated still billed", 200, `{"choices":[{"message":{"content":"x"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":1024}}`, "invalid_completion", true},
	}
	tokens.SetOverrides(map[string]tokens.ModelOverride{"openai/gpt-oss-120b": {InputPerMTok: 1, OutputPerMTok: 4}}, 0)
	t.Cleanup(func() { tokens.SetOverrides(nil, 0) })
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			c, recs := ledgerNous(t, srv.URL)
			if _, err := c.Complete(context.Background(), "hi"); err == nil {
				t.Fatal("want error")
			}
			if len(*recs) != 1 {
				t.Fatalf("records = %d", len(*recs))
			}
			r := (*recs)[0]
			if r.OK || r.Error != tt.wantKind || (r.CostUSD != nil) != tt.wantCost || r.Feature != "unknown" {
				t.Errorf("record = %+v", r)
			}
		})
	}
}

func TestNousLedgerNoKeyAndTimeout(t *testing.T) {
	c, err := newNousClient("http://127.0.0.1:1", "m/x", func(context.Context) (string, error) { return "", errors.New("none") })
	if err != nil {
		t.Fatal(err)
	}
	var recs []llmusage.Record
	c.record = func(r llmusage.Record) error { recs = append(recs, r); return nil }
	if _, err := c.Complete(context.Background(), "hi"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v", err)
	}
	if len(recs) != 1 || recs[0].Error != "no_key" || recs[0].OK {
		t.Errorf("records = %+v", recs)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer slow.Close()
	c2, recs2 := ledgerNous(t, slow.URL)
	c2.timeout = 50 * time.Millisecond
	_, _ = c2.Complete(context.Background(), "hi")
	if len(*recs2) != 1 || (*recs2)[0].Error != "timeout" {
		t.Errorf("records = %+v", *recs2)
	}
}

func TestNousSuccessWithoutUsageHasNullCost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"T"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	c, recs := ledgerNous(t, srv.URL)
	if _, err := c.Complete(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if _, have := c.LastUsage(); have {
		t.Error("no usage block: LastUsage must report none")
	}
	if r := (*recs)[0]; !r.OK || r.CostUSD != nil {
		t.Errorf("record = %+v", r)
	}
}
