package cmd

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/blsmth/agentctl/internal/tokens"
)

// ansiEscape matches ANSI/CSI escape sequences (SGR color codes, resets,
// etc.) emitted by lipgloss styles.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stripANSI removes all ANSI escape sequences from s.
func stripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

// columnStarts returns the byte offset (in the ANSI-stripped string) at
// which each non-space "column" run begins, in order. This is used to
// verify that a table's columns line up: every row should report the same
// number of column-start offsets as the header, at the same offsets.
func columnStarts(line string) []int {
	var starts []int
	inGap := true
	for i, r := range line {
		if r == ' ' {
			inGap = true
			continue
		}
		if inGap {
			starts = append(starts, i)
			inGap = false
		}
	}
	return starts
}

// TestPrintANSITable_ColumnsAlign verifies that printANSITable aligns column
// start offsets across the header and every data row, even when some cells
// are ANSI-styled (via lipgloss) and others are plain — the exact scenario
// that broke with text/tabwriter (it counts escape-sequence bytes as visible
// width, so styled cells desync column widths from plain ones).
func TestPrintANSITable_ColumnsAlign(t *testing.T) {
	lipgloss.Writer.Profile = colorprofile.TrueColor
	t.Cleanup(func() { lipgloss.Writer.Profile = colorprofile.NoTTY })

	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	hdr := lipgloss.NewStyle().Bold(true)

	header := []string{
		hdr.Render("MODEL"),
		hdr.Render("LABEL"),
		hdr.Render("FAMILY"),
		hdr.Render("WINDOW"),
		hdr.Render("RESERVE"),
		hdr.Render("IN"),
		hdr.Render("OUT"),
		hdr.Render("SOURCE"),
	}
	rows := [][]string{
		{
			accent.Render("claude-opus-4"), // styled
			"opus-4",                       // plain
			"opus",                         // plain
			"200k",                         // plain
			"32k",                          // plain
			"15.00",                        // plain
			"75.00",                        // plain
			muted.Render("built-in"),       // styled
		},
		{
			accent.Render("claude-sonnet-4"),
			"sonnet-4",
			"sonnet",
			"200k",
			"64k",
			"3.00",
			"15.00",
			muted.Render("built-in"),
		},
	}

	var buf bytes.Buffer
	printANSITable(&buf, header, rows)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1+len(rows) {
		t.Fatalf("got %d lines, want %d (1 header + %d rows)", len(lines), 1+len(rows), len(rows))
	}

	headerStarts := columnStarts(stripANSI(lines[0]))
	if len(headerStarts) != len(header) {
		t.Fatalf("header has %d columns after stripping ANSI, want %d: %q", len(headerStarts), len(header), stripANSI(lines[0]))
	}

	for i, line := range lines[1:] {
		stripped := stripANSI(line)
		gotStarts := columnStarts(stripped)
		if len(gotStarts) != len(headerStarts) {
			t.Fatalf("row %d: got %d columns, want %d: %q", i, len(gotStarts), len(headerStarts), stripped)
		}
		for col, want := range headerStarts {
			if gotStarts[col] != want {
				t.Errorf("row %d, column %d (%s): start offset = %d, want %d (header offset)\nheader: %q\nrow:    %q",
					i, col, header[col], gotStarts[col], want, stripANSI(lines[0]), stripped)
			}
		}
	}
}

// TestPrintModelsTable_ColumnsAlign is an end-to-end check that the real
// `agentctl models` table (built via printModelsTable, which mixes styled
// MODEL/SOURCE cells with plain FAMILY/WINDOW/RESERVE/IN/OUT cells) renders
// with aligned columns once ANSI is stripped.
func TestPrintModelsTable_ColumnsAlign(t *testing.T) {
	lipgloss.Writer.Profile = colorprofile.TrueColor
	t.Cleanup(func() { lipgloss.Writer.Profile = colorprofile.NoTTY })

	// Reset the registry to just the built-ins so the row count/content is
	// deterministic regardless of any config overrides picked up elsewhere
	// in the test binary.
	tokens.SetOverrides(nil, 0)
	t.Cleanup(func() { tokens.SetOverrides(nil, 0) })

	out := captureStdout(t, printModelsTable)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected header + at least one data row, got %d lines: %q", len(lines), out)
	}

	headerStarts := columnStarts(stripANSI(lines[0]))
	if len(headerStarts) != 8 {
		t.Fatalf("header has %d columns after stripping ANSI, want 8: %q", len(headerStarts), stripANSI(lines[0]))
	}

	for i, line := range lines[1:] {
		stripped := stripANSI(line)
		gotStarts := columnStarts(stripped)
		if len(gotStarts) != len(headerStarts) {
			t.Fatalf("row %d: got %d columns, want %d: %q", i, len(gotStarts), len(headerStarts), stripped)
		}
		for col, want := range headerStarts {
			if gotStarts[col] != want {
				t.Errorf("row %d, column %d: start offset = %d, want %d (header offset)\nheader: %q\nrow:    %q",
					i, col, gotStarts[col], want, stripANSI(lines[0]), stripped)
			}
		}
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns
// whatever fn wrote to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(data)
}
