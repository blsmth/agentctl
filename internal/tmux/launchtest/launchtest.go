// Package launchtest captures what tmux.LaunchAgent would run, without a tmux
// server, so launch-path tests can pin the exact command line and window
// environment each launch path produces (golden tests). Tests only.
package launchtest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// Launch is one captured tmux new-window call.
type Launch struct {
	// Argv is the new-window argv without the -e pairs and the trailing
	// shell invocation.
	Argv []string
	// Env holds the -e KEY=VALUE pairs.
	Env map[string]string
	// Command is the launch script's command block (the lines run inside its
	// subshell: an optional unset line, then the agent command).
	Command string
}

// Unset returns the variables the launch script unsets before the agent
// command (its leading `unset` line), nil when there is none.
func (l Launch) Unset() []string {
	first, _, _ := strings.Cut(l.Command, "\n")
	if !strings.HasPrefix(first, "unset ") {
		return nil
	}
	return strings.Fields(first)[1:]
}

// ChildEnv returns the environment the agent command would start with when
// tmux seeds the new window from inherited (the tmux global and session
// environments): inherited, overlaid by the -e values, minus the variables
// the launch script unsets.
func (l Launch) ChildEnv(inherited map[string]string) map[string]string {
	env := make(map[string]string, len(inherited)+len(l.Env))
	for k, v := range inherited {
		env[k] = v
	}
	for k, v := range l.Env {
		env[k] = v
	}
	for _, k := range l.Unset() {
		delete(env, k)
	}
	return env
}

// AgentCommand returns the command block without its leading `unset` line:
// the agent command itself.
func (l Launch) AgentCommand() string {
	if first, rest, ok := strings.Cut(l.Command, "\n"); ok && strings.HasPrefix(first, "unset ") {
		return rest
	}
	return l.Command
}

// Recorder collects the launches made while it is installed.
type Recorder struct {
	mu       sync.Mutex
	launches []Launch
	calls    [][]string
	t        *testing.T
}

// Capture installs a fake tmux runner for tmux.LaunchAgent and points
// tmux.SessionName at a session that does not exist, so instance numbering
// never sees a real server. Every new-window returns window ID "@9001".
func Capture(t *testing.T) *Recorder {
	t.Helper()
	r := &Recorder{t: t}
	prevSession := tmux.SessionName
	tmux.SessionName = "agentctl-launchtest"
	restore := tmux.SetLaunchTmuxForTest(func(args ...string) ([]byte, error) {
		r.mu.Lock()
		r.calls = append(r.calls, append([]string(nil), args...))
		r.mu.Unlock()
		if len(args) > 0 && args[0] == "new-window" {
			r.record(args)
			return []byte("@9001\n"), nil
		}
		return nil, nil
	})
	t.Cleanup(func() {
		restore()
		tmux.SessionName = prevSession
	})
	return r
}

// Launches returns the launches recorded so far.
func (r *Recorder) Launches() []Launch {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Launch(nil), r.launches...)
}

// Calls returns every tmux argv the launch runner was given, new-window
// included, in order.
func (r *Recorder) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.calls...)
}

// Only returns the single recorded launch, failing the test otherwise.
func (r *Recorder) Only() Launch {
	r.t.Helper()
	ls := r.Launches()
	if len(ls) != 1 {
		r.t.Fatalf("recorded %d launches, want 1", len(ls))
	}
	return ls[0]
}

func (r *Recorder) record(args []string) {
	l := Launch{Env: map[string]string{}}
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-e" && i+1 < len(args) {
			k, v, _ := strings.Cut(args[i+1], "=")
			l.Env[k] = v
			i += 2
			continue
		}
		if a == "#{window_id}" {
			l.Argv = append(l.Argv, a)
			i++
			break
		}
		l.Argv = append(l.Argv, a)
		i++
	}
	// The rest is the shell invocation: <shell> -l -c "exec /bin/sh '<script>'".
	if rest := args[i:]; len(rest) > 0 {
		l.Command = scriptCommand(rest[len(rest)-1])
	}
	r.mu.Lock()
	r.launches = append(r.launches, l)
	r.mu.Unlock()
}

// scriptCommand reads the launch script named in the shell argument and
// returns its command block.
func scriptCommand(shellArg string) string {
	path := strings.TrimPrefix(shellArg, "exec /bin/sh ")
	path = strings.Trim(path, "'")
	data, err := os.ReadFile(path)
	if err != nil {
		return "<unreadable script: " + err.Error() + ">"
	}
	s := string(data)
	const open = "exit_code=$( { { (\n"
	const closing = "\n) 2>&1 1>&4"
	start := strings.Index(s, open)
	end := strings.Index(s, closing)
	if start < 0 || end < start {
		return s
	}
	return s[start+len(open) : end]
}

var uuidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// tempSuffixRe matches the random part os.CreateTemp adds to prompt files.
var tempSuffixRe = regexp.MustCompile(`-[0-9]{5,}\.txt`)

// Normalize replaces the dynamic parts of s: each (old, new) pair in repl
// (applied in order, longest first is the caller's job), UUIDs and temp file
// suffixes.
func Normalize(s string, repl ...string) string {
	for i := 0; i+1 < len(repl); i += 2 {
		if repl[i] == "" {
			continue
		}
		s = strings.ReplaceAll(s, repl[i], repl[i+1])
	}
	s = uuidRe.ReplaceAllString(s, "<UUID>")
	return tempSuffixRe.ReplaceAllString(s, "-<RAND>.txt")
}

