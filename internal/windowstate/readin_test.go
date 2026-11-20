package windowstate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestReadIn(t *testing.T) {
	local := t.TempDir()
	windowstate.SetDirForTest(local)
	if err := os.WriteFile(filepath.Join(local, "@1.json"), []byte(`{"state":"blocked"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	rt := t.TempDir()
	dir := windowstate.WindowsDirFor(rt)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "@1.json"), []byte(`{"state":"working","session_id":"abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinked := t.TempDir()
	if err := os.Symlink(dir, filepath.Join(symlinked, "windows")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		runtimeDir string
		windowID   string
		wantState  string
		wantErr    bool
	}{
		{name: "record in the target dir, not the local one", runtimeDir: rt, windowID: "@1", wantState: "working"},
		{name: "missing record", runtimeDir: rt, windowID: "@2"},
		{name: "missing windows dir", runtimeDir: filepath.Join(t.TempDir(), "proj"), windowID: "@1"},
		{name: "empty runtime dir", runtimeDir: "", windowID: "@1"},
		{name: "symlinked windows dir is refused", runtimeDir: symlinked, windowID: "@1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, err := windowstate.ReadIn(tt.runtimeDir, tt.windowID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if ws.State != tt.wantState {
				t.Errorf("state = %q, want %q", ws.State, tt.wantState)
			}
			if tt.runtimeDir != "" && tt.runtimeDir != symlinked {
				if _, err := os.Stat(filepath.Join(tt.runtimeDir, "windows", tt.windowID+".lock")); !os.IsNotExist(err) {
					t.Errorf("ReadIn created a lock file: %v", err)
				}
			}
		})
	}

	t.Run("missing runtime dir is not created", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "gone", ".agentctl")
		if _, err := windowstate.ReadIn(missing, "@1"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Errorf("runtime dir created: %v", err)
		}
	})
}
