package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a helper that creates a file with given content inside dir.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
	return path
}

const agentFrontmatter = "---\nname: test-agent\ndescription: Test\n---\n"

func TestResolve_NoIncludes(t *testing.T) {
	dir := t.TempDir()
	content := agentFrontmatter + "\n# Hello\n\nWorld\n"
	path := writeFile(t, dir, "agent.md", content)

	rp, err := Resolve(path)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if rp.Agent != "test-agent" {
		t.Errorf("Agent = %q; want %q", rp.Agent, "test-agent")
	}
	if rp.FilePath != path {
		t.Errorf("FilePath = %q; want %q", rp.FilePath, path)
	}
	if rp.RawContent != content {
		t.Errorf("RawContent mismatch")
	}
	if !strings.Contains(rp.Expanded, "Hello") {
		t.Errorf("Expanded missing body content")
	}
	if len(rp.Sections) == 0 {
		t.Error("expected at least one Section")
	}
	if rp.Tokens.Total == 0 {
		t.Error("Tokens.Total should be > 0")
	}
}

func TestResolve_SingleInclude(t *testing.T) {
	dir := t.TempDir()
	shared := writeFile(t, dir, "shared.md", "---\nname: shared\n---\n\n# Shared content\n")
	agent := writeFile(t, dir, "agent.md", agentFrontmatter+"\n@./shared.md\n\n# Own body\n")

	rp, err := Resolve(agent)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !strings.Contains(rp.Expanded, "Shared content") {
		t.Errorf("Expanded should contain shared content; got:\n%s", rp.Expanded)
	}
	if !strings.Contains(rp.Expanded, "Own body") {
		t.Errorf("Expanded should contain own body; got:\n%s", rp.Expanded)
	}

	// Find a section sourced from shared.md.
	var foundInclude bool
	for _, s := range rp.Sections {
		if s.Source == shared && s.IsInclude {
			foundInclude = true
			break
		}
	}
	if !foundInclude {
		t.Error("expected a Section with IsInclude=true sourced from shared.md")
	}

	if rp.Tokens.SharedIncludes == 0 {
		t.Error("SharedIncludes should be > 0")
	}
}

func TestResolve_NestedIncludes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "c.md", "---\nname: c\n---\n\n# Level C\n")
	writeFile(t, dir, "b.md", "---\nname: b\n---\n\n# Level B\n\n@./c.md\n")
	agent := writeFile(t, dir, "agent.md", agentFrontmatter+"\n# Level A\n\n@./b.md\n")

	rp, err := Resolve(agent)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	for _, want := range []string{"Level A", "Level B", "Level C"} {
		if !strings.Contains(rp.Expanded, want) {
			t.Errorf("Expanded missing %q", want)
		}
	}
}

func TestResolve_CycleDetection(t *testing.T) {
	dir := t.TempDir()
	// a.md includes b.md, b.md includes a.md
	writeFile(t, dir, "b.md", "---\nname: b\n---\n\n# B body\n\n@./a.md\n")
	agent := writeFile(t, dir, "a.md", agentFrontmatter+"\n# A body\n\n@./b.md\n")

	rp, err := Resolve(agent)
	if err != nil {
		t.Fatalf("Resolve should not return error on cycle; got: %v", err)
	}

	// Both A and B body should appear, cycle warning should appear.
	if !strings.Contains(rp.Expanded, "A body") {
		t.Error("Expanded missing A body")
	}
	if !strings.Contains(rp.Expanded, "B body") {
		t.Error("Expanded missing B body")
	}

	hasWarning := false
	for _, s := range rp.Sections {
		if strings.Contains(s.Content, "cycle detected") {
			hasWarning = true
			break
		}
	}
	if !hasWarning {
		t.Error("expected a cycle warning section")
	}
}

func TestResolve_MissingFile(t *testing.T) {
	dir := t.TempDir()
	agent := writeFile(t, dir, "agent.md", agentFrontmatter+"\n@./nonexistent.md\n\n# After missing\n")

	rp, err := Resolve(agent)
	if err != nil {
		t.Fatalf("Resolve should not error on missing include; got: %v", err)
	}

	hasWarning := false
	for _, s := range rp.Sections {
		if strings.Contains(s.Content, "file not found") {
			hasWarning = true
			break
		}
	}
	if !hasWarning {
		t.Error("expected a file-not-found warning section")
	}

	if !strings.Contains(rp.Expanded, "After missing") {
		t.Error("rest of file should still resolve after a missing include")
	}
}

func TestResolve_Dedup(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "shared.md", "---\nname: shared\n---\n\n# Shared\n")
	agent := writeFile(t, dir, "agent.md", agentFrontmatter+"\n@./shared.md\n\n@./shared.md\n")

	rp, err := Resolve(agent)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Count occurrences of "Shared" in expanded.
	count := strings.Count(rp.Expanded, "# Shared")
	if count != 1 {
		t.Errorf("shared content should appear exactly once, got %d", count)
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name  string
		input string
		// We just verify the result is within a reasonable range, not exact.
		minTokens int
		maxTokens int
	}{
		{"empty", "", 0, 0},
		{"short", "hello", 1, 3},
		{"forty chars", strings.Repeat("a", 40), 10, 10},
		{"long text", strings.Repeat("word ", 100), 100, 150},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EstimateTokens(tc.input)
			if got < tc.minTokens || got > tc.maxTokens {
				t.Errorf("EstimateTokens(%q) = %d; want [%d, %d]", tc.input, got, tc.minTokens, tc.maxTokens)
			}
		})
	}
}

func TestComputeBreakdown(t *testing.T) {
	dir := t.TempDir()
	shared := writeFile(t, dir, "shared.md", "---\nname: shared\n---\n\n# Shared content here\n")
	_ = shared
	agentPath := writeFile(t, dir, "agent.md", agentFrontmatter+"\n# Own body text here\n\n@./shared.md\n")

	rp, err := Resolve(agentPath)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	bd := rp.Tokens
	if bd.Total == 0 {
		t.Error("Total should be > 0")
	}
	if bd.Frontmatter == 0 {
		t.Error("Frontmatter should be > 0")
	}
	if bd.AgentBody == 0 {
		t.Error("AgentBody should be > 0")
	}
	if bd.SharedIncludes == 0 {
		t.Error("SharedIncludes should be > 0")
	}
	sum := bd.Frontmatter + bd.AgentBody + bd.SharedIncludes
	if sum != bd.Total {
		t.Errorf("Frontmatter+AgentBody+SharedIncludes = %d; want Total = %d", sum, bd.Total)
	}
	if bd.ContextPct <= 0 {
		t.Error("ContextPct should be > 0")
	}
}
