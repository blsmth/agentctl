package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    frontmatter
		wantErr string
	}{
		{
			name:  "valid",
			input: "---\nname: test\ndescription: A test agent\n---\nBody here",
			want:  frontmatter{Name: "test", Description: "A test agent"},
		},
		{
			name:    "no opening delimiter",
			input:   "just markdown",
			wantErr: "no frontmatter",
		},
		{
			name:    "no closing delimiter",
			input:   "---\nname: test\n",
			wantErr: "no frontmatter closing",
		},
		{
			name:  "empty frontmatter",
			input: "---\n\n---\nBody",
			want:  frontmatter{},
		},
		{
			name:  "extra fields ignored",
			input: "---\nname: test\nunknown_field: hi\n---\n",
			want:  frontmatter{Name: "test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFrontmatter([]byte(tt.input))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q doesn't contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseFrontmatter() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestInferDomain(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"go-engineer", "go-engineer", "Go"},
		{"python-engineer", "python-engineer", "Python"},
		{"rust-engineer", "rust-engineer", "Rust"},
		{"ops-agent", "ops-agent", "Ops"},
		{"data-agent", "data-agent", "Data"},
		{"multi-word-worker", "my-cool-worker", "My Cool"},
		{"no suffix", "standalone", "standalone"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferDomain(tt.input)
			if got != tt.want {
				t.Errorf("inferDomain(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildAgentFile(t *testing.T) {
	opts := CreateOpts{
		Name:        "test-engineer",
		Description: "Test agent",
		Model:       "sonnet",
		Tools:       "Read,Write,Edit",
	}
	got := buildAgentFile(opts)

	checks := []string{
		"---\n",
		"name: test-engineer",
		"description: Test agent",
		"model: sonnet",
		"tools: Read,Write,Edit",
		"## Toolchain",
	}

	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Errorf("buildAgentFile() missing %q in output:\n%s", check, got)
		}
	}
}

// setRegistryDir overrides RegistryDir for the duration of the test and
// restores it on cleanup.
func setRegistryDir(t *testing.T, dir string) {
	t.Helper()
	orig := RegistryDir
	RegistryDir = dir
	t.Cleanup(func() { RegistryDir = orig })
}

// emptyConfig returns a *config.Config with no per-agent overrides so that
// Discover falls back to zero-value AgentConfig for every agent.
func emptyConfig() *config.Config {
	return &config.Config{Agents: map[string]config.AgentConfig{}}
}

func TestDiscover_WithFixtures(t *testing.T) {
	dir := t.TempDir()
	setRegistryDir(t, dir)

	// Write two valid .md files and one that should be skipped.
	agentMD := "---\nname: alpha\ndescription: Alpha agent\n---\nAlpha body"
	betaMD := "---\nname: beta\ndescription: Beta agent\n---\nBeta body"
	registryMD := "---\nname: agent-registry\n---\nshould be skipped"
	if err := os.WriteFile(filepath.Join(dir, "alpha.md"), []byte(agentMD), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta.md"), []byte(betaMD), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-registry.md"), []byte(registryMD), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, err := DiscoverAll(emptyConfig(), nil)
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d: %+v", len(agents), agents)
	}

	byName := make(map[string]Agent)
	for _, a := range agents {
		byName[a.Name] = a
	}

	a, ok := byName["alpha"]
	if !ok {
		t.Fatal("agent 'alpha' not found")
	}
	if a.Description != "Alpha agent" {
		t.Errorf("alpha.Description = %q, want 'Alpha agent'", a.Description)
	}
	if a.FilePath == "" {
		t.Error("alpha.FilePath is empty")
	}

	if _, ok := byName["beta"]; !ok {
		t.Fatal("agent 'beta' not found")
	}
}

func TestDiscover_SkipsBrokenSymlink(t *testing.T) {
	dir := t.TempDir()
	setRegistryDir(t, dir)

	// Valid agent.
	if err := os.WriteFile(filepath.Join(dir, "good.md"), []byte("---\nname: good\ndescription: Good\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink pointing to a non-existent target.
	linkPath := filepath.Join(dir, "broken.md")
	if err := os.Symlink("/nonexistent/path/agent.md", linkPath); err != nil {
		t.Fatal(err)
	}

	agents, err := DiscoverAll(emptyConfig(), nil)
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent (broken symlink skipped), got %d: %+v", len(agents), agents)
	}
	if agents[0].Name != "good" {
		t.Errorf("unexpected agent %q", agents[0].Name)
	}
}

func TestDiscoverAll_ProjectDir(t *testing.T) {
	globalDir := t.TempDir()
	setRegistryDir(t, globalDir)

	// Create a project dir with a local agent.
	projDir := t.TempDir()
	agentsDir := filepath.Join(projDir, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: local-agent\ndescription: A local agent\n---\nBody"
	if err := os.WriteFile(filepath.Join(agentsDir, "local-agent.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, err := DiscoverAll(emptyConfig(), []string{projDir})
	if err != nil {
		t.Fatalf("DiscoverAll error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name != "local-agent" {
		t.Errorf("name = %q, want local-agent", agents[0].Name)
	}
	if agents[0].Scope != ScopeLocal {
		t.Errorf("scope = %v, want ScopeLocal", agents[0].Scope)
	}
	if agents[0].ProjectName != filepath.Base(projDir) {
		t.Errorf("projectName = %q, want %q", agents[0].ProjectName, filepath.Base(projDir))
	}
}

func TestDiscoverAll_ShadowGlobal(t *testing.T) {
	globalDir := t.TempDir()
	setRegistryDir(t, globalDir)

	// Global agent.
	globalMD := "---\nname: myagent\ndescription: Global version\n---\nGlobal body"
	if err := os.WriteFile(filepath.Join(globalDir, "myagent.md"), []byte(globalMD), 0o644); err != nil {
		t.Fatal(err)
	}

	// Project-local agent with same name.
	projDir := t.TempDir()
	agentsDir := filepath.Join(projDir, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	localMD := "---\nname: myagent\ndescription: Project version\n---\nLocal body"
	if err := os.WriteFile(filepath.Join(agentsDir, "myagent.md"), []byte(localMD), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, err := DiscoverAll(emptyConfig(), []string{projDir})
	if err != nil {
		t.Fatalf("DiscoverAll error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent (shadowed), got %d", len(agents))
	}
	if agents[0].Description != "Project version" {
		t.Errorf("expected project version to shadow global, got description=%q", agents[0].Description)
	}
	if agents[0].Scope != ScopeLocal {
		t.Errorf("scope = %v, want ScopeLocal", agents[0].Scope)
	}
	if want, _ := filepath.EvalSymlinks(filepath.Join(globalDir, "myagent.md")); agents[0].Shadows != want {
		t.Errorf("Shadows = %q, want %q", agents[0].Shadows, want)
	}
}

func TestDiscoverAll_MultipleProjectDirs(t *testing.T) {
	globalDir := t.TempDir()
	setRegistryDir(t, globalDir)

	// Two project dirs with different agents.
	for _, tc := range []struct{ dir, name string }{
		{t.TempDir(), "agent-a"},
		{t.TempDir(), "agent-b"},
	} {
		agentsDir := filepath.Join(tc.dir, ".claude", "agents")
		if err := os.MkdirAll(agentsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		md := "---\nname: " + tc.name + "\ndescription: " + tc.name + "\n---\nBody"
		if err := os.WriteFile(filepath.Join(agentsDir, tc.name+".md"), []byte(md), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Collect the temp dirs created above — they are the first two in the loop.
	// Re-create to have the paths.
	proj1 := t.TempDir()
	proj2 := t.TempDir()
	for _, tc := range []struct{ dir, name string }{
		{proj1, "agent-a"},
		{proj2, "agent-b"},
	} {
		agentsDir := filepath.Join(tc.dir, ".claude", "agents")
		if err := os.MkdirAll(agentsDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		md := "---\nname: " + tc.name + "\ndescription: " + tc.name + "\n---\nBody"
		if err := os.WriteFile(filepath.Join(agentsDir, tc.name+".md"), []byte(md), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	agents, err := DiscoverAll(emptyConfig(), []string{proj1, proj2})
	if err != nil {
		t.Fatalf("DiscoverAll error: %v", err)
	}

	byName := make(map[string]Agent)
	for _, a := range agents {
		byName[a.Name] = a
	}
	if _, ok := byName["agent-a"]; !ok {
		t.Error("agent-a not found")
	}
	if _, ok := byName["agent-b"]; !ok {
		t.Error("agent-b not found")
	}
}

func TestDiscoverAll_MissingDir(t *testing.T) {
	globalDir := t.TempDir()
	setRegistryDir(t, globalDir)

	// Pass a non-existent project dir — should not error.
	agents, err := DiscoverAll(emptyConfig(), []string{"/nonexistent/path/to/project"})
	if err != nil {
		t.Fatalf("DiscoverAll error: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

func TestDiscoverAll_Dedup(t *testing.T) {
	globalDir := t.TempDir()
	setRegistryDir(t, globalDir)

	projDir := t.TempDir()
	agentsDir := filepath.Join(projDir, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: dedup-agent\ndescription: Dedup test\n---\nBody"
	if err := os.WriteFile(filepath.Join(agentsDir, "dedup-agent.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pass the same dir twice.
	agents, err := DiscoverAll(emptyConfig(), []string{projDir, projDir})
	if err != nil {
		t.Fatalf("DiscoverAll error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent (deduped), got %d", len(agents))
	}
}

func TestDiscover_Extends(t *testing.T) {
	dir := t.TempDir()
	setRegistryDir(t, dir)

	baseMD := "---\nname: base-agent\ndescription: Base\n---\nBase body content"
	localMD := "---\nname: local-agent\ndescription: Local\nextends: base-agent\n---\nLocal body content"

	if err := os.WriteFile(filepath.Join(dir, "base-agent.md"), []byte(baseMD), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "local-agent.md"), []byte(localMD), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, err := DiscoverAll(emptyConfig(), nil)
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}

	byName := make(map[string]Agent)
	for _, a := range agents {
		byName[a.Name] = a
	}

	local, ok := byName["local-agent"]
	if !ok {
		t.Fatal("local-agent not found")
	}
	if local.ComposedBody == "" {
		t.Fatal("local-agent.ComposedBody is empty; expected composed content")
	}
	if !strings.Contains(local.ComposedBody, "Base body content") {
		t.Errorf("ComposedBody missing base body; got: %q", local.ComposedBody)
	}
	if !strings.Contains(local.ComposedBody, "Local body content") {
		t.Errorf("ComposedBody missing local body; got: %q", local.ComposedBody)
	}
}
