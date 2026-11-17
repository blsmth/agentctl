package claudehome

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the package dir to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// agentctl must never read, copy or pass Claude credentials: Claude Code
// owns the login for each config dir, and agentctl only picks the dir. No
// non-test Go source may name Claude Code's credentials file or its keychain
// entry, and `security find-generic-password` is allowed only where agentctl
// reads its own (non-Claude) backend key.
func TestNoCodeTouchesClaudeCredentials(t *testing.T) {
	forbidden := []string{
		".credentials.json",
		"Claude Code-credentials", // Claude Code's macOS keychain service
	}
	// find-generic-password is allowed only in these files, which read
	// agentctl's own keychain service (see TestBackendKeychainServiceIsAgentctl).
	keychainAllowed := map[string]bool{
		filepath.Join("internal", "backendauth", "store.go"): true,
	}
	root := repoRoot(t)
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", "node_modules", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		rel, _ := filepath.Rel(root, path)
		src := string(data)
		for _, f := range forbidden {
			if strings.Contains(src, f) {
				t.Errorf("%s mentions %q: agentctl must never read Claude credentials", rel, f)
			}
		}
		if strings.Contains(src, "find-generic-password") && !keychainAllowed[rel] {
			t.Errorf("%s calls security find-generic-password: agentctl must never read Claude credentials", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d files; the walk is not covering the repo", scanned)
	}
}

// The one allowed find-generic-password caller reads agentctl's own keychain
// service, never Claude Code's.
func TestBackendKeychainServiceIsAgentctl(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "backendauth", "store.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `const keychainService = "agentctl"`) {
		t.Error(`internal/backendauth must read only its own keychain service ("agentctl")`)
	}
}

// sessions/ must never be shared between accounts.
func TestSharedEntriesExcludePrivateState(t *testing.T) {
	for _, e := range SharedEntries {
		switch e {
		case "sessions", "projects", ".credentials.json", ".claude.json", "history.jsonl":
			t.Errorf("%s must not be in the shared allowlist", e)
		}
		if strings.HasPrefix(e, "remote-settings") || strings.HasPrefix(e, "policy-limits") {
			t.Errorf("%s must not be in the shared allowlist", e)
		}
	}
}
