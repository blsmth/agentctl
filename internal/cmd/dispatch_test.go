package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/dispatch"
)

func TestFormatElapsed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input time.Duration
		want  string
	}{
		{name: "seconds", input: 45 * time.Second, want: "45s"},
		{name: "one minute", input: 60 * time.Second, want: "1m00s"},
		{name: "minutes and seconds", input: 90 * time.Second, want: "1m30s"},
		{name: "zero", input: 0, want: "0s"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatElapsed(tc.input)
			if got != tc.want {
				t.Errorf("formatElapsed(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// dispatchPrompt resolves nothing but the default working directory and hands
// every flag to dispatch.LaunchAdhoc, the core agentctl serve shares.
func TestDispatchPromptDelegatesToLaunchAdhoc(t *testing.T) {
	old := launchAdhoc
	t.Cleanup(func() { launchAdhoc = old })

	var got dispatch.AdhocRequest
	launchAdhoc = func(req dispatch.AdhocRequest) (dispatch.Result, error) {
		got = req
		return dispatch.Result{WindowID: "@9", AgentName: req.AgentName}, nil
	}

	dir := t.TempDir()
	wid, agent, err := dispatchPrompt("do it", "spec-key", "go-engineer", dir, "opus", "none", "personal", "", true)
	if err != nil {
		t.Fatalf("dispatchPrompt error = %v, want nil", err)
	}
	if wid != "@9" || agent != "go-engineer" {
		t.Errorf("got %q, %q", wid, agent)
	}
	want := dispatch.AdhocRequest{
		Prompt: "do it", PromptKey: "spec-key", AgentName: "go-engineer", WorkDir: dir,
		Model: "opus", Backend: "none", Account: "personal", UseWorktree: true,
	}
	if got != want {
		t.Errorf("AdhocRequest = %+v, want %+v", got, want)
	}

	launchAdhoc = func(dispatch.AdhocRequest) (dispatch.Result, error) { return dispatch.Result{}, errors.New("boom") }
	if _, _, err := dispatchPrompt("x", "", "go-engineer", dir, "", "", "", "", false); err == nil || err.Error() != "boom" {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestDispatchIssueRepoResolution(t *testing.T) {
	old := resolveRepoSlug
	t.Cleanup(func() { resolveRepoSlug = old })
	resolveRepoSlug = func(string) (string, error) { return "", errors.New("no remote") }

	_, _, err := dispatchIssue(7, "", t.TempDir(), "", "", "", "", "", false)
	if err == nil || !strings.Contains(err.Error(), "could not determine GitHub repo for") {
		t.Fatalf("err = %v, want could-not-determine error", err)
	}
	if strings.Contains(err.Error(), "blsmth") {
		t.Errorf("error leaks author repo: %v", err)
	}
}

func TestDispatchIssueInvalidRepoFlag(t *testing.T) {
	for _, repo := range []string{"not-a-slug", "/x", "a/", "a/b/c", "a b/c", "a/b c"} {
		t.Run(repo, func(t *testing.T) {
			old := resolveRepoSlug
			t.Cleanup(func() { resolveRepoSlug = old })
			resolveRepoSlug = func(string) (string, error) { t.Fatal("must not resolve"); return "", nil }
			_, _, err := dispatchIssue(7, "", t.TempDir(), "", "", "", "", repo, false)
			if err == nil || !strings.Contains(err.Error(), "invalid --repo") {
				t.Fatalf("err = %v, want invalid --repo", err)
			}
		})
	}
}

// With a valid --repo the gh resolver must not be consulted. The flow then
// proceeds to `gh issue view`, which may fail in the test env; we only assert
// the resolver was untouched and the error is not a resolution error.
func TestDispatchIssueRepoFlagSkipsResolver(t *testing.T) {
	old := resolveRepoSlug
	t.Cleanup(func() { resolveRepoSlug = old })
	called := false
	resolveRepoSlug = func(string) (string, error) { called = true; return "", errors.New("x") }
	t.Setenv("PATH", t.TempDir()) // no gh: fetch fails fast
	_, _, err := dispatchIssue(7, "", t.TempDir(), "", "", "", "", "acme/widgets", false)
	if called {
		t.Error("resolver called despite --repo")
	}
	if err == nil || strings.Contains(err.Error(), "could not determine") || !strings.Contains(err.Error(), "acme/widgets") {
		t.Errorf("err = %v, want fetch error naming acme/widgets", err)
	}
}
