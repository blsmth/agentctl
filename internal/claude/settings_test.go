package claude

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// ── Load / Save ───────────────────────────────────────────────────────────────

func TestLoadSaveSettings_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.local.json")

	original := &Settings{
		Permissions: Permissions{
			Allow: []string{"Bash(git *)", "Read(**)"},
			Deny:  []string{"Bash(rm -rf *)"},
		},
	}

	if err := SaveSettings(path, original); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	loaded, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}

	// raw is an internal artifact of having gone through a file; compare the
	// public Permissions field only.
	if !reflect.DeepEqual(original.Permissions, loaded.Permissions) {
		t.Errorf("roundtrip mismatch:\ngot  %+v\nwant %+v", loaded.Permissions, original.Permissions)
	}
}

func TestLoadSettings_NonExistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")

	s, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil Settings for missing file")
	}
	if len(s.Permissions.Allow) != 0 || len(s.Permissions.Deny) != 0 {
		t.Errorf("expected empty settings, got %+v", s)
	}
}

// ── unknown top-level key preservation ────────────────────────────────────────

// rawDoc unmarshals a settings file into its top-level keys, keeping each
// value's exact JSON bytes so subtree formatting can be compared verbatim.
func rawDoc(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	return doc
}

func TestLoadSaveSettings_PreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.local.json")

	input := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": "agentctl hook-pre-tool-use"},
					},
				},
			},
		},
		"statusLine": map[string]any{
			"type":    "command",
			"command": "agentctl statusline",
		},
		"env": map[string]any{
			"FOO": "bar",
		},
		"permissions": map[string]any{
			"allow": []string{"Bash(git *)"},
		},
	}
	inputData, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if err := os.WriteFile(path, inputData, 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	s, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if want := []string{"Bash(git *)"}; !reflect.DeepEqual(s.Permissions.Allow, want) {
		t.Fatalf("Permissions.Allow = %v, want %v", s.Permissions.Allow, want)
	}

	if err := SaveSettings(path, s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	outputData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	inDoc := rawDoc(t, inputData)
	outDoc := rawDoc(t, outputData)

	for _, key := range []string{"hooks", "statusLine", "env", "permissions"} {
		if _, ok := outDoc[key]; !ok {
			t.Errorf("output missing top-level key %q", key)
		}
	}

	for _, key := range []string{"hooks", "statusLine"} {
		if !bytes.Equal(inDoc[key], outDoc[key]) {
			t.Errorf("key %q not byte-identical:\ngot:  %s\nwant: %s", key, outDoc[key], inDoc[key])
		}
	}
}

func TestSaveSettings_MutatingAllowOnlyChangesPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.local.json")

	input := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "agentctl hook-session-start"}}},
			},
		},
		"permissions": map[string]any{
			"allow": []string{"Bash(git *)"},
		},
	}
	inputData, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if err := os.WriteFile(path, inputData, 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	s, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	s.Permissions.Allow = append(s.Permissions.Allow, "Read(**)")

	if err := SaveSettings(path, s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	outputData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	inDoc := rawDoc(t, inputData)
	outDoc := rawDoc(t, outputData)

	if !bytes.Equal(inDoc["hooks"], outDoc["hooks"]) {
		t.Errorf("hooks key changed:\ngot:  %s\nwant: %s", outDoc["hooks"], inDoc["hooks"])
	}

	reloaded, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings (reload): %v", err)
	}
	want := []string{"Bash(git *)", "Read(**)"}
	if !reflect.DeepEqual(reloaded.Permissions.Allow, want) {
		t.Errorf("Permissions.Allow = %v, want %v", reloaded.Permissions.Allow, want)
	}
}

func TestSaveSettings_NonExistentFileSavesOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.local.json")

	s, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	s.Permissions.Allow = append(s.Permissions.Allow, "Bash(git *)")

	if err := SaveSettings(path, s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	doc := rawDoc(t, data)
	if len(doc) != 1 {
		t.Fatalf("expected exactly one top-level key, got %d: %v", len(doc), doc)
	}
	if _, ok := doc["permissions"]; !ok {
		t.Fatalf("expected \"permissions\" key, got %v", doc)
	}
}

func TestLoadSettings_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.local.json")

	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := LoadSettings(path); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestLoadSaveSettings_PreservesAskList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.local.json")

	original := &Settings{
		Permissions: Permissions{
			Allow: []string{"Bash(git *)"},
			Deny:  []string{"Bash(rm -rf *)"},
			Ask:   []string{"Bash(npm publish*)"},
		},
	}

	if err := SaveSettings(path, original); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	loaded, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if !reflect.DeepEqual(original.Permissions, loaded.Permissions) {
		t.Errorf("roundtrip mismatch:\ngot  %+v\nwant %+v", loaded.Permissions, original.Permissions)
	}
}

// ── HasPermission / AddPermission / RemovePermission ─────────────────────────

// ── parsePermission ───────────────────────────────────────────────────────────

