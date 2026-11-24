package improve

import (
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/sessions"
)

type fakeVerdicts []Verdict

func (f fakeVerdicts) Verdicts(time.Time) []Verdict { return f }

type fakeFindings map[string][]string

func (f fakeFindings) Findings(id string) []string { return f[id] }

// stubEvidence replaces every evidence seam for one test.
func stubEvidence(t *testing.T, ss []sessions.Session, closed []closedlog.Record, recs map[string][]audit.Record) {
	t.Helper()
	oldList, oldClosed, oldDirs, oldQuery, oldNow := listSessions, closedLoad, auditDirs, auditQuery, evidenceNow
	oldV, oldF := verdictSource, findingSource
	t.Cleanup(func() {
		listSessions, closedLoad, auditDirs, auditQuery, evidenceNow = oldList, oldClosed, oldDirs, oldQuery, oldNow
		verdictSource, findingSource = oldV, oldF
	})
	listSessions = func() ([]sessions.Session, error) { return ss, nil }
	closedLoad = func(time.Time) ([]closedlog.Record, error) { return closed, nil }
	auditDirs = func() ([]string, error) { return []string{"x"}, nil }
	auditQuery = func(_ []string, f audit.Filter) ([]audit.Record, error) { return recs[f.Session], nil }
	evidenceNow = func() time.Time { return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC) }
	verdictSource, findingSource = fakeVerdicts(nil), nil
}

func TestEvidenceSingleAgent(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	ss := []sessions.Session{
		{ID: "aaaaaaaa-1", Agent: "go-engineer", StartTime: now.Add(-time.Hour), EndTime: now},
		{ID: "bbbbbbbb-2", Agent: "python-engineer", StartTime: now.Add(-time.Hour), EndTime: now},
		{ID: "cccccccc-3", Agent: "go-engineer", StartTime: now.Add(-30 * 24 * time.Hour), EndTime: now.Add(-30 * 24 * time.Hour)},
	}
	closed := []closedlog.Record{{SessionID: "aaaaaaaa-1", Done: true, PRNumber: 12, Model: "opus", Backend: "nous", TurnCount: 4}}
	recs := map[string][]audit.Record{"aaaaaaaa-1": {
		{Event: audit.EventToolDenied, Tool: "Bash", Summary: "rm -rf x"},
		{Event: audit.EventToolFailure, Tool: "Bash", Summary: "go test ./..."},
		{Event: audit.EventToolFailure, Tool: "Bash", Summary: "go test ./..."},
		{Event: audit.EventTool, Tool: "Read"},
	}}
	stubEvidence(t, ss, closed, recs)
	verdictSource = fakeVerdicts{{PR: 12, Verdict: "BLOCKED", Rounds: 2, AuthorSessionID: "aaaaaaaa-1"}}
	findingSource = fakeFindings{"aaaaaaaa-1": {"looped on the same failing command"}}

	p, err := Evidence("go-engineer", false, 7*24*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1 (other agent and old session excluded)", len(p.Sessions))
	}
	s := p.Sessions[0]
	if s.Denied != 1 || s.Failed != 2 || len(s.TopFailed) != 2 || !strings.Contains(s.TopFailed[0], "go test ./... (x2)") {
		t.Errorf("failure stats = %+v", s)
	}
	out := p.Render(0)
	for _, want := range []string{"agent go-engineer", "done, PR #12", "model opus", "backend nous", "1 denied, 2 failed", "verdict: PR #12 BLOCKED (round 2)", "finding: looped"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}

func TestEvidenceFleetAndSessionFilter(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	ss := []sessions.Session{
		{ID: "aaaaaaaa-1", Agent: "go-engineer", StartTime: now.Add(-time.Hour), EndTime: now},
		{ID: "bbbbbbbb-2", Agent: "python-engineer", StartTime: now.Add(-2 * time.Hour), EndTime: now},
	}
	stubEvidence(t, ss, nil, nil)
	p, _ := Evidence("", true, 24*time.Hour, nil)
	if len(p.Sessions) != 2 {
		t.Fatalf("fleet sessions = %d, want 2", len(p.Sessions))
	}
	out := p.Render(0)
	if !strings.Contains(out, "[go-engineer]") || !strings.Contains(out, "[python-engineer]") || !strings.Contains(out, "from transcript") {
		t.Errorf("fleet render:\n%s", out)
	}
	p, _ = Evidence("", true, 24*time.Hour, []string{"bbbb"})
	if len(p.Sessions) != 1 || p.Sessions[0].ID != "bbbbbbbb-2" {
		t.Errorf("session filter = %+v", p.Sessions)
	}
}

func TestRenderCapsAndSaysWhatWasCut(t *testing.T) {
	p := &Pack{Agent: "a", Since: time.Hour}
	for i := 0; i < 200; i++ {
		p.Sessions = append(p.Sessions, SessionEvidence{ID: "abcdefgh-1", Agent: "a", Turns: i, Failed: 1, TopFailed: []string{strings.Repeat("x", 90)}})
	}
	out := p.Render(4096)
	if len(out) > 4096 {
		t.Errorf("render is %d bytes, over the cap", len(out))
	}
	if !strings.Contains(out, "session(s) cut to stay under 4 KB") {
		t.Errorf("no cut notice:\n%s", out[len(out)-200:])
	}
}

func TestLedgerVerdictsAbsent(t *testing.T) {
	if got := (ledgerVerdicts{}).Verdicts(time.Time{}); len(got) != 0 {
		t.Errorf("verdicts with no ledger = %v, want none", got)
	}
}
