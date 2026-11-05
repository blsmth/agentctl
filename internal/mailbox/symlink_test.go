package mailbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/paths"
)

// symlinkedInbox pins paths to a temp project whose .agentctl/inbox is a
// symlink to a directory in a fake home (the `git pull` shape), and returns
// that target dir.
func symlinkedInbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	SetDirForTest("")
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	project := t.TempDir()
	rt := filepath.Join(project, ".agentctl")
	if err := os.MkdirAll(rt, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(rt, "inbox")); err != nil {
		t.Fatal(err)
	}
	paths.Init(project)
	return target
}

// plantMessage writes a valid pending message for @1 under dir.
func plantMessage(t *testing.T, dir string, created time.Time) string {
	t.Helper()
	wd := filepath.Join(dir, "@1")
	if err := os.MkdirAll(wd, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(wd, "20000101T000000.000000000Z-aaaaaaaa.json")
	data := `{"id":"20000101T000000.000000000Z-aaaaaaaa","to":"@1","kind":"fyi","body":"planted","created":"` + created.Format(time.RFC3339Nano) + `"}`
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMailboxRefusesSymlinkedInbox(t *testing.T) {
	t.Run("enqueue does not write through", func(t *testing.T) {
		target := symlinkedInbox(t)
		os.Chmod(target, 0o755) //nolint:errcheck
		if _, err := Enqueue(Message{To: "@1", Kind: KindFYI, Body: "x"}); err == nil {
			t.Error("Enqueue wrote through a symlinked inbox")
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Errorf("target written: %d entries", len(entries))
		}
		if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o755 {
			t.Errorf("symlink target chmodded to %v", fi.Mode().Perm())
		}
	})
	t.Run("planted messages are not read, delivered, or moved", func(t *testing.T) {
		target := symlinkedInbox(t)
		p := plantMessage(t, target, time.Now())
		if msgs, err := Pending("@1"); err == nil || len(msgs) != 0 {
			t.Errorf("Pending = %v, %v; want error and no messages", msgs, err)
		}
		if body, n := DrainFor("@1", "", DefaultTTL); n != 0 || body != "" {
			t.Errorf("DrainFor delivered %d planted messages", n)
		}
		if n, _ := PendingCount("@1"); n != 0 {
			t.Errorf("PendingCount = %d, want 0", n)
		}
		if err := MarkRead("@1", "20000101T000000.000000000Z-aaaaaaaa"); err == nil {
			t.Error("MarkRead moved a message through a symlinked inbox")
		}
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("planted message moved: %v", err)
		}
	})
	t.Run("prune and remove do not delete through", func(t *testing.T) {
		target := symlinkedInbox(t)
		p := plantMessage(t, target, time.Now().Add(-48*time.Hour))
		PruneStale(time.Hour) //nolint:errcheck
		Prune(time.Hour)      //nolint:errcheck
		RemoveInbox("@1")     //nolint:errcheck
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("planted file deleted through symlink: %v", err)
		}
	})
}

func TestListMessagesSkipsSymlinkedFiles(t *testing.T) {
	dir := isolate(t)
	if _, err := Enqueue(Message{To: "@1", Kind: KindFYI, Body: "real"}); err != nil {
		t.Fatal(err)
	}
	outside := plantMessage(t, t.TempDir(), time.Now())
	if err := os.Symlink(outside, filepath.Join(dir, "@1", "20000101T000000.000000000Z-bbbbbbbb.json")); err != nil {
		t.Fatal(err)
	}
	msgs, err := Pending("@1")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Body != "real" {
		t.Errorf("Pending = %+v, want only the real message", msgs)
	}
}
