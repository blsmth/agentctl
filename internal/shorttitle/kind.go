package shorttitle

import "strings"

// ReviewPRMode is the windowstate Mode of a `dispatch --review-pr` window
// (dispatch.ModeReviewPR; a test in internal/dispatch pins the equality).
// Such a window is always kind "review".
const ReviewPRMode = "review-pr"

type kindSpec struct{ name, meaning string }

// kinds is the closed vocabulary of work kinds, in prompt order. The words
// name the intent of the work, not its language (the agent name carries that).
var kinds = []kindSpec{
	{"build", "adding a new feature or capability"},
	{"fix", "changing code to fix a known bug or regression"},
	{"debug", "diagnosing a failure whose root cause is not known yet"},
	{"refactor", "restructuring code with no behavior change"},
	{"tests", "writing or fixing tests, chasing flaky tests"},
	{"docs", "writing or editing documentation"},
	{"review", "reviewing someone else's change (a pull request, a diff)"},
	{"ship", "releasing, merging, installing, deploying, version bumps"},
	{"ops", "CI, infrastructure, config, permissions, environment setup"},
	{"migrate", "moving or upgrading data, schemas, APIs or dependencies"},
	{"research", "investigating, reading or comparing, with no code change"},
	{"plan", "design, specs, planning or conversation with no code deliverable"},
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

// legacyRoles maps kinds stored before the intent vocabulary to their role.
var legacyRoles = map[string]string{
	"go": "build", "python": "build", "swift": "build", "web": "build",
	"infra": "ops", "migration": "migrate", "chat": "plan",
}

// Role is the short label of what a dispatched (sub-agent) window is for,
// derived from its dispatch mode and stored kind with no model call:
// "review" (review-pr mode), "improve" (improve mode), otherwise the kind
// itself. Kinds stored before the intent vocabulary map to their closest
// current one (go, python, swift, web -> build; infra -> ops; migration ->
// migrate; chat -> plan); an unknown or empty kind is "build". Callers show it
// on child rows only.
func Role(mode, kind string) string {
	switch mode {
	case ReviewPRMode:
		return "review"
	case "improve":
		return "improve"
	}
	if IsKind(kind) {
		return kind
	}
	if r, ok := legacyRoles[kind]; ok {
		return r
	}
	return "build"
}
