package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallHooksGlobal verifies that InstallHooks(true) writes valid JSON to
// ~/.claude/settings.json, includes all expected hook events, and is idempotent.
func TestInstallHooksGlobal(t *testing.T) {
	// Point HOME at a temp dir so InstallHooks writes to a controlled location.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")

	// --- First run: file does not exist yet ---
	path, err := InstallHooks(true)
	if err != nil {
		t.Fatalf("InstallHooks(true) first run: %v", err)
	}
	if path != settingsPath {
		t.Errorf("returned path = %q, want %q", path, settingsPath)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings after first run: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal settings after first run: %v", err)
	}

	// Verify "hooks" key is present.
	hooksRaw, ok := settings["hooks"]
	if !ok {
		t.Fatal("settings.json missing \"hooks\" key after first run")
	}

	hooks, ok := hooksRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("\"hooks\" is %T, want map[string]interface{}", hooksRaw)
	}

	// Verify all expected events are present.
	expectedEvents := []string{
		"SessionStart",
		"SessionEnd",
		"Notification",
		"PreToolUse",
		"PostToolUse",
		"PostToolUseFailure",
		"Stop",
		"StopFailure",
		"UserPromptSubmit",
		"SubagentStart",
		"SubagentStop",
		"PostCompact",
		"PreCompact",
		"InstructionsLoaded",
		"ConfigChange",
		"CwdChanged",
		"FileChanged",
		"PermissionRequest",
		"WorktreeCreate",
		"WorktreeRemove",
		"Elicitation",
		"ElicitationResult",
		"TaskCreated",
		"TaskCompleted",
		"TeammateIdle",
		"Setup",
	}
	for _, event := range expectedEvents {
		if _, found := hooks[event]; !found {
			t.Errorf("hooks missing event %q after first run", event)
		}
	}

	// --- Second run: verify idempotency ---
	// Add an unrelated top-level key to ensure it is preserved.
	settings["someOtherKey"] = "preserved"
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatalf("marshal modified settings: %v", err)
	}
	if err := os.WriteFile(settingsPath, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("write modified settings: %v", err)
	}

	if _, err := InstallHooks(true); err != nil {
		t.Fatalf("InstallHooks(true) second run: %v", err)
	}

	data2, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings after second run: %v", err)
	}

	var settings2 map[string]interface{}
	if err := json.Unmarshal(data2, &settings2); err != nil {
		t.Fatalf("unmarshal settings after second run: %v", err)
	}

	// Unrelated key must still be present (InstallHooks only overwrites "hooks").
	if v, _ := settings2["someOtherKey"].(string); v != "preserved" {
		t.Errorf("someOtherKey = %q after second run, want \"preserved\"", v)
	}

	hooks2, ok := settings2["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("\"hooks\" missing or wrong type after second run")
	}

	// All events must still be present after second run.
	for _, event := range expectedEvents {
		if _, found := hooks2[event]; !found {
			t.Errorf("hooks missing event %q after second run", event)
		}
	}

	// Hooks from first and second runs must be structurally identical.
	h1, _ := json.Marshal(hooks)
	h2, _ := json.Marshal(hooks2)
	if string(h1) != string(h2) {
		t.Errorf("hooks changed between first and second run\nfirst:  %s\nsecond: %s", h1, h2)
	}
}

