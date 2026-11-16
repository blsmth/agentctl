package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestTruncateToWidthANSI(t *testing.T) {
	red := "\x1b[31m"
	s := red + "hello world" + "\x1b[0m"
	got := truncateToWidth(s, 6)
	if w := lipgloss.Width(got); w > 6 {
		t.Fatalf("width %d > 6: %q", w, got)
	}
	if ansi.Strip(got) != "hello…" {
		t.Fatalf("stripped = %q", ansi.Strip(got))
	}
	if !strings.HasPrefix(got, red) {
		t.Fatalf("lost leading escape: %q", got)
	}
	if got := truncateToWidth(s, 20); got != s {
		t.Fatalf("fitting string changed: %q", got)
	}
	if got := truncateToWidth("plain text", 5); got != "plai…" {
		t.Fatalf("plain = %q", got)
	}
	if got := truncateToWidth("abc", 0); got != "" {
		t.Fatalf("zero width = %q", got)
	}
}

func TestNormalizeANSILines(t *testing.T) {
	in := "plain\n\x1b[31mred\nstill red\nend\x1b[0m\nafter"
	lines := strings.Split(normalizeANSILines(in), "\n")
	want := []string{
		"plain",
		"\x1b[31mred\x1b[0m",
		"\x1b[31mstill red\x1b[0m",
		"\x1b[31mend\x1b[0m\x1b[0m",
		"after",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %q", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
	// A scrolled subset still renders colored.
	if !strings.HasPrefix(lines[2], "\x1b[31m") || !strings.HasSuffix(lines[2], sgrReset) {
		t.Errorf("sliced line not self-contained: %q", lines[2])
	}
}

func TestNormalizeANSILinesStacking(t *testing.T) {
	in := "\x1b[1m\x1b[32mA\nB\x1b[0;4mC\nD"
	lines := strings.Split(normalizeANSILines(in), "\n")
	if lines[1] != "\x1b[1m\x1b[32mB\x1b[0;4mC\x1b[0m" {
		t.Errorf("line 1 = %q", lines[1])
	}
	if lines[2] != "\x1b[4mD\x1b[0m" {
		t.Errorf("line 2 = %q", lines[2])
	}
}

func TestNormalizeANSILinesPlainUnchanged(t *testing.T) {
	if got := normalizeANSILines("a\nb"); got != "a\nb" {
		t.Fatalf("got %q", got)
	}
}
