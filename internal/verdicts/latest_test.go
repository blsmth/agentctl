package verdicts

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func useTempLedger(t *testing.T) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "verdicts")
	oldDir, oldRoot := dirFn, rootFn
	dirFn = func() (string, error) { return dir, nil }
	rootFn = func() (string, error) { return filepath.Dir(dir), nil }
	latestCache.mu.Lock()
	latestCache.sig, latestCache.recs, latestCache.loads = "", nil, 0
	latestCache.mu.Unlock()
	t.Cleanup(func() { dirFn, rootFn = oldDir, oldRoot })
}

func TestLatestCachesUntilFileChanges(t *testing.T) {
	useTempLedger(t)
	ts := time.Now().Add(-time.Hour)
	if err := Append(Record{TS: ts, PR: 7, Repo: "o/r", Verdict: Blocked}); err != nil {
		t.Fatal(err)
	}
	if err := Append(Record{TS: ts.Add(time.Minute), PR: 8, Repo: "o/r", Verdict: Merge}); err != nil {
		t.Fatal(err)
	}
	r, ok := Latest(7, "o/r")
	if !ok || r.Verdict != Blocked {
		t.Fatalf("Latest(7) = %+v, %v", r, ok)
	}
	for range 3 {
		Latest(8, "")
	}
	if latestCache.loads != 1 {
		t.Fatalf("loads = %d, want 1 (cached)", latestCache.loads)
	}
	if _, ok := Latest(9, ""); ok {
		t.Fatal("PR 9 has no verdict")
	}
	if _, ok := Latest(7, "other/repo"); ok {
		t.Fatal("repo mismatch must not match")
	}

	// A newer verdict on PR 7 changes the file: re-read, newest wins.
	if err := Append(Record{TS: ts.Add(2 * time.Minute), PR: 7, Repo: "o/r", Verdict: Merge}); err != nil {
		t.Fatal(err)
	}
	// Force a distinct mtime even on coarse filesystems.
	dir, _ := dirFn()
	path := filepath.Join(dir, FileName(ts))
	bump := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, bump, bump); err != nil {
		t.Fatal(err)
	}
	r, ok = Latest(7, "o/r")
	if !ok || r.Verdict != Merge || latestCache.loads != 2 {
		t.Fatalf("after append: %+v %v loads=%d", r, ok, latestCache.loads)
	}
}

func TestLatestMissingLedger(t *testing.T) {
	useTempLedger(t)
	if _, ok := Latest(1, ""); ok {
		t.Fatal("empty ledger has no verdict")
	}
}
