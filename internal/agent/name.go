package agent

import (
	"regexp"
	"strings"
)

// MaxNameLen is the longest agent name ValidName accepts.
const MaxNameLen = 64

var validNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidName reports whether name is safe to use as an agent name.
//
// Agent names come from untrusted places -- the `name:` frontmatter of a
// cloned repo's .claude/agents/*.md and its .agentctl/config.json -- and flow
// into tmux window names (which tmux format-expands), shell command lines,
// and path components (settings/<name>.json, env.d/<name>.env). The allowed
// set is therefore deliberately narrow: ASCII letters, digits, '.', '_' and
// '-', at most MaxNameLen bytes, and no leading '.' (hidden files, "..") or
// '-' (would read as a flag).
func ValidName(name string) bool {
	if name == "" || len(name) > MaxNameLen {
		return false
	}
	if name[0] == '.' || name[0] == '-' {
		return false
	}
	return validNameRe.MatchString(name)
}

// ValidPluginName reports whether name is a Claude Code plugin-scoped agent
// reference of the form `plugin:agent` where both halves pass ValidName.
// agentctl does not index plugin agents, but dispatch accepts them by name.
// Exactly one colon: Claude Code namespaces plugin agents as plugin:agent and
// plugin agents/ directories are flat (no nested namespaces observed in any
// installed plugin), so a:b:c is rejected.
func ValidPluginName(name string) bool {
	plugin, agentName, ok := strings.Cut(name, ":")
	return ok && ValidName(plugin) && ValidName(agentName)
}

// ValidLaunchName reports whether name may be handed to a launch: either a
// plain ValidName or a ValidPluginName.
func ValidLaunchName(name string) bool {
	return ValidName(name) || ValidPluginName(name)
}
