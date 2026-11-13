package dispatch

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

func testCfg(baseURL string) *config.Config {
	return &config.Config{Backends: map[string]config.BackendConfig{
		"fenrir": {BaseURL: baseURL, Model: "qwen3.6:35b-a3b", AuthToken: "ollama", MaxContextTokens: 131072},
	}}
}

func TestBackendEnv(t *testing.T) {
	tests := []struct {
		name string
		b    config.BackendConfig
		want map[string]string
	}{
		{
			"full",
			config.BackendConfig{BaseURL: "http://h:11434", Model: "m", AuthToken: "tok", MaxContextTokens: 1000},
			map[string]string{
				"ANTHROPIC_BASE_URL": "http://h:11434", "ANTHROPIC_AUTH_TOKEN": "tok", "ANTHROPIC_API_KEY": "",
				"CLAUDE_CODE_MAX_CONTEXT_TOKENS": "1000",
				"ANTHROPIC_DEFAULT_OPUS_MODEL":   "m", "ANTHROPIC_DEFAULT_SONNET_MODEL": "m",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL": "m", "CLAUDE_CODE_SUBAGENT_MODEL": "m",
			},
		},
		{
			"zero context omitted, default token",
			config.BackendConfig{BaseURL: "http://h", Model: "m"},
			map[string]string{
				"ANTHROPIC_BASE_URL": "http://h", "ANTHROPIC_AUTH_TOKEN": "ollama", "ANTHROPIC_API_KEY": "",
				"ANTHROPIC_DEFAULT_OPUS_MODEL": "m", "ANTHROPIC_DEFAULT_SONNET_MODEL": "m",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL": "m", "CLAUDE_CODE_SUBAGENT_MODEL": "m",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BackendEnv(tt.b)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("BackendEnv = %v, want %v", got, tt.want)
			}
			if v, ok := got["ANTHROPIC_API_KEY"]; !ok || v != "" {
				t.Errorf("ANTHROPIC_API_KEY must be present and empty")
			}
		})
	}
}

func TestResolveBackendPrecedence(t *testing.T) {
	cfg := testCfg("http://h")
	tests := []struct {
		name, agentBackend, override, explicit, agentModel string
		wantModel                                          string
		wantBackend                                        string
		wantErr                                            bool
	}{
		{"backend model beats agent model", "fenrir", "", "", "opus", "qwen3.6:35b-a3b", "fenrir", false},
		{"explicit model beats backend", "fenrir", "", "sonnet", "opus", "sonnet", "fenrir", false},
		{"no backend keeps agent model", "", "", "", "opus", "opus", "", false},
		{"none clears agent backend", "fenrir", "none", "", "opus", "opus", "", false},
		{"override selects backend", "", "fenrir", "", "opus", "qwen3.6:35b-a3b", "fenrir", false},
		{"unknown backend errors", "ghost", "", "", "opus", "opus", "", true},
		{"unknown override errors", "", "ghost", "", "opus", "opus", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// explicit model arrives already applied to ac.Model, as in Launch.
			ac := config.AgentConfig{Backend: tt.agentBackend, Model: tt.agentModel}
			if tt.explicit != "" {
				ac.Model = tt.explicit
			}
			be, err := ResolveBackend(cfg, &ac, tt.override, tt.explicit)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			got := ""
			if be != nil {
				got = be.Name
			}
			if got != tt.wantBackend {
				t.Errorf("backend = %q, want %q", got, tt.wantBackend)
			}
			if !tt.wantErr && ac.Model != tt.wantModel {
				t.Errorf("model = %q, want %q", ac.Model, tt.wantModel)
			}
		})
	}
}

func tagsServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPreflightBackend(t *testing.T) {
	const tags = `{"models":[{"name":"qwen3.6:35b-a3b","model":"qwen3.6:35b-a3b"},{"name":"llama3:8b","model":"llama3:8b"}]}`
	tests := []struct {
		name    string
		status  int
		body    string
		model   string
		wantErr string
	}{
		{"model present", 200, tags, "qwen3.6:35b-a3b", ""},
		{"model missing lists available", 200, tags, "qwen3.6:35b", "llama3:8b"},
		{"non-ollama 404 skips check", 404, "nope", "anything", ""},
		{"non-ollama 200 skips check", 200, `{"hello":"world"}`, "anything", ""},
		{"non-json 200 skips check", 200, `<html>`, "anything", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := tagsServer(t, tt.status, tt.body)
			err := PreflightBackend(config.BackendConfig{BaseURL: srv.URL + "/", Model: tt.model})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestPreflightBackendUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	old := preflightTimeout
	preflightTimeout = time.Second
	t.Cleanup(func() { preflightTimeout = old })
	err := PreflightBackend(config.BackendConfig{BaseURL: url, Model: "m"})
	if err == nil || !strings.Contains(err.Error(), url) {
		t.Fatalf("err = %v, want unreachable error naming %s", err, url)
	}
}

func TestPrepareRestore(t *testing.T) {
	srv := tagsServer(t, 200, `{"models":[{"name":"qwen3.6:35b-a3b"}]}`)
	cfg := testCfg(srv.URL)
	cfg.Agents = map[string]config.AgentConfig{"go-engineer": {Model: "opus", Backend: "fenrir"}}

	t.Run("persisted backend re-applied", func(t *testing.T) {
		ac, env, err := prepareRestore(cfg, RestoreRequest{AgentName: "go-engineer", Backend: "fenrir"})
		if err != nil {
			t.Fatal(err)
		}
		if env["ANTHROPIC_BASE_URL"] != srv.URL || env[EnvBackend] != "fenrir" {
			t.Errorf("env = %v", env)
		}
		if ac.Model != "qwen3.6:35b-a3b" {
			t.Errorf("model = %q", ac.Model)
		}
	})
	t.Run("missing backend skips window", func(t *testing.T) {
		if _, _, err := prepareRestore(cfg, RestoreRequest{AgentName: "go-engineer", Backend: "gone"}); err == nil {
			t.Fatal("expected error for missing backend")
		}
	})
	t.Run("no persisted backend stays on Anthropic", func(t *testing.T) {
		ac, env, err := prepareRestore(cfg, RestoreRequest{AgentName: "go-engineer"})
		if err != nil {
			t.Fatal(err)
		}
		if env != nil || ac.Model != "opus" {
			t.Errorf("env=%v model=%q", env, ac.Model)
		}
	})
}

func TestPrepareBackendEnvAndPreflight(t *testing.T) {
	srv := tagsServer(t, 200, `{"models":[{"name":"other"}]}`)
	cfg := testCfg(srv.URL)
	ac := config.AgentConfig{Backend: "fenrir"}
	env := map[string]string{}
	if _, err := PrepareBackend(cfg, &ac, "", "", env); err == nil {
		t.Fatal("expected preflight failure for missing model")
	}
	if len(env) != 0 {
		t.Errorf("env must stay untouched on failure: %v", env)
	}
}
