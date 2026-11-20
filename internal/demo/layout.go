package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultLabel is the socket name of the demo server for the default root
// (`tmux -L agentctl-demo`).
const DefaultLabel = "agentctl-demo"

// ownerVar is the tmux global environment variable that records which root a
// demo server belongs to. It deliberately avoids the AGENTCTL_ prefix: the
// server's global environment is inherited by every pane.
const ownerVar = "DEMO_ROOT_OWNER"

// Layout is the demo's temp root and everything under it. Nothing the demo
// writes lives outside it, apart from the tmux socket.
type Layout struct {
	Root  string
	label string // pinned by WithLabel; empty means derive from Root
}

// NewLayout validates root and returns its layout. The root must be an
// absolute path whose last element mentions "agentctl-demo", so a stray flag
// can never point `down` (which deletes the root) at a real directory.
func NewLayout(root string) (Layout, error) {
	if root == "" || !filepath.IsAbs(root) {
		return Layout{}, fmt.Errorf("demo root %q must be an absolute path", root)
	}
	root = filepath.Clean(root)
	if !strings.Contains(filepath.Base(root), "agentctl-demo") {
		return Layout{}, fmt.Errorf("demo root %q: last path element must contain \"agentctl-demo\"", root)
	}
	return Layout{Root: root}, nil
}

// DefaultRoot is $TMPDIR/agentctl-demo.
func DefaultRoot() string { return filepath.Join(os.TempDir(), "agentctl-demo") }

// ResolveScene turns the --scene value into a path. A value with a path
// separator is a path (relative to the cwd). A bare name is looked up in
// scenesDir, with or without a .yaml/.yml suffix.
func ResolveScene(scenesDir, scene string) (string, error) {
	if scene == "" {
		return "", fmt.Errorf("no scene given")
	}
	if strings.ContainsRune(scene, '/') || strings.ContainsRune(scene, filepath.Separator) {
		return filepath.Abs(scene)
	}
	name := scene
	if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
		name += ".yaml"
	}
	return filepath.Abs(filepath.Join(scenesDir, name))
}

// WithLabel returns l with its tmux label fixed to label (empty: derive it).
func (l Layout) WithLabel(label string) Layout {
	l.label = label
	return l
}

// Label is the tmux socket name of this root's server: plain agentctl-demo
// for the default root, otherwise agentctl-demo-<hash of the root>, so
// concurrent demos with different roots never share (or kill) a server.
//
// The default root depends on $TMPDIR, which the demo environment overrides,
// so a child process must not re-derive the label: the parent passes it down
// and the child pins it with WithLabel.
func (l Layout) Label() string {
	if l.label != "" {
		return l.label
	}
	if l.Root == DefaultRoot() {
		return DefaultLabel
	}
	sum := sha256.Sum256([]byte(l.Root))
	return DefaultLabel + "-" + hex.EncodeToString(sum[:])[:8]
}

// AttachCommand is the command that attaches to session on this demo's server.
func (l Layout) AttachCommand(session string) string {
	cmd := "tmux -L " + l.Label() + " attach"
	if session != "" {
		cmd += " -t " + session
	}
	return cmd
}

func (l Layout) Bin() string        { return filepath.Join(l.Root, "bin") }
func (l Layout) Agentctl() string   { return filepath.Join(l.Bin(), "agentctl") }
func (l Layout) Home() string       { return filepath.Join(l.Root, "home") }
func (l Layout) ClaudeDir() string  { return filepath.Join(l.Home(), ".claude") }
func (l Layout) ConfigHome() string { return filepath.Join(l.Home(), ".config") }
func (l Layout) StateHome() string  { return filepath.Join(l.Home(), ".local", "state") }
func (l Layout) DataHome() string   { return filepath.Join(l.Home(), ".local", "share") }
func (l Layout) CacheHome() string  { return filepath.Join(l.Home(), ".cache") }
func (l Layout) Tmp() string        { return filepath.Join(l.Root, "tmp") }
func (l Layout) PaneDir() string    { return filepath.Join(l.Root, "panes") }
func (l Layout) Manifest() string   { return filepath.Join(l.Root, "manifest.json") }
func (l Layout) TmuxConf() string   { return filepath.Join(l.Root, "tmux.conf") }

// Repo is a project's checkout, under HOME so paths render as ~/code/<name>.
func (l Layout) Repo(project string) string { return filepath.Join(l.Home(), "code", project) }

// RuntimeDir is a project's agentctl runtime dir.
func (l Layout) RuntimeDir(project string) string { return filepath.Join(l.Repo(project), ".agentctl") }

// Worktree is a window's worktree under its project.
func (l Layout) Worktree(project, branch string) string {
	return filepath.Join(l.Repo(project), ".worktrees", strings.ReplaceAll(branch, "/", "-"))
}

// PaneSpec is the file a fake pane reads.
func (l Layout) PaneSpec(project, key string) string {
	return filepath.Join(l.PaneDir(), project+"--"+key+".json")
}

// Env is the complete environment of every process the demo starts: built
// from scratch, never inherited, so the live session's TMUX, CLAUDE_* and
// AGENTCTL_* variables cannot leak in. tmuxEnv, when set, is the TMUX value
// that points plain `tmux` calls at the demo server.
func (l Layout) Env(tmuxEnv string) []string {
	env := []string{
		"PATH=" + l.Bin() + ":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + l.Home(),
		"USER=demo",
		"LOGNAME=demo",
		"SHELL=/bin/zsh",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"LANG=en_US.UTF-8",
		"TMPDIR=" + l.Tmp() + "/",
		"XDG_CONFIG_HOME=" + l.ConfigHome(),
		"XDG_STATE_HOME=" + l.StateHome(),
		"XDG_DATA_HOME=" + l.DataHome(),
		"XDG_CACHE_HOME=" + l.CacheHome(),
		"CLAUDE_CONFIG_DIR=" + l.ClaudeDir(),
		"GIT_AUTHOR_NAME=Dana Demo",
		"GIT_AUTHOR_EMAIL=dana@example.com",
		"GIT_COMMITTER_NAME=Dana Demo",
		"GIT_COMMITTER_EMAIL=dana@example.com",
		"GIT_CONFIG_NOSYSTEM=1",
		"GH_NO_UPDATE_NOTIFIER=1",
	}
	if tmuxEnv != "" {
		env = append(env, "TMUX="+tmuxEnv)
	}
	return env
}

// Isolate replaces this process's environment with Env(tmuxEnv), so the
// internal packages the seeder calls (paths, windowstate, winname, config)
// resolve only demo paths and talk only to the demo tmux server.
func (l Layout) Isolate(tmuxEnv string) error {
	os.Clearenv()
	for _, kv := range l.Env(tmuxEnv) {
		k, v, _ := strings.Cut(kv, "=")
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("setting %s: %w", k, err)
		}
	}
	return nil
}
