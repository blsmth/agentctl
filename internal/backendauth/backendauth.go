// Package backendauth resolves and stores the API keys for credentialed
// backends (kind "nous").
//
// The key is never placed in tmux argv or env, launch scripts, logs,
// windowstate, state.json or config. It is read on demand: by Claude Code's
// apiKeyHelper (`agentctl backend-token <name>`) and by the launch preflight
// (which only checks that a key can be resolved, with no network call).
//
// No error returned from this package contains the key.
package backendauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

// Credential sources reported by Lookup.
const (
	SourceCmd      = "api_key_cmd"
	SourceKeychain = "keychain"
	SourceFile     = "file"
)

// cmdTimeout bounds an api_key_cmd run.
var cmdTimeout = 10 * time.Second

// execCommand is the process-spawning seam; tests replace it.
var execCommand = exec.CommandContext

// ErrNotFound means no key is stored for a backend.
var ErrNotFound = errors.New("no credential stored")

// Store persists one API key per backend name.
type Store interface {
	Get(name string) (string, error)
	Set(name, key string) error
	Delete(name string) error
	// Source names where keys live ("keychain" or "file"), for status output.
	Source() string
}

// DefaultStore is the platform store. Tests replace it.
var DefaultStore Store = newPlatformStore()

var validName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func checkName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid backend name %q for credential storage", name)
	}
	return nil
}

// checkKey rejects values that cannot be a bearer token.
func checkKey(key string) error {
	if key == "" {
		return errors.New("empty key")
	}
	for _, r := range key {
		if r <= ' ' || r == 0x7f {
			return errors.New("key contains whitespace or control characters")
		}
	}
	return nil
}

// Lookup resolves the key for backend name and reports where it came from.
// api_key_cmd wins over the store. It makes no network call.
func Lookup(ctx context.Context, name string, b config.BackendConfig) (key, source string, err error) {
	if b.APIKeyCmd != "" {
		key, err = runKeyCmd(ctx, b.APIKeyCmd)
		if err != nil {
			return "", SourceCmd, fmt.Errorf("api_key_cmd for backend %q: %w", name, err)
		}
		return key, SourceCmd, nil
	}
	st := DefaultStore
	key, err = st.Get(name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", st.Source(), fmt.Errorf("no API key stored for backend %q; run `agentctl backend login %s`", name, name)
		}
		return "", st.Source(), fmt.Errorf("reading API key for backend %q: %w", name, err)
	}
	return key, st.Source(), nil
}

// Resolve returns the API key for backend name.
func Resolve(ctx context.Context, name string, b config.BackendConfig) (string, error) {
	key, _, err := Lookup(ctx, name, b)
	return key, err
}

// runKeyCmd runs cmd through sh -c and returns the first stdout line, trimmed.
func runKeyCmd(ctx context.Context, cmd string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	c := execCommand(ctx, "/bin/sh", "-c", cmd)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	c.WaitDelay = time.Second
	runErr := c.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("timed out after %s", cmdTimeout)
	}
	out := stdout.String()
	if runErr != nil {
		return "", fmt.Errorf("command failed: %v%s", runErr, redactedStderr(stderr.String(), out))
	}
	key := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	if err := checkKey(key); err != nil {
		return "", fmt.Errorf("command printed no usable key: %w", err)
	}
	return key, nil
}

// redactedStderr returns ": <first stderr line>" with every stdout line
// removed from it, so a command that echoes the key to both streams cannot
// leak it through an error.
func redactedStderr(stderr, stdout string) string {
	line := strings.TrimSpace(strings.SplitN(stderr, "\n", 2)[0])
	if line == "" {
		return ""
	}
	for _, s := range strings.Split(stdout, "\n") {
		if s = strings.TrimSpace(s); s != "" {
			line = strings.ReplaceAll(line, s, "[redacted]")
		}
	}
	if len(line) > 200 {
		line = line[:200]
	}
	return ": " + line
}

// Available reports whether a key can be obtained for backend name, without
// running api_key_cmd (it may prompt, e.g. 1Password) and without any network
// call: a configured api_key_cmd counts as available; otherwise the store must
// hold a key.
func Available(name string, b config.BackendConfig) error {
	if b.APIKeyCmd != "" {
		return nil
	}
	if _, err := DefaultStore.Get(name); err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("no API key stored for backend %q; run `agentctl backend login %s`", name, name)
		}
		return fmt.Errorf("reading API key for backend %q: %w", name, err)
	}
	return nil
}
