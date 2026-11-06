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
	URL    string `json:"url"`
}

// PRInfo is a branch's PR state plus, when one exists, its number and URL.
type PRInfo struct {
	State  PRState
	Number int
	URL    string
}

// LookupPRState shells out to gh to determine the most recent PR's state for
// branch in repoSlug. Any gh failure -- not installed, not authenticated,
// network error, malformed output -- returns PRUnknown, never PRNone: a
// missing or broken gh must never be treated as "no PR exists" and used to
// justify a deletion.
func LookupPRState(repoSlug, branch string) PRState {
	return LookupPR(repoSlug, branch).State
}

// LookupPR is LookupPRState that also returns the PR's number and URL.
func LookupPR(repoSlug, branch string) PRInfo {
	out, err := exec.Command("gh", "pr", "list",
		"--repo", repoSlug,
		"--head", branch,
		"--state", "all",
		"--json", "number,state,url",
	).Output()
	if err != nil {
		return PRInfo{State: PRUnknown}
	}

	var prs []ghPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return PRInfo{State: PRUnknown}
	}
	if len(prs) == 0 {
		return PRInfo{State: PRNone}
	}

	info := PRInfo{Number: prs[0].Number, URL: prs[0].URL}
	switch strings.ToUpper(prs[0].State) {
	case "OPEN":
		info.State = PROpen
		return info
	case "MERGED":
		info.State = PRMerged
		return info
	case "CLOSED":
		info.State = PRClosed
		return info
	default:
		return PRInfo{State: PRUnknown}
	}
}
