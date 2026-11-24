package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/agenthistory"
)

func TestAgentsHistoryCommands(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	agents := filepath.Join(root, "home", ".claude", "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(agents, "a.md")
	if err := os.WriteFile(p, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := agenthistory.HomeStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot("first", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot("second", "improve"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunAgentsHistory(&out, "", "a", 10); err != nil || !strings.Contains(out.String(), "second") {
		t.Fatalf("history: %q %v", out.String(), err)
	}
	out.Reset()
	if err := RunAgentsDiff(&out, "", "a", "HEAD~1"); err != nil || !strings.Contains(out.String(), "-v1") {
		t.Fatalf("diff: %q %v", out.String(), err)
	}
	out.Reset()
	if err := RunAgentsRevert(&out, "", "a", ""); err != nil || !strings.Contains(out.String(), "Reverted") {
		t.Fatalf("revert: %q %v", out.String(), err)
	}
	if got, _ := os.ReadFile(p); string(got) != "v1\n" {
		t.Fatalf("file = %q", got)
	}
	if err := RunAgentsRevert(&out, "", "../settings.json", ""); err == nil {
		t.Fatal("path outside the tracked tree accepted")
	}
}
