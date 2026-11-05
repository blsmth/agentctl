package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// original is a settings file with content a map[string]interface{} round trip
// would mangle: a uint64 beyond float64 precision, an unowned hook event with
// odd spacing, and a key order that is not alphabetical.
const originalSettings = `{
  "zeta": {"big": 18446744073709551615},
  "hooks": {"MyCustomEvent": [ {"hooks": [{"type": "command", "command": "echo hi"}]} ]},
  "alpha": "kept"
}
`

func TestWriteHooksToPath(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
		check func(t *testing.T, path string)
	}{
		{
			name: "unknown top-level key values and order are preserved",
			setup: func(t *testing.T, path string) {
				mustWrite(t, path, originalSettings, 0o644)
			},
			check: func(t *testing.T, path string) {
				data := mustRead(t, path)
				if !bytes.Contains(data, []byte("18446744073709551615")) {
					t.Errorf("large integer was not preserved verbatim:\n%s", data)
				}
				z, h, a := bytes.Index(data, []byte(`"zeta"`)), bytes.Index(data, []byte(`"hooks"`)), bytes.Index(data, []byte(`"alpha"`))
				if z >= h || h >= a {
					t.Errorf("top-level key order changed (zeta=%d hooks=%d alpha=%d)", z, h, a)
				}
				if bytes.Contains(data, []byte(`"permissions"`)) {
					t.Error("writeHooksToPath added a permissions key")
				}
				var doc struct {
					Hooks map[string]json.RawMessage `json:"hooks"`
				}
				if err := json.Unmarshal(data, &doc); err != nil {
					t.Fatalf("parsing result: %v", err)
				}
				if !strings.Contains(string(doc.Hooks["MyCustomEvent"]), "echo hi") {
					t.Errorf("unowned hook event lost: %s", doc.Hooks["MyCustomEvent"])
				}
				if _, ok := doc.Hooks["SessionStart"]; !ok {
					t.Error("owned SessionStart hook not installed")
				}
			},
		},
		{
			name: "backup is written once and keeps the pre-agentctl content",
			setup: func(t *testing.T, path string) {
				mustWrite(t, path, originalSettings, 0o644)
				if err := writeHooksToPath(path, "agentctl"); err != nil {
					t.Fatalf("first write: %v", err)
				}
				mustWrite(t, path, `{"changed": true}`, 0o644)
			},
			check: func(t *testing.T, path string) {
				if got := string(mustRead(t, path+".bak")); got != originalSettings {
					t.Errorf("backup = %q, want original content", got)
				}
			},
		},
		{
			name: "file mode is preserved",
			setup: func(t *testing.T, path string) {
				mustWrite(t, path, `{}`, 0o600)
			},
			check: func(t *testing.T, path string) {
				fi, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if fi.Mode().Perm() != 0o600 {
					t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
				}
			},
		},
		{
			name:  "write is atomic and leaves no temp files",
			setup: func(t *testing.T, path string) {},
			check: func(t *testing.T, path string) {
				entries, err := os.ReadDir(filepath.Dir(path))
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range entries {
					if e.Name() != "settings.json" {
						t.Errorf("unexpected file left behind: %s", e.Name())
					}
				}
			},
		},
		{
			name: "unparseable file is not clobbered",
			setup: func(t *testing.T, path string) {
				mustWrite(t, path, `{not json`, 0o644)
			},
			check: func(t *testing.T, path string) {
				if got := string(mustRead(t, path)); got != `{not json` {
					t.Errorf("file was rewritten: %q", got)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			tc.setup(t, path)
			err := writeHooksToPath(path, "agentctl")
			if strings.HasPrefix(tc.name, "unparseable") {
				if err == nil {
					t.Fatal("expected an error for an unparseable file")
				}
			} else if err != nil {
				t.Fatalf("writeHooksToPath: %v", err)
			}
			tc.check(t, path)
		})
	}
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
