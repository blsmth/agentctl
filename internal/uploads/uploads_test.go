package uploads

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var pngMagic = []byte("\x89PNG\r\n\x1a\n0000000000000000")

func TestExtFor(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
		err  bool
	}{
		{"png", pngMagic, ".png", false},
		{"jpeg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF"), ".jpg", false},
		{"gif", []byte("GIF89a0000"), ".gif", false},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), ".webp", false},
		{"text", []byte("hello"), "", true},
		{"html", []byte("<html><body>x</body></html>"), "", true},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtFor(tt.data)
			if (err != nil) != tt.err || got != tt.want {
				t.Fatalf("ExtFor = %q, %v", got, err)
			}
		})
	}
}

func TestSaveAndPath(t *testing.T) {
	rt := filepath.Join(t.TempDir(), ".agentctl")
	id, err := Save(rt, "@523", ".png", pngMagic)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Path(rt, "@523", id)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(rt, "uploads", "523", id); p != want {
		t.Errorf("path = %s, want %s", p, want)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(p))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", di.Mode().Perm())
	}
	for _, bad := range []string{"../x.png", "abc.png", id + "x", ""} {
		if _, err := Path(rt, "@523", bad); err == nil {
			t.Errorf("Path accepted %q", bad)
		}
	}
	if _, err := Path(rt, "@524", id); err == nil {
		t.Error("Path found another window's upload")
	}
}

func TestPrune(t *testing.T) {
	rt := t.TempDir()
	oldID, _ := Save(rt, "@1", ".png", pngMagic)
	newID, _ := Save(rt, "@2", ".png", pngMagic)
	oldP, _ := Path(rt, "@1", oldID)
	now := time.Now()
	if err := os.Chtimes(oldP, now.Add(-8*24*time.Hour), now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := Prune(rt, now, MaxAge)
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	if _, err := os.Stat(oldP); !os.IsNotExist(err) {
		t.Error("old upload kept")
	}
	if _, err := os.Stat(Dir(rt, "@1")); !os.IsNotExist(err) {
		t.Error("empty window dir kept")
	}
	if _, err := Path(rt, "@2", newID); err != nil {
		t.Errorf("fresh upload removed: %v", err)
	}
	if n, err := Prune(filepath.Join(rt, "none"), now, MaxAge); n != 0 || err != nil {
		t.Errorf("missing dir: %d %v", n, err)
	}
}

func TestDispatchKey(t *testing.T) {
	rt := filepath.Join(t.TempDir(), ".agentctl")
	id, err := Save(rt, DispatchKey, ".png", pngMagic)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Path(rt, DispatchKey, id)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(rt, "uploads", "dispatch", id); p != want {
		t.Errorf("path = %s, want %s", p, want)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	if di, _ := os.Stat(filepath.Dir(p)); di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", di.Mode().Perm())
	}
	// A window's upload is not visible under the dispatch key and vice versa.
	wid, _ := Save(rt, "@7", ".png", pngMagic)
	if _, err := Path(rt, DispatchKey, wid); err == nil {
		t.Error("dispatch key resolved a window upload")
	}
	if _, err := Path(rt, "@7", id); err == nil {
		t.Error("window resolved a dispatch upload")
	}
	// Only the exact reserved key is accepted besides window IDs.
	for _, bad := range []string{"@dispatch", "Dispatch", "dispatch/", "dispatch/..", "../dispatch", "dispatchx", ""} {
		if _, err := Save(rt, bad, ".png", pngMagic); err == nil {
			t.Errorf("Save accepted key %q", bad)
		}
		if _, err := Path(rt, bad, id); err == nil {
			t.Errorf("Path accepted key %q", bad)
		}
	}
}

func TestPruneDispatchDir(t *testing.T) {
	rt := t.TempDir()
	oldID, _ := Save(rt, DispatchKey, ".png", pngMagic)
	newID, _ := Save(rt, DispatchKey, ".png", pngMagic)
	oldP, _ := Path(rt, DispatchKey, oldID)
	now := time.Now()
	old := now.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldP, old, old); err != nil {
		t.Fatal(err)
	}
	if n, err := Prune(rt, now, MaxAge); err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	if _, err := Path(rt, DispatchKey, newID); err != nil {
		t.Errorf("fresh dispatch upload removed: %v", err)
	}
	newP, _ := Path(rt, DispatchKey, newID)
	if err := os.Chtimes(newP, old, old); err != nil {
		t.Fatal(err)
	}
	if n, err := Prune(rt, now, MaxAge); err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	if _, err := os.Stat(Dir(rt, DispatchKey)); !os.IsNotExist(err) {
		t.Error("empty dispatch dir kept")
	}
}
