package dispatch

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLaunchRefusesInvalidAgentName checks Launch rejects unsafe names before
// doing any work (no worktree, no tmux window).
func TestLaunchRefusesInvalidAgentName(t *testing.T) {
	for _, name := range []string{"", "x$(touch /tmp/p)", "../../etc", ".hidden", "a#{window_id}", "a b"} {
		t.Run(name, func(t *testing.T) {
			_, err := Launch(Request{AgentName: name, Mode: ModeBare, WorkDir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), "invalid agent name") {
				t.Errorf("Launch(%q) err = %v, want invalid agent name", name, err)
			}
		})
	}
}

// TestResolveAgentBody_WarnsOnShadow checks the visible stderr warning when a
// project-local agent replaces a global agent of the same name.
func TestResolveAgentBody_WarnsOnShadow(t *testing.T) {
	globalDir := t.TempDir()
	withRegistryDir(t, globalDir)
	if err := os.WriteFile(filepath.Join(globalDir, "go-engineer.md"), []byte("---\nname: go-engineer\n---\nGlobal.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	agentsDir := filepath.Join(proj, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projFile := filepath.Join(agentsDir, "go-engineer.md")
	if err := os.WriteFile(projFile, []byte("---\nname: go-engineer\n---\nProject.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	orig := shadowWarnOut
	shadowWarnOut = &buf
	t.Cleanup(func() { shadowWarnOut = orig })

	if got := resolveAgentBody(emptyConfig(), "go-engineer", proj); got != "Project." {
		t.Fatalf("body = %q, want project body", got)
	}
	if !strings.Contains(buf.String(), "shadows your global agent") || !strings.Contains(buf.String(), projFile) {
		t.Errorf("warning = %q, want shadow warning naming %s", buf.String(), projFile)
	}

	buf.Reset()
	if got := resolveAgentBody(emptyConfig(), "go-engineer"); got != "Global." {
		t.Fatalf("body = %q, want global body", got)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected warning without shadowing: %q", buf.String())
	}
}
