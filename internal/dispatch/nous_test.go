package dispatch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
)

const nousCatalogJSON = `{"data":[
 {"id":"qwen/qwen3-coder-next","context_length":262144,"endpoints":["messages","chat"]},
 {"id":"qwen/qwen3-max","context_length":32768},
 {"id":"openai/gpt-oss-120b","context_length":131072,"endpoints":["chat"]}
]}`

type memStore struct{ m map[string]string }

func (s *memStore) Get(n string) (string, error) {
	if v, ok := s.m[n]; ok {
		return v, nil
	}
	return "", backendauth.ErrNotFound
}
func (s *memStore) Set(n, k string) error { s.m[n] = k; return nil }
func (s *memStore) Delete(n string) error { delete(s.m, n); return nil }
func (s *memStore) Source() string        { return "mem" }

// nousEnvSetup isolates preflight from the real machine: no managed or user
// settings, a fake credential store, and (optionally) a runtime dir.
func nousEnvSetup(t *testing.T, keys map[string]string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	oldManaged := managedSettingsPaths
	managedSettingsPaths = nil
	oldStore := backendauth.DefaultStore
	backendauth.DefaultStore = &memStore{m: keys}
	withRuntimeDir(t, "")
	t.Cleanup(func() { managedSettingsPaths = oldManaged; backendauth.DefaultStore = oldStore })
}

func catalogServer(t *testing.T, status int, body string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("catalog request must not carry credentials")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNousBackendEnvInvariant(t *testing.T) {
	b := config.BackendConfig{Kind: "nous", BaseURL: config.NousDefaultBaseURL, Model: "a/base",
		Models: &config.BackendModels{Haiku: "b/cheap"}, MaxContextTokens: 1234}
	env := BackendEnv(b)
	for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_SUBAGENT_MODEL_FORCE"} {
		if _, ok := env[k]; ok {
			t.Errorf("nous env must not contain %s", k)
		}
	}
	want := map[string]string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL":           "a/base",
		"ANTHROPIC_DEFAULT_SONNET_MODEL":         "a/base",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":          "b/cheap",
		"CLAUDE_CODE_SUBAGENT_MODEL":             "a/base",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS":         "1234",
		"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": "1",
		"AGENTCTL_UNSET_ENV":                     "ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("unexpected extra vars: %v", env)
	}
	for k, v := range env {
		if strings.Contains(v, "sk-") {
			t.Errorf("env[%s] looks like a secret", k)
		}
	}
	b.MaxContextTokens = 0
	if _, ok := BackendEnv(b)["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]; ok {
		t.Error("zero context must be omitted")
	}
}

