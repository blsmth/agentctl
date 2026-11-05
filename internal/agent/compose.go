package agent

import (
	"fmt"
	"os"
	"strings"
)

// ReadBody reads an agent .md file and returns just the body — everything after
// the closing --- of the frontmatter block.
func ReadBody(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("reading agent file %q: %w", filePath, err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "---\n") {
		// No frontmatter — the whole file is the body.
		return content, nil
	}
	end := strings.Index(content[4:], "\n---")
	if end == -1 {
		// No closing delimiter — treat everything after the opening --- as body.
		return content[4:], nil
	}
	// Skip past "---\n" (4 bytes), the frontmatter block (end bytes),
	// then "\n---" (4 bytes) and the newline that follows (1 byte).
	bodyStart := 4 + end + 4
	if bodyStart < len(content) && content[bodyStart] == '\n' {
		bodyStart++
	}
	return strings.TrimRight(content[bodyStart:], "\n"), nil
}

// Compose merges a local agent with its base agent and returns a new Agent
// with ComposedBody set. Resolution rules:
//
//   - Frontmatter fields: local value wins for any field that is non-empty;
//     otherwise the base value is inherited.
//   - Body: base body + "\n\n---\n\n## Local Overrides\n\n" + local body.
func Compose(local Agent, localBody string, base Agent, baseBody string) Agent {
	out := local

	// Inherit Description from base when local has none.
	if out.Description == "" {
		out.Description = base.Description
	}

	// Inherit Dir from base when local has none.
	if out.Dir == "" {
		out.Dir = base.Dir
	}

	// Inherit Model from base when local has none.
	if out.Model == "" {
		out.Model = base.Model
	}

	// Compose body: base first, then local overrides.
	var body strings.Builder
	body.WriteString(strings.TrimRight(baseBody, "\n"))
	if localBody != "" {
		body.WriteString("\n\n---\n\n## Local Overrides\n\n")
		body.WriteString(strings.TrimRight(localBody, "\n"))
	}
	out.ComposedBody = body.String()

	return out
}
