// Package testenv isolates a test binary from the machine it runs on.
//
// Agents run `go test` from inside agentctl panes, which export
// CLAUDE_CONFIG_DIR, TMUX, TMUX_PANE and many AGENTCTL_* variables. Without
// isolation, tests that resolve the runtime dir, a Claude config dir or the
// tmux server would read and write live state. Every package with tests calls
// Main (or Setup) from its TestMain; a guard test in this package enforces it.
//
// The scrub happens in-process, before m.Run, so package state that is
// initialized lazily (such as the once in internal/paths) sees the isolated
// environment. Package-level variables computed from the environment at init
// time run before TestMain and are not covered: compute them lazily instead.
package testenv

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// EnvRoot names the variable that marks an already-isolated environment. It
// holds the temp root and is inherited by subprocesses (helper re-execs of the
// test binary, the built agentctl binary), so a child test binary keeps the
// parent's environment, including anything a test set on purpose.
const EnvRoot = "AGENTCTL_TESTENV_ROOT"

// unsetPrefixes are removed from the environment, apart from keepVars.
var unsetPrefixes = []string{
	"AGENTCTL_",        // pane identity, runtime dir, account, bin, launch log, ...
	"CLAUDE",           // CLAUDE_CONFIG_DIR, CLAUDECODE, CLAUDE_CODE_* (session, messaging, OAuth token)
	"ANTHROPIC_",       // credentials and base URL
	"PI_CODING_AGENT_", // pi's config and session dirs
}

// unsetVars are removed exactly.
var unsetVars = []string{"AGENTCTL", "TMUX", "TMUX_PANE"}

// keepVars are AGENTCTL_* knobs that opt tests in or change test behavior;
// they are never set by a pane, so they survive the scrub.
var keepVars = []string{
	EnvRoot,
	"AGENTCTL_DEBUG",
	"AGENTCTL_TEST",
	"AGENTCTL_TMUX_SMOKE",
	"AGENTCTL_UPDATE_GOLDEN",
}

// keepPrefixes are knob families that survive the scrub.
var keepPrefixes = []string{"AGENTCTL_TEST_", "AGENTCTL_SPIKE_"}

// original holds the environment as it was before Setup, for opt-in tests
// that deliberately reach outside the sandbox (see Original).
var original map[string]string

// Main isolates the environment, runs the tests, removes the temp root and
// exits. Use it as the whole body of a package's TestMain.
func Main(m *testing.M) {
	cleanup := Setup()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// Setup isolates the environment for the whole test binary and returns a
// func that removes the temp dirs it created. Call it first in a TestMain that
// does more than Main. It panics if the environment cannot be isolated, since
// running on would touch live state.
//
// In a subprocess of an isolated test binary (EnvRoot set and valid) it
// changes nothing and its cleanup is a no-op: the parent owns the root.
func Setup() (cleanup func()) {
	original = envMap(os.Environ())
	if inherited(os.Getenv(EnvRoot)) {
		return func() {}
	}
	root, err := os.MkdirTemp("", "actest")
	if err != nil {
		panic(fmt.Sprintf("testenv: creating temp root: %v", err))
	}
	if err := apply(root); err != nil {
		_ = os.RemoveAll(root)
		panic(fmt.Sprintf("testenv: %v", err))
	}
	return func() {
		killSandboxTmux(root)
		removeAll(root)
	}
}

// Original returns a variable's value from before Setup scrubbed it. Only
// opt-in tests that must reach a resource outside the sandbox (a private tmux
// server the developer started by hand, say) should use it.
func Original(key string) (string, bool) {
	v, ok := original[key]
	return v, ok
}

// markerFile sits in every isolation root, so a stale or forged EnvRoot that
// does not point at one is ignored.
const markerFile = ".agentctl-testenv"

// inherited reports whether root is a live isolation root, i.e. a parent test
// binary already isolated this environment. HOME is deliberately not checked:
// a test may point HOME elsewhere before re-executing the binary.
func inherited(root string) bool {
	if root == "" || !filepath.IsAbs(root) {
		return false
	}
	_, err := os.Stat(filepath.Join(root, markerFile))
	return err == nil
}

// apply creates the sandbox dirs under root and rewrites the environment.
func apply(root string) error {
	if err := os.WriteFile(filepath.Join(root, markerFile), nil, 0o600); err != nil {
		return fmt.Errorf("writing marker: %w", err)
	}
	set := sandboxVars(root)
	for k, v := range goToolchainVars() {
		set[k] = v
	}
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "TMUX_TMPDIR"} {
		if err := os.MkdirAll(set[k], 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", k, err)
		}
	}
	// git commits in tests need an identity; the real ~/.gitconfig is gone.
	gitconfig := "[user]\n\tname = agentctl test\n\temail = test@agentctl.invalid\n"
	if err := os.WriteFile(filepath.Join(set["HOME"], ".gitconfig"), []byte(gitconfig), 0o600); err != nil {
		return fmt.Errorf("writing .gitconfig: %w", err)
	}
	for _, k := range scrubbed(os.Environ()) {
		if err := os.Unsetenv(k); err != nil {
			return fmt.Errorf("unsetting %s: %w", k, err)
		}
	}
	for k, v := range set {
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("setting %s: %w", k, err)
		}
	}
	return nil
}

