package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sharedRefRe matches @shared/foo.md include directives in agent body text.
var sharedRefRe = regexp.MustCompile(`@(?:\.\./)?shared/([a-zA-Z0-9_-]+\.md)`)

// ParseSharedRefs extracts @shared/foo.md references from agent body text.
// Returns a deduplicated slice of relative paths like "shared/foo.md".
func ParseSharedRefs(body string) []string {
	matches := sharedRefRe.FindAllStringSubmatch(body, -1)
	seen := make(map[string]bool)
	var refs []string
	for _, m := range matches {
		ref := "shared/" + m[1]
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	return refs
}

// sharedFilePath resolves a ref like "shared/foo.md" to its file on disk
// under SharedDir. Refs are normalized by ParseSharedRefs to always carry a
// leading "shared/" (the form they take in "@shared/foo.md" directives),
// but SharedDir already IS the "shared" directory
// (~/.claude/shared), so that prefix must be stripped before joining --
// joining it unstripped would look for ~/.claude/shared/shared/foo.md.
func sharedFilePath(ref string) string {
	return filepath.Join(SharedDir, strings.TrimPrefix(ref, "shared/"))
}

// maxSharedRefDepth bounds how many levels of @shared/... nesting
// ExpandSharedRefs will follow: a shared file may itself reference another
// shared file. Combined with the visited-set cycle guard below, this is
// belt-and-braces -- the visited set alone already prevents an infinite
// loop, but the depth cap keeps a long chain of legitimate (non-cyclic)
// includes from ballooning the system prompt.
const maxSharedRefDepth = 3

// ExpandSharedRefs reads the shared files referenced by body and returns
// their concatenated contents plus the list of refs that could not be
// resolved.
//
// Claude Code does NOT expand @ imports in agent definition files (verified
// empirically -- see the package doc in the agentctl dev skill), so the raw
// "@shared/foo.md" lines an agent's .md body contains are dead text on their
// own. This function is how their content actually reaches the model: the
// caller is expected to inject the returned string into the system prompt it
// builds by hand.
//
// Refs are expanded in the order they first appear, depth-first, and
// deduplicated across the whole tree via a visited set -- so a ref included
// by two different files (or a cycle, A includes B includes A) is only
// expanded once and can never loop forever. Nesting is capped at
// maxSharedRefDepth levels; refs found deeper than that are silently not
// expanded (they will not appear in expanded and are not reported as
// missing -- they were never "looked up", just not descended into).
//
// missing lists every ref that WAS looked up but could not be read from
// disk, in the order encountered. Callers must surface these somewhere
// (e.g. a log warning) rather than drop them -- silently dropping an
// unresolved include is exactly the failure mode that motivated this
// function existing at all.
func ExpandSharedRefs(body string) (expanded string, missing []string) {
	visited := make(map[string]bool)
	var parts []string
	var missingRefs []string

	var expand func(refs []string, depth int)
	expand = func(refs []string, depth int) {
		if depth > maxSharedRefDepth {
			return
		}
		for _, ref := range refs {
			if visited[ref] {
				continue
			}
			visited[ref] = true

			data, err := os.ReadFile(sharedFilePath(ref))
			if err != nil {
				missingRefs = append(missingRefs, ref)
				continue
			}
			content := string(data)
			parts = append(parts, content)

			if nested := ParseSharedRefs(content); len(nested) > 0 {
				expand(nested, depth+1)
			}
		}
	}

	expand(ParseSharedRefs(body), 1)

	return strings.Join(parts, "\n\n"), missingRefs
}