// Render returns a stable text form of l: argv, then the sorted env, then the
// command block.
func (l Launch) Render() string {
	var b strings.Builder
	b.WriteString("argv:\n")
	for _, a := range l.Argv {
		b.WriteString("  " + a + "\n")
	}
	b.WriteString("env:\n")
	keys := make([]string, 0, len(l.Env))
	for k := range l.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("  " + k + "=" + l.Env[k] + "\n")
	}
	b.WriteString("command:\n")
	for _, line := range strings.Split(l.Command, "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// CheckGolden compares got with the golden file at path. With
// AGENTCTL_UPDATE_GOLDEN=1 it rewrites the file instead.
func CheckGolden(t *testing.T, path, got string) {
	t.Helper()
	if os.Getenv("AGENTCTL_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s (run with AGENTCTL_UPDATE_GOLDEN=1 to create): %v", path, err)
	}
	if string(want) != got {
		t.Errorf("launch differs from golden %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// Env is an isolated launch environment for golden tests.
type Env struct {
	Home    string
	Project string
	t       *testing.T
}

// Setup isolates HOME, XDG dirs, tmux and session env vars, initialises the
// runtime dir under a fresh project dir, writes cfgJSON as the (global and
// project) config, points windowstate and agent discovery at the temp dirs,
// and writes agentFiles (name -> .md content) into $HOME/.claude/agents.
func Setup(t *testing.T, cfgJSON string, agentFiles map[string]string) *Env {
	t.Helper()
	home := t.TempDir()
	for k, v := range map[string]string{
		"HOME":                   home,
		"XDG_CONFIG_HOME":        filepath.Join(home, ".config"),
		"XDG_DATA_HOME":          "",
		"XDG_STATE_HOME":         "",
		"XDG_CACHE_HOME":         filepath.Join(home, ".cache"),
		"AGENTCTL_RUNTIME_DIR":   "",
		"AGENTCTL_INSTANCE":      "",
		"AGENTCTL_SESSION_ID":    "",
		"AGENTCTL_WINDOW_ID":     "",
		"AGENTCTL_ACCOUNT":       "",
		"CLAUDE_CODE_SESSION_ID": "",
		"CLAUDE_CONFIG_DIR":      "",
		"TMUX":                   "",
		"TMUX_PANE":              "",
		"SHELL":                  "/bin/zsh",
	} {
		t.Setenv(k, v)
	}
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".agentctl"), 0o700); err != nil {
		t.Fatal(err)
	}
	paths.ResetForTest()
	paths.ResetDataDirForTest()
	paths.Init(project)
	t.Cleanup(paths.ResetForTest)
	t.Cleanup(paths.ResetDataDirForTest)

	cfgPath := filepath.Join(home, ".config", "agentctl", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	config.SetConfigPathForTest(cfgPath)

	windowstate.SetDirForTest(filepath.Join(project, ".agentctl", "windows"))
	t.Cleanup(windowstate.Init)

	agentsDir := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range agentFiles {
		if err := os.WriteFile(filepath.Join(agentsDir, name+".md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prevReg, prevShared := agent.RegistryDir, agent.SharedDir
	agent.RegistryDir = agentsDir
	agent.SharedDir = filepath.Join(home, ".claude", "shared")
	t.Cleanup(func() { agent.RegistryDir, agent.SharedDir = prevReg, prevShared })
	return &Env{Home: home, Project: project, t: t}
}

// Normalize replaces the env's temp dirs (and their symlink-resolved forms,
// and the Claude project slugs derived from them) plus extra (old, new)
// pairs, then UUIDs and temp suffixes.
func (e *Env) Normalize(s string, extra ...string) string {
	var repl []string
	repl = append(repl, extra...)
	for _, p := range []struct{ dir, name string }{{e.Project, "$PROJECT"}, {e.Home, "$HOME"}} {
		dirs := []string{p.dir}
		if real, err := filepath.EvalSymlinks(p.dir); err == nil && real != p.dir {
			dirs = append([]string{real}, dirs...)
		}
		for _, d := range dirs {
			repl = append(repl, claude.ProjectSlug(d), "<SLUG:"+p.name+">", d, p.name)
		}
	}
	return Normalize(s, repl...)
}

// WindowRecord renders the windowstate fields a launch writes for windowID.
func WindowRecord(windowID string) string {
	ws, err := windowstate.Read(windowID)
	if err != nil {
		return "windowstate: " + err.Error() + "\n"
	}
	return fmt.Sprintf("windowstate:\n  instance=%s\n  agent=%s\n  mode=%s\n  log_path=%s\n  session_name=%s\n  backend=%s\n  account=%s\n",
		ws.Instance, ws.AgentName, ws.Mode, ws.LogPath, ws.SessionName, ws.Backend, ws.Account)
}

// File renders a file's content for a golden ("<missing>" when absent). A
// JSON object's "hooks" key is replaced by its sha256, since it is long and
// identical across launches; any change to it still changes the golden.
func File(label, path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return label + ": <missing>\n"
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) == nil {
		if h, ok := obj["hooks"]; ok {
			sum := sha256.Sum256(h)
			obj["hooks"] = json.RawMessage(strconv.Quote("sha256:" + hex.EncodeToString(sum[:])))
			if out, err := json.MarshalIndent(obj, "", "  "); err == nil {
				data = out
			}
		}
	}
	return label + ":\n" + string(data) + "\n"
}

// BinPair returns the Normalize pair that hides the launch's AGENTCTL_BIN
// value (the test binary, a PATH name or a $HOME fallback, depending on the
// machine).
func BinPair(l Launch) []string {
	return []string{"AGENTCTL_BIN=" + l.Env["AGENTCTL_BIN"] + "\n", "AGENTCTL_BIN=<BIN>\n"}
}

// WriteConfig replaces the config written by Setup (for configs that need
// the env's temp paths).
func WriteConfig(t *testing.T, cfgJSON string) {
	t.Helper()
	path := config.FilePath()
	if err := os.WriteFile(path, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
}
