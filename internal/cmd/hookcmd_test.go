package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/hooks"
)

func init() {
	// Test binaries live in go-build temp dirs, which Persisted refuses.
	persistedPrefixFn = func() (string, error) { return "agentctl", nil }
}

func TestNormalizeHookCommand(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(bin, "agentctl")
	if err := os.WriteFile(good, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	cellar := filepath.Join(home, "Cellar", "agentctl")
	if err := os.MkdirAll(cellar, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cellar, "agentctl"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	noexec := filepath.Join(home, "noexec")
	_ = os.MkdirAll(noexec, 0o755)
	if err := os.WriteFile(filepath.Join(noexec, "agentctl"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	spaced := filepath.Join(home, "my bin")
	_ = os.MkdirAll(spaced, 0o755)
	if err := os.WriteFile(filepath.Join(spaced, "agentctl"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, in, want string
	}{
		{"bare", "agentctl hook-stop", "agentctl hook-stop"},
		{"absolute", good + " hook-stop", "agentctl hook-stop"},
		{"tilde", "~/.local/bin/agentctl hook-stop", "agentctl hook-stop"},
		{"cellar", filepath.Join(cellar, "agentctl") + " hook-stop", "agentctl hook-stop"},
		{"quoted with space", "'" + filepath.Join(spaced, "agentctl") + "' hook-stop", "agentctl hook-stop"},
		{"missing", filepath.Join(home, "gone", "agentctl") + " hook-stop", "MISSING:" + filepath.Join(home, "gone", "agentctl") + " hook-stop"},
		{"not executable", filepath.Join(noexec, "agentctl") + " hook-stop", "MISSING:" + filepath.Join(noexec, "agentctl") + " hook-stop"},
		{"other command untouched", "true", "true"},
		{"other abs binary untouched", "/bin/echo hi", "/bin/echo hi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeHookCommand(tt.in, home); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHealthHookDriftSemantic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	bin := filepath.Join(home, ".local", "bin")
	_ = os.MkdirAll(bin, 0o755)
	exe := filepath.Join(bin, "agentctl")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(prefix string) healthHookDriftReport {
		proj := t.TempDir()
		settings := map[string]interface{}{"hooks": hooks.BuildHooksConfig(prefix)}
		writeSettingsJSON(t, proj, settings)
		return healthCheckHookDrift(proj)
	}

	for _, prefix := range []string{"agentctl", exe, "~/.local/bin/agentctl"} {
		r := run(prefix)
		if len(r.Missing) != 0 || len(r.Mismatched) != 0 {
			t.Errorf("prefix %q reported drift: %+v", prefix, r)
		}
	}
	r := run(filepath.Join(home, "nowhere", "agentctl"))
	if len(r.Mismatched) == 0 {
		t.Error("missing binary target was not flagged as drift")
	}
}

func writeSettingsJSON(t *testing.T, proj string, settings map[string]interface{}) {
	t.Helper()
	writeSettingsHooks(t, proj, settings["hooks"].(map[string]interface{}))
}

func TestInstallHooksRefusesEphemeralBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	orig := persistedPrefixFn
	defer func() { persistedPrefixFn = orig }()
	persistedPrefixFn = hooks.PersistedPrefix // real check; test binary is in a go-build dir

	_, err := InstallHooks(true)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("err = %v, want refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".claude", "settings.json")); statErr == nil {
		t.Error("settings written despite refusal")
	}
}

// TestNormalizeHookValue_SkipZeroValues verifies that optional fields with
// zero values (timeout: 0, async: false) are omitted during comparison,
// preventing false positives when the canonical config omits these fields.
func TestNormalizeHookValue_SkipZeroValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// The canonical config never includes timeout or async for hooks that don't need them.
	// A settings file that explicitly sets them to zero/false should normalize to the same
	// structure, not be flagged as drift.
	tests := []struct {
		name     string
		val      interface{}
		expected interface{}
	}{
		{
			name: "hook with timeout: 0 normalized to missing timeout",
			val: map[string]interface{}{
				"type":    "command",
				"command": "agentctl hook-pre-tool",
				"timeout": 0,
			},
			expected: map[string]interface{}{
				"type":    "command",
				"command": "agentctl hook-pre-tool",
			},
		},
		{
			name: "hook with async: false normalized to missing async",
			val: map[string]interface{}{
				"type":    "command",
				"command": "agentctl hook-pre-tool",
				"async":   false,
			},
			expected: map[string]interface{}{
				"type":    "command",
				"command": "agentctl hook-pre-tool",
			},
		},
		{
			name: "hook with timeout: 600 preserved",
			val: map[string]interface{}{
				"type":    "command",
				"command": "agentctl hook-validate-bash",
				"timeout": 600,
			},
			expected: map[string]interface{}{
				"type":    "command",
				"command": "agentctl hook-validate-bash",
				"timeout": 600,
			},
		},
		{
			name: "hook with async: true preserved",
			val: map[string]interface{}{
				"type":    "command",
				"command": "agentctl hook-session-end",
				"async":   true,
			},
			expected: map[string]interface{}{
				"type":    "command",
				"command": "agentctl hook-session-end",
				"async":   true,
			},
		},
		{
			name: "PreToolUse array with optional fields at zero values",
			val: []interface{}{
				map[string]interface{}{
					"matcher": "*",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "agentctl hook-pre-tool",
							"timeout": 0, // Should be omitted
						},
					},
				},
				map[string]interface{}{
					"matcher": "Bash",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "agentctl hook-validate-bash",
							"timeout": 600, // Should be preserved
						},
					},
				},
			},
			expected: []interface{}{
				map[string]interface{}{
					"matcher": "*",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "agentctl hook-pre-tool",
						},
					},
				},
				map[string]interface{}{
					"matcher": "Bash",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "agentctl hook-validate-bash",
							"timeout": 600,
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeHookValue(tt.val, home)
			if !equalAnyType(got, tt.expected) {
				t.Errorf("normalizeHookValue() = %#v, want %#v", got, tt.expected)
			}
		})
	}
}

// equalAnyType reports whether two values are equal, handling interface{} types.
// It's used for testing normalizeHookValue where the output type depends on input.
func equalAnyType(a, b interface{}) bool {
	// Marshal both to JSON and compare - handles map ordering and type differences
	ma, err1 := json.Marshal(a)
	mb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ma) == string(mb)
}
