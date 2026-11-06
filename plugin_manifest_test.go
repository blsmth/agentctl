package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests guard the Claude Code plugin layout, since CI has no `claude`
// binary to run `claude plugin validate`.

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
}

// assertRelPath checks that a manifest path starts with "./" and exists
// relative to base.
func assertRelPath(t *testing.T, label, base, p string) {
	t.Helper()
	if !strings.HasPrefix(p, "./") {
		t.Errorf("%s = %q, must start with ./", label, p)
		return
	}
	if _, err := os.Stat(filepath.Join(base, p)); err != nil {
		t.Errorf("%s = %q does not exist: %v", label, p, err)
	}
}

func TestPluginMarketplaceManifest(t *testing.T) {
	var mp struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"plugins"`
	}
	readJSON(t, ".claude-plugin/marketplace.json", &mp)
	if mp.Name != "agentctl" {
		t.Errorf("marketplace name = %q, want agentctl", mp.Name)
	}
	if len(mp.Plugins) != 1 {
		t.Fatalf("got %d plugins, want 1", len(mp.Plugins))
	}
	p := mp.Plugins[0]
	if p.Name != "agentctl" {
		t.Errorf("plugin name = %q, want agentctl", p.Name)
	}
	assertRelPath(t, "plugins[0].source", ".", p.Source)
}

func TestPluginManifest(t *testing.T) {
	var m map[string]any
	readJSON(t, "plugin/.claude-plugin/plugin.json", &m)
	if m["name"] != "agentctl" {
		t.Errorf("name = %v, want agentctl", m["name"])
	}
	for _, banned := range []string{"agents", "skills", "hooks"} {
		if _, ok := m[banned]; ok {
			t.Errorf("plugin.json must not declare %q: agentctl owns its hooks and agents", banned)
		}
	}
	for _, key := range []string{"commands", "agents", "skills", "hooks", "mcpServers", "outputStyles"} {
		switch v := m[key].(type) {
		case string:
			assertRelPath(t, key, "plugin", v)
		case []any:
			for _, e := range v {
				if s, ok := e.(string); ok {
					assertRelPath(t, key, "plugin", s)
				}
			}
		}
	}
	for _, dir := range []string{"hooks", "agents", "skills"} {
		if _, err := os.Stat(filepath.Join("plugin", dir)); err == nil {
			t.Errorf("plugin/%s must not exist", dir)
		}
	}
}

func TestPluginCommandsHaveDescription(t *testing.T) {
	files, err := filepath.Glob("plugin/commands/*.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("no plugin commands found (err=%v)", err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if d := commandDescription(string(data)); d == "" {
				t.Errorf("%s: missing frontmatter or empty description", f)
			}
		})
	}
}

// commandDescription returns the description from leading YAML frontmatter.
func commandDescription(text string) string {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return ""
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	for _, l := range strings.Split(rest[:end], "\n") {
		if v, ok := strings.CutPrefix(l, "description:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}
