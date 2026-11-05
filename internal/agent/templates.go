package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/plugin"
)

// templateFS is the embedded plugin tree; the built-in templates are the
// plugin's agent definitions (plugin/agents/*.md), the single source of truth.
var templateFS fs.FS = plugin.FS

// templatesDir is the directory within templateFS holding the templates.
const templatesDir = "agents"

// TemplateInfo describes a built-in agent template.
type TemplateInfo struct {
	// Name is the template identifier (filename without .md).
	Name string
	// Description is the one-line description from the template frontmatter.
	Description string
}

// ListTemplates returns metadata for every built-in template.
func ListTemplates() ([]TemplateInfo, error) {
	entries, err := fs.ReadDir(templateFS, templatesDir)
	if err != nil {
		return nil, fmt.Errorf("reading embedded templates: %w", err)
	}

	var templates []TemplateInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		data, err := fs.ReadFile(templateFS, templatesDir+"/"+entry.Name())
		if err != nil {
			continue
		}

		fm, err := parseFrontmatter(data)
		if err != nil {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".md")
		if fm.Name != "" {
			name = fm.Name
		}

		templates = append(templates, TemplateInfo{
			Name:        name,
			Description: fm.Description,
		})
	}

	return templates, nil
}

// LoadTemplate returns the raw bytes of the named built-in template.
// name should be the template name without the .md extension.
func LoadTemplate(name string) ([]byte, error) {
	data, err := fs.ReadFile(templateFS, templatesDir+"/"+name+".md")
	if err != nil {
		return nil, fmt.Errorf("template %q not found: %w", name, err)
	}
	return data, nil
}

// InstallTemplates copies all built-in templates into destDir. Files that
// already exist are skipped unless force is true.
//
// Returns the list of installed and skipped template names.
func InstallTemplates(destDir string, force bool) (installed, skipped []string, err error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("creating registry dir: %w", err)
	}

	entries, err := fs.ReadDir(templateFS, templatesDir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading embedded templates: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		dest := filepath.Join(destDir, entry.Name())
		name := strings.TrimSuffix(entry.Name(), ".md")

		if !force {
			if _, statErr := os.Stat(dest); statErr == nil {
				skipped = append(skipped, name)
				continue
			}
		}

		data, readErr := fs.ReadFile(templateFS, templatesDir+"/"+entry.Name())
		if readErr != nil {
			err = fmt.Errorf("reading template %q: %w", entry.Name(), readErr)
			return
		}

		if writeErr := os.WriteFile(dest, data, 0o644); writeErr != nil {
			err = fmt.Errorf("writing template %q: %w", entry.Name(), writeErr)
			return
		}

		installed = append(installed, name)
	}

	return installed, skipped, nil
}
