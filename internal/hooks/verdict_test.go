package hooks

import (
	"encoding/json"
	"testing"

	"github.com/blsmth/agentctl/internal/verdicts"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestRecordVerdict(t *testing.T) {
	var got []verdicts.Record
	origAppend, origAll := appendVerdict, allWindowRecords
	t.Cleanup(func() { appendVerdict, allWindowRecords = origAppend, origAll })
	appendVerdict = func(r verdicts.Record) error { got = append(got, r); return nil }
	allWindowRecords = func() map[string]windowstate.WindowState {
		return map[string]windowstate.WindowState{
			"@1": {AgentName: "go-engineer", SessionID: "auth-sess", Branch: "agent/go-engineer/issue-9", PRNumber: 12, PRURL: "https://github.com/acme/widgets/pull/12"},
			"@2": {AgentName: "code-reviewer", SessionID: "rev-sess"},
		}
	}
	msg := func(s string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"to": "x", "message": s})
		return b
	}

	t.Run("resolved author", func(t *testing.T) {
		got = nil
		recordVerdict(msg("REVIEW PR #12 — VERDICT: BLOCKED (round 2)"), "@2", "rev-sess", "code-reviewer")
		if len(got) != 1 {
			t.Fatalf("records = %d", len(got))
		}
		r := got[0]
		if r.PR != 12 || r.Verdict != "BLOCKED" || r.Rounds != 2 || r.Author != "@1" || r.AuthAgent != "go-engineer" ||
			r.AuthSess != "auth-sess" || r.Branch != "agent/go-engineer/issue-9" || r.Repo != "acme/widgets" || r.Reviewer != "@2" {
			t.Fatalf("record = %+v", r)
		}
	})
	t.Run("unresolved author keeps what is known", func(t *testing.T) {
		got = nil
		recordVerdict(msg("REVIEW PR #99 — VERDICT: MERGE"), "@2", "rev-sess", "code-reviewer")
		if len(got) != 1 || got[0].Author != "" || got[0].PR != 99 || got[0].RevSess != "rev-sess" {
			t.Fatalf("records = %+v", got)
		}
	})
	t.Run("two candidates", func(t *testing.T) {
		got = nil
		allWindowRecords = func() map[string]windowstate.WindowState {
			return map[string]windowstate.WindowState{
				"@1": {AgentName: "code-reviewer", Mode: "review-pr", Branch: "agent/code-reviewer/review-pr-5-x", LogPR: windowstate.LogPRScan{PRNumber: 5}},
				"@3": {AgentName: "other", LogPR: windowstate.LogPRScan{PRNumber: 5}},
				"@4": {AgentName: "go-engineer", Branch: "agent/go-engineer/issue-2", PRNumber: 5},
				"@5": {AgentName: "go-engineer-2", Branch: "b", PRNumber: 5},
			}
		}
		for i := 0; i < 20; i++ { // map order must not matter
			got = nil
			recordVerdict(msg("REVIEW PR #5 — VERDICT: MERGE"), "@9", "s", "code-reviewer")
			if len(got) != 1 || got[0].Author != "@4" || got[0].AuthAgent != "go-engineer" {
				t.Fatalf("record = %+v", got)
			}
		}
	})
	t.Run("recorded review target wins", func(t *testing.T) {
		got = nil
		allWindowRecords = func() map[string]windowstate.WindowState {
			return map[string]windowstate.WindowState{
				"@2": {Mode: "review-pr", Review: &windowstate.ReviewTarget{PR: 7, Branch: "feat/x", Repo: "acme/widgets",
					AuthorWindow: "@8", AuthorAgent: "go-engineer", AuthorSession: "s8"}},
				"@3": {AgentName: "other", PRNumber: 7},
			}
		}
		recordVerdict(msg("REVIEW PR #7 — VERDICT: MERGE"), "@2", "rs", "code-reviewer")
		r := got[0]
		if r.Author != "@8" || r.AuthAgent != "go-engineer" || r.AuthSess != "s8" || r.Branch != "feat/x" || r.Repo != "acme/widgets" {
			t.Fatalf("record = %+v", r)
		}
	})
	t.Run("recorded target without author falls back", func(t *testing.T) {
		got = nil
		allWindowRecords = func() map[string]windowstate.WindowState {
			return map[string]windowstate.WindowState{
				"@2": {Review: &windowstate.ReviewTarget{PR: 7, Branch: "feat/x", Repo: "acme/widgets"}},
				"@3": {AgentName: "other", PRNumber: 7},
			}
		}
		recordVerdict(msg("REVIEW PR #7 — VERDICT: MERGE"), "@2", "rs", "code-reviewer")
		r := got[0]
		if r.Author != "@3" || r.Branch != "feat/x" || r.Repo != "acme/widgets" {
			t.Fatalf("record = %+v", r)
		}
	})
	t.Run("other messages ignored", func(t *testing.T) {
		got = nil
		recordVerdict(msg("hello"), "@2", "s", "a")
		recordVerdict(json.RawMessage(`not json`), "@2", "s", "a")
		if len(got) != 0 {
			t.Fatalf("records = %+v", got)
		}
	})
}
