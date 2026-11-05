package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/prompt"
)

// makeResolvedPrompt builds a minimal ResolvedPrompt for testing.
func makeResolvedPrompt(t *testing.T, content string) *prompt.ResolvedPrompt {
	t.Helper()
	rp := &prompt.ResolvedPrompt{
		Agent:      "test-agent",
		FilePath:   "/home/user/.claude/agents/test-agent.md",
		RawContent: content,
		Expanded:   content,
		Sections: []prompt.Section{
			{
				Source:    "/home/user/.claude/agents/test-agent.md",
				Content:   content,
				IsInclude: false,
				Tokens:    prompt.EstimateTokens(content),
			},
		},
		ResolvedAt: time.Now(),
	}
	rp.Tokens = prompt.ComputeBreakdown(rp)
	return rp
}

func TestPromptPreviewComponent_NilContent(t *testing.T) {
	var p PromptPreviewComponent
	p.SetSize(80, 24)

	// Should not panic.
	out := p.View()
	if out == "" {
		t.Error("expected non-empty View() for nil content")
	}
	// Token bar should show "(no content)" when content is nil.
	if !strings.Contains(out, "no content") {
		t.Errorf("expected 'no content' in View() for nil content, got:\n%s", out)
	}
}

func TestPromptPreviewComponent_EmptyResolvedPrompt(t *testing.T) {
	var p PromptPreviewComponent
	p.SetSize(80, 24)

	rp := &prompt.ResolvedPrompt{} // zero-value
	p.SetContent(rp)

	// Should not panic.
	out := p.View()
	if out == "" {
		t.Error("expected non-empty View() for empty ResolvedPrompt")
	}
}

func TestPromptPreviewComponent_TokenBar(t *testing.T) {
	tests := []struct {
		name         string
		tokens       int
		wantPct      string
		wantInOutput string
	}{
		{
			name:         "zero tokens",
			tokens:       0,
			wantPct:      "0%",
			wantInOutput: "0% of context",
		},
		{
			name:         "50k tokens = 25%",
			tokens:       50_000,
			wantPct:      "25%",
			wantInOutput: "25% of context",
		},
		{
			name:         "200k tokens = 100%",
			tokens:       200_000,
			wantPct:      "100%",
			wantInOutput: "100% of context",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p PromptPreviewComponent
			p.SetSize(120, 40)

			rp := &prompt.ResolvedPrompt{
				FilePath:   "/tmp/test.md",
				RawContent: "hello",
			}
			// Manually set token count for precise testing.
			rp.Tokens.Total = tc.tokens
			if tc.tokens > 0 {
				rp.Tokens.ContextPct = float64(tc.tokens) / 200_000 * 100
			}
			p.SetContent(rp)

			bar := p.renderTokenBar()
			// Strip ANSI codes for comparison.
			plain := stripANSI(bar)
			if !strings.Contains(plain, tc.wantInOutput) {
				t.Errorf("token bar %q does not contain %q", plain, tc.wantInOutput)
			}
		})
	}
}

func TestPromptPreviewComponent_RawToggle(t *testing.T) {
	var p PromptPreviewComponent
	p.SetSize(80, 30)

	mdContent := "# Hello\n\nThis is **bold** text.\n\n@../shared/go.md\n"
	rp := makeResolvedPrompt(t, mdContent)
	p.SetContent(rp)

	// Default: expanded (glamour-rendered) mode.
	if p.rawMode {
		t.Error("expected expanded mode by default")
	}

	// Toggle to raw mode.
	consumed := p.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if !consumed {
		t.Error("expected Update to consume 'r' key")
	}
	if !p.rawMode {
		t.Error("expected raw mode after 'r' toggle")
	}

	// Raw view should contain the original @ line.
	rawView := p.View()
	plain := stripANSI(rawView)
	if !strings.Contains(plain, "@../shared/go.md") {
		t.Errorf("raw view should contain @-include line, got:\n%s", plain)
	}

	// Toggle back to expanded mode.
	p.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if p.rawMode {
		t.Error("expected expanded mode after second 'r' toggle")
	}
}