func TestResolveBackendNousExplicitModel(t *testing.T) {
	cfg := &config.Config{Backends: map[string]config.BackendConfig{
		"nous": {Kind: "nous", BaseURL: config.NousDefaultBaseURL, Model: "qwen/qwen3-coder-next"},
	}}
	tests := []struct {
		model   string
		wantErr bool
	}{
		{"", false},
		{"qwen/qwen3-max", false},
		{"~anthropic/claude-sonnet-latest", false},
		{"claude-sonnet-4-5", true},
		{"opus", true},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			ac := config.AgentConfig{}
			_, err := ResolveBackend(cfg, &ac, "nous", tt.model)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPreflightNous(t *testing.T) {
	good := config.BackendConfig{Kind: "nous", Model: "qwen/qwen3-coder-next"}
	tests := []struct {
		name    string
		status  int
		body    string
		b       config.BackendConfig
		keys    map[string]string
		wantErr string
		wantCtx int
	}{
		{"present, ctx autofill", 200, nousCatalogJSON, good, map[string]string{"nous": "k"}, "", 262144},
		{"alias present", 200, nousCatalogJSON, config.BackendConfig{Kind: "nous", Model: "qwen/qwen3-coder-next", Models: &config.BackendModels{Haiku: "openai/gpt-oss-120b"}}, map[string]string{"nous": "k"}, "", 262144},
		{"missing with suggestions", 200, nousCatalogJSON, config.BackendConfig{Kind: "nous", Model: "qwen/nope"}, map[string]string{"nous": "k"}, "similar: qwen/qwen3-coder-next, qwen/qwen3-max", 0},
		{"missing alias", 200, nousCatalogJSON, config.BackendConfig{Kind: "nous", Model: "qwen/qwen3-coder-next", Models: &config.BackendModels{Haiku: "zzz/none"}}, map[string]string{"nous": "k"}, `"zzz/none" is not in the catalog`, 0},
		{"500", 500, `oops`, good, map[string]string{"nous": "k"}, "HTTP 500", 0},
		{"bad json", 200, `{"data":[]}`, good, map[string]string{"nous": "k"}, "not a valid model list", 0},
		{"no credential", 200, nousCatalogJSON, good, map[string]string{}, "agentctl backend login nous", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nousEnvSetup(t, tt.keys)
			srv := catalogServer(t, tt.status, tt.body, nil)
			tt.b.BaseURL = srv.URL
			info, err := preflightNous("nous", tt.b, "")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if info.ContextLength != tt.wantCtx {
					t.Errorf("ContextLength = %d, want %d", info.ContextLength, tt.wantCtx)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestPreflightNousUnreachable(t *testing.T) {
	nousEnvSetup(t, map[string]string{"nous": "k"})
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := preflightNous("nous", config.BackendConfig{Kind: "nous", BaseURL: url, Model: "a/b"}, "")
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v", err)
	}
}

func TestPreflightNousCatalogCache(t *testing.T) {
	nousEnvSetup(t, map[string]string{"nous": "k"})
	rt := filepath.Join(t.TempDir(), ".agentctl")
	withRuntimeDir(t, rt)
	var hits atomic.Int32
	srv := catalogServer(t, 200, nousCatalogJSON, &hits)
	b := config.BackendConfig{Kind: "nous", BaseURL: srv.URL, Model: "qwen/qwen3-coder-next"}

	for i := 0; i < 2; i++ {
		if _, err := preflightNous("nous", b, ""); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("catalog fetched %d times, want 1 (cache hit)", hits.Load())
	}

	// Expired cache refetches.
	old := nowFn
	nowFn = func() time.Time { return time.Now().Add(11 * time.Minute) }
	t.Cleanup(func() { nowFn = old })
	if _, err := preflightNous("nous", b, ""); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Errorf("catalog fetched %d times after expiry, want 2", hits.Load())
	}
}

func TestPrepareBackendNous(t *testing.T) {
	nousEnvSetup(t, map[string]string{"nous": "k"})
	srv := catalogServer(t, 200, nousCatalogJSON, nil)
	cfg := &config.Config{Backends: map[string]config.BackendConfig{
		"nous": {Kind: "nous", BaseURL: srv.URL, Model: "qwen/qwen3-coder-next"},
	}}

	t.Run("records backend and fills context", func(t *testing.T) {
		ac := config.AgentConfig{}
		env := map[string]string{}
		be, err := PrepareBackend(cfg, &ac, "nous", "", "", env)
		if err != nil || be == nil {
			t.Fatalf("be=%v err=%v", be, err)
		}
		if ac.Backend != "nous" || env[EnvBackend] != "nous" || env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "262144" {
			t.Errorf("ac.Backend=%q env=%v", ac.Backend, env)
		}
		if ac.Model != "qwen/qwen3-coder-next" {
			t.Errorf("model = %q", ac.Model)
		}
	})
	t.Run("none clears backend", func(t *testing.T) {
		ac := config.AgentConfig{Backend: "nous"}
		env := map[string]string{}
		be, err := PrepareBackend(cfg, &ac, BackendNone, "", "", env)
		if err != nil || be != nil || ac.Backend != "" || len(env) != 0 {
			t.Errorf("be=%v err=%v ac.Backend=%q env=%v", be, err, ac.Backend, env)
		}
	})
	t.Run("configured max context wins", func(t *testing.T) {
		c2 := &config.Config{Backends: map[string]config.BackendConfig{
			"nous": {Kind: "nous", BaseURL: srv.URL, Model: "qwen/qwen3-coder-next", MaxContextTokens: 5000},
		}}
		ac := config.AgentConfig{}
		env := map[string]string{}
		if _, err := PrepareBackend(c2, &ac, "nous", "", "", env); err != nil {
			t.Fatal(err)
		}
		if env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "5000" {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("failure leaves env untouched", func(t *testing.T) {
		ac := config.AgentConfig{}
		env := map[string]string{}
		if _, err := PrepareBackend(cfg, &ac, "nous", "claude-opus-4", "", env); err == nil {
			t.Fatal("non-slug model accepted")
		}
		if len(env) != 0 {
			t.Errorf("env = %v", env)
		}
	})
}

func TestCheckShadowingCredentials(t *testing.T) {
	write := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name    string
		setup   func(t *testing.T, home, repo, wt, managed string)
		wantErr string
	}{
		{"clean", func(t *testing.T, home, repo, wt, managed string) {}, ""},
		{"blanked is fine", func(t *testing.T, home, repo, wt, managed string) {
			write(t, filepath.Join(wt, ".claude", "settings.local.json"), `{"env":{"ANTHROPIC_API_KEY":"","ANTHROPIC_AUTH_TOKEN":""}}`)
		}, ""},
		{"user settings", func(t *testing.T, home, repo, wt, managed string) {
			write(t, filepath.Join(home, ".claude", "settings.json"), `{"env":{"ANTHROPIC_AUTH_TOKEN":"tok"}}`)
		}, filepath.Join(".claude", "settings.json")},
		{"repo local settings", func(t *testing.T, home, repo, wt, managed string) {
			write(t, filepath.Join(repo, ".claude", "settings.local.json"), `{"env":{"ANTHROPIC_API_KEY":"sk-ant-x"}}`)
		}, "ANTHROPIC_API_KEY"},
		{"managed", func(t *testing.T, home, repo, wt, managed string) {
			write(t, managed, `{"env":{"ANTHROPIC_API_KEY":"sk-ant-x"}}`)
		}, "managed.json"},
		{"unrelated env", func(t *testing.T, home, repo, wt, managed string) {
			write(t, filepath.Join(repo, ".claude", "settings.json"), `{"env":{"FOO":"bar"}}`)
		}, ""},
		{"malformed ignored", func(t *testing.T, home, repo, wt, managed string) {
			write(t, filepath.Join(repo, ".claude", "settings.json"), `{not json`)
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, repo := t.TempDir(), t.TempDir()
			wt := filepath.Join(repo, ".worktrees", "w")
			managed := filepath.Join(t.TempDir(), "managed.json")
			t.Setenv("HOME", home)
			old := managedSettingsPaths
			managedSettingsPaths = []string{managed}
			t.Cleanup(func() { managedSettingsPaths = old })
			tt.setup(t, home, repo, wt, managed)
			err := checkShadowingCredentials(wt)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "sk-ant-x") || strings.Contains(err.Error(), `"tok"`) {
				t.Errorf("error leaks the credential: %v", err)
			}
		})
	}
}

// --- settings JSON ---

func pinNousConfig(t *testing.T) {
	t.Helper()
	oldExec, oldLoad := execPath, loadConfigFn
	execPath = func() (string, error) { return "/opt/it's here/agentctl", nil }
	loadConfigFn = func() *config.Config {
		return &config.Config{Backends: map[string]config.BackendConfig{
			"nous":   {Kind: "nous", BaseURL: config.NousDefaultBaseURL, Model: "a/b"},
			"fenrir": {BaseURL: "http://fenrir:11434", Model: "m", AuthToken: "ollama"},
		}}
	}
	t.Cleanup(func() { execPath, loadConfigFn = oldExec, oldLoad })
}

type parsedSettings struct {
	APIKeyHelper string            `json:"apiKeyHelper"`
	Env          map[string]string `json:"env"`
	Permissions  struct {
		Deny []string `json:"deny"`
	} `json:"permissions"`
}

func parseSettings(t *testing.T, data []byte) (parsedSettings, map[string]json.RawMessage) {
	t.Helper()
	var p parsedSettings
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	return p, raw
}

func TestBuildAgentSettingsJSONNous(t *testing.T) {
	pinNousConfig(t)
	data, err := BuildAgentSettingsJSON(config.AgentConfig{Backend: "nous"}, false)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := parseSettings(t, data)
	if want := `'/opt/it'\''s here/agentctl' backend-token 'nous'`; p.APIKeyHelper != want {
		t.Errorf("apiKeyHelper = %q, want %q", p.APIKeyHelper, want)
	}
	if v, ok := p.Env["ANTHROPIC_BASE_URL"]; !ok || v != config.NousDefaultBaseURL {
		t.Errorf("env = %v", p.Env)
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if v, ok := p.Env[k]; !ok || v != "" {
			t.Errorf("env[%s] must be present and empty, got %q (present=%v)", k, v, ok)
		}
	}
	deny := strings.Join(p.Permissions.Deny, "|")
	for _, want := range []string{"Bash(*backend-token*)", "Edit(**/.claude/settings*.json)"} {
		if !strings.Contains(deny, want) {
			t.Errorf("deny %v missing %s", p.Permissions.Deny, want)
		}
	}
}

func TestBuildAgentSettingsJSONNoNousKeys(t *testing.T) {
	pinNousConfig(t)
	for _, backend := range []string{"", "fenrir"} {
		data, err := BuildAgentSettingsJSON(config.AgentConfig{Backend: backend}, true)
		if err != nil {
			t.Fatal(err)
		}
		_, raw := parseSettings(t, data)
		for _, k := range []string{"apiKeyHelper", "env"} {
			if _, ok := raw[k]; ok {
				t.Errorf("backend %q: settings must not contain %q", backend, k)
			}
		}
		if strings.Contains(string(data), "backend-token") {
			t.Errorf("backend %q: unexpected backend-token", backend)
		}
	}
}

func TestBuildAgentSettingsJSONUnknownBackendErrors(t *testing.T) {
	pinNousConfig(t)
	if _, err := BuildAgentSettingsJSON(config.AgentConfig{Backend: "gone"}, false); err == nil {
		t.Error("expected error for a backend that no longer resolves")
	}
}

func TestSettingsFileStem(t *testing.T) {
	tests := []struct{ agent, backend, want string }{
		{"go-engineer", "", "go-engineer"},
		{"go-engineer", "nous", "go-engineer@nous"},
		{"go-engineer", "we ird/../x", "go-engineer@we_ird_.._x"},
	}
	for _, tt := range tests {
		if got := settingsFileStem(tt.agent, tt.backend); got != tt.want {
			t.Errorf("settingsFileStem(%q,%q) = %q, want %q", tt.agent, tt.backend, got, tt.want)
		}
	}
}

// TestWriteAgentSettingsRefreshesWorktree covers restore/resume: an earlier
// nous dispatch leaves routing in the worktree's settings.local.json, and a
// later launch with no backend must remove it.
func TestWriteAgentSettingsRefreshesWorktree(t *testing.T) {
	pinNousConfig(t)
	repo := t.TempDir()
	wt := filepath.Join(repo, ".worktrees", "agent-x-issue-1")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(wt, ".claude", "settings.local.json")
	rt := filepath.Join(repo, ".agentctl")
	withRuntimeDir(t, rt)

	read := func() string {
		t.Helper()
		b, err := os.ReadFile(localPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	path := WriteAgentSettings("go-engineer", config.AgentConfig{Backend: "nous"}, wt)
	if filepath.Base(path) != "go-engineer@nous.json" {
		t.Errorf("settings path = %q", path)
	}
	if got := read(); !strings.Contains(got, "apiKeyHelper") || !strings.Contains(got, "ANTHROPIC_BASE_URL") {
		t.Fatalf("worktree settings missing nous routing:\n%s", got)
	}

	path = WriteAgentSettings("go-engineer", config.AgentConfig{}, wt)
	if filepath.Base(path) != "go-engineer.json" {
		t.Errorf("settings path = %q", path)
	}
	got := read()
	for _, bad := range []string{"apiKeyHelper", "ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "backend-token"} {
		if strings.Contains(got, bad) {
			t.Errorf("stale %q left in worktree settings after backend none:\n%s", bad, got)
		}
	}
}
