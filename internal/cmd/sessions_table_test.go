package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderSessionsTableAligned(t *testing.T) {
	rows := []sessionsCells{
		{"2h", "go-engineer", "agentctl", "opus", "12", "1h2m", "$1.234"},
		{"3d", "code-reviewer", "local-infra", "sonnet", "345", "5m", "?"},
		{"4d", "x", "p", "haiku", "1", "10s", "-"},
	}
	var buf bytes.Buffer
	if err := renderSessionsTable(&buf, rows); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(ansi.Strip(buf.String()), "\n"), "\n")
	if len(lines) != 2+len(rows) {
		t.Fatalf("got %d lines", len(lines))
	}
	// Column start offsets are derived from the header and the cell
	// following each column's padding must start at the same offset.
	hdr := lines[0]
	var starts []int
	for _, h := range []string{"AGE", "AGENT", "PROJECT", "MODEL", "TURNS", "DUR", "COST"} {
		starts = append(starts, strings.Index(hdr, h))
	}
	for _, line := range lines[1:] {
		rl := []rune(line)
		for _, s := range starts[1:] {
			if s >= len(rl) || rl[s-1] != ' ' || rl[s] == ' ' {
				t.Errorf("misaligned at col %d in %q", s, line)
			}
		}
	}
}
