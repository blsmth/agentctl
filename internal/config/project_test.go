package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProjectConfig is a helper that writes a ProjectConfig as JSON to
// .agentctl/config.json under dir.
func writeProjectConfig(t *testing.T, dir string, cfg ProjectConfig) {
	t.Helper()
	agentctlDir := filepath.Join(dir, ".agentctl")
	if err := os.MkdirAll(agentctlDir, 0o755); err != nil {
		t.Fatalf("writeProjectConfig: mkdir: %v", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("writeProjectConfig: marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentctlDir, "config.json"), data, 0o644); err != nil {
		t.Fatalf("writeProjectConfig: write: %v", err)
	}
}

// ---------------------------------------------------------------------------
// LoadProject
// ---------------------------------------------------------------------------

func TestLoadProject_HappyPath(t *testing.T) {
	dir := t.TempDir()
	want := ProjectConfig{
		BlockedCommands: []string{"kubectl delete"},
		BlockedPatterns: []string{`git\s+push\s+.*origin\s+main\b`},
	}
	writeProjectConfig(t, dir, want)

	got := LoadProject(dir)
	if got == nil {
		t.Fatal("LoadProject returned nil, want non-nil config")
	}
	if len(got.BlockedCommands) != 1 || got.BlockedCommands[0] != "kubectl delete" {
		t.Errorf("BlockedCommands = %v, want [kubectl delete]", got.BlockedCommands)
	}
	if len(got.BlockedPatterns) != 1 {
		t.Errorf("BlockedPatterns = %v, want 1 entry", got.BlockedPatterns)
	}
}

func TestLoadProject_WalkUp(t *testing.T) {
	// Create a project root with the config and a nested subdirectory.
	root := t.TempDir()
	writeProjectConfig(t, root, ProjectConfig{
		BlockedCommands: []string{"rm -rf"},
	})

	// Start search from a deeply nested subdirectory.
	nested := filepath.Join(root, "cmd", "agent", "subpkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	got := LoadProject(nested)
	if got == nil {
		t.Fatal("LoadProject walk-up returned nil, want config from parent")
	}
	if len(got.BlockedCommands) != 1 || got.BlockedCommands[0] != "rm -rf" {
		t.Errorf("BlockedCommands = %v, want [rm -rf]", got.BlockedCommands)
	}
}

func TestLoadProject_NotFound(t *testing.T) {
	// A fresh temp dir with no .agentctl directory.
	dir := t.TempDir()

	got := LoadProject(dir)
	if got != nil {
		t.Errorf("LoadProject in dir without config returned %+v, want nil", got)
	}
}

func TestLoadProject_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	agentctlDir := filepath.Join(dir, ".agentctl")
	if err := os.MkdirAll(agentctlDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write deliberately malformed JSON.
	if err := os.WriteFile(filepath.Join(agentctlDir, "config.json"), []byte("{bad json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := LoadProject(dir)
	if got != nil {
		t.Errorf("LoadProject with invalid JSON returned %+v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// ExpandProtectedBranches
// ---------------------------------------------------------------------------

func TestExpandProtectedBranches_Main(t *testing.T) {
	pc := &ProjectConfig{
		ProtectedBranches: []string{"main"},
	}
	patterns := pc.ExpandProtectedBranches()
	if len(patterns) != 1 {
		t.Fatalf("ExpandProtectedBranches() returned %d patterns, want 1", len(patterns))
	}
	want := `git\s+push\s+.*origin\s+main\b`
	if patterns[0] != want {
		t.Errorf("ExpandProtectedBranches()[0] = %q, want %q", patterns[0], want)
	}
}

func TestExpandProtectedBranches_Multiple(t *testing.T) {
	pc := &ProjectConfig{
		ProtectedBranches: []string{"main", "production", "release"},
	}
	patterns := pc.ExpandProtectedBranches()
	if len(patterns) != 3 {
		t.Fatalf("ExpandProtectedBranches() returned %d patterns, want 3", len(patterns))
	}
	for _, branch := range []string{"main", "production", "release"} {
		found := false
		for _, p := range patterns {
			if strings.Contains(p, branch) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ExpandProtectedBranches() missing pattern for branch %q", branch)
		}
	}
}

func TestExpandProtectedBranches_Empty(t *testing.T) {
	pc := &ProjectConfig{}
	patterns := pc.ExpandProtectedBranches()
	if len(patterns) != 0 {
		t.Errorf("ExpandProtectedBranches() on empty config = %v, want nil/empty", patterns)
	}
}

// ---------------------------------------------------------------------------
// Merge semantics
// ---------------------------------------------------------------------------

// TestLoadProject_PerAgentConfig verifies per-agent overrides are loaded.
func TestLoadProject_PerAgentConfig(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, ProjectConfig{
		Agents: map[string]AgentConfig{
			"go-engineer": {
				BlockedCommands: []string{"go install"},
			},
		},
	})

	got := LoadProject(dir)
	if got == nil {
		t.Fatal("LoadProject returned nil")
	}
	agentCfg, ok := got.Agents["go-engineer"]
	if !ok {
		t.Fatal("LoadProject: missing agents.go-engineer")
	}
	if len(agentCfg.BlockedCommands) != 1 || agentCfg.BlockedCommands[0] != "go install" {
		t.Errorf("agents.go-engineer.blocked_commands = %v, want [go install]", agentCfg.BlockedCommands)
	}
}

// TestLoadProject_FullConfig verifies a complete config round-trips correctly.
func TestLoadProject_FullConfig(t *testing.T) {
	dir := t.TempDir()
	want := ProjectConfig{
		BlockedCommands:   []string{"kubectl delete"},
		BlockedPatterns:   []string{`git\s+push\s+.*origin\s+main\b`},
		ProtectedBranches: []string{"main", "production"},
		Agents: map[string]AgentConfig{
			"go-engineer": {
				BlockedCommands: []string{"go install"},
			},
		},
	}
	writeProjectConfig(t, dir, want)

	got := LoadProject(dir)
	if got == nil {
		t.Fatal("LoadProject returned nil")
	}
	if len(got.BlockedCommands) != len(want.BlockedCommands) {
		t.Errorf("BlockedCommands len = %d, want %d", len(got.BlockedCommands), len(want.BlockedCommands))
	}
	if len(got.ProtectedBranches) != len(want.ProtectedBranches) {
		t.Errorf("ProtectedBranches len = %d, want %d", len(got.ProtectedBranches), len(want.ProtectedBranches))
	}
	if len(got.Agents) != 1 {
		t.Errorf("Agents len = %d, want 1", len(got.Agents))
	}
}
