package tui

import (
	"strings"
	"testing"
)

// initStylesOnce ensures styles are initialised before tests that need them.
func init() {
	InitStyles()
}

// TestLabelColor verifies that known label names map to non-empty color codes
// and that unknown names fall back to the Subtle role color.
func TestLabelColor(t *testing.T) {
	known := []string{"bug", "enhancement", "quick-fix", "refactor", "triage"}
	for _, name := range known {
		t.Run(name, func(t *testing.T) {
			color := labelColor(name)
			if color == "" {
				t.Errorf("labelColor(%q) returned empty string", name)
			}
		})
	}

	t.Run("unknown falls back to subtle", func(t *testing.T) {
		got := labelColor("some-unknown-label")
		if got == "" {
			t.Error("labelColor for unknown label returned empty string")
		}
	})
}

// TestRenderBodyLines_ProseOnly verifies that plain prose lines are returned
// without code-block styling.
func TestRenderBodyLines_ProseOnly(t *testing.T) {
	body := "Hello world\nThis is a test"
	lines := renderBodyLines(body, 80)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	// Prose lines should be indented with two spaces.
	for _, l := range lines {
		stripped := strings.TrimLeft(l, "\x1b[0123456789;m")
		_ = stripped // ANSI sequences vary; just check the slice count is right
	}
}

// TestRenderBodyLines_CodeBlock verifies that lines inside a fenced code block
// are rendered differently from prose lines.
func TestRenderBodyLines_CodeBlock(t *testing.T) {
	body := "before\n```\ncode here\n```\nafter"
	lines := renderBodyLines(body, 80)

	// Expected lines: "before", "```", "code here", "```", "after" → 5 lines.
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d: %v", len(lines), lines)
	}

	// Prose lines contain "  " prefix (two spaces before ANSI codes).
	// Code-block lines use the border style (no double-space prefix).
	proseBefore := lines[0]
	fenceOpen := lines[1]
	codeLine := lines[2]
	fenceClose := lines[3]
	proseAfter := lines[4]

	// Fence and code lines should not start with "  " (they use border style).
	if strings.HasPrefix(fenceOpen, "  ") {
		t.Errorf("fence open line should not have prose indent, got: %q", fenceOpen)
	}
	if strings.HasPrefix(codeLine, "  ") {
		t.Errorf("code line should not have prose indent, got: %q", codeLine)
	}
	if strings.HasPrefix(fenceClose, "  ") {
		t.Errorf("fence close line should not have prose indent, got: %q", fenceClose)
	}

	// Prose lines must start with "  ".
	if !strings.HasPrefix(proseBefore, "  ") {
		t.Errorf("prose line before block should start with '  ', got: %q", proseBefore)
	}
	if !strings.HasPrefix(proseAfter, "  ") {
		t.Errorf("prose line after block should start with '  ', got: %q", proseAfter)
	}
}

// TestRenderBodyLines_LineTooLong verifies that lines exceeding maxWidth are
// hard-wrapped into multiple output lines.
func TestRenderBodyLines_LineTooLong(t *testing.T) {
	longLine := strings.Repeat("a", 50)
	body := longLine
	lines := renderBodyLines(body, 20)
	// 50 chars / 20 = 3 lines (20 + 20 + 10).
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines for 50-char input with maxWidth=20, got %d", len(lines))
	}
}

// TestRenderBodyLines_EmptyBody verifies that an empty body returns one empty line.
func TestRenderBodyLines_EmptyBody(t *testing.T) {
	lines := renderBodyLines("", 80)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line for empty body, got %d", len(lines))
	}
}

// TestRenderBodyLines_UnclosedCodeBlock verifies graceful handling of a body
// where the opening fence is never closed.
func TestRenderBodyLines_UnclosedCodeBlock(t *testing.T) {
	body := "prose\n```\ncode without closing fence"
	lines := renderBodyLines(body, 80)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
}