// TestInstallHooksGlobal_ExistingFile verifies that InstallHooks(true) correctly
// merges into an existing settings.json rather than overwriting it entirely.
func TestInstallHooksGlobal_ExistingFile(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")

	// Write a pre-existing settings file with non-hook content.
	existing := map[string]interface{}{
		"theme":             "dark",
		"autoUpdaterStatus": "disabled",
	}
	raw, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		t.Fatalf("marshal existing: %v", err)
	}
	if err := os.WriteFile(settingsPath, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write existing settings: %v", err)
	}

	if _, err := InstallHooks(true); err != nil {
		t.Fatalf("InstallHooks(true): %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	// Existing keys must be preserved.
	if v, _ := result["theme"].(string); v != "dark" {
		t.Errorf("theme = %q, want \"dark\"", v)
	}
	if v, _ := result["autoUpdaterStatus"].(string); v != "disabled" {
		t.Errorf("autoUpdaterStatus = %q, want \"disabled\"", v)
	}

	// Hooks key must now be present.
	if _, ok := result["hooks"]; !ok {
		t.Error("settings.json missing \"hooks\" key")
	}
}

// TestInstallHooksProjectLocal verifies that InstallHooks(false) writes to
// .claude/settings.local.json in the current working directory, creating the
// .claude/ directory if needed.
func TestInstallHooksProjectLocal(t *testing.T) {
	// Use a temp dir as the project root and cd into it.
	tmpProject := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpProject); err != nil {
		t.Fatalf("chdir to temp project: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	// Point HOME at a separate temp dir so we can verify the global file is not written.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	// .claude/ should not exist yet — InstallHooks must create it.
	if _, err := os.Stat(filepath.Join(tmpProject, ".claude")); !os.IsNotExist(err) {
		t.Fatal("expected .claude/ to not exist before first run")
	}

	path, err := InstallHooks(false)
	if err != nil {
		t.Fatalf("InstallHooks(false): %v", err)
	}

	// Path must end with settings.local.json.
	if !strings.HasSuffix(path, "settings.local.json") {
		t.Errorf("project-local path %q should end with settings.local.json", path)
	}

	// The file must exist and contain valid JSON with a "hooks" key.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}

	if _, ok := result["hooks"]; !ok {
		t.Error("settings.local.json missing \"hooks\" key")
	}

	// The global settings.json must NOT have been written.
	globalPath := filepath.Join(tmpHome, ".claude", "settings.json")
	if _, err := os.Stat(globalPath); err == nil {
		t.Errorf("InstallHooks(false) unexpectedly wrote to global path %s", globalPath)
	}

	// Second run must be idempotent and preserve existing keys.
	result["projectKey"] = "preserved"
	out, _ := json.MarshalIndent(result, "", "  ")
	os.WriteFile(path, append(out, '\n'), 0o644) //nolint:errcheck

	if _, err := InstallHooks(false); err != nil {
		t.Fatalf("InstallHooks(false) second run: %v", err)
	}

	data2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after second run: %v", err)
	}
	var result2 map[string]interface{}
	if err := json.Unmarshal(data2, &result2); err != nil {
		t.Fatalf("unmarshal after second run: %v", err)
	}
	if v, _ := result2["projectKey"].(string); v != "preserved" {
		t.Errorf("projectKey = %q after second run, want \"preserved\"", v)
	}
	if _, ok := result2["hooks"]; !ok {
		t.Error("settings.local.json missing \"hooks\" key after second run")
	}
}

// TestInstallHooksRepairsDriftedOwnedEvent verifies the regression this ticket
// exists for: an agentctl-owned hook event (Stop) that has drifted to some
// other value (e.g. rebound to the shell builtin "true") must be overwritten
// with the canonical value on the next InstallHooks run, not preserved.
func TestInstallHooksRepairsDriftedOwnedEvent(t *testing.T) {
	tmpProject := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpProject); err != nil {
		t.Fatalf("chdir to temp project: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	claudeDir := filepath.Join(tmpProject, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.local.json")

	// Simulate the observed drift: Stop rebound to the shell builtin "true",
	// which exits 0 and so passes any check that only probes exit codes.
	drifted := map[string]interface{}{
		"hooks": map[string]interface{}{
			"Stop": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "true",
						},
					},
				},
			},
			// A hook event agentctl does not own — must survive untouched.
			"MyCustomEvent": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "echo custom",
						},
					},
				},
			},
		},
	}
	raw, err := json.MarshalIndent(drifted, "", "  ")
	if err != nil {
		t.Fatalf("marshal drifted settings: %v", err)
	}
	if err := os.WriteFile(settingsPath, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write drifted settings: %v", err)
	}

	if _, err := InstallHooks(false); err != nil {
		t.Fatalf("InstallHooks(false): %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	hooksResult, ok := result["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("\"hooks\" missing or wrong type after repair")
	}

	// Stop must have been repaired to reference hook-check-done, not "true".
	stopJSON, _ := json.Marshal(hooksResult["Stop"])
	if strings.Contains(string(stopJSON), `"true"`) {
		t.Errorf("Stop hook still drifted to \"true\" after InstallHooks: %s", stopJSON)
	}
	if !strings.Contains(string(stopJSON), "hook-check-done") {
		t.Errorf("Stop hook not repaired to canonical hook-check-done: %s", stopJSON)
	}

	// The user's own hook event must be preserved untouched.
	customJSON, _ := json.Marshal(hooksResult["MyCustomEvent"])
	if !strings.Contains(string(customJSON), "echo custom") {
		t.Errorf("MyCustomEvent was not preserved: %s", customJSON)
	}
}
