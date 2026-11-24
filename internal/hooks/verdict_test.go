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
	msg := func(s string) json.RawMessage { b, _ := json.Marshal(map[string]string{"to": "x", "message": s}); return b }

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
	t.Run("other messages ignored", func(t *testing.T) {
		got = nil
		recordVerdict(msg("hello"), "@2", "s", "a")
		recordVerdict(json.RawMessage(`not json`), "@2", "s", "a")
		if len(got) != 0 {
			t.Fatalf("records = %+v", got)
		}
	})
}
