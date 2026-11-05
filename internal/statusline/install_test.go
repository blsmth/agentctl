package statusline

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func settingsFilePath(t *testing.T, home string) string {
	t.Helper()
	return filepath.Join(home, ".claude", "settings.json")
}

func TestInstall_FreshFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	if err := Install(false, false); err != nil {
		t.Fatalf("Install: %v", err)
	}

	data, err := os.ReadFile(settingsFilePath(t, home))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sl, ok := doc["statusLine"].(map[string]interface{})
	if !ok {
		t.Fatalf("statusLine key missing or wrong type: %+v", doc)
	}
	if sl["command"] != command {
		t.Errorf("command = %v, want %v", sl["command"], command)
	}
	if sl["type"] != "command" {
		t.Errorf("type = %v, want command", sl["type"])
	}
	// No "timeout" key -- Claude Code rejects the block if present (spec §6).
	if _, hasTimeout := sl["timeout"]; hasTimeout {
		t.Error("statusLine entry has a \"timeout\" key, which Claude Code rejects")
	}
	// refreshInterval keeps the clock/rate-limit gauge advancing while a
	// session is idle.
	if ri, ok := sl["refreshInterval"].(float64); !ok || ri != 1 {
		t.Errorf("refreshInterval = %v, want 1", sl["refreshInterval"])
	}
}

func TestInstall_IdempotentReinstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	if err := Install(false, false); err != nil {
		t.Fatalf("Install (first): %v", err)
	}
	if err := Install(false, false); err != nil {
		t.Fatalf("Install (second, same value): %v", err)
	}
}

func TestInstall_RefusesDifferingWithoutForce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := map[string]interface{}{
		"statusLine": map[string]interface{}{
			"type":    "command",
			"command": "some-other-statusline-script",
		},
	}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(settingsFilePath(t, home), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := Install(false, false)
	if err == nil {
		t.Fatal("expected Install to refuse overwriting a differing statusLine, got nil error")
	}

	// The file must be untouched.
	after, err := os.ReadFile(settingsFilePath(t, home))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(after, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sl := doc["statusLine"].(map[string]interface{})
	if sl["command"] != "some-other-statusline-script" {
		t.Errorf("existing statusLine was modified: %+v", sl)
	}
}

func TestInstall_ForceOverwrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := map[string]interface{}{
		"statusLine": map[string]interface{}{
			"type":    "command",
			"command": "some-other-statusline-script",
		},
	}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(settingsFilePath(t, home), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := Install(true, false); err != nil {
		t.Fatalf("Install(force=true): %v", err)
	}

	after, err := os.ReadFile(settingsFilePath(t, home))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(after, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sl := doc["statusLine"].(map[string]interface{})
	if sl["command"] != command {
		t.Errorf("command = %v, want %v", sl["command"], command)
	}
}

// TestInstall_UpgradesOwnEntryWithoutForce is the regression test for this trap:
// adding refreshInterval to desiredEntry means every previously
// installed entry now differs from desired. An entry whose command is ours
// must be upgraded in place without requiring --force -- only a genuinely
// foreign command should still be refused (see
// TestInstall_RefusesDifferingWithoutForce).
func TestInstall_UpgradesOwnEntryWithoutForce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Simulate a pre-refreshInterval install: our command, but no refreshInterval key.
	existing := map[string]interface{}{
		"statusLine": map[string]interface{}{
			"type":    "command",
			"command": command,
			"padding": 0,
		},
	}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(settingsFilePath(t, home), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := Install(false, false); err != nil {
		t.Fatalf("Install (upgrade, no --force): %v", err)
	}

	after, err := os.ReadFile(settingsFilePath(t, home))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(after, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sl, ok := doc["statusLine"].(map[string]interface{})
	if !ok {
		t.Fatalf("statusLine key missing or wrong type: %+v", doc)
	}
	if sl["command"] != command {
		t.Errorf("command = %v, want %v", sl["command"], command)
	}
	if ri, ok := sl["refreshInterval"].(float64); !ok || ri != 1 {
		t.Errorf("refreshInterval = %v, want 1 (upgrade should have added it)", sl["refreshInterval"])
	}
}

func TestInstall_Uninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	if err := Install(false, false); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := Install(false, true); err != nil {
		t.Fatalf("Install(uninstall=true): %v", err)
	}

	data, err := os.ReadFile(settingsFilePath(t, home))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := doc["statusLine"]; ok {
		t.Errorf("statusLine key still present after uninstall: %+v", doc)
	}
}

func TestInstall_UninstallNoOpWhenAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	if err := Install(false, true); err != nil {
		t.Fatalf("Install(uninstall=true) on fresh home: %v", err)
	}
}

// TestInstall_PreservesHooksBlock pins the regression where: installing/uninstalling the statusLine entry
// must never drop an existing "hooks" block.
func TestInstall_PreservesHooksBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
				map[string]interface{}{
					"matcher": "Bash",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "agentctl hook-pre-tool"},
					},
				},
			},
		},
		"env": map[string]interface{}{"FOO": "bar"},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(settingsFilePath(t, home), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := Install(false, false); err != nil {
		t.Fatalf("Install: %v", err)
	}

	after, err := os.ReadFile(settingsFilePath(t, home))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(after, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := doc["hooks"]; !ok {
		t.Fatal("hooks block was dropped by Install")
	}
	if _, ok := doc["env"]; !ok {
		t.Fatal("env block was dropped by Install")
	}
	if _, ok := doc["statusLine"]; !ok {
		t.Fatal("statusLine block missing after Install")
	}

	// Round-trip once more via uninstall to make sure hooks survives that
	// path too.
	if err := Install(false, true); err != nil {
		t.Fatalf("Install(uninstall=true): %v", err)
	}
	final, err := os.ReadFile(settingsFilePath(t, home))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	var finalDoc map[string]json.RawMessage
	if err := json.Unmarshal(final, &finalDoc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := finalDoc["hooks"]; !ok {
		t.Fatal("hooks block was dropped by Install(uninstall=true)")
	}
	if _, ok := finalDoc["statusLine"]; ok {
		t.Fatal("statusLine block still present after uninstall")
	}
}

func TestMain(m *testing.M) {
	// The test binary lives in a temp dir, which the real resolver refuses.
	persistedPrefixFn = func() (string, error) { return "agentctl", nil }
	os.Exit(m.Run())
}

func TestInstall_UsesPersistedPrefix(t *testing.T) {
	orig := persistedPrefixFn
	t.Cleanup(func() { persistedPrefixFn = orig })
	persistedPrefixFn = func() (string, error) { return "'/opt/my tools/agentctl'", nil }

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	if err := Install(false, false); err != nil {
		t.Fatalf("Install: %v", err)
	}
	data, err := os.ReadFile(settingsFilePath(t, home))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if got, want := doc["statusLine"]["command"], "'/opt/my tools/agentctl' statusline"; got != want {
		t.Errorf("command = %v, want %v", got, want)
	}
	// Re-install is idempotent and does not need --force.
	if err := Install(false, false); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
}

func TestInstall_RefusesEphemeralBinary(t *testing.T) {
	orig := persistedPrefixFn
	t.Cleanup(func() { persistedPrefixFn = orig })
	persistedPrefixFn = func() (string, error) { return "", errors.New("running from a temp dir") }

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	if err := Install(false, false); err == nil {
		t.Fatal("Install succeeded with an ephemeral binary, want error")
	}
	if _, err := os.Stat(settingsFilePath(t, home)); err == nil {
		t.Error("settings.json written despite refusal")
	}
}