func TestParsePermission(t *testing.T) {
	tests := []struct {
		input       string
		wantType    string
		wantContent string
	}{
		{"Bash(git *)", "Bash", "git *"},
		{"Read(**)", "Read", "**"},
		{"WebFetch(https://example.com)", "WebFetch", "https://example.com"},
		// malformed — no parens
		{"malformed", "", ""},
		// malformed — no closing paren
		{"Bash(git *", "", ""},
		// empty string
		{"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotType, gotContent := parsePermission(tt.input)
			if gotType != tt.wantType || gotContent != tt.wantContent {
				t.Errorf("parsePermission(%q) = (%q, %q), want (%q, %q)",
					tt.input, gotType, gotContent, tt.wantType, tt.wantContent)
			}
		})
	}
}

// ── PermissionCovers ──────────────────────────────────────────────────────────

func TestPermissionCovers_Identity(t *testing.T) {
	perm := "Bash(git *)"
	if !PermissionCovers(perm, perm) {
		t.Errorf("PermissionCovers(%q, %q) = false, want true (identity)", perm, perm)
	}
}

func TestPermissionCovers_WildcardSuffix(t *testing.T) {
	tests := []struct {
		broad    string
		specific string
		want     bool
	}{
		// cmd wildcard: "git *" covers sub-commands
		{"Bash(git *)", "Bash(git log:*)", true},
		{"Bash(git *)", "Bash(git status)", true},
		{"Bash(git *)", "Bash(git log)", true},
		{"Bash(git *)", "Bash(git:*)", true},
		{"Bash(git *)", "Bash(git)", true},
		// a different command sharing a prefix is not covered
		{"Bash(git *)", "Bash(gitignore-check)", false},
		{"Bash(git *)", "Bash(github-cli foo)", false},
		{"Bash(git *)", "Bash(gitk)", false},
		{"Bash(git *)", "Bash(gitfoo:*)", false},
		// explicit generic trailing wildcard stays a plain prefix match
		{"Bash(git*)", "Bash(gitk)", true},
		// path wildcard
		{"Read(path/**)", "Read(path/subdir/file.go)", true},
		{"Read(path/**)", "Read(other/file.go)", false},
		// same type but unrelated content
		{"Bash(git *)", "Bash(rm -rf /)", false},
	}

	for _, tt := range tests {
		t.Run(tt.broad+"_covers_"+tt.specific, func(t *testing.T) {
			got := PermissionCovers(tt.broad, tt.specific)
			if got != tt.want {
				t.Errorf("PermissionCovers(%q, %q) = %v, want %v", tt.broad, tt.specific, got, tt.want)
			}
		})
	}
}

func TestPermissionCovers_NonMatching(t *testing.T) {
	tests := []struct {
		broad    string
		specific string
	}{
		// Different types
		{"Bash(git *)", "Read(git *)"},
		// Completely unrelated
		{"Bash(npm *)", "Bash(pip install foo)"},
	}

	for _, tt := range tests {
		t.Run(tt.broad+"_vs_"+tt.specific, func(t *testing.T) {
			if PermissionCovers(tt.broad, tt.specific) {
				t.Errorf("PermissionCovers(%q, %q) = true, want false", tt.broad, tt.specific)
			}
		})
	}
}

// ── RemoveRedundant ───────────────────────────────────────────────────────────

// ── Backup / atomic / order ───────────────────────────────────────────────────

func TestSaveSettings_BackupOnceAndPreservesKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	orig := `{
  "zeta": {"b": 1, "a": [1, 2]},
  "permissions": {"allow": ["Read(**)"]},
  "alpha": "x",
  "hooks": {"Stop": []}
}
`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Permissions.Allow = append(s.Permissions.Allow, "Bash(git *)")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}

	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(bak) != orig {
		t.Errorf("backup differs from original:\n%s", bak)
	}

	// Second save must not overwrite the backup.
	s.Permissions.Allow = append(s.Permissions.Allow, "Bash(go *)")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	bak2, _ := os.ReadFile(path + ".bak")
	if string(bak2) != orig {
		t.Errorf("backup was overwritten on second save")
	}

	data, _ := os.ReadFile(path)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("saved file is not valid JSON: %v", err)
	}
	if got := string(bytes.Join(bytes.Fields(doc["zeta"]), nil)); got != `{"b":1,"a":[1,2]}` {
		t.Errorf("zeta not preserved verbatim: %s", got)
	}
	if _, ok := doc["hooks"]; !ok {
		t.Error("hooks dropped")
	}
	if got := topLevelKeyOrder(data); !reflect.DeepEqual(got, []string{"zeta", "permissions", "alpha", "hooks"}) {
		t.Errorf("key order = %v", got)
	}

	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "settings.json" && e.Name() != "settings.json.bak" {
			t.Errorf("stray file %s", e.Name())
		}
	}
}

func TestSaveSettings_NewFileNoBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "settings.json")
	if err := SaveSettings(path, &Settings{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("unexpected backup for new file: %v", err)
	}
}

func TestSaveSettings_DanglingSymlinkWritesThrough(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink("real.json", link); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(link, &Settings{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was replaced: %v %v", info, err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target not written: %v", err)
	}
}
