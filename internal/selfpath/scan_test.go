package selfpath

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoDirectOsExecutable fails when a non-test .go file outside
// internal/selfpath and internal/logging references os.Executable. Every
// consumer of the running binary's path must go through selfpath (Spawnable
// for anything that execs or spawns it), so that a test binary can never
// re-exec itself: that reruns the whole suite and fork-bombs the machine.
func TestNoDirectOsExecutable(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s: %v", root, err)
	}
	allowed := []string{
		filepath.Join(root, "internal", "selfpath") + string(os.PathSeparator),
		filepath.Join(root, "internal", "logging") + string(os.PathSeparator),
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for _, a := range allowed {
			if strings.HasPrefix(path, a) {
				return nil
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if strings.Contains(code, "os.Executable") {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s:%d references os.Executable; use selfpath.Spawnable (exec/spawn) or selfpath.Path/Lookup (render only)", rel, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
