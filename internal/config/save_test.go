package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testCfg() *Config {
	return &Config{Agents: map[string]AgentConfig{"go-engineer": {}}}
}

func TestSaveToPathRoundTripAndCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.json")
	if err := saveToPath(path, testCfg()); err != nil {
		t.Fatal(err)
	}
	got := loadFromPath(path)
	if _, ok := got.Agents["go-engineer"]; !ok {
		t.Fatalf("agent missing after round trip: %+v", got.Agents)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("expected only config.json, got %v", entries)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, want 0700", di.Mode().Perm())
	}
}

func TestSaveToPathThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "config.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unsupported")
	}
	if err := saveToPath(link, testCfg()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v %v", fi, err)
	}
	if _, ok := loadFromPath(target).Agents["go-engineer"]; !ok {
		t.Fatal("target not updated")
	}
}

func TestSaveToPathFailureKeepsOriginal(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission semantics unavailable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	orig := []byte(`{"agents":{}}`)
	if err := os.WriteFile(path, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := saveToPath(path, testCfg()); err == nil {
		t.Fatal("expected error")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(orig) {
		t.Fatalf("original modified: %s", got)
	}
}

func TestSaveToPathMkdirError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveToPath(filepath.Join(blocker, "x", "config.json"), testCfg()); err == nil {
		t.Fatal("expected mkdir error")
	}
}
