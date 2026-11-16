package backendauth

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

const secret = "sk-nous-SECRET-0123456789"

// fakeStore is an in-memory Store.
type fakeStore struct{ m map[string]string }

func (f *fakeStore) Get(n string) (string, error) {
	if v, ok := f.m[n]; ok {
		return v, nil
	}
	return "", ErrNotFound
}
func (f *fakeStore) Set(n, k string) error { f.m[n] = k; return nil }
func (f *fakeStore) Delete(n string) error { delete(f.m, n); return nil }
func (f *fakeStore) Source() string        { return "fake" }

func useStore(t *testing.T, m map[string]string) {
	t.Helper()
	old := DefaultStore
	DefaultStore = &fakeStore{m: m}
	t.Cleanup(func() { DefaultStore = old })
}

func TestResolveFromStore(t *testing.T) {
	useStore(t, map[string]string{"nous": secret})
	got, err := Resolve(context.Background(), "nous", config.BackendConfig{})
	if err != nil || got != secret {
		t.Fatalf("Resolve = %q, %v", got, err)
	}
	_, err = Resolve(context.Background(), "other", config.BackendConfig{})
	if err == nil || !strings.Contains(err.Error(), "agentctl backend login other") {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestResolveKeyCmd(t *testing.T) {
	useStore(t, map[string]string{"nous": "store-key"})
	tests := []struct {
		name, cmd, want string
		wantErr         bool
	}{
		{"first line trimmed", "printf '  " + secret + "  \\nsecond\\n'", secret, false},
		{"beats store", "echo cmd-key", "cmd-key", false},
		{"empty output", "true", "", true},
		{"failure", "exit 3", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(context.Background(), "nous", config.BackendConfig{APIKeyCmd: tt.cmd})
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("Resolve = %q, %v", got, err)
			}
		})
	}
}

func TestKeyCmdTimeout(t *testing.T) {
	old := cmdTimeout
	cmdTimeout = 200 * time.Millisecond
	t.Cleanup(func() { cmdTimeout = old })
	start := time.Now()
	_, err := Resolve(context.Background(), "nous", config.BackendConfig{APIKeyCmd: "sleep 30"})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timeout", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout took %s", time.Since(start))
	}
}

func TestNoKeyInErrors(t *testing.T) {
	// A failing command that prints the key to both streams.
	cmd := "echo " + secret + "; echo failed " + secret + " >&2; exit 1"
	_, err := Resolve(context.Background(), "nous", config.BackendConfig{APIKeyCmd: cmd})
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks key: %v", err)
	}

	// Verify must not echo the key or the body on failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"error":{"type":"api_error","message":"`+secret+`"}}`)
	}))
	defer srv.Close()
	err = Verify(context.Background(), config.BackendConfig{BaseURL: srv.URL, Model: "a/b"}, secret)
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "api_error") {
		t.Fatalf("Verify err = %v", err)
	}
	// Transport failure.
	srv.Close()
	err = Verify(context.Background(), config.BackendConfig{BaseURL: srv.URL, Model: "a/b"}, secret)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("Verify transport err = %v", err)
	}
}

func TestVerifyStatusMapping(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   string
	}{
		{200, `{}`, ""},
		{401, `{"error":{"type":"authentication_error"}}`, "key invalid or out of credits"},
		{404, `{"error":{"type":"not_found_error"}}`, "model not found"},
		{429, `{"error":{"type":"rate_limit_error","message":"hidden"}}`, "rate_limit_error"},
		{502, `<html>bad gateway</html>`, "unexpected HTTP 502"},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			var gotAuth, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			err := Verify(context.Background(), config.BackendConfig{BaseURL: srv.URL, Model: "a/b"}, "k")
			if gotAuth != "Bearer k" || gotPath != "/v1/messages" {
				t.Errorf("auth=%q path=%q", gotAuth, gotPath)
			}
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "hidden") || strings.Contains(err.Error(), "bad gateway") {
				t.Errorf("body echoed: %v", err)
			}
		})
	}
}

