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
