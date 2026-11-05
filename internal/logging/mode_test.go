package logging

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/paths"
)

func TestInitCreatesPrivateLogFiles(t *testing.T) {
	tests := []struct {
		name     string
		preexist bool // create a world-readable log (and dir) before Init
	}{
		{name: "fresh log is 0600 in a 0700 dir", preexist: false},
		{name: "existing 0644 log and 0755 dir are tightened", preexist: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetState()
			paths.ResetForTest()
			t.Setenv("AGENTCTL_RUNTIME_DIR", "")
			t.Setenv("AGENTCTL_SESSION_ID", "")
			t.Setenv("AGENTCTL_DEBUG", "")
			paths.Init(t.TempDir())
			t.Cleanup(func() {
				resetState()
				paths.ResetForTest()
			})

			path := LogPath()
			if tc.preexist {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			Init()
			Log("a line containing a prompt")

			assertMode(t, filepath.Dir(path), 0o700)
			assertMode(t, path, 0o600)
		})
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s mode = %v, want %v", path, got, want)
	}
}
