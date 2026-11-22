package shorttitle

import "strings"

// ReviewPRMode is the windowstate Mode of a `dispatch --review-pr` window
// (dispatch.ModeReviewPR; a test in internal/dispatch pins the equality).
// Such a window is always kind "review".
const ReviewPRMode = "review-pr"

type kindSpec struct{ name, meaning string }

// kinds is the closed vocabulary of work kinds, in prompt order.
var kinds = []kindSpec{
	{"review", "reviewing someone else's change (a pull request, a diff)"},
	{"go", "writing code mainly in Go"},
	{"python", "writing code mainly in Python"},
	{"swift", "writing code mainly in Swift"},
	{"web", "frontend or web work (HTML, CSS, JavaScript, TypeScript)"},
	{"infra", "infrastructure, CI, deployment, cloud, build tooling"},
	{"docs", "writing or editing documentation"},
	{"tests", "writing or fixing tests"},
	{"migration", "moving, renaming or upgrading data, schemas, APIs or dependencies"},
	{"research", "investigating, reading or comparing, with no code change"},
	{"debug", "diagnosing a bug or failure"},
	{"chat", "conversation, questions or planning with no clear deliverable"},
}

// Kinds returns the allowed kind words.
func Kinds() []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = k.name
	}
	return out
}

// IsKind reports whether s is exactly an allowed kind.
func IsKind(s string) bool {
	for _, k := range kinds {
		if k.name == s {
			return true
		}
	}
	return false
}

// isSingleWord reports whether s, minus markdown and bracket decoration, is
// exactly one word.
func isSingleWord(s string) bool {
	s = strings.Trim(strings.TrimSpace(s), "#*-•>`[]() \t")
	return s != "" && len(strings.Fields(s)) == 1
}

// normalizeKind lowercases and trims s (markdown, brackets, a "kind:" label)
// and returns it when it is an allowed kind, else "".
func normalizeKind(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimLeft(s, "#*-•>`[( \t")
	s = strings.TrimRight(s, "`])* \t")
	s = strings.TrimSpace(strings.TrimPrefix(s, "kind:"))
	if IsKind(s) {
		return s
	}
	return ""
}

// Role is the short label of what a dispatched (sub-agent) window is for,
// derived from its dispatch mode and stored kind with no model call: "review"
// (review-pr mode, kind review), "research" (research, chat), "fix" (debug),
// "docs", "tests", and "build" for every other kind or none. Callers show it
// on child rows only.
func Role(mode, kind string) string {
	if mode == ReviewPRMode {
		return "review"
	}
	switch kind {
	case "review":
		return "review"
	case "research", "chat":
		return "research"
	case "debug":
		return "fix"
	case "docs":
		return "docs"
	case "tests":
		return "tests"
	}
	return "build"
}
