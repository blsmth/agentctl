package cleanup

import (
	"encoding/json"
	"os/exec"
	"strings"
)

// ghPR is the subset of `gh pr list --json number,state` we need.
type ghPR struct {
	Number int    `json:"number"`
	State  string `json:"state"` // OPEN, MERGED, CLOSED
}

// LookupPRState shells out to gh to determine the most recent PR's state for
// branch in repoSlug. Any gh failure -- not installed, not authenticated,
// network error, malformed output -- returns PRUnknown, never PRNone: a
// missing or broken gh must never be treated as "no PR exists" and used to
// justify a deletion.
func LookupPRState(repoSlug, branch string) PRState {
	out, err := exec.Command("gh", "pr", "list",
		"--repo", repoSlug,
		"--head", branch,
		"--state", "all",
		"--json", "number,state",
	).Output()
	if err != nil {
		return PRUnknown
	}

	var prs []ghPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return PRUnknown
	}
	if len(prs) == 0 {
		return PRNone
	}

	switch strings.ToUpper(prs[0].State) {
	case "OPEN":
		return PROpen
	case "MERGED":
		return PRMerged
	case "CLOSED":
		return PRClosed
	default:
		return PRUnknown
	}
}
