package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// originEnv points the state dir at a fresh temp dir and returns it.
func originEnv(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	return filepath.Join(state, "agentctl")
}

func TestRecordHubOriginRoundTrip(t *testing.T) {
	stateDir := originEnv(t)
	proj := t.TempDir()
	if err := RecordHubOrigin(proj); err != nil {
		t.Fatal(err)
	}
	got, ok := HubOrigin()
	if !ok || got != proj {
		t.Fatalf("HubOrigin() = %q, %v; want %q", got, ok, proj)
	}
	fi, err := os.Stat(filepath.Join(stateDir, hubOriginFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("origin file mode = %o, want 600", perm)
	}
	// Every invocation overwrites it.
	other := t.TempDir()
	if err := RecordHubOrigin(other); err != nil {
		t.Fatal(err)
	}
	if got, _ := HubOrigin(); got != other {
		t.Errorf("after second record HubOrigin() = %q, want %q", got, other)
	}
}

func TestRecordHubOriginSkips(t *testing.T) {
	stateDir := originEnv(t)
	keep := t.TempDir()
	if err := RecordHubOrigin(keep); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "repo", ".worktrees", "agent-x")
	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	hub := filepath.Join(stateDir, "hub")
	if err := os.MkdirAll(filepath.Join(hub, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	legacyHub := filepath.Join(cfgHome, "agentctl", "hub")
	if err := os.MkdirAll(legacyHub, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{"", "relative/dir", hub, filepath.Join(hub, "sub"), legacyHub, wt, filepath.Join(wt, "pkg")} {
		if err := RecordHubOrigin(cwd); err != nil {
			t.Errorf("RecordHubOrigin(%q): %v", cwd, err)
		}
		if got, _ := HubOrigin(); got != keep {
			t.Errorf("RecordHubOrigin(%q) replaced the origin: got %q, want %q", cwd, got, keep)
		}
	}
}

func TestHubOriginMissingOrBad(t *testing.T) {
	stateDir := originEnv(t)
	if _, ok := HubOrigin(); ok {
		t.Error("HubOrigin() ok with no file")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, hubOriginFile)
	for _, content := range []string{"relative\n", filepath.Join(t.TempDir(), "gone") + "\n", ""} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, ok := HubOrigin(); ok {
			t.Errorf("HubOrigin() with %q = %q, ok; want !ok", content, got)
		}
	}
}

func TestCheckNewSessionDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, dir, wantErr string
	}{
		{"ok", dir, ""},
		{"relative", "rel/dir", "not an absolute path"},
		{"missing", filepath.Join(dir, "nope"), "no such directory"},
		{"file", file, "not a directory: " + file},
		{"root", "/", "refusing"},
		{"home", home, "home directory"},
		{"home via symlink", link, "home directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckNewSessionDir(tt.dir)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
