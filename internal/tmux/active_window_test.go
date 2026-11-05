package tmux

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteActiveWindowFile(t *testing.T) {
	tests := []struct {
		name     string
		windowID string
		wantErr  bool
	}{
		{"window id", "@12", false},
		{"empty", "", true},
		{"no at sign", "12", true},
		{"injection", "@1; rm -rf ~", true},
		{"newline", "@1\n@2", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ActiveWindowFileName)
			err := WriteActiveWindowFile(path, tt.windowID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("file written despite error: %v", err)
				}
				return
			}
			if got := ActiveWindowIDFast(path); got != tt.windowID {
				t.Errorf("read back %q, want %q", got, tt.windowID)
			}
		})
	}
}

// TestWriteActiveWindowFileReplacesSymlink is the regression test for the
// old `echo ... > file` hook: a symlink a cloned repo commits at
// .agentctl/active-window must be replaced, not written through.
func TestWriteActiveWindowFileReplacesSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("precious\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ActiveWindowFileName)
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}

	if err := WriteActiveWindowFile(path, "@3"); err != nil {
		t.Fatal(err)
	}

	if b, _ := os.ReadFile(victim); string(b) != "precious\n" {
		t.Errorf("symlink target overwritten: %q", b)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("active-window is still a symlink")
	}
	if got := ActiveWindowIDFast(path); got != "@3" {
		t.Errorf("read back %q, want @3", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("leftover temp files: %v", entries)
	}
}
