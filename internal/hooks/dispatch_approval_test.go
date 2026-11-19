package hooks

import (
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
	}
	for _, tt := range tests {
		t.Run(tt.mode+"/"+tt.command, func(t *testing.T) {
			if got := dispatchNeedsApproval(tt.command, tt.mode); got != tt.want {
				t.Errorf("dispatchNeedsApproval = %v, want %v", got, tt.want)
			}
		})
	}
}
