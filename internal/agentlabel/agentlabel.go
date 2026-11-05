// Package agentlabel is the single source of truth for mapping GitHub issue
// labels to agent names. It is a leaf package (imports nothing of ours) so
// both internal/cmd and internal/tui can depend on it.
package agentlabel

// Default is the fallback agent used when no label maps to an agent.
const Default = "go-engineer"

// aliases maps short label names to the shipped agents they select.
var aliases = map[string]string{
	"go":        "go-engineer",
	"python":    "python-engineer",
	"terraform": "terraform-engineer",
}

// Infer returns the agent selected by labels, or "" when none match. Exact
// agent-name labels win over short aliases, regardless of label order.
func Infer(labels []string) string {
	for _, l := range labels {
		for _, a := range aliases {
			if a == l {
				return a
			}
		}
	}
	for _, l := range labels {
		if a, ok := aliases[l]; ok {
			return a
		}
	}
	return ""
}

// InferOrDefault is Infer with a fallback to Default.
func InferOrDefault(labels []string) string {
	if a := Infer(labels); a != "" {
		return a
	}
	return Default
}
