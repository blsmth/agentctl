// Package prompt provides utilities for resolving and analysing agent prompt
// files, including recursive expansion of @-include references and token
// estimation.
package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/agent"
)

const maxDepth = 10

// atIncludeRe matches a standalone @ reference line, e.g. "@../shared/go.md".
var atIncludeRe = regexp.MustCompile(`^@(.+\.md)\s*$`)

// ResolvedPrompt is the result of fully expanding an agent prompt file.
type ResolvedPrompt struct {
	Agent      string    // agent name
	FilePath   string    // source .md file
	RawContent string    // original file content
	Expanded   string    // fully resolved with all @-includes inlined
	Sections   []Section // ordered sections with provenance
	Tokens     TokenBreakdown
	ResolvedAt time.Time
}

// Section is a single contiguous block of content within a ResolvedPrompt,
// with provenance tracked back to its source file.
type Section struct {
	Source    string // file path that contributed this section
	Content   string
	StartLine int // line offset in Expanded
	Tokens    int
	IsInclude bool // true when this section came from an @-include
}

// TokenBreakdown holds per-category token counts.
type TokenBreakdown struct {
	Total          int
	Frontmatter    int
	AgentBody      int     // tokens from the agent's own .md
	SharedIncludes int     // tokens from @-referenced files
	ContextPct     float64 // Total / tokens.DefaultContextWindow * 100
}

// Resolve reads the agent file at filePath, expands all @-include references
// recursively, and returns a ResolvedPrompt describing the fully-expanded
// content with per-section provenance and token counts.
func Resolve(filePath string) (*ResolvedPrompt, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("resolving path %q: %w", filePath, err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("reading file %q: %w", absPath, err)
	}
	raw := string(data)

	// Parse frontmatter to discover the agent name.
	fm, fmErr := agent.ParseFrontmatter(data)
	agentName := strings.TrimSuffix(filepath.Base(absPath), ".md")
	if fmErr == nil && fm.Name != "" {
		agentName = fm.Name
	}

	// Extract frontmatter text for token counting.
	fmText := extractFrontmatter(raw)

	// Body is everything after the frontmatter.
	body := extractBody(raw)

	// If the agent has an extends field, use agent.Compose to build the base
	// body before @-include expansion. We resolve the base agent by looking in
	// the same registry directory as the agent file.
	if fmErr == nil && fm.Extends != "" {
		body = composeExtends(absPath, fm, body)
	}

	visited := map[string]bool{absPath: true}
	var sections []Section
	currentLine := 0

	// Add frontmatter as the first section (non-include, sourced from the agent file).
	if fmText != "" {
		s := Section{
			Source:    absPath,
			Content:   fmText,
			StartLine: currentLine,
			IsInclude: false,
		}
		s.Tokens = EstimateTokens(fmText)
		sections = append(sections, s)
		currentLine += lineCount(fmText)
	}

	// Expand the body (which may include @-references).
	bodySections, err := expandContent(body, absPath, visited, 0)
	if err != nil {
		return nil, err
	}

	for i := range bodySections {
		bodySections[i].StartLine = currentLine
		currentLine += lineCount(bodySections[i].Content)
		sections = append(sections, bodySections[i])
	}

	// Build expanded string.
	var sb strings.Builder
	if fmText != "" {
		sb.WriteString(fmText)
		sb.WriteByte('\n')
	}
	for _, s := range sections {
		if s.Source == absPath && !s.IsInclude {
			continue // already wrote frontmatter; body goes below
		}
		sb.WriteString(s.Content)
		sb.WriteByte('\n')
	}

	// Rebuild expanded from sections in order.
	expanded := buildExpanded(fmText, bodySections)

	rp := &ResolvedPrompt{
		Agent:      agentName,
		FilePath:   absPath,
		RawContent: raw,
		Expanded:   expanded,
		Sections:   sections,
		ResolvedAt: time.Now(),
	}
	rp.Tokens = ComputeBreakdown(rp)

	return rp, nil
}

