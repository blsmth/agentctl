package hooks

import (
	"slices"
	"strconv"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

// stubDispatchGate replaces the dispatch popup seam with a recorder that
// reports "no popup shown" (the silent pass-through) and returns whether it
// was called.
func stubDispatchGate(t *testing.T) *bool {
	t.Helper()
	called := new(bool)
	orig := dispatchGate
	dispatchGate = func(_, _ string) (bool, bool, string) {
		*called = true
		return false, false, ""
	}
	t.Cleanup(func() { dispatchGate = orig })
	return called
}

// runValidateBashWith runs ValidateBash for command under cfg and reports
// whether the dispatch gate ran and what was written to stdout.
func runValidateBashWith(t *testing.T, cfg *config.Config, command string) (gated bool, out string) {
	t.Helper()
	d := DefaultDeps()
	d.LoadConfig = func() *config.Config { return cfg }
	d.LoadProjectConfig = func(string) *config.ProjectConfig { return nil }
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	called := stubDispatchGate(t)
	failIfDispatchPopupRunnerCalled(t)

	stdin := `{"tool_name":"Bash","tool_input":{"command":` + strconv.Quote(command) + `}}`
	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_AGENT_NAME": "lead",
		"AGENTCTL_WINDOW_ID":  "w99",
		"AGENTCTL_SESSION_ID": "",
		"TMUX_PANE":           "",
	}
	out, _ = runHookCaptured(t, envVars, stdin, ValidateBash)
	return *called, out
}

func TestValidateBash_DispatchApproval(t *testing.T) {
	const (
		review      = "agentctl dispatch --review-pr 42"
		reviewEq    = "agentctl dispatch --review-pr=12 --agent code-reviewer"
		plain       = "agentctl dispatch --agent go-engineer --prompt hi"
		mixed       = "agentctl dispatch --review-pr 42 && agentctl dispatch --issue 7"
		twoReviews  = "agentctl dispatch --review-pr 1; agentctl dispatch --review-pr 2"
		promptQuote = "agentctl dispatch --agent a --prompt '--review-pr 3'"
	)
	tests := []struct {
		name      string
		mode      string
		command   string
		wantGated bool
	}{
		{"default: review dispatch not gated", "", review, false},
		{"default: --review-pr=N form not gated", "", reviewEq, false},
		{"default: several review dispatches not gated", "", twoReviews, false},
		{"default: plain dispatch gated", "", plain, true},
		{"default: mixed review and plain gated", "", mixed, true},
		{"default: --review-pr inside a prompt is not a review", "", promptQuote, true},
		{"non-review: mixed gated", "non-review", mixed, true},
		{"off: plain dispatch not gated", "off", plain, false},
		{"off: review dispatch not gated", "off", review, false},
		{"all: review dispatch gated", "all", review, true},
		{"all: plain dispatch gated", "all", plain, true},
		{"unknown value: review dispatch gated", "bogus", review, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{DispatchApproval: tt.mode}
			gated, out := runValidateBashWith(t, cfg, tt.command)
			if gated != tt.wantGated {
				t.Errorf("dispatch gate called = %v, want %v", gated, tt.wantGated)
			}
			if out != "" {
				t.Errorf("ValidateBash wrote %q, want no output", out)
			}
		})
	}
}

// An exempted dispatch is an ordinary Bash command: with bash_preview on it
// still reaches the generic Bash popup rather than skipping every gate.
func TestValidateBash_ExemptDispatchFallsThroughToBashPreview(t *testing.T) {
	on := true
	cfg := &config.Config{BashPreview: &on, DispatchApproval: config.DispatchApprovalOff}
	// Outside tmux bashPreviewGate passes through silently; reaching it
	// without calling the dispatch gate is what this asserts.
	t.Setenv("TMUX", "")
	gated, out := runValidateBashWith(t, cfg, "agentctl dispatch --review-pr 42")
	if gated {
		t.Error("dispatch gate called for an exempted dispatch")
	}
	if out != "" {
		t.Errorf("ValidateBash wrote %q, want no output", out)
	}
}

// The dispatch+cron mix is denied whatever dispatch_approval says.
func TestValidateBash_DispatchCronMixDeniedWhenOff(t *testing.T) {
	cfg := &config.Config{DispatchApproval: config.DispatchApprovalOff}
	gated, out := runValidateBashWith(t, cfg,
		"agentctl dispatch --review-pr 1 && agentctl cron add --name x --schedule '@daily' --agent a --prompt hi")
	if gated {
		t.Error("dispatch gate called")
	}
	if out == "" {
		t.Error("want a deny decision, got no output")
	}
}

