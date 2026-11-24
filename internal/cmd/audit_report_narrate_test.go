package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/auditnarrate"
	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/llm"
	"github.com/blsmth/agentctl/internal/sessionaudit"
	"github.com/blsmth/agentctl/internal/sessions"
)

type stubCompleter struct {
	calls int
	err   error
}

func (s *stubCompleter) Complete(context.Context, string) (string, error) {
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	return "Went fine. Risky bits. Change one habit.", nil
}

// stubNarrator replaces reportNarrator with one that never touches the
// network, keychain or real state dir.
func stubNarrator(t *testing.T, c *stubCompleter) {
	t.Helper()
	dir := t.TempDir()
	old, oldErr := reportNarrator, reportNoteErr
	t.Cleanup(func() { reportNarrator, reportNoteErr = old, oldErr })
	reportNarrator = func() *auditnarrate.Narrator {
		return &auditnarrate.Narrator{
			Settings: func() config.TitleSettings {
				return config.TitleSettings{Enabled: true, BackendName: "nous", Model: "m"}
			},
			KeyAvailable: func(config.TitleSettings) error { return nil },
			NewCompleter: func(config.TitleSettings) (llm.Completer, error) { return c, nil },
			NoCredits:    func(string, time.Time) (backendauth.CreditState, bool) { return backendauth.CreditState{}, false },
			StateDir:     func() (string, error) { return dir, nil },
			Now:          time.Now,
		}
	}
}

func TestAuditReportNarrate(t *testing.T) {
	path := filepath.Join(fixtureDir, "session.jsonl")

	t.Run("off by default", func(t *testing.T) {
		setupAuditReport(t, false)
		c := &stubCompleter{}
		stubNarrator(t, c)
		var buf bytes.Buffer
		if err := RunAuditReport(&buf, AuditReportOptions{Target: path, JSON: true}); err != nil {
			t.Fatal(err)
		}
		if c.calls != 0 || strings.Contains(buf.String(), "narrative") {
			t.Errorf("calls=%d output has narrative", c.calls)
		}
	})
	t.Run("text", func(t *testing.T) {
		setupAuditReport(t, false)
		c := &stubCompleter{}
		stubNarrator(t, c)
		var buf bytes.Buffer
		if err := RunAuditReport(&buf, AuditReportOptions{Target: path, Narrate: true}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "Verdict\n  Went fine.") || c.calls != 1 {
			t.Errorf("calls=%d:\n%s", c.calls, buf.String())
		}
	})
	t.Run("json field", func(t *testing.T) {
		setupAuditReport(t, false)
		stubNarrator(t, &stubCompleter{})
		var buf bytes.Buffer
		if err := RunAuditReport(&buf, AuditReportOptions{Target: path, JSON: true, Narrate: true}); err != nil {
			t.Fatal(err)
		}
		var r sessionaudit.Report
		if err := json.Unmarshal(buf.Bytes(), &r); err != nil || r.Narrative == "" {
			t.Fatalf("err=%v narrative=%q", err, r.Narrative)
		}
	})
	t.Run("failure is a note, report still printed", func(t *testing.T) {
		setupAuditReport(t, false)
		stubNarrator(t, &stubCompleter{err: errors.New("boom")})
		var buf bytes.Buffer
		if err := RunAuditReport(&buf, AuditReportOptions{Target: path, Narrate: true}); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !strings.Contains(out, "Safety") || !strings.Contains(out, "Narrative unavailable: boom") {
			t.Errorf("output:\n%s", out)
		}
	})
	t.Run("failure with json keeps stdout valid", func(t *testing.T) {
		setupAuditReport(t, false)
		stubNarrator(t, &stubCompleter{err: errors.New("boom")})
		var errBuf bytes.Buffer
		reportNoteErr = &errBuf
		var buf bytes.Buffer
		if err := RunAuditReport(&buf, AuditReportOptions{Target: path, JSON: true, Narrate: true}); err != nil {
			t.Fatal(err)
		}
		var r sessionaudit.Report
		if err := json.Unmarshal(buf.Bytes(), &r); err != nil || r.Narrative != "" {
			t.Fatalf("err=%v", err)
		}
		if !strings.Contains(errBuf.String(), "Narrative unavailable") {
			t.Errorf("stderr = %q", errBuf.String())
		}
	})
}

func TestAuditReportNarrateAll(t *testing.T) {
	setupAuditReport(t, false)
	home := t.TempDir()
	sessions.SetClaudeDirForTest(home)
	t.Cleanup(func() { sessions.SetClaudeDirForTest("") })
	writeSession(t, home, "hookshot", fixtureSession, "2026-11-20", "go-engineer")
	writeSession(t, home, "hookshot", "11111111-2222-3333-4444-555555555555", "2026-11-21", "go-engineer")
	c := &stubCompleter{}
	stubNarrator(t, c)

	var buf bytes.Buffer
	err := RunAuditReport(&buf, AuditReportOptions{All: true, Since: "36500d", Limit: 20, JSON: true, Narrate: true})
	if err != nil {
		t.Fatal(err)
	}
	var m sessionaudit.Multi
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Aggregate.Narrative == "" {
		t.Error("aggregate has no narrative")
	}
	withFindings := 0
	for _, r := range m.Reports {
		if auditnarrate.HasFindings(r) {
			withFindings++
			if r.Narrative == "" {
				t.Errorf("%s has findings but no narrative", r.Session.ID)
			}
		} else if r.Narrative != "" {
			t.Errorf("%s is clean but narrated", r.Session.ID)
		}
	}
	if c.calls != withFindings+1 {
		t.Errorf("calls = %d, want %d sessions + 1 aggregate", c.calls, withFindings)
	}
}

func TestNarrateAllCapsAndStopsOnError(t *testing.T) {
	mk := func(n int) []*sessionaudit.Report {
		var rs []*sessionaudit.Report
		for i := 0; i < n; i++ {
			r := &sessionaudit.Report{Session: sessionaudit.Session{ID: string(rune('a'+i%26)) + strings.Repeat("x", i/26+1)}}
			r.Safety.Findings = 1
			rs = append(rs, r)
		}
		return rs
	}
	t.Run("cap", func(t *testing.T) {
		c := &stubCompleter{}
		stubNarrator(t, c)
		rs := mk(25)
		agg := sessionaudit.Summarize(rs, 0)
		note := narrateAll(rs, &agg)
		if c.calls != maxNarrated+1 || !strings.Contains(note, "skipped 5") {
			t.Errorf("calls=%d note=%q", c.calls, note)
		}
	})
	t.Run("stops at first failure", func(t *testing.T) {
		c := &stubCompleter{err: errors.New("dead")}
		stubNarrator(t, c)
		rs := mk(5)
		agg := sessionaudit.Summarize(rs, 0)
		if note := narrateAll(rs, &agg); c.calls != 1 || !strings.Contains(note, "dead") {
			t.Errorf("calls=%d note=%q", c.calls, note)
		}
	})
}
