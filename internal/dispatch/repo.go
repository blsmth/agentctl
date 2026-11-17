package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// repoSlugTimeout bounds `gh repo view`. It goes to the network, and a hung
// gh must not hang a dispatch (or agentctl serve, which resolves the slug
// while a phone waits).
const repoSlugTimeout = 15 * time.Second

// commandRunner runs name with args in dir and returns stdout. It exists so
// tests can inject gh behavior.
type commandRunner func(dir, name string, args ...string) ([]byte, error)

func execRunner(dir, name string, args ...string) ([]byte, error) {
	return runWithTimeout(repoSlugTimeout, dir, name, args...)
}

// runWithTimeout runs name in dir and returns stdout, killing it after
// timeout.
func runWithTimeout(timeout time.Duration, dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// A child that inherited stdout must not keep Output waiting after the
	// kill.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s timed out after %s: %w", name, timeout, ctx.Err())
	}
	return out, err
}

var (
	ghRunner commandRunner = execRunner

	repoCacheMu sync.Mutex
	repoCache   = make(map[string]string)
)

// RepoSlug returns the GitHub "owner/repo" slug for the git repository that
// contains dir, as reported by `gh repo view`. There is deliberately no
// fallback: if the repo cannot be determined an error is returned so callers
// can skip repo-targeted actions rather than act on the wrong repository.
// Successful results are cached per dir for the life of the process.
func RepoSlug(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("resolving repo slug: empty directory")
	}

	repoCacheMu.Lock()
	slug, ok := repoCache[dir]
	repoCacheMu.Unlock()
	if ok {
		return slug, nil
	}

	out, err := ghRunner(dir, "gh", "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if err != nil {
		return "", fmt.Errorf("resolving repo slug in %s: %w", dir, err)
	}
	slug = strings.TrimSpace(string(out))
	if slug == "" {
		return "", fmt.Errorf("resolving repo slug in %s: gh returned empty output", dir)
	}

	repoCacheMu.Lock()
	repoCache[dir] = slug
	repoCacheMu.Unlock()
	return slug, nil
}
