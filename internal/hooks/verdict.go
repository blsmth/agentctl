package hooks

import (
	"encoding/json"
	"regexp"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/verdicts"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// Seams for tests.
var (
	appendVerdict    = verdicts.Append
	allWindowRecords = windowstate.All
)

// sendMessageBody returns the text of a SendMessage tool_input. The field is
// "message" today; "content" is accepted too in case it is renamed.
func sendMessageBody(raw json.RawMessage) string {
	var in struct {
		Message string `json:"message"`
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	if in.Message != "" {
		return in.Message
	}
	return in.Content
}

// recordVerdict appends a ledger line when a SendMessage body is a review
// verdict. It runs only for the SendMessage tool and starts with one
// substring check, so every other message costs nothing. Best effort.
//
// The author is the live window in this runtime dir whose windowstate
// records the PR (dispatch completion's PRNumber, else the statusline's
// LogPR scan of its transcript), other than the reviewer itself. Unlike
// `dispatch --review-pr`, no `gh` call is made to learn the head branch, so
// when no window records the PR the author fields stay empty.
func recordVerdict(toolInput json.RawMessage, windowID, sessionID, agentName string) {
	pr, verdict, rounds, ok := verdicts.Parse(sendMessageBody(toolInput))
	if !ok {
		return
	}
	rec := verdicts.Record{
		PR:       pr,
		Verdict:  verdict,
		Rounds:   rounds,
		Repo:     paths.DirName(),
		Reviewer: windowID,
		RevAgent: agentName,
		RevSess:  sessionID,
	}
	for id, ws := range allWindowRecords() {
		if id == windowID || !recordsPR(ws, pr) {
			continue
		}
		// Prefer a window with a branch (the dispatched author) over a
		// bare one that merely linked the PR.
		if rec.Author != "" && ws.Branch == "" {
			continue
		}
		rec.Author, rec.AuthAgent, rec.AuthSess, rec.Branch = id, ws.AgentName, ws.SessionID, ws.Branch
		if repo := repoFromPRURL(prURL(ws)); repo != "" {
			rec.Repo = repo
		}
	}
	if err := appendVerdict(rec); err != nil {
		logging.Log("hook-post-tool: verdict ledger: %v", err)
	}
}

func recordsPR(ws windowstate.WindowState, pr int) bool {
	return ws.PRNumber == pr || ws.LogPR.PRNumber == pr
}

func prURL(ws windowstate.WindowState) string {
	if ws.PRURL != "" {
		return ws.PRURL
	}
	return ws.LogPR.PRURL
}

var prURLRe = regexp.MustCompile(`^https?://[^/]+/([^/]+/[^/]+)/pull/\d+`)

// repoFromPRURL returns "owner/repo" from a pull request URL, or "".
func repoFromPRURL(u string) string {
	if m := prURLRe.FindStringSubmatch(u); m != nil {
		return m[1]
	}
	return ""
}
