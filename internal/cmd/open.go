package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/worktree"
)

// openDeps are the side effects of `agentctl open`, injectable so tests never
// touch tmux, git or spawn agentctl.
type openDeps struct {
	cwd  func() (string, error)
	home func() (string, error)
	// repoRoot is the main repo root containing dir (the root itself for a
	// repo, the main checkout for a worktree), false outside a git repo.
	repoRoot func(dir string) (string, bool)
	isDir    func(path string) (exists, dir bool)
	// ensure initializes dir's project and starts its session detached when it
	// is not running, refusing a same-named session of another directory.
	ensure      func(dir string) error
	sessionName func(dir string) string
	// currentSession is the tmux session this process runs in ("" outside tmux).
	currentSession func() string
	switchTo       func(session string) error
	attach         func(session string) error
	out            io.Writer
}

var openLive = openDeps{
	cwd:  os.Getwd,
	home: os.UserHomeDir,
	repoRoot: func(dir string) (string, bool) {
		if _, err := worktree.Toplevel(dir); err != nil {
			return "", false
		}
		return worktree.MainRepoRoot(dir), true
	},
	isDir: func(p string) (bool, bool) {
		fi, err := os.Stat(p)
		return err == nil, err == nil && fi.IsDir()
	},
	ensure:         ensureOpenSession,
	sessionName:    tmux.SessionNameForProjectRoot,
	currentSession: tmux.CurrentSessionName,
	switchTo:       func(s string) error { return tmux.SwitchClientToSession("", s) },
	attach:         tmux.AttachSessionNamed,
	out:            os.Stdout,
}

// OpenOptions are the flags of `agentctl open`.
type OpenOptions struct {
	// Detach starts the session when needed but neither switches nor attaches;
	// the session name is printed.
	Detach bool
	// Ensure is the hidden helper mode: this process is already pinned to dir's
	// project (cwd and runtime dir); ensure the session and stop.
	Ensure bool
}

// RunOpen is `agentctl open [dir]`: start or switch to the agentctl session
// for a project directory.
func RunOpen(dir string, opts OpenOptions) error {
	if opts.Ensure {
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("--ensure needs an absolute directory")
		}
		return ensureHubSession(dir, hubNewSessionLive)
	}
	return runOpen(dir, opts, openLive)
}

func runOpen(arg string, opts OpenOptions, d openDeps) error {
	dir, err := resolveOpenDir(arg, d)
	if err != nil {
		return err
	}
	name := d.sessionName(dir)
	if err := d.ensure(dir); err != nil {
		return err
	}
	if opts.Detach {
		fmt.Fprintln(d.out, name)
		return nil
	}
	if cur := d.currentSession(); cur != "" {
		if cur == name {
			fmt.Fprintf(d.out, "already in %s\n", name)
			return nil
		}
		if err := d.switchTo(name); err != nil {
			return fmt.Errorf("switching to %s: %w", name, err)
		}
		return nil
	}
	if err := d.attach(name); err != nil {
		return fmt.Errorf("attaching to %s: %w", name, err)
	}
	return nil
}

// resolveOpenDir turns the argument into the project root: "" is the cwd, "~"
// and relative paths are expanded, and inside a git repo the main repo root
// wins (so a subdirectory or a .worktrees/ checkout opens the repo's session).
func resolveOpenDir(arg string, d openDeps) (string, error) {
	p := strings.TrimSpace(arg)
	if p == "" || p == "." {
		cwd, err := d.cwd()
		if err != nil {
			return "", fmt.Errorf("reading the current directory: %w", err)
		}
		p = cwd
	}
	switch {
	case p == "~" || strings.HasPrefix(p, "~/"):
		home, err := d.home()
		if err != nil || home == "" {
			return "", fmt.Errorf("cannot expand ~: home directory unknown")
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	case strings.HasPrefix(p, "~"):
		return "", fmt.Errorf("unsupported path: %s", p)
	}
	if !filepath.IsAbs(p) {
		cwd, err := d.cwd()
		if err != nil {
			return "", fmt.Errorf("reading the current directory: %w", err)
		}
		p = filepath.Join(cwd, p)
	}
	p = filepath.Clean(p)
	exists, isDir := d.isDir(p)
	switch {
	case !exists:
		return "", fmt.Errorf("%s does not exist", p)
	case !isDir:
		return "", fmt.Errorf("%s is not a directory", p)
	}
	if root, ok := d.repoRoot(p); ok && root != "" {
		p = filepath.Clean(root)
	}
	return p, nil
}

// ensureOpenSession runs ensureHubSession for dir. This process is bound to
// the project it was started in, so when that is not dir the work happens in
// a helper pinned to dir (cwd and runtime dir), the way the hub does it.
func ensureOpenSession(dir string) error {
	if tmux.SamePath(paths.RuntimeDir(), filepath.Join(dir, ".agentctl")) {
		return ensureHubSession(dir, hubNewSessionLive)
	}
	exe, err := selfpath.Spawnable()
	if err != nil {
		return fmt.Errorf("locating agentctl: %w", err)
	}
	c := exec.Command(exe, "open", "--ensure", dir)
	c.Dir = dir
	c.Env = closedlog.ResumeEnv(dir, os.Environ())
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		if msg = strings.TrimPrefix(msg, "Error: "); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return fmt.Errorf("preparing %s: %w", dir, err)
	}
	return nil
}
