package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/winlink"
)

// forceLinks makes linkWindow emit hyperlinks regardless of whether stdout
// is a terminal, or never emit them, for the duration of the test.
func forceLinks(t *testing.T, on bool) {
	t.Helper()
	prev := linkWindow
	if on {
		linkWindow = winlink.Wrap
	} else {
		linkWindow = func(_, visible string) string { return visible }
	}
	t.Cleanup(func() { linkWindow = prev })
}

const oscLink = "\x1b]8;;agentctl://window/"

func TestPrintGridAlignmentWithLinks(t *testing.T) {
	grid := [][]cell{
		{{text: "@5", style: lipgloss.NewStyle(), window: "@5"}, {text: "alpha", style: cliAccent()}},
		{{text: "@123", style: lipgloss.NewStyle(), window: "@123"}, {text: "b", style: cliAccent()}},
		{{text: "-", style: cliMuted(), window: ""}, {text: "c", style: cliAccent()}},
	}
	headers := []string{"WIN", "AGENT"}

	var plain, linked bytes.Buffer
	forceLinks(t, false)
	printGrid(&plain, headers, grid)
	forceLinks(t, true)
	printGrid(&linked, headers, grid)

	if got := strings.Count(linked.String(), oscLink); got != 2 {
		t.Fatalf("want 2 window links, got %d in %q", got, linked.String())
	}
	if strings.Contains(plain.String(), "\x1b]8;") {
		t.Fatalf("plain output contains a hyperlink: %q", plain.String())
	}
	if ansi.Strip(plain.String()) != ansi.Strip(linked.String()) {
		t.Errorf("links changed visible layout:\nplain:\n%s\nlinked:\n%s",
			ansi.Strip(plain.String()), ansi.Strip(linked.String()))
	}
	// The AGENT column must start at the same visible column on every row.
	lines := strings.Split(strings.TrimRight(ansi.Strip(linked.String()), "\n"), "\n")
	col := strings.Index(lines[0], "AGENT")
	for _, l := range lines[2:] {
		if idx := strings.IndexAny(l, "abc"); idx != col {
			t.Errorf("row %q: second column at %d, want %d", l, idx, col)
		}
	}
}

func testSweepReport() sweepReport {
	return sweepReport{
		Closed: []sweepRow{{WindowID: "@65", Name: "go-engineer", Status: "done", Detail: "landed"}},
		Attention: []sweepRow{
			{WindowID: "@7", Name: "w1", Agent: "go-engineer", Status: "open-pr", Detail: "review PR"},
			{WindowID: "@1234", Name: "window-two", Agent: "x", Status: "dirty", Detail: "commit"},
		},
	}
}

func TestSweepReportLinksKeepTableAligned(t *testing.T) {
	forceLinks(t, false)
	plain := captureStdout(t, func() { printSweepReport(testSweepReport(), false) })
	forceLinks(t, true)
	linked := captureStdout(t, func() { printSweepReport(testSweepReport(), false) })

	if got := strings.Count(linked, oscLink); got != 3 {
		t.Fatalf("want 3 window links (1 closed + 2 table rows), got %d:\n%q", got, linked)
	}
	if ansi.Strip(plain) != ansi.Strip(linked) {
		t.Errorf("links changed visible layout:\nplain:\n%s\nlinked:\n%s", ansi.Strip(plain), ansi.Strip(linked))
	}
	// Every bordered table line has the same visible width.
	var widths []int
	for _, l := range strings.Split(linked, "\n") {
		s := ansi.Strip(l)
		if strings.HasPrefix(s, "│") || strings.HasPrefix(s, "╭") || strings.HasPrefix(s, "╰") || strings.HasPrefix(s, "├") {
			widths = append(widths, ansi.StringWidth(l))
		}
	}
	if len(widths) < 4 {
		t.Fatalf("expected a bordered table, got:\n%s", ansi.Strip(linked))
	}
	for _, w := range widths[1:] {
		if w != widths[0] {
			t.Errorf("table line widths differ: %v", widths)
			break
		}
	}
}

func TestSweepReportJSONHasNoLinks(t *testing.T) {
	forceLinks(t, true)
	out := captureStdout(t, func() { printSweepReport(testSweepReport(), true) })
	if strings.Contains(out, "\x1b") {
		t.Fatalf("JSON output contains escape sequences: %q", out)
	}
	var rep sweepReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if rep.Closed[0].WindowID != "@65" {
		t.Errorf("window_id = %q, want @65", rep.Closed[0].WindowID)
	}
}
