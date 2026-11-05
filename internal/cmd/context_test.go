package cmd

import (
	"strings"
	"testing"

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
	wantCols := []string{"KEY", "AGENT", "SIZE", "AGE", "PREVIEW"}
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
