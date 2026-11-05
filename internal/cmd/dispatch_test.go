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

// directory with no resolvable GitHub repo is not fatal and launches with an
// empty RepoSlug.
func TestDispatchPromptProceedsWithoutRepo(t *testing.T) {
	oldResolve, oldLaunch := resolveRepoSlug, launchAgent
	t.Cleanup(func() { resolveRepoSlug, launchAgent = oldResolve, oldLaunch })

	resolveRepoSlug = func(string) (string, error) { return "", errors.New("no remote") }
	var got dispatch.Request
	launchAgent = func(req dispatch.Request) (dispatch.Result, error) {
		got = req
		return dispatch.Result{WindowID: "@9", AgentName: req.AgentName}, nil
	}

	wid, agent, err := dispatchPrompt("do it", "", "go-engineer", t.TempDir(), "", false)
	if err != nil {
		t.Fatalf("dispatchPrompt error = %v, want nil", err)
	}
	if wid != "@9" || agent != "go-engineer" {
		t.Errorf("got %q, %q", wid, agent)
	}
	if got.RepoSlug != "" {
		t.Errorf("RepoSlug = %q, want empty", got.RepoSlug)
	}
}

func TestDispatchIssueRepoResolution(t *testing.T) {
	old := resolveRepoSlug
	t.Cleanup(func() { resolveRepoSlug = old })
	resolveRepoSlug = func(string) (string, error) { return "", errors.New("no remote") }

	_, _, err := dispatchIssue(7, "", t.TempDir(), "", "", false)
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
			_, _, err := dispatchIssue(7, "", t.TempDir(), "", repo, false)
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
	_, _, err := dispatchIssue(7, "", t.TempDir(), "", "acme/widgets", false)
	if called {
		t.Error("resolver called despite --repo")
	}
	if err == nil || strings.Contains(err.Error(), "could not determine") || !strings.Contains(err.Error(), "acme/widgets") {
		t.Errorf("err = %v, want fetch error naming acme/widgets", err)
	}
}