// --- file store ---

func TestFileStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	s := &fileStore{dir: dir}
	if _, err := s.Get("nous"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get before Set = %v", err)
	}
	if err := s.Set("nous", secret); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", fi.Mode().Perm())
	}
	p := filepath.Join(dir, "nous")
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v", fi.Mode().Perm())
	}
	if got, err := s.Get("nous"); err != nil || got != secret {
		t.Fatalf("Get = %q, %v", got, err)
	}

	// Broader than 0600 is refused, without leaking the key.
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Get("nous")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("0644 read err = %v", err)
	}

	if err := s.Delete("nous"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("nous"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete = %v", err)
	}
}

func TestStoreRejectsBadInput(t *testing.T) {
	s := &fileStore{dir: t.TempDir()}
	for _, name := range []string{"", "../x", "a/b", "a b"} {
		if err := s.Set(name, "k"); err == nil {
			t.Errorf("Set(%q) accepted", name)
		}
	}
	for _, key := range []string{"", "a b", "a\nb"} {
		if err := s.Set("ok", key); err == nil {
			t.Errorf("Set key %q accepted", key)
		}
	}
}

// --- keychain store via a fake exec ---

// TestHelperProcess is re-executed as the fake `security` binary.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("BACKENDAUTH_HELPER") != "1" {
		return
	}
	rec := os.Getenv("BACKENDAUTH_RECORD")
	in, _ := io.ReadAll(os.Stdin)
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	_ = os.WriteFile(rec, []byte(strings.Join(args, "\x00")+"\x01"+string(in)), 0o600)
	if len(args) > 0 && args[0] == "find-generic-password" {
		if os.Getenv("BACKENDAUTH_MISSING") == "1" {
			os.Exit(44)
		}
		_, _ = io.WriteString(os.Stdout, secret+"\n")
	}
	os.Exit(0)
}

func fakeSecurity(t *testing.T, missing bool) (record string) {
	t.Helper()
	record = filepath.Join(t.TempDir(), "rec")
	old := execCommand
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		a := append([]string{"-test.run=TestHelperProcess", "--"}, args...)
		c := exec.CommandContext(ctx, os.Args[0], a...)
		env := append(os.Environ(), "BACKENDAUTH_HELPER=1", "BACKENDAUTH_RECORD="+record)
		if missing {
			env = append(env, "BACKENDAUTH_MISSING=1")
		}
		c.Env = env
		if name != securityBin {
			t.Errorf("exec'd %q, want %q", name, securityBin)
		}
		return c
	}
	t.Cleanup(func() { execCommand = old })
	return record
}

func TestKeychainSetKeepsKeyOutOfArgv(t *testing.T) {
	rec := fakeSecurity(t, false)
	if err := (&keychainStore{}).Set("nous", secret); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	argv, stdin, _ := strings.Cut(string(data), "\x01")
	if argv != "-i" {
		t.Errorf("argv = %q, want only -i", argv)
	}
	if strings.Contains(argv, secret) || strings.Contains(argv, hex.EncodeToString([]byte(secret))) {
		t.Error("key in argv")
	}
	for _, want := range []string{"add-generic-password", "-s agentctl", "-a backend:nous", hex.EncodeToString([]byte(secret))} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin %q missing %q", stdin, want)
		}
	}
}

func TestKeychainGetAndNotFound(t *testing.T) {
	rec := fakeSecurity(t, false)
	got, err := (&keychainStore{}).Get("nous")
	if err != nil || got != secret {
		t.Fatalf("Get = %q, %v", got, err)
	}
	data, _ := os.ReadFile(rec)
	if want := "find-generic-password\x00-s\x00agentctl\x00-a\x00backend:nous\x00-w"; !strings.HasPrefix(string(data), want) {
		t.Errorf("argv = %q", data)
	}

	fakeSecurity(t, true)
	if _, err := (&keychainStore{}).Get("nous"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Get = %v", err)
	}
}
