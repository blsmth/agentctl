package auditnarrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/llm"
	"github.com/blsmth/agentctl/internal/sessionaudit"
)

type stubComp struct {
	calls   int
	prompts []string
	out     string
	err     error
}

func (s *stubComp) Complete(_ context.Context, p string) (string, error) {
	s.calls++
	s.prompts = append(s.prompts, p)
	return s.out, s.err
}

func newTest(t *testing.T, c *stubComp) *Narrator {
	t.Helper()
	dir := t.TempDir()
	return &Narrator{
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

func report(id string, nFindings int) *sessionaudit.Report {
	r := &sessionaudit.Report{Session: sessionaudit.Session{ID: id, Agent: "go-engineer", Project: "agentctl", Source: "/secret/path", Title: "private title"}}
	for i := 0; i < nFindings; i++ {
		sev := sessionaudit.Low
		if i%7 == 0 {
			sev = sessionaudit.High
		}
		r.Findings = append(r.Findings, sessionaudit.Finding{
			Rule: fmt.Sprintf("rule-%d", i), Severity: sev, Step: i + 1,
			Evidence: strings.Repeat("x", 100), Why: strings.Repeat("because ", 10),
		})
	}
	r.Safety.Findings = nFindings
	return r
}

func TestSessionCachesAndRenarratesOnChange(t *testing.T) {
	c := &stubComp{out: "  Good. Risky. Change.  "}
	n := newTest(t, c)
	ctx := context.Background()

	got, err := n.Session(ctx, report("abc", 2))
	if err != nil || got != "Good. Risky. Change." {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := n.Session(ctx, report("abc", 2)); err != nil || c.calls != 1 {
		t.Fatalf("unchanged report should be cached: calls=%d err=%v", c.calls, err)
	}
	if _, err := n.Session(ctx, report("abc", 3)); err != nil || c.calls != 2 {
		t.Fatalf("changed report should re-narrate: calls=%d err=%v", c.calls, err)
	}
}

func TestPromptContentAndCap(t *testing.T) {
	c := &stubComp{out: "ok"}
	n := newTest(t, c)
	if _, err := n.Session(context.Background(), report("big", 300)); err != nil {
		t.Fatal(err)
	}
	p := c.prompts[0]
	if len(p) > MaxPromptBytes {
		t.Errorf("prompt is %d bytes, cap %d", len(p), MaxPromptBytes)
	}
	for _, leak := range []string{"/secret/path", "private title"} {
		if strings.Contains(p, leak) {
			t.Errorf("prompt leaks %q", leak)
		}
	}
	if !strings.Contains(p, "findings_omitted") || !strings.Contains(p, `"severity":"high"`) {
		t.Errorf("expected omitted count and high findings kept: %s", p[:400])
	}
}

func TestFailuresAreErrors(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		n := newTest(t, &stubComp{})
		n.Settings = func() config.TitleSettings { return config.TitleSettings{} }
		if _, err := n.Session(context.Background(), report("a", 1)); !errors.Is(err, ErrDisabled) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no credits marker spends nothing", func(t *testing.T) {
		c := &stubComp{out: "x"}
		n := newTest(t, c)
		n.NoCredits = func(string, time.Time) (backendauth.CreditState, bool) { return backendauth.CreditState{}, true }
		if _, err := n.Session(context.Background(), report("a", 1)); !errors.Is(err, backendauth.ErrNoCredits) || c.calls != 0 {
			t.Fatalf("err = %v calls=%d", err, c.calls)
		}
	})
	t.Run("no key", func(t *testing.T) {
		c := &stubComp{out: "x"}
		n := newTest(t, c)
		n.KeyAvailable = func(config.TitleSettings) error { return errors.New("none") }
		if _, err := n.Session(context.Background(), report("a", 1)); err == nil || c.calls != 0 {
			t.Fatalf("err = %v calls=%d", err, c.calls)
		}
	})
	t.Run("completion error is not cached", func(t *testing.T) {
		c := &stubComp{err: errors.New("boom")}
		n := newTest(t, c)
		for i := 0; i < 2; i++ {
			if _, err := n.Session(context.Background(), report("a", 1)); err == nil {
				t.Fatal("want error")
			}
		}
		if c.calls != 2 {
			t.Errorf("calls = %d", c.calls)
		}
	})
	t.Run("empty completion", func(t *testing.T) {
		n := newTest(t, &stubComp{out: "  "})
		if _, err := n.Session(context.Background(), report("a", 1)); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestHasFindings(t *testing.T) {
	if HasFindings(nil) || HasFindings(report("a", 0)) || !HasFindings(report("a", 1)) {
		t.Error("HasFindings wrong")
	}
}

func TestAggregateCap(t *testing.T) {
	c := &stubComp{out: "agg"}
	n := newTest(t, c)
	var reps []*sessionaudit.Report
	for i := 0; i < 20; i++ {
		reps = append(reps, report(fmt.Sprint(i), 1))
	}
	agg := sessionaudit.Summarize(reps, 2)
	if got, err := n.Aggregate(context.Background(), reps, agg); err != nil || got != "agg" {
		t.Fatalf("%q %v", got, err)
	}
	if len(c.prompts[0]) > MaxPromptBytes {
		t.Errorf("prompt %d bytes", len(c.prompts[0]))
	}
	// Same input: cached by hash.
	if _, err := n.Aggregate(context.Background(), reps, agg); err != nil || c.calls != 1 {
		t.Errorf("calls=%d err=%v", c.calls, err)
	}
}

func TestCacheKeySanitized(t *testing.T) {
	n := newTest(t, &stubComp{})
	p, err := n.cachePath("../../etc/passwd", strings.Repeat("a", 64))
	if err != nil || strings.Contains(p, "..") && !strings.HasSuffix(p, ".._.._etc_passwd.json") {
		t.Fatalf("path %q err %v", p, err)
	}
}
