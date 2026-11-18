package mailbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnqueueInWritesToOtherRuntimeDir(t *testing.T) {
	SetDirForTest("")
	rt := filepath.Join(t.TempDir(), ".agentctl")
	if err := os.MkdirAll(rt, 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := EnqueueIn(rt, Message{To: "@7", ToSession: "s", Kind: KindFYI, Body: "agentctl notice: hi"})
	if err != nil {
		t.Fatalf("EnqueueIn: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rt, "inbox", "@7", id+".json")); err != nil {
		t.Fatalf("message not in the other session's inbox: %v", err)
	}

	SetDirForTest(filepath.Join(rt, "inbox"))
	t.Cleanup(func() { SetDirForTest("") })
	msgs, err := Pending("@7")
	if err != nil || len(msgs) != 1 || msgs[0].Body != "agentctl notice: hi" {
		t.Fatalf("Pending = %+v, %v", msgs, err)
	}
}

func TestEnqueueInRejectsSymlinkedRuntimeDir(t *testing.T) {
	SetDirForTest("")
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "inbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	rt := filepath.Join(base, ".agentctl")
	if err := os.MkdirAll(rt, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "inbox"), filepath.Join(rt, "inbox")); err != nil {
		t.Fatal(err)
	}
	if _, err := EnqueueIn(rt, Message{To: "@7", Kind: KindFYI, Body: "x"}); err == nil {
		t.Fatal("EnqueueIn wrote through a symlinked inbox")
	}
}
