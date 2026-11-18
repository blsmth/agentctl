package cmd

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/cliout"
)

func TestStripPanePadding(t *testing.T) {
	pad := strings.Repeat("⠀", 20)
	tests := []struct {
		name, in, want string
	}{
		{"braille-only line dropped", "a\n" + pad + "  \nb", "a\nb"},
		{"padding before content trimmed", pad + "sonnet 5.5 · 3% ctx\nok", "sonnet 5.5 · 3% ctx\nok"},
		{"blank lines dropped", "a\n\n   \nb\n", "a\nb"},
		{"indentation of normal lines stripped only as whitespace", "  x", "x"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripPanePadding(tt.in); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrintGrid_PlainWhenStripped(t *testing.T) {
	var buf bytes.Buffer
	grid := [][]cell{{{text: "x", style: lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Bold(true)}}}
	var raw bytes.Buffer
	printGrid(&raw, []string{"COL"}, grid)
	if !strings.ContainsRune(raw.String(), 0x1b) {
		t.Fatal("precondition: styled grid should contain ESC")
	}
	printGrid(cliout.StripWriter(&buf), []string{"COL"}, grid)
	if strings.ContainsRune(buf.String(), 0x1b) {
		t.Fatalf("output contains ESC: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "COL") || !strings.Contains(buf.String(), "x") {
		t.Fatalf("content lost: %q", buf.String())
	}
}
