package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
	var mp struct {
		Plugins []struct {
			Version string `json:"version"`
		} `json:"plugins"`
	}
	readJSON(t, ".claude-plugin/marketplace.json", &mp)
	if len(mp.Plugins) == 1 && m["version"] != mp.Plugins[0].Version {
		t.Errorf("plugin.json version %v != marketplace.json version %q", m["version"], mp.Plugins[0].Version)
	}
	for _, banned := range []string{"agents", "hooks"} {
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
	for _, dir := range []string{"hooks", "agents"} {
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

func TestPluginSkillsHaveFrontmatter(t *testing.T) {
	files, err := filepath.Glob("plugin/skills/*/SKILL.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("no plugin skills found (err=%v)", err)
	}
	for _, f := range files {
		dir := filepath.Base(filepath.Dir(f))
		t.Run(dir, func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			rest, ok := strings.CutPrefix(string(data), "---\n")
			if !ok {
				t.Fatalf("%s: missing frontmatter", f)
			}
			fm, _, ok := strings.Cut(rest, "\n---")
			if !ok {
				t.Fatalf("%s: unterminated frontmatter", f)
			}
			var meta struct {
				Name        string `yaml:"name"`
				Description string `yaml:"description"`
			}
			if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
				t.Fatalf("%s: invalid frontmatter: %v", f, err)
			}
			if meta.Name != dir {
				t.Errorf("%s: name = %q, want %q", f, meta.Name, dir)
			}
			if strings.TrimSpace(meta.Description) == "" {
				t.Errorf("%s: empty description", f)
			}
		})
	}
}

// ---- artifact kit drift guard ----
//
// Every template inlines kit/kit.css and kit/kit.js between marker lines. The
// inlined copy must be byte-identical to the kit file. Run with
// UPDATE_ARTIFACT_KIT=1 to rewrite the regions instead of failing.

const artifactsDir = "plugin/skills/artifacts"

// kitBlockTypes is the closed set of block types; keep in sync with kit.js
// SCHEMA and kit/SPEC.md.
var kitBlockTypes = map[string]bool{
	"summary": true, "kpis": true, "chart": true, "table": true, "text": true,
	"mermaid": true, "comparison": true, "timeline": true, "html": true, "sources": true,
}

// kitRegion locates the text between begin and end marker lines. start is the
// offset just after the begin marker line; end is the offset of the end marker.
func kitRegion(src, begin, end string) (start, stop int, ok bool) {
	i := strings.Index(src, begin+"\n")
	if i < 0 {
		return 0, 0, false
	}
	start = i + len(begin) + 1
	j := strings.Index(src[start:], end)
	if j < 0 {
		return 0, 0, false
	}
	return start, start + j, true
}

func TestArtifactKitInlined(t *testing.T) {
	update := os.Getenv("UPDATE_ARTIFACT_KIT") == "1"
	css, err := os.ReadFile(filepath.Join(artifactsDir, "kit/kit.css"))
	if err != nil {
		t.Fatal(err)
	}
	js, err := os.ReadFile(filepath.Join(artifactsDir, "kit/kit.js"))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"kit.css": css, "kit.js": js} {
		if !strings.HasSuffix(string(body), "\n") {
			t.Errorf("%s must end with a newline", name)
		}
	}
	if strings.Contains(string(js), "</script") || strings.Contains(string(css), "</style") {
		t.Error("kit files must not contain </script or </style (they are inlined into HTML)")
	}
	type region struct{ begin, end, want string }
	cssR := region{"/* kit:css:begin */", "/* kit:css:end */", string(css)}
	jsR := region{"// kit:js:begin", "// kit:js:end", string(js)}
	templates := map[string][]region{
		"report.html":     {cssR, jsR},
		"dashboard.html":  {cssR, jsR},
		"comparison.html": {cssR, jsR},
		"diagram.html":    {cssR, jsR},
		"mockup.html":     {cssR},
	}
	for name, regions := range templates {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(artifactsDir, "templates", name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			src := string(data)
			changed := false
			for _, r := range regions {
				start, stop, ok := kitRegion(src, r.begin, r.end)
				if !ok {
					t.Errorf("%s: missing %q ... %q markers", name, r.begin, r.end)
					continue
				}
				if src[start:stop] == r.want {
					continue
				}
				if update {
					src = src[:start] + r.want + src[stop:]
					changed = true
					continue
				}
				t.Errorf("%s: region %s differs from the kit file; run UPDATE_ARTIFACT_KIT=1 go test -run ArtifactKit .", name, r.begin)
			}
			if changed {
				if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestArtifactTemplateSpecs(t *testing.T) {
	for _, name := range []string{"report.html", "dashboard.html", "comparison.html", "diagram.html"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(artifactsDir, "templates", name))
			if err != nil {
				t.Fatal(err)
			}
			src := string(data)
			const open = `<script type="application/json" id="spec">`
			i := strings.Index(src, open)
			if i < 0 {
				t.Fatalf("%s: no spec script", name)
			}
			body := src[i+len(open):]
			j := strings.Index(body, "</script>")
			if j < 0 {
				t.Fatalf("%s: unterminated spec script", name)
			}
			var spec struct {
				Kind   string           `json:"kind"`
				Blocks []map[string]any `json:"blocks"`
			}
			if err := json.Unmarshal([]byte(body[:j]), &spec); err != nil {
				t.Fatalf("%s: spec is not valid JSON: %v", name, err)
			}
			if len(spec.Blocks) == 0 {
				t.Errorf("%s: spec has no blocks", name)
			}
			hasSources := false
			for i, b := range spec.Blocks {
				typ, _ := b["type"].(string)
				if !kitBlockTypes[typ] {
					t.Errorf("%s: blocks[%d] has unknown type %q", name, i, typ)
				}
				hasSources = hasSources || typ == "sources"
			}
			if (spec.Kind == "report" || spec.Kind == "dashboard") && !hasSources {
				t.Errorf("%s: %s spec needs a sources block", name, spec.Kind)
			}
		})
	}
}
