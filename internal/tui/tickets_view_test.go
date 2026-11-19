package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestRenderMarkdownKeepsTables is the regression for tables broken by the
// re-wrap: every row keeps its column borders and fits the width.
func TestRenderMarkdownKeepsTables(t *testing.T) {
	body := "Intro paragraph.\n\n" +
		"| Key | Default | Meaning |\n|---|---|---|\n" +
		"| auto_poll | false | Poll GitHub for ready issues and dispatch them unattended |\n" +
		"| auto_poll_interval | 5m | How often the autopilot polls the repository for work |\n"
	for _, width := range []int{40, 70} {
		out := tkRenderMarkdown(body, width)
		var rows int
		for _, l := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("width %d: line is %d wide: %q", width, w, ansi.Strip(l))
			}
			plain := strings.TrimSpace(ansi.Strip(l))
			if strings.Contains(plain, "│") {
				rows++
			}
			if strings.Contains(plain, "unattended") && !strings.Contains(plain, "│") {
				t.Errorf("width %d: a cell wrapped out of its column: %q", width, plain)
			}
		}
		if rows < 3 {
			t.Errorf("width %d: table lost its columns:\n%s", width, ansi.Strip(out))
		}
	}
}

// TestRenderMarkdownNoOrphanWords covers the glamour double-wrap the
// re-wrap exists for.
func TestRenderMarkdownNoOrphanWords(t *testing.T) {
	body := "Pilot engram https://github.com/Gentleman-Programming/engram (MIT, single Go binary, SQLite FTS5, MCP + Claude Code hooks) as a shared, searchable memory."
	for width := 60; width <= 85; width++ {
		lines := strings.Split(tkRenderMarkdown(body, width), "\n")
		for i, l := range lines[:len(lines)-1] {
			if p := strings.TrimSpace(ansi.Strip(l)); p != "" && len(strings.Fields(p)) == 1 && i > 0 {
				t.Errorf("width %d: orphan word %q", width, p)
			}
		}
	}
}

func TestRenderMarkdownKeepsHyphenatedWords(t *testing.T) {
	body := "Install engram and wire it for that agent only so the orchestrator and code-reviewer stay untouched by it."
	for width := 40; width <= 90; width++ {
		for _, l := range strings.Split(tkRenderMarkdown(body, width), "\n") {
			if p := strings.TrimSpace(ansi.Strip(l)); strings.HasSuffix(p, "-") {
				t.Errorf("width %d: line ends at a hyphen: %q", width, p)
			}
		}
	}
}

func TestTruncateMiddle(t *testing.T) {
	got := truncateMiddle("/tmp/tickets-ux/xdg/agentctl/config.json", 24)
	if len([]rune(got)) != 24 || !strings.HasSuffix(got, "config.json") || !strings.Contains(got, "…") {
		t.Errorf("truncateMiddle = %q", got)
	}
	if got := truncateMiddle("~/.config/agentctl/config.json", 60); got != "~/.config/agentctl/config.json" {
		t.Errorf("a short path changed: %q", got)
	}
}

// TestRenderMarkdownCodeSpansFit covers inline code at a wrap point: lines
// must fit the width and a code span's background must not open the next
// line (it bled into the indent).
func TestRenderMarkdownCodeSpansFit(t *testing.T) {
	body := "- **Account isolation:** engram defaults to one DB at `~/.engram/`. Accounts must never share state; point the DB at a per-account path (e.g. under the account's `CLAUDE_CONFIG_DIR`) or confirm the pilot can't leak across accounts."
	for width := 40; width <= 90; width++ {
		for _, l := range strings.Split(tkRenderMarkdown(body, width), "\n") {
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("width %d: line is %d wide: %q", width, w, ansi.Strip(l))
			}
			if strings.HasPrefix(l, " ") && !strings.HasSuffix(l, "\x1b[0m") && strings.Contains(l, "\x1b[") {
				t.Errorf("width %d: a styled line is left open: %q", width, l)
			}
		}
	}
}
