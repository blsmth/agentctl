package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
)

// CreateOpts holds the options for creating a new agent definition.
type CreateOpts struct {
	Name        string
	Description string
	Model       string // "inherit", "sonnet", "opus", "haiku"
	Tools       string // comma-separated tool list
	Dir         string // fixed working directory, empty = portable
	Extends     string // base template name, empty = standalone
}

// Exists checks if an agent with the given name already exists.
func Exists(name string) bool {
	path := filepath.Join(RegistryDir, name+".md")
	_, err := os.Stat(path)
	return err == nil
}

// Create writes a new agent .md file and initializes config.
func Create(opts CreateOpts) error {
	if !ValidName(opts.Name) {
		return fmt.Errorf("invalid agent name %q: use letters, digits, '.', '_' or '-' (max %d, no leading '.' or '-')", opts.Name, MaxNameLen)
	}
	if Exists(opts.Name) {
		return fmt.Errorf("agent %q already exists", opts.Name)
	}

	// Build the .md file content
	content := buildAgentFile(opts)
	path := filepath.Join(RegistryDir, opts.Name+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing agent file: %w", err)
	}

	// Initialize config with defaults and agent-specific metadata.
	cfg := config.Load()
	ac := config.DefaultAgentConfig()
	ac.Dir = opts.Dir
	ac.Model = opts.Model
	if err := cfg.SetAgent(opts.Name, ac); err != nil {
		logging.Warn("agent: could not update config for %q: %v", opts.Name, err)
	}

	return nil
}

func buildAgentFile(opts CreateOpts) string {
	var sb strings.Builder

	// Frontmatter
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", opts.Name))
	sb.WriteString(fmt.Sprintf("description: %s\n", opts.Description))
	sb.WriteString(fmt.Sprintf("tools: %s\n", opts.Tools))
	sb.WriteString(fmt.Sprintf("model: %s\n", opts.Model))
	if opts.Extends != "" {
		sb.WriteString(fmt.Sprintf("extends: %s\n", opts.Extends))
	}
	sb.WriteString("---\n\n")

	// When extending a base template, the body is just local overrides.
	if opts.Extends != "" {
		sb.WriteString("<!-- Local overrides — add project-specific instructions here.\n")
		sb.WriteString("     The base template's prompt is inherited automatically via extends. -->\n")
		return sb.String()
	}

	// Blank-slate body for standalone agents.
	domain := inferDomain(opts.Name)
	sb.WriteString(fmt.Sprintf("You are a senior %s engineer.\n\n", domain))

	sb.WriteString("## Toolchain\n\n")
	sb.WriteString("<!-- Add preferred tools, formatters, linters -->\n\n")

	sb.WriteString("## Code Style\n\n")
	sb.WriteString("<!-- Add naming conventions, patterns to use/avoid -->\n\n")

	sb.WriteString("## Testing\n\n")
	sb.WriteString("<!-- Add testing patterns and preferences -->\n\n")

	sb.WriteString("## Delegation — stay in your lane\n\n")
	sb.WriteString("| Work type | Delegate to |\n")
	sb.WriteString("|-----------|-------------|\n")
	sb.WriteString("| Python code changes | `python-engineer` |\n")
	sb.WriteString("| Terraform changes | `terraform-engineer` |\n")
	sb.WriteString("| AWS/Azure CLI | `devops-engineer` |\n")

	return sb.String()
}

// inferDomain extracts a domain name from the agent name.
// "go-engineer" → "Go", "rust-engineer" → "Rust", "my-agent" → "my-agent"
func inferDomain(name string) string {
	suffixes := []string{"-engineer", "-agent", "-worker"}
	for _, suffix := range suffixes {
		if strings.HasSuffix(name, suffix) {
			domain := strings.TrimSuffix(name, suffix)
			// Capitalize first letter of each word
			parts := strings.Split(domain, "-")
			for i, p := range parts {
				if len(p) > 0 {
					parts[i] = strings.ToUpper(p[:1]) + p[1:]
				}
			}
			return strings.Join(parts, " ")
		}
	}
	return name
}
