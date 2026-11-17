package approval

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMarkPendingClear(t *testing.T) {
	rt := t.TempDir()
	if Pending(rt, time.Now()) {
		t.Fatal("pending before mark")
	}
	clear := Mark(rt, "@7")
	if !Pending(rt, time.Now()) {
		t.Fatal("not pending after mark")
	}
	clear()
	if Pending(rt, time.Now()) {
		t.Fatal("pending after clear")
	}
}

func TestStaleMarkerIgnored(t *testing.T) {
	rt := t.TempDir()
	Mark(rt, "@7")
	if Pending(rt, time.Now().Add(StaleAfter+time.Second)) {
		t.Fatal("stale marker should be ignored")
	}
	old := time.Now().Add(-2 * StaleAfter)
	if err := os.Chtimes(filepath.Join(rt, "approval-pending", "@7"), old, old); err != nil {
		t.Fatal(err)
	}
	if Pending(rt, time.Now()) {
		t.Fatal("old-mtime marker should be ignored")
	}
}

func TestMarkNoop(t *testing.T) {
	Mark("", "@1")()
	Mark(t.TempDir(), "")()
	Mark(t.TempDir(), "../x")()
}

func TestGC(t *testing.T) {
	rd := t.TempDir()
	Mark(rd, "@1")
	Mark(rd, "@2")
	old := time.Now().Add(-StaleAfter - time.Minute)
	if err := os.Chtimes(filepath.Join(dir(rd), "@1"), old, old); err != nil {
		t.Fatal(err)
	}
	if n := GC(rd, time.Now()); n != 1 {
		t.Fatalf("GC removed %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(dir(rd), "@1")); !os.IsNotExist(err) {
		t.Fatal("stale marker survived")
	}
	if _, err := os.Stat(filepath.Join(dir(rd), "@2")); err != nil {
		t.Fatal("fresh marker removed")
	}
	if GC("", time.Now()) != 0 {
		t.Fatal("GC with empty dir must be a no-op")
	}
}
