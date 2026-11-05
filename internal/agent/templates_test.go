package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListTemplates(t *testing.T) {
	templates, err := ListTemplates()
	if err != nil {
		t.Fatalf("ListTemplates() error: %v", err)
	}

	// We ship at least 5 built-in templates per acceptance criteria.
	if len(templates) < 5 {
		t.Errorf("expected at least 5 templates, got %d", len(templates))
	}

	wantNames := []string{"architect", "go-engineer", "python-engineer", "terraform-engineer", "devops-engineer"}
	byName := make(map[string]TemplateInfo)
	for _, tmpl := range templates {
		byName[tmpl.Name] = tmpl
	}

	for _, name := range wantNames {
		tmpl, ok := byName[name]
		if !ok {
			t.Errorf("expected template %q not found in list", name)
			continue
		}
		if tmpl.Description == "" {
			t.Errorf("template %q has empty description", name)
		}
	}
}

func TestLoadTemplate(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"architect", false},
		{"go-engineer", false},
		{"python-engineer", false},
		{"terraform-engineer", false},
		{"devops-engineer", false},
		{"nonexistent-template", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := LoadTemplate(tt.name)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("LoadTemplate(%q) expected error, got nil", tt.name)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadTemplate(%q) error: %v", tt.name, err)
			}
			if len(data) == 0 {
				t.Errorf("LoadTemplate(%q) returned empty data", tt.name)
			}
			// Every template must have valid frontmatter.
			if !strings.HasPrefix(string(data), "---\n") {
				t.Errorf("LoadTemplate(%q) data does not start with frontmatter delimiter", tt.name)
			}
			// Every template must be parseable.
			fm, fmErr := parseFrontmatter(data)
			if fmErr != nil {
				t.Errorf("LoadTemplate(%q) frontmatter parse error: %v", tt.name, fmErr)
			}
			if fm.Description == "" {
				t.Errorf("LoadTemplate(%q) template has empty description field", tt.name)
			}
		})
	}
}

func TestInstallTemplates(t *testing.T) {
	dir := t.TempDir()

	// First install — all templates should be installed.
	installed, skipped, err := InstallTemplates(dir, false)
	if err != nil {
		t.Fatalf("InstallTemplates(false) error: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("first install: expected 0 skipped, got %d: %v", len(skipped), skipped)
	}
	if len(installed) < 5 {
		t.Errorf("first install: expected at least 5 installed, got %d", len(installed))
	}

	// Verify files exist in dir.
	for _, name := range installed {
		path := filepath.Join(dir, name+".md")
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("installed template file not found: %s", path)
		}
	}

	// Second install without force — all should be skipped.
	installed2, skipped2, err := InstallTemplates(dir, false)
	if err != nil {
		t.Fatalf("InstallTemplates(false) second call error: %v", err)
	}
	if len(installed2) != 0 {
		t.Errorf("second install (no-force): expected 0 installed, got %d: %v", len(installed2), installed2)
	}
	if len(skipped2) < 5 {
		t.Errorf("second install (no-force): expected all to be skipped, got %d skipped", len(skipped2))
	}

	// Third install with force — all should be installed again.
	installed3, skipped3, err := InstallTemplates(dir, true)
	if err != nil {
		t.Fatalf("InstallTemplates(true) error: %v", err)
	}
	if len(skipped3) != 0 {
		t.Errorf("forced install: expected 0 skipped, got %d: %v", len(skipped3), skipped3)
	}
	if len(installed3) < 5 {
		t.Errorf("forced install: expected at least 5 installed, got %d", len(installed3))
	}
}

func TestInstallTemplates_SkipsExisting(t *testing.T) {
	dir := t.TempDir()

	// Pre-create the go-engineer file with custom content.
	customContent := "custom content that should not be overwritten"
	if err := os.WriteFile(filepath.Join(dir, "go-engineer.md"), []byte(customContent), 0o644); err != nil {
		t.Fatal(err)
	}

	_, skipped, err := InstallTemplates(dir, false)
	if err != nil {
		t.Fatalf("InstallTemplates error: %v", err)
	}

	// go-engineer should be in skipped.
	found := false
	for _, s := range skipped {
		if s == "go-engineer" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected go-engineer to be in skipped list, got: %v", skipped)
	}

	// Existing content must not be overwritten.
	data, err := os.ReadFile(filepath.Join(dir, "go-engineer.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != customContent {
		t.Errorf("go-engineer.md was overwritten; content = %q", string(data))
	}
}

func TestBuildAgentFile_WithExtends(t *testing.T) {
	opts := CreateOpts{
		Name:        "my-go-engineer",
		Description: "Custom Go engineer",
		Model:       "sonnet",
		Tools:       "Read, Write, Edit",
		Extends:     "go-engineer",
	}
	got := buildAgentFile(opts)

	if !strings.Contains(got, "extends: go-engineer") {
		t.Errorf("buildAgentFile with Extends missing 'extends:' in output:\n%s", got)
	}
	// When extends is set, the body should be a stub, not the full template.
	if strings.Contains(got, "## Toolchain") {
		t.Errorf("buildAgentFile with Extends should not include full skeleton body, got:\n%s", got)
	}
}
