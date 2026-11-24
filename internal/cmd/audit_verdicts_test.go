package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/verdicts"
)

func stubVerdicts(t *testing.T, recs []verdicts.Record) *time.Time {
	t.Helper()
	var gotSince time.Time
	origLoad, origNow := verdictsLoad, verdictsNow
	t.Cleanup(func() { verdictsLoad, verdictsNow = origLoad, origNow })
	verdictsNow = func() time.Time { return time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC) }
	verdictsLoad = func(since time.Time) ([]verdicts.Record, error) { gotSince = since; return recs, nil }
	return &gotSince
}

func TestRunAuditVerdictsTable(t *testing.T) {
	since := stubVerdicts(t, []verdicts.Record{
		{TS: time.Now(), PR: 5, Verdict: "BLOCKED", AuthAgent: "go-engineer", RevAgent: "code-reviewer", Rounds: 2},
		{TS: time.Now(), PR: 6, Verdict: "MERGE", AuthAgent: "go-engineer"},
		{TS: time.Now(), PR: 7, Verdict: "MERGE"},
	})
	var b bytes.Buffer
	if err := RunAuditVerdicts(&b, "7d", false); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC); !since.Equal(want) {
		t.Fatalf("since = %v, want %v", since, want)
	}
	out := b.String()
	for _, want := range []string{"#5", "BLOCKED", "go-engineer", "50%", "(unknown)", "0%"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunAuditVerdictsJSONAndEmpty(t *testing.T) {
	stubVerdicts(t, nil)
	var b bytes.Buffer
	if err := RunAuditVerdicts(&b, "", true); err != nil {
		t.Fatal(err)
	}
	var rep verdictsReport
	if err := json.Unmarshal(b.Bytes(), &rep); err != nil || rep.Verdicts == nil || rep.Authors == nil {
		t.Fatalf("json = %s (%v)", b.String(), err)
	}
	b.Reset()
	if err := RunAuditVerdicts(&b, "30d", false); err != nil || !strings.Contains(b.String(), "No review verdicts") {
		t.Fatalf("out = %q, %v", b.String(), err)
	}
	if err := RunAuditVerdicts(&b, "bogus", false); err == nil {
		t.Fatal("want error for bad --since")
	}
}
