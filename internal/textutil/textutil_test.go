package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		max      int
		ellipsis string
		want     string
	}{
		{name: "shorter is unchanged", s: "hello", max: 10, ellipsis: "...", want: "hello"},
		{name: "exact fit is unchanged", s: "hello", max: 5, ellipsis: "…", want: "hello"},
		{name: "ascii with dots", s: "hello world", max: 8, ellipsis: "...", want: "hello..."},
		{name: "single-rune ellipsis", s: "hello world", max: 6, ellipsis: "…", want: "hello…"},
		{name: "no ellipsis", s: "hello world", max: 5, ellipsis: "", want: "hello"},
		{name: "multi-byte runes are not split", s: "héllo wörld", max: 6, ellipsis: "…", want: "héllo…"},
		{name: "empty string", s: "", max: 5, ellipsis: "...", want: ""},
		{name: "zero max", s: "hello", max: 0, ellipsis: "…", want: ""},
		{name: "max shorter than ellipsis", s: "hello", max: 2, ellipsis: "...", want: ".."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.s, tt.max, tt.ellipsis)
			if got != tt.want {
				t.Errorf("Truncate(%q, %d, %q) = %q, want %q", tt.s, tt.max, tt.ellipsis, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("Truncate(%q, %d, %q) = %q is not valid UTF-8", tt.s, tt.max, tt.ellipsis, got)
			}
			if n := utf8.RuneCountInString(got); tt.max >= 0 && n > tt.max {
				t.Errorf("Truncate(%q, %d, %q) has %d runes, want <= %d", tt.s, tt.max, tt.ellipsis, n, tt.max)
			}
		})
	}
}

func TestTaskLine(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		max    int
		want   string
	}{
		{"empty", "", 80, ""},
		{"plain", "Fix the flaky test", 80, "Fix the flaky test"},
		{"skips heading and blanks", "## Goal\n\n  Redesign the hub sidebar row\nmore", 80, "Redesign the hub sidebar row"},
		{"skips front matter and rules", "---\n---\n***\nShip it", 80, "Ship it"},
		{"skips code fence", "```go\nfmt.Println()\n```\nAfter the code", 80, "After the code"},
		{"strips list marker and emphasis", "- **Bump** the `gutter` to 3", 80, "Bump the gutter to 3"},
		{"strips numbered list and quote", "> 1. Do the thing", 80, "Do the thing"},
		{"link keeps text", "See [the spec](https://x.y/z) first", 80, "See the spec first"},
		{"collapses whitespace", "a \t  b", 80, "a b"},
		{"truncates", "abcdefghij", 5, "abcd…"},
		{"only headings", "# A\n## B", 80, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TaskLine(tt.prompt, tt.max); got != tt.want {
				t.Errorf("TaskLine(%q, %d) = %q, want %q", tt.prompt, tt.max, got, tt.want)
			}
		})
	}
}
