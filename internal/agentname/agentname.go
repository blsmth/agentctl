// Package agentname holds the agent-name safety rules. It is a leaf package so
// both internal/agent and internal/config (which internal/agent imports) can
// apply the same checks; internal/agent re-exports them.
package agentname

import (
	"regexp"
	"strings"
)

// MaxLen is the longest agent name Valid accepts.
const MaxLen = 64

var validRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Valid reports whether name is safe to use as an agent name: ASCII letters,
// digits, '.', '_' and '-', at most MaxLen bytes, and no leading '.' or '-'.
// See agent.ValidName for why the set is this narrow.
func Valid(name string) bool {
	if name == "" || len(name) > MaxLen {
		return false
	}
	if name[0] == '.' || name[0] == '-' {
		return false
	}
	return validRe.MatchString(name)
}

// ValidPlugin reports whether name has the form `plugin:agent` with both
// halves passing Valid and exactly one colon.
func ValidPlugin(name string) bool {
	plugin, agentName, ok := strings.Cut(name, ":")
	return ok && Valid(plugin) && Valid(agentName)
}

// ValidLaunch reports whether name may be handed to a launch: either a plain
// Valid name or a ValidPlugin reference.
func ValidLaunch(name string) bool {
	return Valid(name) || ValidPlugin(name)
}
