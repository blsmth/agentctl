package tmux

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneLaunchDir(t *testing.T) {
	now := time.Now()
	old := now.Add(-8 * 24 * time.Hour)
	young := now.Add(-6 * 24 * time.Hour)
	files := map[string]time.Time{
		"adhoc-sys-1.txt":          old,
		"review-pr-sys-2.txt":      old,
		"launch-3.sh":              old,
		"adhoc-sys-4.txt":          young,
		"launch-5.sh":              young,
		"adhoc-sys-live.txt":       old, // referenced by a live window
		"launch-agent:1.log":       old, // not a pruned kind
		"agentctl-pi.ts":           old, // harness file
		"notes.txt":                old,
		"adhoc-sys-6.txt.keep":     old,
		"launch-brandon:7-sys.txt": old, // matches *-sys-*? no: needs "-sys-"
	}
	dir := t.TempDir()
	for name, mt := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	live := filepath.Join(dir, "adhoc-sys-live.txt")
	n, err := PruneLaunchDir(dir, now, LaunchFileMaxAge, func() (string, error) {
		return "claude --append-system-prompt-file " + live + " hi\n", nil
	})
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v, want 3", n, err)
	}
	for name := range files {
		_, err := os.Stat(filepath.Join(dir, name))
		gone := os.IsNotExist(err)
		wantGone := name == "adhoc-sys-1.txt" || name == "review-pr-sys-2.txt" || name == "launch-3.sh"
		if gone != wantGone {
			t.Errorf("%s: gone=%v, want %v", name, gone, wantGone)
		}
	}
}

func TestPruneLaunchDirFailsClosedAndMissingDir(t *testing.T) {
	now := time.Now()
	if n, err := PruneLaunchDir(filepath.Join(t.TempDir(), "none"), now, LaunchFileMaxAge, nil); n != 0 || err != nil {
		t.Errorf("missing dir: %d, %v", n, err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "adhoc-sys-1.txt")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-30 * 24 * time.Hour)
	_ = os.Chtimes(p, old, old)
	n, err := PruneLaunchDir(dir, now, LaunchFileMaxAge, func() (string, error) { return "", errors.New("no ps") })
	if n != 0 || err == nil {
		t.Errorf("ps failure: %d, %v", n, err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Error("file deleted although processes could not be listed")
	}
}
