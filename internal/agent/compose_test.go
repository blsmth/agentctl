package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a test helper that creates a file with the given content.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	return path
}

// ── ReadBody tests ────────────────────────────────────────────────────────────

func TestReadBody_WithFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "agent.md", "---\nname: test\n---\nThis is the body.\n")

	body, err := ReadBody(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "This is the body." {
		t.Errorf("ReadBody() = %q, want %q", body, "This is the body.")
	}
}

func TestReadBody_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "agent.md", "No frontmatter here.\nJust content.")

	body, err := ReadBody(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "No frontmatter here.\nJust content." {
		t.Errorf("ReadBody() = %q, want full content", body)
	}
}

func TestReadBody_UnclosedFrontmatter(t *testing.T) {
	// Opening --- present but no closing --- : everything after opening delimiter
	// is treated as the body.
	dir := t.TempDir()
	path := writeFile(t, dir, "agent.md", "---\nname: test\nsome body text\n")

	body, err := ReadBody(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The function returns content after the opening "---\n"
	if !strings.Contains(body, "name: test") {
		t.Errorf("ReadBody() = %q; want content after opening delimiter", body)
	}
}

// ── Compose tests ─────────────────────────────────────────────────────────────

func TestCompose_InheritsDescription(t *testing.T) {
	base := Agent{Name: "base", Description: "Base description", Dir: "/base", Model: "sonnet"}
	local := Agent{Name: "local", Description: ""}

	out := Compose(local, "local body", base, "base body")
	if out.Description != "Base description" {
		t.Errorf("Description = %q, want %q", out.Description, "Base description")
	}
}

func TestCompose_OverridesDescription(t *testing.T) {
	base := Agent{Name: "base", Description: "Base description"}
	local := Agent{Name: "local", Description: "Local description"}

	out := Compose(local, "local body", base, "base body")
	if out.Description != "Local description" {
		t.Errorf("Description = %q, want %q", out.Description, "Local description")
	}
}

func TestCompose_BodyCombined(t *testing.T) {
	base := Agent{Name: "base"}
	local := Agent{Name: "local"}

	out := Compose(local, "local content", base, "base content")
	if !strings.Contains(out.ComposedBody, "base content") {
		t.Errorf("ComposedBody missing base content: %q", out.ComposedBody)
	}
	if !strings.Contains(out.ComposedBody, "local content") {
		t.Errorf("ComposedBody missing local content: %q", out.ComposedBody)
	}
	if !strings.Contains(out.ComposedBody, "---") {
		t.Errorf("ComposedBody missing separator: %q", out.ComposedBody)
	}
	// Base must appear before local
	baseIdx := strings.Index(out.ComposedBody, "base content")
	localIdx := strings.Index(out.ComposedBody, "local content")
	if baseIdx > localIdx {
		t.Errorf("base content should appear before local content in ComposedBody")
	}
}

func TestCompose_EmptyLocalBody(t *testing.T) {
	base := Agent{Name: "base"}
	local := Agent{Name: "local"}

	out := Compose(local, "", base, "base content")
	if out.ComposedBody != "base content" {
		t.Errorf("ComposedBody = %q, want %q", out.ComposedBody, "base content")
	}
}

func TestCompose_InheritsDir(t *testing.T) {
	base := Agent{Name: "base", Dir: "/base/dir"}
	local := Agent{Name: "local", Dir: ""}

	out := Compose(local, "", base, "")
	if out.Dir != "/base/dir" {
		t.Errorf("Dir = %q, want %q", out.Dir, "/base/dir")
	}
}

func TestCompose_InheritsModel(t *testing.T) {
	base := Agent{Name: "base", Model: "claude-opus"}
	local := Agent{Name: "local", Model: ""}

	out := Compose(local, "", base, "")
	if out.Model != "claude-opus" {
		t.Errorf("Model = %q, want %q", out.Model, "claude-opus")
	}
}
