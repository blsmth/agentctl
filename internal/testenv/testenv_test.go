package testenv

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	Main(m)
}

func TestShouldUnset(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"CLAUDE_CONFIG_DIR", true},
		{"CLAUDECODE", true},
		{"CLAUDE_CODE_OAUTH_TOKEN", true},
		{"CLAUDE_CODE_SESSION_ID", true},
		{"ANTHROPIC_API_KEY", true},
		{"ANTHROPIC_AUTH_TOKEN", true},
		{"TMUX", true},
		{"TMUX_PANE", true},
		{"AGENTCTL", true},
		{"AGENTCTL_RUNTIME_DIR", true},
		{"AGENTCTL_WINDOW_ID", true},
		{"AGENTCTL_ACCOUNT", true},
		{"AGENTCTL_BIN", true},
		{"AGENTCTL_LAUNCH_LOG", true},
		{"AGENTCTL_HARNESS", true},
		{"PI_CODING_AGENT_DIR", true},
		{EnvRoot, false},
		{"AGENTCTL_TEST", false},
		{"AGENTCTL_TEST_TMUX", false},
		{"AGENTCTL_UPDATE_GOLDEN", false},
		{"AGENTCTL_TMUX_SMOKE", false},
		{"AGENTCTL_SPIKE_TMUX_SOCKET", false},
		{"TMUX_TMPDIR", false}, // overwritten, not unset
		{"PATH", false},
		{"GOFLAGS", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := shouldUnset(tt.key); got != tt.want {
				t.Errorf("shouldUnset(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestScrubbed(t *testing.T) {
	env := []string{"PATH=/bin", "TMUX=/tmp/x,1,0", "AGENTCTL_RUNTIME_DIR=/live", "AGENTCTL_TEST_TMUX=1", "EMPTY="}
	got := scrubbed(env)
	want := []string{"TMUX", "AGENTCTL_RUNTIME_DIR"}
	if !slices.Equal(got, want) {
		t.Errorf("scrubbed = %v, want %v", got, want)
	}
}

func TestSandboxVarsUnderRoot(t *testing.T) {
	root := t.TempDir()
	for k, v := range sandboxVars(root) {
		if !strings.HasPrefix(v, root) {
			t.Errorf("%s = %q, not under root %q", k, v, root)
		}
	}
}

func TestInherited(t *testing.T) {
	root := t.TempDir()
	if inherited(root) {
		t.Error("root without marker counted as inherited")
	}
	if err := os.WriteFile(filepath.Join(root, markerFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !inherited(root) {
		t.Error("root with marker not counted as inherited")
	}
	if inherited("") || inherited("relative/dir") {
		t.Error("empty or relative root counted as inherited")
	}
}

// TestSetupIsolated checks the live environment of this binary, which Main
// already isolated.
func TestSetupIsolated(t *testing.T) {
	root := os.Getenv(EnvRoot)
	if !inherited(root) {
		t.Fatalf("%s = %q is not an isolation root", EnvRoot, root)
	}
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "TMUX_TMPDIR"} {
		v := os.Getenv(k)
		if !strings.HasPrefix(v, root) {
			t.Errorf("%s = %q, want under %q", k, v, root)
		}
		if st, err := os.Stat(v); err != nil || !st.IsDir() {
			t.Errorf("%s = %q is not a directory: %v", k, v, err)
		}
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if shouldUnset(k) {
			t.Errorf("%s survived the scrub", k)
		}
	}
}

// TestSetupInheritedIsNoop checks that a subprocess of an isolated binary
// keeps the environment it was given, including deliberate test settings.
func TestSetupInheritedIsNoop(t *testing.T) {
	saved := original
	t.Cleanup(func() { original = saved })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTCTL_WINDOW_ID", "@7")
	cleanup := Setup()
	cleanup()
	if got := os.Getenv("HOME"); got != home {
		t.Errorf("HOME = %q, want %q", got, home)
	}
	if got := os.Getenv("AGENTCTL_WINDOW_ID"); got != "@7" {
		t.Errorf("AGENTCTL_WINDOW_ID = %q, want @7", got)
	}
	if _, err := os.Stat(os.Getenv(EnvRoot)); err != nil {
		t.Errorf("inherited cleanup removed the root: %v", err)
	}
}

func TestGoToolchainVarsKeptOutOfSandbox(t *testing.T) {
	root := os.Getenv(EnvRoot)
	for _, k := range goVars {
		if v := os.Getenv(k); v == "" || strings.HasPrefix(v, root) {
			t.Errorf("%s = %q, want the real (non-sandbox) value", k, v)
		}
	}
}

func TestRemoveAllReadOnly(t *testing.T) {
	root, err := os.MkdirTemp("", "actest-rm")
	if err != nil {
		t.Fatal(err)
	}
	ro := filepath.Join(root, "mod", "pkg")
	if err := os.MkdirAll(ro, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ro, "f"), nil, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	removeAll(root)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("root survived removeAll: %v", err)
		_ = os.Chmod(ro, 0o700)
		_ = os.RemoveAll(root)
	}
}

func TestGoDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOPATH", "")
	d := goDefaults()
	if got, want := d["GOPATH"], filepath.Join(home, "go"); got != want {
		t.Errorf("GOPATH = %q, want %q", got, want)
	}
	if got, want := d["GOMODCACHE"], filepath.Join(home, "go", "pkg", "mod"); got != want {
		t.Errorf("GOMODCACHE = %q, want %q", got, want)
	}
	for _, k := range []string{"GOCACHE", "GOENV"} {
		if d[k] == "" {
			t.Errorf("%s default is empty", k)
		}
	}
}
