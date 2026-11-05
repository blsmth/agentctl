package dispatch

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// commandRunner runs name with args in dir and returns stdout. It exists so
// tests can inject gh behavior.
type commandRunner func(dir, name string, args ...string) ([]byte, error)

func execRunner(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.Output()
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