func TestDispatchNeedsApproval(t *testing.T) {
	tests := []struct {
		command string
		mode    string
		want    bool
	}{
		{"agentctl dispatch --review-pr 5", config.DispatchApprovalNonReview, false},
		{"agentctl dispatch --review-pr=5", config.DispatchApprovalNonReview, false},
		{"agentctl dispatch --author --review-pr --issue 3", config.DispatchApprovalNonReview, true},
		{"agentctl dispatch --issue 3", config.DispatchApprovalNonReview, true},
		// A help lookup segment is not gated, so it does not keep the gate.
		{"agentctl dispatch --help; agentctl dispatch --review-pr 5", config.DispatchApprovalNonReview, false},
		{"agentctl dispatch --review-pr 5", config.DispatchApprovalAll, true},
		{"agentctl dispatch --issue 3", config.DispatchApprovalOff, false},
		{"agentctl dispatch --review-pr 5", "bogus", true},
		// --review-pr must carry a literal positive PR number: 0 means
		// "unset" to dispatch and would run an ordinary dispatch.
		{"agentctl dispatch --agent go-engineer --prompt evil --review-pr=0", config.DispatchApprovalNonReview, true},
		{"agentctl dispatch --agent go-engineer --prompt evil --review-pr 0", config.DispatchApprovalNonReview, true},
		{"agentctl dispatch --agent go-engineer --prompt evil --review-pr", config.DispatchApprovalNonReview, true},
		{"agentctl dispatch --review-pr $N", config.DispatchApprovalNonReview, true},
		{"agentctl dispatch --review-pr=$N", config.DispatchApprovalNonReview, true},
		{"agentctl dispatch --review-pr abc", config.DispatchApprovalNonReview, true},
		{"agentctl dispatch --review-pr --prompt evil", config.DispatchApprovalNonReview, true},
		{`agentctl dispatch --review-pr "7"`, config.DispatchApprovalNonReview, false},
		// The CLI keeps the last occurrence.
		{"agentctl dispatch --review-pr 5 --review-pr 0", config.DispatchApprovalNonReview, true},
		{"agentctl dispatch --review-pr 0 --review-pr 5", config.DispatchApprovalNonReview, false},
		// A comment or a newline ends the dispatch segment.
		{"agentctl dispatch --agent go-engineer --prompt evil # --review-pr 5", config.DispatchApprovalNonReview, true},
		{"agentctl dispatch --agent go-engineer --prompt evil\necho --review-pr 5", config.DispatchApprovalNonReview, true},
		{"agentctl dispatch --review-pr 5 # reviewing the tmux change", config.DispatchApprovalNonReview, false},
		// '#' right after an unquoted metacharacter also starts a comment.
		{"agentctl dispatch --prompt evil -a x;# --review-pr 5", config.DispatchApprovalNonReview, true},
		{"(agentctl dispatch --prompt evil -a x)# --review-pr 5", config.DispatchApprovalNonReview, true},
		{"agentctl dispatch --prompt evil -a x&# --review-pr 5", config.DispatchApprovalNonReview, true},
		{"agentctl dispatch --prompt evil -a x|# --review-pr 5", config.DispatchApprovalNonReview, true},
		{"agentctl dispatch --review-pr 5 --prompt 'see #12'", config.DispatchApprovalNonReview, false},
		{"agentctl dispatch --prompt 'x;'# --review-pr 5", config.DispatchApprovalNonReview, false},
		{"agentctl dispatch --review-pr 5\necho done", config.DispatchApprovalNonReview, false},
		// A backslash-newline continuation keeps the segment going.
		{"agentctl dispatch \\\n  --review-pr 5", config.DispatchApprovalNonReview, false},
		{"agentctl dispatch --agent go-engineer \\\n  --prompt evil", config.DispatchApprovalNonReview, true},
	}
	for _, tt := range tests {
		t.Run(tt.mode+"/"+tt.command, func(t *testing.T) {
			if got := dispatchNeedsApproval(tt.command, tt.mode); got != tt.want {
				t.Errorf("dispatchNeedsApproval = %v, want %v", got, tt.want)
			}
		})
	}
}

// A shell comment or a newline must end the dispatch segment for help
// detection too: neither "# -h" nor a -h on the next line makes the dispatch
// a help lookup.
func TestIsAgentctlDispatch_CommentAndNewlineEndSegment(t *testing.T) {
	for _, cmd := range []string{
		"agentctl dispatch -a x --prompt evil # -h",
		"agentctl dispatch -a x --prompt evil #--help",
		"agentctl dispatch -a x --prompt evil\necho -h",
		"agentctl dispatch -a x --prompt evil;# -h",
		"agentctl dispatch -a x --prompt evil&# -h",
		"(agentctl dispatch -a x --prompt evil)# -h",
	} {
		if !isAgentctlDispatch(cmd) {
			t.Errorf("isAgentctlDispatch(%q) = false, want true", cmd)
		}
	}
	for _, cmd := range []string{
		"agentctl dispatch -h # show flags",
		"agentctl dispatch -a 'x#y' -h",
	} {
		if isAgentctlDispatch(cmd) {
			t.Errorf("isAgentctlDispatch(%q) = true, want false (help lookup)", cmd)
		}
	}
}