// buildExpanded assembles the final expanded string from the frontmatter text
// and the ordered body sections.
func buildExpanded(fmText string, bodySections []Section) string {
	var sb strings.Builder
	if fmText != "" {
		sb.WriteString(fmText)
		sb.WriteByte('\n')
	}
	for i, s := range bodySections {
		sb.WriteString(s.Content)
		if i < len(bodySections)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// expandContent recursively expands @-include lines within content. It returns
// an ordered slice of Sections. The sourceFile parameter is the file whose
// content is being expanded (used for provenance). visited tracks absolute
// paths already included (cycle detection). depth limits recursion.
func expandContent(content, sourceFile string, visited map[string]bool, depth int) ([]Section, error) {
	if depth > maxDepth {
		// Return the content as-is when depth limit is reached.
		return []Section{{
			Source:    sourceFile,
			Content:   content,
			IsInclude: false,
			Tokens:    EstimateTokens(content),
		}}, nil
	}

	lines := strings.Split(content, "\n")
	var sections []Section
	var buf strings.Builder

	flush := func() {
		text := strings.TrimRight(buf.String(), "\n")
		if text != "" {
			sections = append(sections, Section{
				Source:    sourceFile,
				Content:   text,
				IsInclude: false,
				Tokens:    EstimateTokens(text),
			})
		}
		buf.Reset()
	}

	for _, line := range lines {
		m := atIncludeRe.FindStringSubmatch(line)
		if m == nil {
			buf.WriteString(line)
			buf.WriteByte('\n')
			continue
		}

		// Flush buffered non-include lines.
		flush()

		// Resolve the include path relative to the directory of sourceFile.
		ref := m[1]
		refAbs, err := filepath.Abs(filepath.Join(filepath.Dir(sourceFile), ref))
		if err != nil {
			// Warn and continue.
			sections = append(sections, warningSection(ref, fmt.Sprintf("could not resolve path: %v", err)))
			continue
		}

		if visited[refAbs] {
			// Cycle detected — emit a warning section.
			sections = append(sections, warningSection(refAbs, "cycle detected, skipping"))
			continue
		}

		includeData, err := os.ReadFile(refAbs)
		if err != nil {
			// Missing file — emit a warning section.
			sections = append(sections, warningSection(refAbs, fmt.Sprintf("file not found: %v", err)))
			continue
		}

		// Mark visited before recursing to detect mutual cycles.
		visited[refAbs] = true

		includeBody := extractBody(string(includeData))
		childSections, err := expandContent(includeBody, refAbs, visited, depth+1)
		if err != nil {
			return nil, err
		}

		// Merge child sections, marking them as includes.
		for _, cs := range childSections {
			cs.IsInclude = true
			sections = append(sections, cs)
		}
	}

	flush()
	return sections, nil
}

// warningSection returns a Section that documents a resolution warning.
func warningSection(source, msg string) Section {
	text := fmt.Sprintf("<!-- agentctl warning: %s: %s -->", source, msg)
	return Section{
		Source:    source,
		Content:   text,
		IsInclude: true,
		Tokens:    EstimateTokens(text),
	}
}

// extractFrontmatter returns the raw frontmatter block (including delimiters)
// from a markdown file, or "" if none is present.
func extractFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return ""
	}
	end := strings.Index(content[4:], "\n---")
	if end == -1 {
		return ""
	}
	return content[:4+end+4] // up to and including the closing "---"
}

// extractBody returns everything after the frontmatter block.
func extractBody(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return content
	}
	end := strings.Index(content[4:], "\n---")
	if end == -1 {
		return content[4:]
	}
	bodyStart := 4 + end + 4
	if bodyStart < len(content) && content[bodyStart] == '\n' {
		bodyStart++
	}
	return strings.TrimRight(content[bodyStart:], "\n")
}

// composeExtends resolves the base agent and prepends its body to localBody
// using the same rules as agent.Compose. Returns localBody unmodified if the
// base cannot be resolved.
func composeExtends(agentPath string, fm agent.Frontmatter, localBody string) string {
	registryDir := filepath.Dir(agentPath)
	basePath := filepath.Join(registryDir, fm.Extends+".md")

	baseData, err := os.ReadFile(basePath)
	if err != nil {
		return localBody
	}
	baseBody := extractBody(string(baseData))
	baseFM, _ := agent.ParseFrontmatter(baseData)

	local := agent.Agent{Name: fm.Name}
	base := agent.Agent{Name: baseFM.Name}
	if base.Name == "" {
		base.Name = fm.Extends
	}

	composed := agent.Compose(local, localBody, base, baseBody)
	return composed.ComposedBody
}

// lineCount returns the number of lines in s.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
