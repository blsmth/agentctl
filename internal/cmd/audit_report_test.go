package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/sessionaudit"
	"github.com/blsmth/agentctl/internal/sessions"
)

const fixtureSession = "5b2e0c4a-1f7d-4c3e-9a8b-6d1e2f3a4b5c"

var fixtureDir = filepath.Join("..", "sessionaudit", "testdata")

// setupAuditReport pins every seam: audit records come from a temp dir
// holding the fixture trail (unless noAudit), cost is a stub, and output is
// plain, in UTC.
func setupAuditReport(t *testing.T, noAudit bool) {
	t.Helper()
	auditDir := t.TempDir()
	if !noAudit {
		data, err := os.ReadFile(filepath.Join(fixtureDir, "audit.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(auditDir, fixtureSession+".jsonl"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldDirs, oldCost, oldHome, oldColor, oldLoc, oldFind, oldAllowed := reportAuditDirs, reportCost, reportHome, reportColor, reportLoc, reportFindSessions, reportAllowedDirs
	t.Cleanup(func() {
		reportAuditDirs, reportCost, reportHome, reportColor, reportLoc, reportFindSessions, reportAllowedDirs = oldDirs, oldCost, oldHome, oldColor, oldLoc, oldFind, oldAllowed
	})
	reportAllowedDirs = func() []string { return nil }
	reportAuditDirs = func(string) []string { return []string{auditDir} }
	reportCost = func(_ sessions.Session, u []sessionaudit.TurnUsage) (float64, bool) { return float64(len(u)) * 0.05, true }
	reportHome = func() (string, error) { return "/Users/dev", nil }
	reportColor = func(io.Writer) bool { return false }
	reportLoc = func() *time.Location { return time.UTC }
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (set UPDATE_GOLDEN=1 to create)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s (UPDATE_GOLDEN=1 to rewrite):\n%s", path, got)
	}
}

func TestAuditReportGoldenText(t *testing.T) {
	setupAuditReport(t, false)
	var buf bytes.Buffer
	path := filepath.Join(fixtureDir, "session.jsonl")
	if err := RunAuditReport(&buf, AuditReportOptions{Target: path}); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "audit_report.golden", buf.Bytes())
}

func TestAuditReportNoAuditTrail(t *testing.T) {
	setupAuditReport(t, true)
	var buf bytes.Buffer
	if err := RunAuditReport(&buf, AuditReportOptions{Target: filepath.Join(fixtureDir, "session.jsonl")}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "No audit trail for this session") {
		t.Errorf("missing absent-trail note:\n%s", out)
	}
	if !strings.Contains(out, "blocked by a rule (inferred from the result)") {
		t.Errorf("denial not inferred:\n%s", out)
	}
}

func TestAuditReportJSON(t *testing.T) {
	setupAuditReport(t, false)
	var buf bytes.Buffer
	path := filepath.Join(fixtureDir, "session.jsonl")
	if err := RunAuditReport(&buf, AuditReportOptions{Target: path, JSON: true, Severity: "high"}); err != nil {
		t.Fatal(err)
	}
	var r sessionaudit.Report
	if err := json.Unmarshal(buf.Bytes(), &r); err != nil {
		t.Fatalf("%v:\n%s", err, buf.String())
	}
	if r.Schema != 1 || r.Session.ID != fixtureSession || r.Session.Source != path || !r.Audit.Merged {
		t.Errorf("report = %+v", r.Session)
	}
	for _, f := range r.Findings {
		if f.Severity != sessionaudit.High {
			t.Errorf("--severity high kept %s (%s)", f.Rule, f.Severity)
		}
	}
	if len(r.Findings) == 0 || r.Safety.Findings <= len(r.Findings) {
		t.Errorf("filter: %d shown, safety counted %d", len(r.Findings), r.Safety.Findings)
	}
}

func TestAuditReportValidates(t *testing.T) {
	setupAuditReport(t, true)
	if err := RunAuditReport(io.Discard, AuditReportOptions{Severity: "urgent", Target: "x"}); err == nil {
		t.Error("unknown severity accepted")
	}
	if err := RunAuditReport(io.Discard, AuditReportOptions{All: true, Target: "@3"}); err == nil {
		t.Error("--all with a target accepted")
	}
}

func TestAuditReportEmptyTranscript(t *testing.T) {
	setupAuditReport(t, true)
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := RunAuditReport(&buf, AuditReportOptions{Target: path}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No agent steps yet.") {
		t.Errorf("empty transcript:\n%s", buf.String())
	}
}

// writeSession copies the fixture into a Claude projects dir under home as
// session id, with every timestamp's date replaced to order the sessions.
func writeSession(t *testing.T, home, project, id, day, agent string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(data), fixtureSession, id)
	s = strings.ReplaceAll(s, "2026-11-20T", day+"T")
	s = strings.ReplaceAll(s, `"agentSetting": "go-engineer"`, `"agentSetting": "`+agent+`"`)
	s = strings.ReplaceAll(s, "/acme/hookshot/", "/acme/"+project+"/")
	dir := filepath.Join(home, "projects", "-Users-dev-code-acme-"+project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAuditReportAll(t *testing.T) {
	setupAuditReport(t, false)
	home := t.TempDir()
	sessions.SetClaudeDirForTest(home)
	t.Cleanup(func() { sessions.SetClaudeDirForTest("") })
	writeSession(t, home, "hookshot", fixtureSession, "2026-11-20", "go-engineer")
	writeSession(t, home, "hookshot", "11111111-2222-3333-4444-555555555555", "2026-11-21", "go-engineer")
	writeSession(t, home, "relay", "99999999-2222-3333-4444-555555555555", "2026-11-19", "code-reviewer")
	reportFindSessions = func(limit int, maxAge time.Duration) ([]sessions.Session, error) {
		ss, err := sessions.FindRecentSessions(limit, maxAge)
		// A listed transcript that vanished before it was read is skipped.
		return append(ss, sessions.Session{ID: "gone", Agent: "go-engineer", LogFile: filepath.Join(home, "gone.jsonl")}), err
	}

	var buf bytes.Buffer
	if err := RunAuditReport(&buf, AuditReportOptions{All: true, Since: "36500d", Limit: 20}); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "audit_report_all.golden", buf.Bytes())

	tests := []struct {
		name string
		opts AuditReportOptions
		want []string // session IDs, in order
	}{
		{"newest first", AuditReportOptions{}, []string{"11111111-2222-3333-4444-555555555555", fixtureSession, "99999999-2222-3333-4444-555555555555"}},
		{"limit", AuditReportOptions{Limit: 1}, []string{"11111111-2222-3333-4444-555555555555"}},
		{"agent", AuditReportOptions{Agent: "code-reviewer"}, []string{"99999999-2222-3333-4444-555555555555"}},
		{"project", AuditReportOptions{Project: "relay"}, []string{"99999999-2222-3333-4444-555555555555"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			opts.All, opts.JSON, opts.Since = true, true, "36500d"
			var buf bytes.Buffer
			if err := RunAuditReport(&buf, opts); err != nil {
				t.Fatal(err)
			}
			var m sessionaudit.Multi
			if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
				t.Fatalf("%v:\n%s", err, buf.String())
			}
			var got []string
			for _, r := range m.Reports {
				got = append(got, r.Session.ID)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("sessions = %v, want %v", got, tt.want)
			}
			if m.Schema != 1 || m.Aggregate.Sessions != len(tt.want) {
				t.Errorf("schema %d aggregate %+v", m.Schema, m.Aggregate)
			}
		})
	}
}
