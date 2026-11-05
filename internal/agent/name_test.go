package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"go-engineer", true},
		{"python_engineer", true},
		{"v1.2", true},
		{"A", true},
		{strings.Repeat("a", MaxNameLen), true},
		{strings.Repeat("a", MaxNameLen+1), false},
		{"", false},
		{".hidden", false},
		{"..", false},
		{"-flag", false},
		{"../../etc/passwd", false},
		{"a/b", false},
		{"x$(touch /tmp/p)", false},
		{"x`id`", false},
		{"x;id", false},
		{"#{window_id}", false},
		{"with space", false},
		{"go-engineer~2", false},
		{"plugin:agent", false},
		{"naïve", false},
		{"a\nb", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidName(tt.name); got != tt.want {
				t.Errorf("ValidName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestValidLaunchName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"go-engineer", true},
		{"plugin:agent", true},
		{"my-plugin:code-reviewer", true},
		{"plugin:", false},
		{":agent", false},
		{"a:b:c", false},
		{"plugin:$(id)", false},
		{"../x:agent", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidLaunchName(tt.name); got != tt.want {
				t.Errorf("ValidLaunchName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestDiscoverAll_SkipsInvalidNames checks that definitions whose resolved
// name (frontmatter or file name) fails ValidName are dropped from both the
// global registry and project dirs, and that an invalid extends is ignored.
func TestDiscoverAll_SkipsInvalidNames(t *testing.T) {
	globalDir := t.TempDir()
	setRegistryDir(t, globalDir)
	projDir := t.TempDir()
	agentsDir := filepath.Join(projDir, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(globalDir, "good.md"):       "---\nname: good\n---\nbody",
		filepath.Join(globalDir, "bad-global.md"): "---\nname: \"x$(touch /tmp/p)\"\n---\nbody",
		filepath.Join(agentsDir, "ok.md"):         "---\nname: ok\nextends: \"../../evil\"\n---\nbody",
		filepath.Join(agentsDir, "traversal.md"):  "---\nname: ../../settings/evil\n---\nbody",
		filepath.Join(agentsDir, "hash.md"):       "---\nname: \"a#(touch x)\"\n---\nbody",
		filepath.Join(agentsDir, ".dot.md"):       "---\n---\nbody", // name falls back to ".dot"
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	agents, err := DiscoverAll(emptyConfig(), []string{projDir})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range agents {
		names = append(names, a.Name)
		if a.Name == "ok" && a.Extends != "" {
			t.Errorf("invalid extends kept: %q", a.Extends)
		}
	}
	if got := strings.Join(names, ","); got != "good,ok" {
		t.Errorf("discovered %q, want good,ok", got)
	}
}
