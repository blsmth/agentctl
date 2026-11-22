package sessionaudit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/logstream"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fixtureParams are the params the golden report is built with; the cost
// stub keeps the golden independent of the price table.
func fixtureParams() Params {
	p := DefaultParams()
	p.Home = "/Users/dev"
	p.Cost = func(u []TurnUsage) (float64, bool) { return float64(len(u)) * 0.05, true }
	return p
}

func loadFixture(t *testing.T) ([]logstream.Entry, []audit.Record) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entries, err := logstream.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("testdata", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var recs []audit.Record
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		var r audit.Record
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.SchemaVersion > 0 {
			recs = append(recs, r)
		}
	}
	return entries, recs
}

func TestGoldenJSON(t *testing.T) {
	entries, recs := loadFixture(t)
	r := Analyze(entries, recs, fixtureParams())
	got, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "report.golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("report JSON differs from %s (run with -update):\n%s", path, got)
	}

	var back Report
	if err := json.Unmarshal(got, &back); err != nil || back.Schema != SchemaVersion {
		t.Errorf("round trip: schema %d, err %v", back.Schema, err)
	}
}

// Without the audit trail the denial is inferred from the result text
// ("hook error": a rule), and the report says the trail was absent; with it,
// the trail's denied_by (a person) wins.
func TestFixtureWithAndWithoutAudit(t *testing.T) {
	entries, recs := loadFixture(t)
	with := Analyze(entries, recs, fixtureParams())
	without := Analyze(entries, nil, fixtureParams())

	if !with.Audit.Merged || with.Audit.Matched == 0 || without.Audit.Merged {
		t.Fatalf("audit with=%+v without=%+v", with.Audit, without.Audit)
	}
	why := func(r *Report) string {
		for _, f := range r.Findings {
			if f.Rule == "denied" {
				return f.Why
			}
		}
		return ""
	}
	if w := why(with); w != "refused by a person (audit trail)" {
		t.Errorf("with audit: %q", w)
	}
	if w := why(without); !strings.Contains(w, "rule") || !strings.Contains(w, "inferred") {
		t.Errorf("without audit: %q", w)
	}
	for _, f := range with.Findings {
		if f.Rule == "git-force-push" || f.Rule == "rm-recursive" {
			t.Errorf("false positive %s at step %d: %s", f.Rule, f.Step, f.Evidence)
		}
	}
	if with.Metrics.SidechainEntries != 2 {
		t.Errorf("SidechainEntries = %d", with.Metrics.SidechainEntries)
	}
}
