package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgentFile(t *testing.T, dir, name, frontmatter string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".md")
	if err := os.WriteFile(p, []byte("---\nname: "+name+"\n"+frontmatter+"---\nbody of "+p+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolvesGlobal(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, registry, project string)
		wantGlobal bool
		wantReason string
	}{
		{
			name: "global file",
			setup: func(t *testing.T, registry, _ string) {
				writeAgentFile(t, registry, "code-reviewer", "")
			},
			wantGlobal: true,
		},
		{
			name: "project-local file shadows the global one",
			setup: func(t *testing.T, registry, project string) {
				writeAgentFile(t, registry, "code-reviewer", "")
				writeAgentFile(t, filepath.Join(project, ".claude", "agents"), "code-reviewer", "")
			},
			wantReason: "is not a file in",
		},
		{
			name: "project-local file, no global one",
			setup: func(t *testing.T, _, project string) {
				writeAgentFile(t, filepath.Join(project, ".claude", "agents"), "code-reviewer", "")
			},
			wantReason: "is not a file in",
		},
		{
			name: "registry entry symlinked to a project file (agentctl sync)",
			setup: func(t *testing.T, registry, project string) {
				src := writeAgentFile(t, filepath.Join(project, "elsewhere"), "code-reviewer", "")
				if err := os.MkdirAll(registry, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(src, filepath.Join(registry, "code-reviewer.md")); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "is not a file in",
		},
		{
			name: "global agent extending a project-local base",
			setup: func(t *testing.T, registry, project string) {
				writeAgentFile(t, registry, "code-reviewer", "extends: reviewer-base\n")
				writeAgentFile(t, registry, "reviewer-base", "")
				writeAgentFile(t, filepath.Join(project, ".claude", "agents"), "reviewer-base", "")
			},
			wantReason: `extends "reviewer-base"`,
		},
		{
			name: "global agent extending a global base",
			setup: func(t *testing.T, registry, _ string) {
				writeAgentFile(t, registry, "code-reviewer", "extends: reviewer-base\n")
				writeAgentFile(t, registry, "reviewer-base", "")
			},
			wantGlobal: true,
		},
		{
			name:       "missing",
			setup:      func(*testing.T, string, string) {},
			wantReason: "no agent named",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			registry := filepath.Join(root, "home", ".claude", "agents")
			project := filepath.Join(root, "project")
			if err := os.MkdirAll(project, 0o755); err != nil {
				t.Fatal(err)
			}
			setRegistryDir(t, registry)
			tt.setup(t, registry, project)
			got := ResolvesGlobal(emptyConfig(), "code-reviewer", project)
			if got.Global != tt.wantGlobal {
				t.Fatalf("Global = %v (%+v), want %v", got.Global, got, tt.wantGlobal)
			}
			if !strings.Contains(got.Reason, tt.wantReason) {
				t.Errorf("Reason = %q, want it to contain %q", got.Reason, tt.wantReason)
			}
		})
	}
}

// An account's agents dir is a symlink to the shared registry: a definition
// reached through it is still the global file.
func TestResolvesGlobalThroughSymlinkedRegistry(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "shared-agents")
	writeAgentFile(t, shared, "code-reviewer", "")
	link := filepath.Join(root, "account", "agents")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, link); err != nil {
		t.Fatal(err)
	}
	setRegistryDir(t, link)
	if got := ResolvesGlobal(emptyConfig(), "code-reviewer"); !got.Global {
		t.Fatalf("got %+v, want global", got)
	}
}
