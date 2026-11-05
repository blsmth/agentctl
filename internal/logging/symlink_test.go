package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/paths"
)

func TestInitRefusesSymlinks(t *testing.T) {
	tests := []struct {
		name string
		// plant builds the attack under runtimeDir and returns the victim.
		plant func(t *testing.T, runtimeDir, outside string) string
	}{
		{
			name: "log file is a symlink",
			plant: func(t *testing.T, runtimeDir, outside string) string {
				victim := filepath.Join(outside, "victim")
				if err := os.MkdirAll(filepath.Join(runtimeDir, "logs"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(victim, filepath.Join(runtimeDir, "logs", "agentctl.log")); err != nil {
					t.Fatal(err)
				}
				return victim
			},
		},
		{
			name: "logs dir is a symlink",
			plant: func(t *testing.T, runtimeDir, outside string) string {
				if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(runtimeDir, "logs")); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(outside, "agentctl.log")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetState()
			paths.ResetForTest()
			t.Setenv("AGENTCTL_RUNTIME_DIR", "")
			t.Setenv("AGENTCTL_SESSION_ID", "")
			t.Setenv("AGENTCTL_DEBUG", "")
			t.Cleanup(func() {
				resetState()
				paths.ResetForTest()
			})
			project := t.TempDir()
			outside := t.TempDir()
			victim := tt.plant(t, filepath.Join(project, ".agentctl"), outside)
			if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
				t.Fatal(err)
			}
			paths.Init(project)

			// Init reports the refusal on stderr; silence it for the test.
			stderr := os.Stderr
			devnull, _ := os.Open(os.DevNull)
			os.Stderr = devnull
			Init()
			Log("a line that must not reach the victim")
			os.Stderr = stderr
			devnull.Close() //nolint:errcheck

			got, err := os.ReadFile(victim)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "original" {
				t.Errorf("victim modified: %q", got)
			}
			fi, err := os.Stat(victim)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o644 {
				t.Errorf("victim chmodded to %v", fi.Mode().Perm())
			}
		})
	}
}

func TestPruneSessionLogsDoesNotDeleteThroughSymlink(t *testing.T) {
	resetState()
	paths.ResetForTest()
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Cleanup(func() {
		resetState()
		paths.ResetForTest()
	})
	project := t.TempDir()
	outside := t.TempDir()
	old := filepath.Join(outside, "precious.log")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ancient := time.Now().Add(-365 * 24 * time.Hour)
	if err := os.Chtimes(old, ancient, ancient); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(project, ".agentctl", "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(logs, "sessions")); err != nil {
		t.Fatal(err)
	}
	paths.Init(project)

	if n, err := PruneSessionLogs(time.Hour, nil); err == nil || n != 0 {
		t.Errorf("PruneSessionLogs = %d, %v; want refusal", n, err)
	}
	if _, err := os.Lstat(old); err != nil {
		t.Errorf("file deleted through symlink: %v", err)
	}
}

// TestGuardedWriterDropsAfterSwap: a long-lived logger whose logs dir is
// replaced by a symlink mid-run (git pull) stops writing rather than
// following it.
func TestGuardedWriterDropsAfterSwap(t *testing.T) {
	root := t.TempDir()
	logs := filepath.Join(root, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	var sink countingWriter
	g := &guardedWriter{root: root, path: filepath.Join(logs, "agentctl.log"), w: &sink}
	g.Write([]byte("a")) //nolint:errcheck
	if err := os.Rename(logs, logs+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), logs); err != nil {
		t.Fatal(err)
	}
	g.Write([]byte("b")) //nolint:errcheck
	if sink.n != 1 {
		t.Errorf("writes reaching lumberjack = %d, want 1", sink.n)
	}
}

type countingWriter struct{ n int }

func (c *countingWriter) Write(p []byte) (int, error) { c.n++; return len(p), nil }
