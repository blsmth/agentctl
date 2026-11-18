package hooks

import (
	"slices"
	"testing"
)

func TestParseDispatchReviewPR(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		wantPR    int
		wantAgent string
	}{
		{"flag", "agentctl dispatch --review-pr 42", 42, ""},
		{"equals form", "agentctl dispatch --review-pr=7 --agent code-reviewer", 7, "code-reviewer"},
		{"with author and prompt", "agentctl dispatch --review-pr 9 --author @12 --prompt 'look at x'", 9, ""},
		{"issue dispatch", "agentctl dispatch --issue 5", 0, ""},
		{"inside a prompt is not the flag", "agentctl dispatch --agent a --prompt '--review-pr 3'", 0, "a"},
		{"after a control operator", "agentctl dispatch --issue 1 && echo --review-pr 4", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseDispatchReviewPR(tt.command); got != tt.wantPR {
				t.Errorf("parseDispatchReviewPR = %d, want %d", got, tt.wantPR)
			}
			if a, _, _, _ := parseDispatchFlags(tt.command); a != tt.wantAgent {
				t.Errorf("agent = %q, want %q", a, tt.wantAgent)
			}
		})
	}
}

func TestReviewPRDispatchIsGated(t *testing.T) {
	for _, cmd := range []string{
		"agentctl dispatch --review-pr 42",
		"agentctl dispatch --review-pr 42 --author @12",
		// --author's value is skipped, so a value that looks like help is not.
		"agentctl dispatch --review-pr 42 --author --help",
	} {
		if !isAgentctlDispatch(cmd) {
			t.Errorf("%q is not gated", cmd)
		}
	}
	if isAgentctlDispatch("agentctl dispatch --review-pr 42 --help") {
		t.Error("help lookup is gated")
	}
}

func TestTokenizeBackslash(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"escaped apostrophe between quotes", `--prompt 'don'\''t stop'`, []string{"--prompt", "don't stop"}},
		{"newline kept inside quotes", "--prompt 'line one\nline two'", []string{"--prompt", "line one\nline two"}},
		{"escaped space", `a\ b c`, []string{"a b", "c"}},
		{"line continuation", "a \\\nb", []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tokenizeCommand(tt.input); !slices.Equal(got, tt.want) {
				t.Errorf("tokenizeCommand = %q, want %q", got, tt.want)
			}
		})
	}
	// An escaped apostrophe is literal text, so an inline context set stays
	// displayable.
	if v, found, literal := inlineContextSet("k", `agentctl context set k 'it'\''s fine' && agentctl dispatch --prompt-key k --agent a`); !found || !literal || v != "it's fine" {
		t.Errorf("inlineContextSet = (%q, %v, %v)", v, found, literal)
	}
}