// sandboxVars returns the variables pointed into root. TMUX_TMPDIR moves the
// default tmux socket, so a bare `tmux` cannot reach the live server; its
// path is short because macOS caps socket paths near 104 bytes.
func sandboxVars(root string) map[string]string {
	home := filepath.Join(root, "home")
	return map[string]string{
		EnvRoot:           root,
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME":  filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"TMUX_TMPDIR":     filepath.Join(root, "t"),
	}
}

// scrubbed returns the names in environ that Setup unsets.
func scrubbed(environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if shouldUnset(k) {
			out = append(out, k)
		}
	}
	return out
}

func shouldUnset(k string) bool {
	if hasAny(k, keepPrefixes) {
		return false
	}
	for _, v := range keepVars {
		if k == v {
			return false
		}
	}
	for _, v := range unsetVars {
		if k == v {
			return true
		}
	}
	return hasAny(k, unsetPrefixes)
}

func hasAny(k string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

func envMap(environ []string) map[string]string {
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// goVars are the Go toolchain locations that default to paths under HOME.
// Tests that run `go build` must keep the real ones: with HOME moved, GOPATH
// would default into the sandbox and every test binary would download the
// whole module cache again (read-only files that then resist cleanup).
var goVars = []string{"GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"}

// goToolchainVars resolves goVars that are not already set, as the go
// command sees them under the original HOME. It must run before HOME moves.
func goToolchainVars() map[string]string {
	var missing []string
	for _, k := range goVars {
		if os.Getenv(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	out, err := exec.Command("go", append([]string{"env", "-json"}, missing...)...).Output()
	got := map[string]string{}
	if err == nil {
		_ = json.Unmarshal(out, &got)
	}
	defaults := goDefaults()
	set := map[string]string{}
	for _, k := range missing {
		if v := got[k]; v != "" {
			set[k] = v
		} else if v := defaults[k]; v != "" {
			set[k] = v
		}
	}
	return set
}

// goDefaults mirrors the go command's own defaults, for when `go env` cannot
// run. GOENV settings are then not applied, but nothing lands in the sandbox.
func goDefaults() map[string]string {
	d := map[string]string{}
	if home, err := os.UserHomeDir(); err == nil {
		gopath := os.Getenv("GOPATH")
		if gopath == "" {
			gopath = filepath.Join(home, "go")
		}
		d["GOPATH"] = gopath
		d["GOMODCACHE"] = filepath.Join(gopath, "pkg", "mod")
	}
	if dir, err := os.UserCacheDir(); err == nil {
		d["GOCACHE"] = filepath.Join(dir, "go-build")
	}
	if dir, err := os.UserConfigDir(); err == nil {
		d["GOENV"] = filepath.Join(dir, "go", "env")
	}
	return d
}

// removeAll deletes root, first making directories writable when a plain
// RemoveAll fails (a module cache a test populated is read-only).
func removeAll(root string) {
	if os.RemoveAll(root) == nil {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(root)
}

// killSandboxTmux stops any tmux server a test started on a socket in the
// sandbox (<root>/t/tmux-<uid>/*: the default one and any -L <name> ones), so
// it cannot outlive the test binary as an orphan. It only ever addresses
// sockets under root, never the developer's real TMUX_TMPDIR; errors are
// ignored.
func killSandboxTmux(root string) {
	for _, sock := range sandboxSockets(root) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, "tmux", "-S", sock, "kill-server")
		cmd.Env = append(os.Environ(), "TMUX_TMPDIR="+filepath.Join(root, "t"))
		_ = cmd.Run()
		cancel()
	}
}

// sandboxSockets lists the tmux sockets under the sandbox's TMUX_TMPDIR.
func sandboxSockets(root string) []string {
	dir := filepath.Join(root, "t", fmt.Sprintf("tmux-%d", os.Getuid()))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Type()&os.ModeSocket != 0 {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// LeakedTmux reports whether a tmux server is still running on any socket in
// this test binary's sandbox. Call it from TestMain after m.Run and before the
// cleanup: a unit test that reaches the real tmux exec path leaves one behind.
// A child of an isolated binary inherits EnvRoot and TMUX_TMPDIR, so it checks
// the same sockets as its parent. It is false outside an isolated environment
// and when the sandbox has no live server.
func LeakedTmux() bool {
	root := os.Getenv(EnvRoot)
	if !inherited(root) || os.Getenv("TMUX_TMPDIR") != filepath.Join(root, "t") {
		return false
	}
	for _, sock := range sandboxSockets(root) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := exec.CommandContext(ctx, "tmux", "-S", sock, "list-sessions").Run()
		cancel()
		if err == nil {
			return true
		}
	}
	return false
}
