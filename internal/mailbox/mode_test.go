package mailbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMailboxFilesArePrivate(t *testing.T) {
	tests := []struct {
		name     string
		preexist bool // inbox root already exists world-readable
	}{
		{name: "fresh mailbox", preexist: false},
		{name: "existing 0755 root is tightened", preexist: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "inbox")
			SetDirForTest(root)
			t.Cleanup(func() { SetDirForTest("") })
			if tc.preexist {
				if err := os.MkdirAll(root, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(root, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			id, err := Enqueue(Message{From: "@1", To: "@2", Kind: "fyi", Body: "issue text"})
			if err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
			inbox := filepath.Join(root, "@2")
			assertMode(t, root, 0o700)
			assertMode(t, inbox, 0o700)
			assertMode(t, filepath.Join(inbox, id+".json"), 0o600)

			if err := MarkRead("@2", id); err != nil {
				t.Fatalf("MarkRead: %v", err)
			}
			assertMode(t, filepath.Join(inbox, "read"), 0o700)
			assertMode(t, filepath.Join(inbox, "read", id+".json"), 0o600)
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
