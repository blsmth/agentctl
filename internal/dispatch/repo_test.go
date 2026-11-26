package dispatch

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func stubRunner(t *testing.T, fn commandRunner) *int {
	t.Helper()
	repoCacheMu.Lock()
	repoCache = make(map[string]string)
	repoCacheMu.Unlock()
	old := ghRunner
	calls := 0
	ghRunner = func(dir, name string, args ...string) ([]byte, error) {
		calls++
		return fn(dir, name, args...)
	}
	t.Cleanup(func() { ghRunner = old })
	return &calls
}

func TestRepoSlug(t *testing.T) {
	tests := []struct {
		name    string
		dir     string
		out     string
		err     error
		want    string
		wantErr bool
	}{
		{name: "success trims", dir: "/x", out: "acme/widgets\n", want: "acme/widgets"},
		{name: "gh error", dir: "/x", err: errors.New("boom"), wantErr: true},
		{name: "empty output", dir: "/x", out: "  \n", wantErr: true},
		{name: "empty dir", dir: "", out: "acme/widgets", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubRunner(t, func(dir, name string, args ...string) ([]byte, error) {
				return []byte(tc.out), tc.err
			})
			got, err := RepoSlug(tc.dir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRepoSlugCachesSuccessOnly(t *testing.T) {
	fail := true
	calls := stubRunner(t, func(dir, name string, args ...string) ([]byte, error) {
		if fail {
			return nil, errors.New("down")
		}
		return []byte("a/b"), nil
	})
	if _, err := RepoSlug("/d"); err == nil {
		t.Fatal("want error")
	}
	fail = false
	for i := 0; i < 3; i++ {
		if got, err := RepoSlug("/d"); err != nil || got != "a/b" {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	if *calls != 2 {
		t.Errorf("runner calls = %d, want 2 (error not cached, success cached)", *calls)
	}
}

// AGENTCTL_REPO is exported to dispatched agents for information only; it must
// never override resolution (an agent in repo A dispatching into repo B).
func TestRepoSlugIgnoresEnv(t *testing.T) {
	t.Setenv("AGENTCTL_REPO", "env/repo")
	stubRunner(t, func(string, string, ...string) ([]byte, error) { return []byte("gh/repo\n"), nil })
	got, err := RepoSlug("/x")
	if err != nil || got != "gh/repo" {
		t.Fatalf("got %q, %v; want gh/repo", got, err)
	}
}

// A hung gh must not hang the dispatch: the runner kills it at the timeout.
func TestRunWithTimeoutKillsHungCommand(t *testing.T) {
	start := time.Now()
	_, err := runWithTimeout(100*time.Millisecond, t.TempDir(), "sleep", "10")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("returned after %v", d)
	}
	if out, err := runWithTimeout(5*time.Second, t.TempDir(), "echo", "ok"); err != nil || string(out) != "ok\n" {
		t.Errorf("echo = %q, %v", out, err)
	}
}

func exitErr(stderr string) error {
	return &exec.ExitError{Stderr: []byte(stderr)}
}

func TestGhErrorsIncludeStderr(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		want    []string
		wantNot []string
	}{
		{name: "stderr surfaced", err: exitErr("  GraphQL: boom\n"), want: []string{"GraphQL: boom"}},
		{name: "rate limit", err: exitErr("GraphQL: API rate limit already exceeded"),
			want: []string{"rate limit exhausted", "API rate limit already exceeded"}},
		{name: "no stderr", err: exitErr(""), wantNot: []string{"rate limit"}},
		{name: "plain error", err: errors.New("boom"), want: []string{"boom"}},
	}
	for _, tc := range tests {
		for _, call := range []string{"RepoSlug", "FetchPR"} {
			t.Run(tc.name+"/"+call, func(t *testing.T) {
				stubRunner(t, func(string, string, ...string) ([]byte, error) { return nil, tc.err })
				var err error
				if call == "RepoSlug" {
					_, err = RepoSlug("/x")
				} else {
					_, err = FetchPR("a/b", 1)
				}
				if err == nil {
					t.Fatal("want error")
				}
				for _, w := range tc.want {
					if call == "FetchPR" && w == "rate limit exhausted" {
						continue
					}
					if !strings.Contains(err.Error(), w) {
						t.Errorf("err %q missing %q", err, w)
					}
				}
				for _, w := range tc.wantNot {
					if strings.Contains(err.Error(), w) {
						t.Errorf("err %q should not contain %q", err, w)
					}
				}
			})
		}
	}
}
