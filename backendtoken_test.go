package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tokenConfig has a valid nous backend whose key comes from api_key_cmd, an
// invalid backend that makes config loading emit warnings, and a nous backend
// with no credential at all.
const tokenConfig = `{"backends":{
 "nous":{"kind":"nous","model":"qwen/qwen3-coder-next","api_key_cmd":"echo sk-test-key-123"},
 "broken":{"kind":"nous","model":"x/y","auth":"oauth"},
 "bad-ollama":{"base_url":"nope","model":"m"},
 "nokey":{"kind":"nous","model":"qwen/qwen3-coder-next","api_key_cmd":"exit 1"},
 "local":{"base_url":"http://h:11434","model":"m"}
}}`

func writeTokenConfig(t *testing.T) {
	t.Helper()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "agentctl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "agentctl", "config.json"), []byte(tokenConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", t.TempDir())
}

func TestRunBackendToken(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"success is exactly key+newline", []string{"nous"}, 0, "sk-test-key-123\n", ""},
		{"unknown backend", []string{"ghost"}, 1, "", "ghost"},
		{"invalid backend", []string{"broken"}, 1, "", "broken"},
		{"credential failure", []string{"nokey"}, 1, "", "api_key_cmd"},
		{"ollama backend", []string{"local"}, 1, "", "no stored credential"},
		{"no args", nil, 1, "", "usage"},
		{"too many args", []string{"a", "b"}, 1, "", "usage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeTokenConfig(t)
			var out, errb bytes.Buffer
			code := runBackendToken(tt.args, &out, &errb)
			if code != tt.wantCode {
				t.Errorf("exit = %d, want %d (stderr %q)", code, tt.wantCode, errb.String())
			}
			if out.String() != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", out.String(), tt.wantStdout)
			}
			if !strings.Contains(errb.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want containing %q", errb.String(), tt.wantStderr)
			}
			if strings.Contains(errb.String(), "sk-test-key-123") {
				t.Errorf("stderr leaks the key: %q", errb.String())
			}
		})
	}
}

// TestBackendTokenBinaryFastPath runs the real binary: stdout must be byte
// exact despite the invalid backends in the config, and the fast path must not
// create a runtime dir in the cwd.
func TestBackendTokenBinaryFastPath(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "agentctl")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	writeTokenConfig(t)
	cwd := t.TempDir()

	c := exec.Command(bin, "backend-token", "nous")
	c.Dir = cwd
	c.Env = os.Environ()
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		t.Fatalf("run: %v (stderr %q)", err, errb.String())
	}
	if out.String() != "sk-test-key-123\n" {
		t.Errorf("stdout = %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(cwd, ".agentctl")); err == nil {
		t.Error("backend-token created .agentctl in the cwd")
	}

	c = exec.Command(bin, "backend-token", "nokey")
	c.Dir = cwd
	c.Env = os.Environ()
	out.Reset()
	errb.Reset()
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Errorf("exit err = %v, want exit status 1", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout on failure = %q, want empty", out.String())
	}
}
