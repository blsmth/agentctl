package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/closedlog"
)

func stubClosed(t *testing.T, recs []closedlog.Record, now time.Time) *time.Time {
	t.Helper()
	var gotSince time.Time
	oldL, oldN := closedLoad, closedNow
	closedLoad = func(since time.Time) ([]closedlog.Record, error) { gotSince = since; return recs, nil }
	closedNow = func() time.Time { return now }
	t.Cleanup(func() { closedLoad, closedNow = oldL, oldN })
	return &gotSince
}

func TestRunStatusClosedTable(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	since := stubClosed(t, []closedlog.Record{
		{ClosedAt: now.Add(-2 * time.Hour), WindowID: "@3", SessionID: "aaaa1111", Session: "agentctl-acme", ProjectDir: "/x/acme", Instance: "go-engineer-2", ShortTitle: "Fix login", Done: true, PRNumber: 42},
		{ClosedAt: now.Add(-3 * 24 * time.Hour), WindowID: "@4", SessionID: "bbbb2222", Session: "agentctl-other", ProjectDir: "/x/other", Instance: "reviewer", FirstPrompt: "look at\nthis", State: "idle", Reason: closedlog.ReasonCascade},
	}, now)
	var out bytes.Buffer
	if err := RunStatusClosed(&out, ClosedOptions{Since: "7d"}); err != nil {
		t.Fatal(err)
	}
	if !since.Equal(now.Add(-7 * 24 * time.Hour)) {
		t.Errorf("since = %v", *since)
	}
	s := out.String()
	if strings.Contains(s, "\x1b[") {
		t.Errorf("table has escape codes: %q", s)
	}
	for _, want := range []string{"ID", "NAME", "aaaa", "bbbb", "AGENT", "PROJECT", "CLOSED", "STATE", "PR", "Fix login", "go-engineer-2", "acme", "2h ago", "done", "#42", "look at", "3d ago", "idle (cascade)"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q:\n%s", want, s)
		}
	}
	if i, j := strings.Index(s, "Fix login"), strings.Index(s, "look at"); i > j {
		t.Errorf("rows out of order:\n%s", s)
	}

	out.Reset()
	if err := RunStatusClosed(&out, ClosedOptions{Session: "oth"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Fix login") || !strings.Contains(out.String(), "look at") {
		t.Errorf("--session filter:\n%s", out.String())
	}
}

func TestRunStatusClosedJSONAndEmpty(t *testing.T) {
	now := time.Now()
	stubClosed(t, nil, now)
	var out bytes.Buffer
	if err := RunStatusClosed(&out, ClosedOptions{JSON: true}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Errorf("empty JSON = %q", out.String())
	}
	out.Reset()
	if err := RunStatusClosed(&out, ClosedOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No closed agents.") {
		t.Errorf("empty table = %q", out.String())
	}
	stubClosed(t, []closedlog.Record{{ClosedAt: now, WindowID: "@1", SessionID: "u"}}, now)
	out.Reset()
	if err := RunStatusClosed(&out, ClosedOptions{JSON: true}); err != nil {
		t.Fatal(err)
	}
	var got []closedlog.Record
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got) != 1 || got[0].SessionID != "u" {
		t.Errorf("JSON = %v, %v", got, err)
	}
	if err := RunStatusClosed(&out, ClosedOptions{Since: "bogus"}); err == nil {
		t.Error("bad --since accepted")
	}
}
