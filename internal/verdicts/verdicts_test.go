package verdicts

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		pr, rounds int
		verdict    string
		ok         bool
	}{
		{"merge", "REVIEW PR #12 — VERDICT: MERGE\nlooks good", 12, 0, "MERGE", true},
		{"followups not truncated", "REVIEW PR #3 — VERDICT: MERGE-WITH-FOLLOWUPS", 3, 0, "MERGE-WITH-FOLLOWUPS", true},
		{"blocked with round", "REVIEW PR #7 - VERDICT: BLOCKED (round 2)", 7, 2, "BLOCKED", true},
		{"ordinal round", "REVIEW PR #7 — VERDICT: NEEDS-BRENDAN, 3rd round", 7, 3, "NEEDS-BRENDAN", true},
		{"template line", "REVIEW PR #310 — VERDICT: BLOCKED\nHead reviewed: 0c0db97. Rounds: 2. CI: green", 310, 2, "BLOCKED", true},
		{"leading whitespace", "  \nREVIEW PR #4 — VERDICT: MERGE", 4, 0, "MERGE", true},
		{"quoted mid-message", "Relaying: REVIEW PR #12 — VERDICT: MERGE", 0, 0, "", false},
		{"quoted on later line", "fyi\nREVIEW PR #12 — VERDICT: MERGE", 0, 0, "", false},
		{"other message", "hello", 0, 0, "", false},
		{"unknown verdict", "REVIEW PR #7 — VERDICT: MAYBE", 0, 0, "", false},
		{"no number", "REVIEW PR # — VERDICT: MERGE", 0, 0, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr, v, r, ok := Parse(tt.body)
			if pr != tt.pr || v != tt.verdict || r != tt.rounds || ok != tt.ok {
				t.Fatalf("Parse = %d %q %d %v", pr, v, r, ok)
			}
		})
	}
}

func TestAppendLoadPrune(t *testing.T) {
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := Append(Record{TS: at, PR: 1, Verdict: Blocked, AuthAgent: "go-engineer"}); err != nil {
		t.Fatal(err)
	}
	if err := Append(Record{TS: at.Add(time.Hour), PR: 2, Verdict: Merge, AuthAgent: "go-engineer"}); err != nil {
		t.Fatal(err)
	}
	if err := Append(Record{TS: at.Add(2 * time.Hour), PR: 3, Verdict: Merge}); err != nil {
		t.Fatal(err)
	}
	recs, err := Load(at.Add(-time.Hour))
	if err != nil || len(recs) != 3 || recs[0].PR != 3 {
		t.Fatalf("Load = %+v, %v", recs, err)
	}
	if recs, _ := Load(at.Add(90 * time.Minute)); len(recs) != 1 {
		t.Fatalf("since filter kept %d", len(recs))
	}

	stats := ByAuthor(recs)
	if len(stats) != 2 || stats[0].Agent != "go-engineer" || stats[0].Total != 2 || stats[0].Rate != 0.5 || stats[1].Agent != Unknown {
		t.Fatalf("ByAuthor = %+v", stats)
	}

	dir, _ := dirFn()
	p := filepath.Join(dir, FileName(at))
	old := at.Add(-200 * 24 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if n, _ := Prune(at, MaxAge); n != 1 {
		t.Fatalf("Prune removed %d", n)
	}
}

func TestLoadSkipsCorruptAndMissing(t *testing.T) {
	dir, _ := dirFn()
	if recs, err := Load(time.Unix(0, 0)); err != nil || len(recs) != 0 {
		t.Fatalf("missing ledger: %v %v", recs, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "verdicts-2026-11.ndjson")
	if err := os.WriteFile(f, []byte("not json\n{\"ts\":\"2026-11-01T00:00:00Z\",\"pr\":4,\"verdict\":\"MERGE\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if recs, _ := Load(time.Unix(0, 0)); len(recs) != 1 || recs[0].PR != 4 {
		t.Fatalf("got %+v", recs)
	}
}
