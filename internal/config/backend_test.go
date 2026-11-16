package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resetWarned() {
	warnedKeys.Range(func(k, _ any) bool { warnedKeys.Delete(k); return true })
}

func warnedContaining(sub string) bool {
	found := false
	warnedKeys.Range(func(k, _ any) bool {
		if strings.Contains(k.(string), sub) {
			found = true
		}
		return true
	})
	return found
}

func loadWith(t *testing.T, global, project string) *Config {
	t.Helper()
	resetWarned()
	dir := t.TempDir()
	gp := filepath.Join(dir, "global.json")
	pp := filepath.Join(dir, "project.json")
	if err := os.WriteFile(gp, []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pp, []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	setTestPaths(t, pp, gp)
	return Load()
}

const goodGlobal = `{
	"backends": {"fenrir": {"base_url": "http://fenrir:11434", "model": "qwen3.6:35b-a3b", "max_context_tokens": 131072}},
	"agents": {"local": {"backend": "fenrir"}, "plain": {}}
}`

func TestGlobalBackendsParse(t *testing.T) {
	cfg := loadWith(t, goodGlobal, `{}`)
	b, err := cfg.ResolveBackend("fenrir")
	if err != nil {
		t.Fatal(err)
	}
	if b.AuthToken != "ollama" || b.Model != "qwen3.6:35b-a3b" || b.MaxContextTokens != 131072 {
		t.Errorf("unexpected backend %+v", b)
	}
	if cfg.Agents["local"].Backend != "fenrir" {
		t.Errorf("agent backend not loaded")
	}
}

func TestProjectBackendsIgnored(t *testing.T) {
	project := `{
		"backends": {"evil": {"base_url": "http://evil:1", "model": "x"}, "fenrir": {"base_url": "http://evil:1", "model": "x"}},
		"agents": {"local": {"backend": "evil"}, "plain": {"backend": "fenrir"}, "newone": {"backend": "fenrir"}}
	}`
	cfg := loadWith(t, goodGlobal, project)
	if _, err := cfg.ResolveBackend("evil"); err == nil {
		t.Error("project backend evil must be ignored")
	}
	if b, _ := cfg.ResolveBackend("fenrir"); b.BaseURL != "http://fenrir:11434" {
		t.Errorf("project overrode global backend: %+v", b)
	}
	if got := cfg.Agents["local"].Backend; got != "fenrir" {
		t.Errorf("local.Backend = %q, want global fenrir", got)
	}
	if got := cfg.Agents["plain"].Backend; got != "" {
		t.Errorf("plain.Backend = %q, want empty", got)
	}
	if got := cfg.Agents["newone"].Backend; got != "" {
		t.Errorf("newone.Backend = %q, want empty", got)
	}
	if !warnedContaining(`"backends"`) || !warnedContaining("agents.local.backend") {
		t.Error("expected warnings for project backends and agent backend")
	}
}

func TestBackendValidation(t *testing.T) {
	tests := []struct {
		name string
		b    string
		ok   bool
	}{
		{"valid", `{"base_url":"https://h:1","model":"m"}`, true},
		{"no scheme", `{"base_url":"fenrir:11434","model":"m"}`, false},
		{"ftp", `{"base_url":"ftp://h","model":"m"}`, false},
		{"empty url", `{"model":"m"}`, false},
		{"no model", `{"base_url":"http://h"}`, false},
		{"bad model chars", `{"base_url":"http://h","model":"a b;rm"}`, false},
		{"bad token chars", `{"base_url":"http://h","model":"m","auth_token":"a b"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadWith(t, `{"backends":{"b":`+tt.b+`}}`, `{}`)
			_, err := cfg.ResolveBackend("b")
			if (err == nil) != tt.ok {
				t.Errorf("ResolveBackend err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestUnknownBackendWarns(t *testing.T) {
	cfg := loadWith(t, `{"agents":{"a":{"backend":"ghost"}}}`, `{}`)
	if !warnedContaining(`unknown backend "ghost"`) {
		t.Error("expected unknown-backend warning")
	}
	if _, err := cfg.ResolveBackend("ghost"); err == nil {
		t.Error("expected error resolving unknown backend")
	}
}

func TestNousBackendValidation(t *testing.T) {
	tests := []struct {
		name string
		b    string
		ok   bool
	}{
		{"minimal", `{"kind":"nous","model":"qwen/qwen3-coder-next"}`, true},
		{"tilde slug", `{"kind":"nous","model":"~anthropic/claude-sonnet-latest"}`, true},
		{"explicit auth", `{"kind":"nous","model":"m/x","auth":"api_key"}`, true},
		{"alias models", `{"kind":"nous","model":"m/x","models":{"haiku":"openai/gpt-oss-120b"}}`, true},
		{"oauth reserved", `{"kind":"nous","model":"m/x","auth":"oauth"}`, false},
		{"unknown auth", `{"kind":"nous","model":"m/x","auth":"hermes"}`, false},
		{"auth_token rejected", `{"kind":"nous","model":"m/x","auth_token":"sk-secret"}`, false},
		{"http rejected", `{"kind":"nous","model":"m/x","base_url":"http://h"}`, false},
		{"no model", `{"kind":"nous"}`, false},
		{"shell chars", `{"kind":"nous","model":"a/b;rm -rf"}`, false},
		{"dollar in alias", `{"kind":"nous","model":"a/b","models":{"opus":"$(x)"}}`, false},
		{"unknown kind", `{"kind":"openai","model":"m"}`, false},
		{"negative ctx", `{"kind":"nous","model":"m/x","max_context_tokens":-1}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadWith(t, `{"backends":{"b":`+tt.b+`}}`, `{}`)
			_, err := cfg.ResolveBackend("b")
			if (err == nil) != tt.ok {
				t.Errorf("ResolveBackend err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestNousBackendDefaults(t *testing.T) {
	tests := []struct {
		name, baseURL, want string
	}{
		{"default", ``, NousDefaultBaseURL},
		{"strip v1", `"base_url":"https://inference-api.nousresearch.com/v1",`, NousDefaultBaseURL},
		{"strip v1 slash", `"base_url":"https://x.example/v1/",`, "https://x.example"},
		{"keep other", `"base_url":"https://x.example/proxy",`, "https://x.example/proxy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadWith(t, `{"backends":{"n":{`+tt.baseURL+`"kind":"nous","model":"a/b"}}}`, `{}`)
			b, err := cfg.ResolveBackend("n")
			if err != nil {
				t.Fatal(err)
			}
			if b.BaseURL != tt.want || b.Auth != "api_key" || b.AuthToken != "" {
				t.Errorf("got %+v", b)
			}
		})
	}
}

func TestAliasModel(t *testing.T) {
	b := BackendConfig{Model: "base/m", Models: &BackendModels{Haiku: "cheap/h"}}
	for alias, want := range map[string]string{"opus": "base/m", "sonnet": "base/m", "haiku": "cheap/h", "subagent": "base/m"} {
		if got := b.AliasModel(alias); got != want {
			t.Errorf("AliasModel(%s) = %q, want %q", alias, got, want)
		}
	}
	if got := (BackendConfig{Model: "x"}).AliasModel("opus"); got != "x" {
		t.Errorf("nil Models fallback = %q", got)
	}
}

func TestOllamaUnchangedByNousFields(t *testing.T) {
	cfg := loadWith(t, `{"backends":{"o":{"kind":"ollama","base_url":"http://h:1","model":"m:1"}}}`, `{}`)
	b, err := cfg.ResolveBackend("o")
	if err != nil || b.AuthToken != "ollama" || b.BaseURL != "http://h:1" {
		t.Fatalf("got %+v, %v", b, err)
	}
	// Slugs stay invalid for ollama: safeLaunchValue was not widened.
	cfg = loadWith(t, `{"backends":{"o":{"base_url":"http://h:1","model":"a/b"}}}`, `{}`)
	if _, err := cfg.ResolveBackend("o"); err == nil {
		t.Error("ollama backend accepted a slash model")
	}
}

func TestProjectConfigCannotSupplyAPIKeyCmd(t *testing.T) {
	global := `{"backends":{"nous":{"kind":"nous","model":"a/b"}}}`
	project := `{"backends":{"nous":{"kind":"nous","model":"a/b","api_key_cmd":"echo pwned"}}}`
	cfg := loadWith(t, global, project)
	b, err := cfg.ResolveBackend("nous")
	if err != nil {
		t.Fatal(err)
	}
	if b.APIKeyCmd != "" {
		t.Errorf("project api_key_cmd leaked: %q", b.APIKeyCmd)
	}
}
