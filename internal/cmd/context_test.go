package cmd

import (
	"os"
	"strings"
	"testing"
	"time"

	ctxstore "github.com/blsmth/agentctl/internal/context"
)

// TestCmdContextList_HasSizeColumn covers the SIZE column added between
// AGENT and AGE -- without it, a 30KB entry and a 20-byte entry are visually
// indistinguishable in the list output, which is exactly how the entries
// causing the original context-store bloat went unnoticed.
func TestCmdContextList_HasSizeColumn(t *testing.T) {
	ctxstore.SetPathForTest(t.TempDir())

	if err := ctxstore.Set("small", ctxstore.Entry{Content: "hi"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	big := strings.Repeat("x", 5000)
	if err := ctxstore.Set("big", ctxstore.Entry{Content: big}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	out := captureStdout(t, cmdContextList)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected header + rule + at least 2 data rows, got %d lines: %q", len(lines), out)
	}

	header := lines[0]
	gotCols := strings.Fields(header)
	wantCols := []string{"KEY", "AGENT", "SIZE", "AGE", "USED", "PREVIEW"}
	if len(gotCols) != len(wantCols) {
		t.Fatalf("header columns = %v, want %v", gotCols, wantCols)
	}
	for i, want := range wantCols {
		if gotCols[i] != want {
			t.Errorf("header column %d = %q, want %q (full header: %v)", i, gotCols[i], want, gotCols)
		}
	}

	if !strings.Contains(out, "2B") {
		t.Errorf("expected small entry's byte size (2B) in output, got:\n%s", out)
	}
	if !strings.Contains(out, "5000B") {
		t.Errorf("expected big entry's byte size (5000B) in output, got:\n%s", out)
	}
}

func TestCmdContextSet_AgentFlagIgnored(t *testing.T) {
	ctxstore.SetPathForTest(t.TempDir())
	old := os.Args
	t.Cleanup(func() { os.Args = old })
	os.Args = []string{"agentctl", "context", "set", "k", "v", "--agent", "go-engineer", "--tag", "t"}

	var stdout string
	stderr := captureStderr(t, func() { stdout = captureStdout(t, cmdContextSet) })

	if stdout != "context: set \"k\"\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, "--agent is ignored") {
		t.Errorf("stderr = %q, want one line saying --agent is ignored", stderr)
	}
	e, ok, _ := ctxstore.Get("k")
	if !ok || e.Agent != "" || e.Content != "v" || len(e.Tags) != 1 {
		t.Errorf("entry = %+v", e)
	}

	os.Args = []string{"agentctl", "context", "set", "k2", "v"}
	if stderr := captureStderr(t, func() { captureStdout(t, cmdContextSet) }); stderr != "" {
		t.Errorf("stderr without --agent = %q, want empty", stderr)
	}
}

func TestCmdContextList_UsedColumn(t *testing.T) {
	ctxstore.SetPathForTest(t.TempDir())
	for _, k := range []string{"fresh", "used"} {
		if err := ctxstore.Set(k, ctxstore.Entry{Content: k}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ctxstore.MarkUsed("used", time.Now().Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, cmdContextList)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) > 5 && f[0] == "fresh":
			if f[4] != "-" {
				t.Errorf("unused USED = %q in %q", f[4], line)
			}
		case len(f) > 5 && f[0] == "used":
			if f[4] != "3h" {
				t.Errorf("used USED = %q in %q", f[4], line)
			}
		}
	}
}

func TestParseContextPrune(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    contextPruneOpts
		wantErr bool
	}{
		{"default", []string{"--used"}, contextPruneOpts{olderThan: 24 * time.Hour}, false},
		{"days and dry run", []string{"--used", "--older-than", "3d", "--dry-run"}, contextPruneOpts{olderThan: 72 * time.Hour, dryRun: true}, false},
		{"hours", []string{"--used", "--older-than", "2h"}, contextPruneOpts{olderThan: 2 * time.Hour}, false},
		{"missing --used", []string{"--older-than", "1d"}, contextPruneOpts{}, true},
		{"bad duration", []string{"--used", "--older-than", "soon"}, contextPruneOpts{}, true},
		{"empty duration", []string{"--used", "--older-than", ""}, contextPruneOpts{}, true},
		{"missing value", []string{"--used", "--older-than"}, contextPruneOpts{}, true},
		{"unknown flag", []string{"--used", "--all"}, contextPruneOpts{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseContextPrune(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
