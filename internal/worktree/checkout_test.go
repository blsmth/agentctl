package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspect(t *testing.T) {
	repo := setupRepoNoRemote(t)
	gitRun(t, repo, "branch", "-M", "main")
	linked := filepath.Join(t.TempDir(), "linked")
	gitRun(t, repo, "worktree", "add", "-b", "feature", linked)
	sub := filepath.Join(linked, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	detached := filepath.Join(t.TempDir(), "detached")
	gitRun(t, repo, "worktree", "add", "--detach", detached)
	unborn := t.TempDir()
	gitRun(t, unborn, "init", "-b", "trunk")

	tests := []struct {
		name string
		dir  string
		want Checkout
	}{
		{"main checkout", repo, Checkout{Repo: true, Branch: "main"}},
		{"linked worktree", linked, Checkout{Repo: true, Linked: true, Branch: "feature"}},
		{"linked worktree subdirectory", sub, Checkout{Repo: true, Linked: true, Branch: "feature"}},
		{"detached linked worktree", detached, Checkout{Repo: true, Linked: true}},
		{"unborn branch", unborn, Checkout{Repo: true, Branch: "trunk"}},
		{"not a repo", t.TempDir(), Checkout{}},
		{"missing dir", filepath.Join(t.TempDir(), "gone"), Checkout{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Inspect(tt.dir); got != tt.want {
				t.Errorf("Inspect(%s) = %+v, want %+v", tt.dir, got, tt.want)
			}
		})
	}
}
