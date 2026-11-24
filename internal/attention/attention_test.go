package attention

import (
	"strings"
	"testing"
)

func TestQuestion(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		want   bool
		detail string
	}{
		{name: "empty", text: "", want: false},
		{name: "plain report", text: "Done. PR opened: https://github.com/o/r/pull/12", want: false},
		{name: "waiting on a child", text: "Dispatched the reviewer. I'll wait for its report.", want: false},
		{
			name: "trailing question", want: true, detail: "Want me to merge it?",
			text: "All checks pass.\n\nThe PR is green. Want me to merge it?",
		},
		{
			name: "question in bold", want: true, detail: "Merge now?",
			text: "Summary of the change.\n\n**Merge now?**",
		},
		{
			name: "question only in an earlier paragraph", want: false,
			text: "Should we keep the flag? I checked and we should.\n\nKept it; tests pass.",
		},
		{
			name: "question mark inside a code fence", want: false,
			text: "Ran the query:\n\n```\nSELECT * FROM t WHERE a = ?\n```",
		},
		{
			name: "direct ask without question mark", want: true, detail: "Should I open the PR now.",
			text: "Implementation is done. Should I open the PR now.",
		},
		{
			name: "let me know", want: true, detail: "Let me know which option you prefer.",
			text: "There are two ways to do this.\n\nOption A is smaller. Let me know which option you prefer.",
		},
		{
			name: "which as an ask", want: true, detail: "Which one do you want: A or B.",
			text: "Two candidates.\n\nWhich one do you want: A or B.",
		},
		{
			name: "needs brendan verdict anywhere", want: true, detail: "REVIEW PR #12 — VERDICT: NEEDS-BRENDAN",
			text: "REVIEW PR #12 — VERDICT: NEEDS-BRENDAN\nThe migration drops a column.\n\nRest looks fine.",
		},
		{
			name: "blocked verdict", want: true, detail: "VERDICT: BLOCKED (CI red)",
			text: "Relayed.\n\nVERDICT: BLOCKED (CI red)\n\nWorker notified.",
		},
		{
			name: "blocked line", want: true, detail: "BLOCKED: no AWS credentials in this shell.",
			text: "Tried three times.\n\n**BLOCKED**: no AWS credentials in this shell.",
		},
		{name: "lowercase blocked is not a verdict", text: "Blocked: none.\n\nAll good.", want: false},
		{name: "horizontal rule after the ask", text: "Ship it?\n\n---", want: true, detail: "Ship it?"},
		{name: "url with query string", text: "See https://x.test/a?b=1", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Question(tt.text)
			if ok != tt.want {
				t.Fatalf("Question(%q) ok = %v, want %v (detail %q)", tt.text, ok, tt.want, got)
			}
			if tt.detail != "" && got != tt.detail {
				t.Errorf("detail = %q, want %q", got, tt.detail)
			}
		})
	}
}

func TestQuestionDetailCapped(t *testing.T) {
	long := strings.Repeat("word ", 60) + "ok?"
	got, ok := Question(long)
	if !ok {
		t.Fatal("not a question")
	}
	if n := len([]rune(got)); n != MaxDetailRunes {
		t.Errorf("detail is %d runes, want %d", n, MaxDetailRunes)
	}
}
