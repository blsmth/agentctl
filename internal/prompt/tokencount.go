package prompt

import "github.com/blsmth/agentctl/internal/tokens"

// charsPerToken is the average characters-per-token heuristic (GPT-family
// models average ~4 chars/token; Claude is similar).
const charsPerToken = 4

// contextWindowSize is the assumed usable context size used to compute
// ContextPct in TokenBreakdown when no model name is in scope. Routed
// through tokens.UsableWindow("") — rather than tokens.DefaultContextWindow
// directly — so the output-reserve accounting lives in exactly one place
// (internal/tokens) and this heuristic stays in sync with it. Use
// tokens.CtxPct(model, used) directly when the model name is known.
var contextWindowSize = tokens.UsableWindow("")

// EstimateTokens returns an approximate token count for text using the 4
// chars-per-token heuristic.
func EstimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	n := len(text) / charsPerToken
	if len(text)%charsPerToken != 0 {
		n++
	}
	return n
}

// ComputeBreakdown derives a TokenBreakdown from the sections of a
// ResolvedPrompt. Frontmatter is identified by sections whose source matches
// the top-level agent file and IsInclude is false and which appear before the
// first body section. Include sections contribute to SharedIncludes; own-file
// body sections contribute to AgentBody.
func ComputeBreakdown(rp *ResolvedPrompt) TokenBreakdown {
	var td TokenBreakdown

	// The first section from the agent's own file that is not an include is
	// the frontmatter section (if any). Subsequent own-file non-include
	// sections are the agent body.
	frontmatterDone := false

	for _, s := range rp.Sections {
		t := EstimateTokens(s.Content)
		td.Total += t

		if s.IsInclude {
			td.SharedIncludes += t
			frontmatterDone = true
			continue
		}

		if s.Source == rp.FilePath && !frontmatterDone {
			// First own-file non-include section = frontmatter.
			td.Frontmatter += t
			frontmatterDone = true
			continue
		}

		td.AgentBody += t
	}

	if contextWindowSize > 0 {
		td.ContextPct = float64(td.Total) / float64(contextWindowSize) * 100
	}

	return td
}