func TestPromptPreviewComponent_SectionSeparators(t *testing.T) {
	var p PromptPreviewComponent
	p.SetSize(100, 40)

	includeSource := "/home/user/.claude/shared/go.md"
	rp := &prompt.ResolvedPrompt{
		Agent:    "test",
		FilePath: "/home/user/.claude/agents/test.md",
		Sections: []prompt.Section{
			{
				Source:    "/home/user/.claude/agents/test.md",
				Content:   "# Agent Instructions\n",
				IsInclude: false,
				Tokens:    5,
			},
			{
				Source:    includeSource,
				Content:   "# Go Guidelines\n",
				IsInclude: true,
				Tokens:    5,
			},
		},
		ResolvedAt: time.Now(),
	}
	rp.Tokens = prompt.ComputeBreakdown(rp)
	p.SetContent(rp)

	out := p.View()
	plain := stripANSI(out)

	// In expanded mode, a separator should appear between the two sections.
	if !strings.Contains(plain, "── from") {
		t.Errorf("expected section provenance separator in expanded view, got:\n%s", plain)
	}
}

func TestPromptPreviewComponent_Resize(t *testing.T) {
	var p PromptPreviewComponent
	p.SetSize(80, 24)

	rp := makeResolvedPrompt(t, "# Hello\n\nSome content.\n")
	p.SetContent(rp)
	_ = p.View() // trigger initial render

	// Resize to a new size.
	p.SetSize(120, 40)

	// After resize, dirty flag should be set.
	if !p.dirty {
		t.Error("expected dirty=true after resize")
	}

	// View should render without panic.
	out := p.View()
	if out == "" {
		t.Error("expected non-empty View() after resize")
	}
	if p.dirty {
		t.Error("expected dirty=false after View() call")
	}
}

func TestPromptPreviewComponent_ScrollKeys(t *testing.T) {
	var p PromptPreviewComponent
	p.SetSize(80, 10)

	// Build content tall enough to scroll.
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("Line of content for scrolling test.\n")
	}
	rp := makeResolvedPrompt(t, sb.String())
	p.SetContent(rp)
	_ = p.View() // initialise viewport

	scrollKeys := []struct {
		name string
		msg  tea.KeyPressMsg
	}{
		{"pgdown", tea.KeyPressMsg{Code: tea.KeyPgDown}},
		{"pgup", tea.KeyPressMsg{Code: tea.KeyPgUp}},
		{"home", tea.KeyPressMsg{Code: tea.KeyHome}},
		{"end", tea.KeyPressMsg{Code: tea.KeyEnd}},
		{"down", tea.KeyPressMsg{Code: 'j', Text: "j"}},
		{"up", tea.KeyPressMsg{Code: 'k', Text: "k"}},
		{"j", tea.KeyPressMsg{Code: 'j', Text: "j"}},
		{"k", tea.KeyPressMsg{Code: 'k', Text: "k"}},
	}
	for _, tc := range scrollKeys {
		consumed := p.Update(tc.msg)
		if !consumed {
			t.Errorf("expected scroll key %q to be consumed", tc.name)
		}
	}
}

func TestPromptPreviewComponent_UnknownKeyNotConsumed(t *testing.T) {
	var p PromptPreviewComponent
	p.SetSize(80, 24)

	consumed := p.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if consumed {
		t.Error("expected unknown key 'x' to NOT be consumed")
	}
}

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{12400, "12,400"},
		{200000, "200,000"},
		{1234567, "1,234,567"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := formatTokenCount(tc.n)
			if got != tc.want {
				t.Errorf("formatTokenCount(%d) = %q, want %q", tc.n, got, tc.want)
			}
		})
	}
}

func TestGlamourRender_RendersMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{"heading", "# Hello World\n", "Hello World"},
		{"bold", "**bold text**\n", "bold text"},
		{"code block", "```go\nfmt.Println(\"hi\")\n```\n", "fmt.Println"},
		{"list", "- item one\n- item two\n", "item one"},
		{"link", "[click here](https://example.com)\n", "click here"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := glamourRender(tc.input, 80)
			plain := stripANSI(out)
			if !strings.Contains(plain, tc.contains) {
				t.Errorf("glamourRender(%q) output %q does not contain %q", tc.input, plain, tc.contains)
			}
		})
	}
}
