package hooks

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/selfpath"
)

var update = flag.Bool("update", false, "regenerate plugin/hooks/hooks.json from BuildHooksConfig")

// pluginHooksPath is the plugin's hook manifest, relative to this package.
const pluginHooksPath = "../../plugin/hooks/hooks.json"

// renderPluginHooksJSON renders the canonical hook set in the plugin's
// hooks.json shape. Plugin hooks run via PATH, so the prefix is bare
// "agentctl".
func renderPluginHooksJSON(t *testing.T) []byte {
	t.Helper()
	out, err := json.MarshalIndent(map[string]interface{}{
		"hooks": BuildHooksConfig("agentctl"),
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append(out, '\n')
}

// TestPluginHooksJSONMatches fails if plugin/hooks/hooks.json drifts from
// BuildHooksConfig. Regenerate with `go generate ./internal/hooks` (or
// `go test ./internal/hooks -run TestPluginHooksJSONMatches -update`).
func TestPluginHooksJSONMatches(t *testing.T) {
	want := renderPluginHooksJSON(t)
	if *update {
		if err := os.WriteFile(pluginHooksPath, want, 0o644); err != nil {
			t.Fatalf("writing %s: %v", pluginHooksPath, err)
		}
		return
	}
	got, err := os.ReadFile(pluginHooksPath)
	if err != nil {
		t.Fatalf("reading %s: %v", pluginHooksPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is out of date with BuildHooksConfig; run `go generate ./internal/hooks`", filepath.Clean(pluginHooksPath))
	}
}

func TestPluginHooksIncludeStopCheckDone(t *testing.T) {
	if !bytes.Contains(renderPluginHooksJSON(t), []byte(`"agentctl hook-check-done"`)) {
		t.Error("rendered plugin hooks lack Stop -> hook-check-done")
	}
}

func TestHooksConfigUsesCommandPrefix(t *testing.T) {
	want := commandPrefix() + " hook-session-start"
	b, err := json.Marshal(HooksConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(want)) {
		t.Errorf("HooksConfig does not use prefix %q", want)
	}
	if bytes.Contains(b, []byte("~/.local/bin")) {
		t.Error("HooksConfig still hardcodes ~/.local/bin")
	}
}

func TestAgentctlBinaryPathIsRunningBinary(t *testing.T) {
	if got := agentctlBinaryPath(); got != selfpath.Path() {
		t.Errorf("agentctlBinaryPath() = %q, want %q", got, selfpath.Path())
	}
}
