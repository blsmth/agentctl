package tmux

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestSaveRestoreFor(t *testing.T) {
	rt := t.TempDir()
	path := filepath.Join(rt, "restore.json")
	// A session name that cannot exist: no live windows (server-crash case).
	const session = "agentctl-test-nonexistent-session"

	t.Run("clean start does not clobber existing file", func(t *testing.T) {
		if err := os.WriteFile(path, []byte(`{"agents":[{"name":"keep"}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if n := saveRestoreFor(session, rt, map[string]windowstate.WindowState{
			"@1": {Instance: "shell"},
		}); n != 0 {
			t.Fatalf("saved %d, want 0", n)
		}
		got, _ := os.ReadFile(path)
		if string(got) != `{"agents":[{"name":"keep"}]}` {
			t.Errorf("file overwritten: %s", got)
		}
	})

	t.Run("records alone produce restore.json", func(t *testing.T) {
		n := saveRestoreFor(session, rt, map[string]windowstate.WindowState{
			"@2": {Instance: "go-engineer", AgentName: "go-engineer", WorkDir: "/w", WindowIndex: 1},
		})
		if n != 1 {
			t.Fatalf("saved %d, want 1", n)
		}
		got, _ := os.ReadFile(path)
		if len(got) == 0 || string(got) == `{"agents":[{"name":"keep"}]}` {
			t.Errorf("file not rewritten: %s", got)
		}
	})
}
