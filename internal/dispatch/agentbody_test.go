package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
)

// withRegistryDir points agent.RegistryDir at dir for the duration of the
// test and restores it on cleanup.
func withRegistryDir(t *testing.T, dir string) {
	t.Helper()
	orig := agent.RegistryDir
	agent.RegistryDir = dir
	t.Cleanup(func() { agent.RegistryDir = orig })
}

func emptyConfig() *config.Config {
	return &config.Config{Agents: map[string]config.AgentConfig{}}
}

func TestResolveAgentBody_Found(t *testing.T) {
	dir := t.TempDir()
	withRegistryDir(t, dir)

	md := "---\nname: go-engineer\ndescription: Go engineer\n---\nThe agent body.\n"
	if err := os.WriteFile(filepath.Join(dir, "go-engineer.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	got := resolveAgentBody(emptyConfig(), "go-engineer")
	if got == "" {
		t.Fatal("resolveAgentBody() returned empty string, want the agent body")
	}
	if got != "The agent body." {
		t.Errorf("resolveAgentBody() = %q, want %q", got, "The agent body.")
	}
}

func TestResolveAgentBody_NotFound(t *testing.T) {
	dir := t.TempDir()
	withRegistryDir(t, dir)

	got := resolveAgentBody(emptyConfig(), "does-not-exist")
	if got != "" {
		t.Errorf("resolveAgentBody() = %q, want empty string for an unresolvable agent", got)
	}
}

func TestResolveAgentBody_UsesComposedBody(t *testing.T) {
	dir := t.TempDir()
	withRegistryDir(t, dir)

	base := "---\nname: base-agent\n---\nBase body.\n"
	child := "---\nname: child-agent\nextends: base-agent\n---\nChild body.\n"
	if err := os.WriteFile(filepath.Join(dir, "base-agent.md"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "child-agent.md"), []byte(child), 0o644); err != nil {
		t.Fatal(err)
	}

	got := resolveAgentBody(emptyConfig(), "child-agent")
	if got == "" {
		t.Fatal("resolveAgentBody() returned empty string, want the composed body")
	}
	for _, want := range []string{"Base body.", "Child body."} {
		if !strings.Contains(got, want) {
			t.Errorf("resolveAgentBody() = %q, want it to contain %q", got, want)
		}
	}
}

func TestProjectDirsForBodyResolve_DedupesAndPrioritizesExtra(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"other-agent": {Dir: "/tmp/agentctl-test-configured-dir"},
	}}

	got := projectDirsForBodyResolve(cfg, "/tmp/agentctl-test-extra", "/tmp/agentctl-test-extra")

	if len(got) == 0 {
		t.Fatal("projectDirsForBodyResolve() returned no dirs")
	}
	if got[0] != "/tmp/agentctl-test-extra" {
		t.Errorf("projectDirsForBodyResolve()[0] = %q, want the extra dir first", got[0])
	}
	seen := make(map[string]int)
	for _, d := range got {
		seen[d]++
	}
	for d, n := range seen {
		if n > 1 {
			t.Errorf("projectDirsForBodyResolve() contains %q %d times, want deduped", d, n)
		}
	}
	var sawConfigured bool
	for _, d := range got {
		if d == "/tmp/agentctl-test-configured-dir" {
			sawConfigured = true
		}
	}
	if !sawConfigured {
		t.Error("projectDirsForBodyResolve() should include agent-configured dirs")
	}
}
