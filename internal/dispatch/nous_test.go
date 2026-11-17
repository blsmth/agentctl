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
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/tokens"
)

const nousCatalogJSON = `{"data":[
 {"id":"qwen/qwen3-coder-next","context_length":262144,"endpoints":["messages","chat"],"pricing":{"prompt":"0.00000012","completion":"0.0000008"}},
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
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "agentctl/") {
			t.Errorf("catalog request User-Agent = %q, want agentctl/<version>", ua)
			w.WriteHeader(403)
			return
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
			info, err := preflightNous("nous", tt.b, "", "", "")
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
	_, err := preflightNous("nous", config.BackendConfig{Kind: "nous", BaseURL: url, Model: "a/b"}, "", "", "")
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
		if _, err := preflightNous("nous", b, "", "", ""); err != nil {
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
	if _, err := preflightNous("nous", b, "", "", ""); err != nil {
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
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			old := managedSettingsPaths
			managedSettingsPaths = []string{managed}
			t.Cleanup(func() { managedSettingsPaths = old })
			tt.setup(t, home, repo, wt, managed)
			err := checkShadowingCredentials("", wt)
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
	makeLinkedWorktree(t, repo, wt)
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

// makeLinkedWorktree fakes the on-disk shape of `git worktree add`: repo/.git
// is a directory and wt/.git is a file pointing into repo/.git/worktrees/.
func makeLinkedWorktree(t *testing.T, repo, wt string) {
	t.Helper()
	admin := filepath.Join(repo, ".git", "worktrees", filepath.Base(wt))
	for _, d := range []string{admin, wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentctlWorktreeRoot(t *testing.T) {
	repo := t.TempDir()
	wt := filepath.Join(repo, ".worktrees", "w")
	makeLinkedWorktree(t, repo, wt)

	// Layout matches but no .git file: a look-alike directory.
	fake := filepath.Join(repo, ".worktrees", "fake")
	if err := os.MkdirAll(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	// .git file pointing at some other repository.
	other := t.TempDir()
	foreign := filepath.Join(repo, ".worktrees", "foreign")
	makeLinkedWorktree(t, other, foreign)
	// Linked worktree of this repo but not under .worktrees.
	elsewhere := filepath.Join(t.TempDir(), "w")
	makeLinkedWorktree(t, repo, elsewhere)

	tests := []struct {
		name   string
		dir    string
		wantOK bool
	}{
		{"real worktree", wt, true},
		{"look-alike dir", fake, false},
		{"foreign repo", foreign, false},
		{"not under .worktrees", elsewhere, false},
		{"repo root", repo, false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, ok := agentctlWorktreeRoot(tt.dir)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && !sameDir(root, repo) {
				t.Errorf("root = %q, want %q", root, repo)
			}
		})
	}
}

// TestWriteAgentSettingsNousOutsideWorktree: a nous launch in the main
// checkout is allowed; routing travels only in the --settings file and the
// checkout's own settings.local.json is left untouched.
func TestWriteAgentSettingsNousOutsideWorktree(t *testing.T) {
	pinNousConfig(t)
	repo := t.TempDir()
	withRuntimeDir(t, filepath.Join(repo, ".agentctl"))
	localPath := filepath.Join(repo, ".claude", "settings.local.json")

	path := WriteAgentSettings("go-engineer", config.AgentConfig{Backend: "nous"}, repo)
	if path == "" {
		t.Fatal("no settings path returned")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"apiKeyHelper", "ANTHROPIC_BASE_URL"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("--settings file missing %q:\n%s", want, b)
		}
	}
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Errorf("main checkout settings.local.json must not be written; stat err = %v", err)
	}
}

func TestPrepareRestoreNousOutsideWorktree(t *testing.T) {
	nousEnvSetup(t, map[string]string{"nous": "k"})
	srv := catalogServer(t, 200, nousCatalogJSON, nil)
	cfg := &config.Config{Backends: map[string]config.BackendConfig{
		"nous": {Kind: "nous", BaseURL: srv.URL, Model: "qwen/qwen3-coder-next"},
	}}
	repo := t.TempDir()
	wt := filepath.Join(repo, ".worktrees", "w")
	makeLinkedWorktree(t, repo, wt)

	for name, dir := range map[string]string{"main checkout": repo, "worktree": wt} {
		if _, env, _, err := prepareRestore(cfg, harness.For(""), RestoreRequest{AgentName: "a", Backend: "nous", WorkDir: dir}); err != nil || env[EnvBackend] != "nous" {
			t.Errorf("%s: env=%v err=%v", name, env, err)
		}
	}
}

func TestPreflightNousChecksExplicitModel(t *testing.T) {
	nousEnvSetup(t, map[string]string{"nous": "k"})
	srv := catalogServer(t, 200, nousCatalogJSON, nil)
	b := config.BackendConfig{Kind: "nous", BaseURL: srv.URL, Model: "qwen/qwen3-coder-next"}

	if _, err := preflightNous("nous", b, "", "", "qwen/missing"); err == nil || !strings.Contains(err.Error(), "qwen/missing") {
		t.Errorf("missing explicit model: err = %v", err)
	}
	info, err := preflightNous("nous", b, "", "", "qwen/qwen3-max")
	if err != nil {
		t.Fatal(err)
	}
	if info.ContextLength != 32768 {
		t.Errorf("context should follow the explicit model, got %d", info.ContextLength)
	}
}

func TestPreflightNousDoesNotRunAPIKeyCmd(t *testing.T) {
	nousEnvSetup(t, map[string]string{})
	srv := catalogServer(t, 200, nousCatalogJSON, nil)
	marker := filepath.Join(t.TempDir(), "ran")
	b := config.BackendConfig{Kind: "nous", BaseURL: srv.URL, Model: "qwen/qwen3-coder-next",
		APIKeyCmd: "touch " + marker + "; echo key"}
	if _, err := preflightNous("nous", b, "", "", ""); err != nil {
		t.Fatalf("configured api_key_cmd should satisfy preflight: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("preflight ran api_key_cmd")
	}
}

func TestMergeAgentctlSettings(t *testing.T) {
	existing := `{
  "permissions": {"allow": ["Bash(ls)"], "deny": ["Bash(*backend-token*)", "Edit(**/.claude/settings*.json)", "Bash(rm)"]},
  "env": {"FOO": "bar", "ANTHROPIC_BASE_URL": "https://old", "ANTHROPIC_API_KEY": ""},
  "apiKeyHelper": "old",
  "hooks": {"Stop": []},
  "model": "keepme"
}`
	t.Run("nous", func(t *testing.T) {
		gen := `{"apiKeyHelper":"new","env":{"ANTHROPIC_BASE_URL":"https://new","ANTHROPIC_API_KEY":"","ANTHROPIC_AUTH_TOKEN":""},` +
			`"permissions":{"deny":["Bash(*backend-token*)","Edit(**/.claude/settings*.json)"]}}`
		out, err := mergeAgentctlSettings([]byte(existing), []byte(gen))
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Model        string
			APIKeyHelper string
			Hooks        json.RawMessage
			Env          map[string]string
			Permissions  struct{ Allow, Deny []string }
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if got.Model != "keepme" || got.APIKeyHelper != "new" || got.Hooks != nil {
			t.Errorf("got %+v", got)
		}
		if got.Env["FOO"] != "bar" || got.Env["ANTHROPIC_BASE_URL"] != "https://new" {
			t.Errorf("env = %v", got.Env)
		}
		if strings.Join(got.Permissions.Allow, ",") != "Bash(ls)" {
			t.Errorf("allow = %v", got.Permissions.Allow)
		}
		if strings.Join(got.Permissions.Deny, ",") != "Bash(rm),Bash(*backend-token*),Edit(**/.claude/settings*.json)" {
			t.Errorf("deny = %v", got.Permissions.Deny)
		}
	})
	t.Run("no backend removes owned keys, keeps the rest", func(t *testing.T) {
		out, err := mergeAgentctlSettings([]byte(existing), []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		s := string(out)
		for _, bad := range []string{"apiKeyHelper", "ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "backend-token", "hooks", "settings*.json"} {
			if strings.Contains(s, bad) {
				t.Errorf("%q survived:\n%s", bad, s)
			}
		}
		for _, keep := range []string{`"FOO": "bar"`, "Bash(ls)", "Bash(rm)", "keepme"} {
			if !strings.Contains(s, keep) {
				t.Errorf("%q lost:\n%s", keep, s)
			}
		}
	})
	t.Run("empty existing", func(t *testing.T) {
		out, err := mergeAgentctlSettings(nil, []byte(`{"apiKeyHelper":"x"}`))
		if err != nil || !strings.Contains(string(out), "apiKeyHelper") {
			t.Errorf("out=%s err=%v", out, err)
		}
	})
	t.Run("invalid existing errors", func(t *testing.T) {
		if _, err := mergeAgentctlSettings([]byte(`{bad`), []byte(`{}`)); err == nil {
			t.Error("expected error")
		}
	})
}

func TestWriteWorktreeSettingsKeepsSavedPermissions(t *testing.T) {
	pinNousConfig(t)
	repo := t.TempDir()
	wt := filepath.Join(repo, ".worktrees", "w")
	makeLinkedWorktree(t, repo, wt)
	local := filepath.Join(wt, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	saved := `{"permissions":{"allow":["Bash(make test)"]},"enableAllProjectMcpServers":true}`
	if err := os.WriteFile(local, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"nous", "", "nous"} {
		if err := WriteWorktreeSettings(wt, repo, config.AgentConfig{Backend: backend}); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(local)
		if !strings.Contains(string(b), "Bash(make test)") || !strings.Contains(string(b), "enableAllProjectMcpServers") {
			t.Fatalf("backend %q: saved settings lost:\n%s", backend, b)
		}
		if got := strings.Contains(string(b), "apiKeyHelper"); got != (backend == "nous") {
			t.Fatalf("backend %q: apiKeyHelper present = %v", backend, got)
		}
	}
}

func TestWriteAgentSettingsWorktreeFailureStillWritesSettings(t *testing.T) {
	pinNousConfig(t)
	repo := t.TempDir()
	wt := filepath.Join(repo, ".worktrees", "w")
	makeLinkedWorktree(t, repo, wt)
	// A symlinked .claude makes the worktree write refuse.
	if err := os.Symlink(t.TempDir(), filepath.Join(wt, ".claude")); err != nil {
		t.Fatal(err)
	}
	withRuntimeDir(t, filepath.Join(repo, ".agentctl"))
	if path := WriteAgentSettings("go-engineer", config.AgentConfig{Backend: "nous"}, wt); path == "" {
		t.Error("--settings file must still be written when the worktree write fails")
	}
}

func TestCatalogCachePreservesPricing(t *testing.T) {
	body := `{"data":[{"id":"openai/gpt-6-astra","context_length":1050000,"pricing":{"prompt":"0.0000100000","completion":"0.0000500000","input_cache_read":"0.0000010000","input_cache_write":"0.0000125000"}}]}`
	var cat nousCatalog
	if err := json.Unmarshal([]byte(body), &cat); err != nil {
		t.Fatal(err)
	}
	// Round-trip through the compact cache encoding, as loadNousCatalog does.
	data, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}
	got := tokens.ParseCatalog(data)["openai/gpt-6-astra"]
	if got.InputPerMTok != 10 || got.OutputPerMTok != 50 || got.CacheReadPerMTok != 1 || got.CacheCreatePerMTok != 12.5 {
		t.Errorf("cached pricing = %+v, want 10/50/1/12.5 per MTok", got)
	}
	if !catalogHasPricing(cat) {
		t.Error("catalogHasPricing = false")
	}
	if catalogHasPricing(nousCatalog{Data: []nousModel{{ID: "x"}}}) {
		t.Error("a catalog without pricing must not count as priced (old cache files are refetched)")
	}
}