func TestTokenizeQuoted_CommentsAndNewlines(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"a b # c d", []string{"a", "b"}},
		{"a # c\nb", []string{"a", ";", "b"}},
		{"a#b c", []string{"a#b", "c"}},
		{"a;# c", []string{"a;"}},
		{"a)#c\nb", []string{"a)", ";", "b"}},
		{"'a;'#b", []string{"a;#b"}},
		{"a\\;#b", []string{"a;#b"}},
		{"a 'see #12'", []string{"a", "see #12"}},
		{"a '# not a comment'", []string{"a", "# not a comment"}},
		{"a \\# b", []string{"a", "#", "b"}},
		{"a 'x\ny'", []string{"a", "x\ny"}},
		{"a \\\nb", []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			var got []string
			for _, tok := range tokenizeQuoted(tt.in) {
				got = append(got, tok.text)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("tokenizeQuoted(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Under "non-review" only a reviewer dispatch is exempt: --review-pr N run by
// code-reviewer, either as the CLI default (no --agent) or named literally.
func TestValidateBash_ReviewExemptionRequiresCodeReviewer(t *testing.T) {
	if config.DefaultReviewAgent != "code-reviewer" {
		t.Fatalf("DefaultReviewAgent = %q; the cases below assume code-reviewer", config.DefaultReviewAgent)
	}
	tests := []struct {
		name      string
		command   string
		wantGated bool
	}{
		{"no --agent resolves to the default reviewer", "agentctl dispatch --review-pr 5", false},
		{"--agent code-reviewer", "agentctl dispatch --agent code-reviewer --review-pr 5", false},
		{"-a code-reviewer", "agentctl dispatch -a code-reviewer --review-pr 5", false},
		{"--agent=code-reviewer", "agentctl dispatch --review-pr 5 --agent=code-reviewer", false},
		{"quoted reviewer value", "agentctl dispatch --review-pr 5 --agent 'code-reviewer'", false},
		{"reviewer with review focus prompt", `agentctl dispatch --review-pr 5 --prompt "focus on the locking"`, false},
		{"reviewer with quoted expanding prompt", `agentctl dispatch --review-pr 5 --prompt "$(cat focus.md)"`, false},
		{"repeated --agent ending in the reviewer", "agentctl dispatch --agent go-engineer --review-pr 5 -a code-reviewer", false},

		{"--agent go-engineer", "agentctl dispatch --agent go-engineer --review-pr 5", true},
		{"-a go-engineer", "agentctl dispatch -a go-engineer --review-pr 5", true},
		{"--agent=go-engineer", "agentctl dispatch --review-pr=5 --agent=go-engineer", true},
		{"arbitrary agent with free text prompt", `agentctl dispatch --review-pr 5 --agent anything --prompt "do whatever"`, true},
		{"repeated --agent ending in a non-reviewer", "agentctl dispatch --agent code-reviewer --review-pr 5 --agent go-engineer", true},
		{"-a ending in a non-reviewer", "agentctl dispatch -a code-reviewer -a go-engineer --review-pr 5", true},
		{"quoted --agent flag", `agentctl dispatch --review-pr 5 "--agent" go-engineer`, true},
		{"expanding agent value", "agentctl dispatch --review-pr 5 --agent $AGENT", true},
		{"expanding word in flag position", "agentctl dispatch --review-pr 5 $EXTRA", true},
		{"unquoted expanding prompt splits", "agentctl dispatch --review-pr 5 --prompt $(echo x --agent go-engineer)", true},
		{"attached short agent flag", "agentctl dispatch --review-pr 5 -ago-engineer", true},
		{"--agent without a value", "agentctl dispatch --review-pr 5 --agent", true},
		{"reviewer review plus non-reviewer review", "agentctl dispatch --review-pr 1; agentctl dispatch --review-pr 2 --agent go-engineer", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gated, out := runValidateBashWith(t, &config.Config{}, tt.command)
			if gated != tt.wantGated {
				t.Errorf("dispatch gate called = %v, want %v", gated, tt.wantGated)
			}
			if out != "" {
				t.Errorf("ValidateBash wrote %q, want no output", out)
			}
		})
	}
}
