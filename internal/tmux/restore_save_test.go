package tmux

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/windowstate"
)

const keepContent = `{"agents":[{"name":"keep"}]}`

func stubLive(t *testing.T, live map[string]state.LiveWindow, err error) {
	t.Helper()
	prev := listRestoreWindows
	listRestoreWindows = func(string) (map[string]state.LiveWindow, error) { return live, err }
	t.Cleanup(func() { listRestoreWindows = prev })
}

func rec(inst string, idx int) windowstate.WindowState {
	return windowstate.WindowState{Instance: inst, AgentName: inst, WorkDir: "/w", WindowIndex: idx}
}

func savedNames(t *testing.T, rt string) []string {
	t.Helper()
	s, err := state.LoadFrom(filepath.Join(rt, state.RestoreFileName))
	if err != nil || s == nil {
		t.Fatalf("load restore.json: %v, %v", s, err)
	}
	var out []string
	for _, a := range s.Agents {
		out = append(out, a.Name)
	}
	return out
}

func TestSaveRestoreFor(t *testing.T) {
	shell := map[string]state.LiveWindow{"@1": {Index: 1}}

	t.Run("clean start does not clobber existing file", func(t *testing.T) {
		rt := t.TempDir()
		path := filepath.Join(rt, state.RestoreFileName)
		os.WriteFile(path, []byte(keepContent), 0o600) //nolint:errcheck
		stubLive(t, shell, nil)
		if n := saveRestoreFor("s", rt, map[string]windowstate.WindowState{"@1": rec("shell", 0)}, restoreOpts{filterLive: true}); n != 0 {
			t.Fatalf("saved %d, want 0", n)
		}
		if got, _ := os.ReadFile(path); string(got) != keepContent {
			t.Errorf("file overwritten: %s", got)
		}
	})

	t.Run("closed window record excluded when session is live", func(t *testing.T) {
		rt := t.TempDir()
		stubLive(t, map[string]state.LiveWindow{"@1": {Index: 1}, "@2": {Index: 2}}, nil)
		n := saveRestoreFor("s", rt, map[string]windowstate.WindowState{
			"@2": rec("open", 2), "@7": rec("closed", 3),
		}, restoreOpts{filterLive: true})
		if got := savedNames(t, rt); n != 1 || len(got) != 1 || got[0] != "open" {
			t.Errorf("n=%d got=%v, want only open", n, got)
		}
	})

	t.Run("list error does not include everything", func(t *testing.T) {
		rt := t.TempDir()
		path := filepath.Join(rt, state.RestoreFileName)
		os.WriteFile(path, []byte(keepContent), 0o600) //nolint:errcheck
		stubLive(t, nil, errors.New("boom"))
		n := saveRestoreFor("s", rt, map[string]windowstate.WindowState{"@7": rec("closed", 3)}, restoreOpts{filterLive: true})
		if got, _ := os.ReadFile(path); n != 0 || string(got) != keepContent {
			t.Errorf("n=%d file=%s, want untouched", n, got)
		}
	})

	t.Run("start path with no file saves all records (crash)", func(t *testing.T) {
		rt := t.TempDir()
		stubLive(t, shell, nil)
		n := saveRestoreFor("s", rt, map[string]windowstate.WindowState{
			"@5": rec("b", 2), "@3": rec("a", 1),
		}, restoreOpts{keepExisting: true})
		if got := savedNames(t, rt); n != 2 || got[0] != "a" || got[1] != "b" {
			t.Errorf("n=%d got=%v", n, got)
		}
	})

	t.Run("start path leaves existing restore.json untouched", func(t *testing.T) {
		rt := t.TempDir()
		path := filepath.Join(rt, state.RestoreFileName)
		os.WriteFile(path, []byte(keepContent), 0o600) //nolint:errcheck
		stubLive(t, shell, nil)
		n := saveRestoreFor("s", rt, map[string]windowstate.WindowState{"@5": rec("a", 1)}, restoreOpts{keepExisting: true})
		if got, _ := os.ReadFile(path); n != 0 || string(got) != keepContent {
			t.Errorf("n=%d file=%s, want untouched", n, got)
		}
	})
}
