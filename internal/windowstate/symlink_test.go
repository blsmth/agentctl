package windowstate

import (
	"os"
	"path/filepath"
	"testing"
)

// useDirs points the package at runtimeDir/windows for the test.
func useDirs(t *testing.T, runtimeDir string) {
	t.Helper()
	prevRoot, prevDir := rootDir, windowsDir
	setDirs(runtimeDir, filepath.Join(runtimeDir, "windows"))
	t.Cleanup(func() { setDirs(prevRoot, prevDir) })
}

func writeVictim(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertVictim(t *testing.T, path string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("%s modified: %q", path, got)
	}
}

func TestWriteRefusesSymlinks(t *testing.T) {
	t.Run("state file is a symlink", func(t *testing.T) {
		rt := t.TempDir()
		useDirs(t, rt)
		victim := filepath.Join(t.TempDir(), "victim")
		writeVictim(t, victim)
		if err := os.MkdirAll(windowsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filePath("@1")); err != nil {
			t.Fatal(err)
		}
		if err := Write("@1", WindowState{State: "working"}); err == nil {
			t.Error("Write succeeded through symlink")
		}
		assertVictim(t, victim)
	})
	t.Run("windows dir is a symlink", func(t *testing.T) {
		rt := t.TempDir()
		useDirs(t, rt)
		outside := t.TempDir()
		victim := filepath.Join(outside, "@1.json")
		writeVictim(t, victim)
		if err := os.Symlink(outside, windowsDir); err != nil {
			t.Fatal(err)
		}
		if err := Write("@1", WindowState{State: "working"}); err == nil {
			t.Error("Write succeeded through symlinked dir")
		}
		assertVictim(t, victim)
	})
	t.Run("lock file is a symlink", func(t *testing.T) {
		rt := t.TempDir()
		useDirs(t, rt)
		victim := filepath.Join(t.TempDir(), "victim")
		writeVictim(t, victim)
		if err := os.MkdirAll(windowsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filePath("@1")+".lock"); err != nil {
			t.Fatal(err)
		}
		if err := Update("@1", func(ws *WindowState) { ws.State = "idle" }); err == nil {
			t.Error("Update opened a symlinked lock file")
		}
		assertVictim(t, victim)
	})
}

func TestWriteModes(t *testing.T) {
	rt := t.TempDir()
	useDirs(t, rt)
	// A windows dir left 0755 by an older version is tightened.
	if err := os.MkdirAll(windowsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Update("@1", func(ws *WindowState) { ws.State = "working" }); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		windowsDir:               0o700,
		filePath("@1"):           0o600,
		filePath("@1") + ".lock": 0o600,
	} {
		fi, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", path, fi.Mode().Perm(), want)
		}
	}
}

func TestInitFallbackIsPrivate(t *testing.T) {
	prevRoot, prevDir := rootDir, windowsDir
	t.Cleanup(func() { setDirs(prevRoot, prevDir) })
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	// paths.Init has not been called in this package's tests, so
	// paths.WindowsDir() is "" and Init takes the fallback.
	Init()
	if windowsDir == "" || filepath.Base(filepath.Dir(windowsDir)) == "tmp" {
		t.Fatalf("windowsDir = %q, want a per-user dir", windowsDir)
	}
	fi, err := os.Lstat(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("fallback root mode = %v, want 0700", fi.Mode().Perm())
	}
}

func TestRemoveAllDoesNotDeleteThroughSymlink(t *testing.T) {
	rt := t.TempDir()
	useDirs(t, rt)
	outside := t.TempDir()
	victim := filepath.Join(outside, "precious")
	writeVictim(t, victim)
	if err := os.Symlink(outside, windowsDir); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAll(); err == nil {
		t.Error("RemoveAll succeeded on a symlinked windows dir")
	}
	Remove("precious")
	assertVictim(t, victim)
}

func TestReadRefusesSymlinkedStateFile(t *testing.T) {
	rt := t.TempDir()
	useDirs(t, rt)
	if err := os.MkdirAll(windowsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	forged := filepath.Join(t.TempDir(), "forged.json")
	if err := os.WriteFile(forged, []byte(`{"state":"working","worktree_dir":"/"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(forged, filePath("@1")); err != nil {
		t.Fatal(err)
	}
	if ws, err := Read("@1"); err == nil {
		t.Errorf("Read followed a symlinked state file: %+v", ws)
	}
	if got := AllFrom(windowsDir); len(got) != 0 {
		t.Errorf("AllFrom read a symlinked state file: %+v", got)
	}
}

func TestRemoveCleansTempFilesAndLockIsTightened(t *testing.T) {
	rt := t.TempDir()
	useDirs(t, rt)
	if err := os.MkdirAll(windowsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A lock file left 0644 by an older version is tightened on use.
	lock := filePath("@1") + ".lock"
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(lock, 0o644) //nolint:errcheck
	if err := Update("@1", func(ws *WindowState) { ws.State = "idle" }); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(lock); fi.Mode().Perm() != 0o600 {
		t.Errorf("lock mode = %v, want 0600", fi.Mode().Perm())
	}

	// Leftover temp files from an interrupted safefs.WriteFile, plus one for
	// a different window that must survive.
	mine := filepath.Join(windowsDir, ".@1.json.123456.tmp")
	other := filepath.Join(windowsDir, ".@10.json.123456.tmp")
	for _, p := range []string{mine, other} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	Remove("@1")
	for _, p := range []string{filePath("@1"), lock, mine} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s not removed", filepath.Base(p))
		}
	}
	if _, err := os.Lstat(other); err != nil {
		t.Errorf("other window's temp file removed: %v", err)
	}
}
