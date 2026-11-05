package cmd

import (
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

func TestPluginInstalledDetection(t *testing.T) {
	home := t.TempDir()
	if pluginInstalled(home) {
		t.Error("empty home reported plugin installed")
	}
	cmds := filepath.Join(home, ".claude", "commands", "agentctl")
	_ = os.MkdirAll(cmds, 0o755)
	_ = os.WriteFile(filepath.Join(cmds, "start.md"), []byte("x"), 0o644)
	if !pluginInstalled(home) {
		t.Error("install-plugin commands not detected")
	}

	home2 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home2, ".claude"), 0o755)
	_ = os.WriteFile(filepath.Join(home2, ".claude", "settings.json"), []byte(`{"enabledPlugins":{"agentctl@blsmth":true}}`), 0o644)
	if !pluginInstalled(home2) {
		t.Error("enabledPlugins entry not detected")
	}
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

func TestWarnIfPluginInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	cmds := filepath.Join(home, ".claude", "commands", "agentctl")
	_ = os.MkdirAll(cmds, 0o755)
	_ = os.WriteFile(filepath.Join(cmds, "start.md"), []byte("x"), 0o644)
	var sb strings.Builder
	warnIfPluginInstalled(&sb)
	if !strings.Contains(sb.String(), "fires twice") {
		t.Errorf("no double-fire warning: %q", sb.String())
	}
}
