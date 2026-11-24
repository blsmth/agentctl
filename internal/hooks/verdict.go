package hooks

import (
	"encoding/json"
	"regexp"
	"sort"

	"github.com/blsmth/agentctl/internal/logging"
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
		Reviewer: windowID,
		RevAgent: agentName,
		RevSess:  sessionID,
	}
	all := allWindowRecords()
	// Recorded at dispatch: no scan, no gh. Otherwise look the PR up.
	if !fromReview(&rec, all[windowID].Review) {
		if id, ws, ok := findAuthor(all, windowID, pr); ok {
			rec.Author, rec.AuthAgent, rec.AuthSess = id, ws.AgentName, ws.SessionID
			if rec.Branch == "" {
				rec.Branch = ws.Branch
			}
			if repo := repoFromPRURL(prURL(ws)); repo != "" {
				rec.Repo = repo
			}
		}
	}
	if err := appendVerdict(rec); err != nil {
		logging.Log("hook-post-tool: verdict ledger: %v", err)
	}
}

// fromReview fills rec from the target dispatch recorded on the reviewer's
// windowstate. It reports false (nothing applied) when the record is missing,
// is for another PR, or names no author, so the lookup fallback runs (the
// recorded branch and repo are still applied when the PR matches).
func fromReview(rec *verdicts.Record, rt *windowstate.ReviewTarget) bool {
	if rt == nil || rt.PR != rec.PR {
		return false
	}
	rec.Branch, rec.Repo = rt.Branch, rt.Repo
	if rt.AuthorWindow == "" {
		return false
	}
	rec.Author, rec.AuthAgent, rec.AuthSess = rt.AuthorWindow, rt.AuthorAgent, rt.AuthorSession
	return true
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

// findAuthor picks the window that authored PR pr: never the reviewer itself
// or another review-pr window, a window whose dispatch completion recorded
// the PR (PRNumber) over one that only saw it in its transcript (LogPR), and
// the lowest window ID among equals so the result is deterministic.
func findAuthor(all map[string]windowstate.WindowState, reviewer string, pr int) (string, windowstate.WindowState, bool) {
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	bestID, best, bestRank := "", windowstate.WindowState{}, 0
	for _, id := range ids {
		ws := all[id]
		if id == reviewer || ws.Mode == "review-pr" || !recordsPR(ws, pr) {
			continue
		}
		rank := 1
		if ws.PRNumber == pr {
			rank = 2
		}
		if rank > bestRank {
			bestID, best, bestRank = id, ws, rank
		}
	}
	return bestID, best, bestRank > 0
}
