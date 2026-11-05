package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRefusesRepoTrackedRuntimeDir runs the real binary inside a repository
// that commits a symlink under .agentctl/ (the reported exploit shape) and
// asserts it exits non-zero, names the file, and writes nothing into the
// runtime dir -- for both a regular subcommand and the hook fast path. HOME
// and XDG_CONFIG_HOME point at a temp dir; the session's agentctl pins are
// cleared so the real runtime dir is never touched.
func TestRefusesRepoTrackedRuntimeDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := getTestBinary(t)

	home := t.TempDir()
	zshenv := filepath.Join(home, ".zshenv")
	if err := os.WriteFile(zshenv, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	settings := filepath.Join(repo, ".agentctl", "settings")
	if err := os.MkdirAll(settings, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(zshenv, filepath.Join(settings, "go-engineer.json")); err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "-C", repo, "init", "-q")
	git.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	git = exec.Command("git", "-C", repo, "add", "-f", ".agentctl")
	git.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	// Every agentctl variable the binary reads is set explicitly: this test
	// often runs inside an agentctl window, whose AGENTCTL=1 and pins must
	// not leak in (CI has none of them).
	baseEnv := append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"AGENTCTL_RUNTIME_DIR=",
		"AGENTCTL_INSTANCE=",
		"AGENTCTL_SESSION_ID=",
		"AGENTCTL_WINDOW_ID=",
		"TMUX=",
		"TMUX_PANE=",
	)

	assertUntouched := func(t *testing.T) {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(repo, ".agentctl"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "settings" {
			t.Errorf("runtime dir was written to: %v", entries)
		}
		if got, _ := os.ReadFile(zshenv); string(got) != "original" {
			t.Errorf("~/.zshenv modified: %q", got)
		}
	}

	tests := []struct {
		name     string
		args     []string
		agentctl string // value of AGENTCTL
		refuse   bool   // want non-zero exit naming the tracked file
	}{
		{name: "context list", args: []string{"context", "list"}, agentctl: "1", refuse: true},
		{name: "hook in agentctl session", args: []string{"hook-pre-tool"}, agentctl: "1", refuse: true},
		// Outside an agentctl session the hook handler is a no-op, so the
		// check is skipped -- but nothing may be written either.
		{name: "hook outside agentctl", args: []string{"hook-pre-tool"}, agentctl: "", refuse: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			cmd := exec.Command(bin, tt.args...)
			cmd.Dir = repo
			cmd.Env = append(baseEnv, "AGENTCTL="+tt.agentctl)
			cmd.Stdin = strings.NewReader("{}")
			cmd.Stderr = &stderr
			err := cmd.Run()
			if tt.refuse {
				if err == nil {
					t.Fatal("agentctl ran in a repo that tracks .agentctl/ content")
				}
				if !strings.Contains(stderr.String(), ".agentctl/settings/go-engineer.json") {
					t.Errorf("stderr does not name the tracked file:\n%s", stderr.String())
				}
			} else if err != nil {
				t.Fatalf("no-op hook failed: %v\n%s", err, stderr.String())
			}
			assertUntouched(t)
		})
	}
}
