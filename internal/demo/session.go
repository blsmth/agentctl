package demo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/tmux"
)

// bootSession holds the demo server up until the agentctl sessions exist.
const bootSession = "demo-boot"

// Manifest records what `up` built, for the tape generator and `status`.
type Manifest struct {
	Label    string                 `json:"label,omitempty"` // tmux socket label
	Scene    string                 `json:"scene"`
	Sessions map[string]string      `json:"sessions"` // project -> tmux session
	Windows  map[string]WindowPlace `json:"windows"`  // window key -> place
	Selected map[string]string      `json:"selected"` // project -> selected window key
}

// label is the socket label the demo ran on (the default for old manifests).
func (m *Manifest) label() string {
	if m.Label == "" {
		return DefaultLabel
	}
	return m.Label
}

// WindowPlace is where a scene window ended up.
type WindowPlace struct {
	Project string `json:"project"`
	Session string `json:"session"`
	ID      string `json:"id"`
}

// Target is the tmux target of the window ("=session:@N").
func (p WindowPlace) Target() string { return tmux.ExactSessionTarget(p.Session) + p.ID }

// SessionFor is the agentctl session name of a project.
func SessionFor(project string) string { return tmux.SessionNameForDirName(project) }

// UpOptions are the inputs of Up.
type UpOptions struct {
	ScenePath string
	Fixtures  string
	// Self is this seeder binary: it runs the per-project seeding and the
	// fake panes.
	Self string
}

// Up builds the scene's world from scratch: it tears down any previous demo
// (server and root, keeping bin/), writes the files, starts the demo tmux
// server, lets the real agentctl binary start each project's session, then
// seeds every project's windows in a child process (paths.Init is once per
// process, and each project has its own runtime dir).
func Up(l Layout, s *Scene, opts UpOptions) (*Manifest, error) {
	t := Tmux{Layout: l}
	if err := Reset(l, t); err != nil {
		return nil, err
	}
	if _, err := os.Stat(l.Agentctl()); err != nil {
		return nil, fmt.Errorf("agentctl binary missing (build it with `go build -o %s .`): %w", l.Agentctl(), err)
	}
	if err := WriteWorld(l, s, opts.Fixtures, time.Now()); err != nil {
		return nil, err
	}
	if _, err := t.Run("-f", l.TmuxConf(), "new-session", "-d", "-s", bootSession, "-c", l.Home()); err != nil {
		return nil, fmt.Errorf("starting demo tmux server: %w", err)
	}
	if err := t.Claim(); err != nil {
		return nil, fmt.Errorf("recording demo root on tmux server: %w", err)
	}
	sock, err := t.SocketEnv()
	if err != nil {
		return nil, err
	}
	m := &Manifest{Label: l.Label(), Scene: opts.ScenePath, Sessions: map[string]string{}, Windows: map[string]WindowPlace{}, Selected: map[string]string{}}
	for _, p := range s.Projects {
		name, err := startSession(l, t, sock, p.Name)
		if err != nil {
			return nil, err
		}
		m.Sessions[p.Name] = name
	}
	for _, p := range s.Projects {
		ids, err := runProjectSeed(l, sock, opts, p.Name)
		if err != nil {
			return nil, err
		}
		for k, id := range ids {
			m.Windows[k] = WindowPlace{Project: p.Name, Session: m.Sessions[p.Name], ID: id}
		}
		for _, w := range s.WindowsIn(p.Name) {
			if w.Select {
				m.Selected[p.Name] = w.Key
			}
		}
	}
	_, _ = t.Run("kill-session", "-t", tmux.ExactSession(bootSession))
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding manifest: %w", err)
	}
	return m, writeFile(l.Manifest(), data, 0o600)
}

// startSession runs bare `agentctl` in the project checkout, exactly as a
// user would, pointed at the demo server through TMUX. agentctl creates and
// configures the session, then fails to attach (no terminal); only the
// session's existence decides success.
func startSession(l Layout, t Tmux, sock, project string) (string, error) {
	name := SessionFor(project)
	cmd := exec.Command(l.Agentctl())
	cmd.Dir = l.Repo(project)
	cmd.Env = l.Env(sock)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	_ = cmd.Run() // the attach at the end always fails: there is no terminal
	if _, err := t.Run("has-session", "-t", tmux.ExactSession(name)); err != nil {
		return "", fmt.Errorf("agentctl did not start session %s: %s", name, strings.TrimSpace(out.String()))
	}
	return name, nil
}

// runProjectSeed runs `<self> project` for one project and parses the
// "key @id" lines it prints.
func runProjectSeed(l Layout, sock string, opts UpOptions, project string) (map[string]string, error) {
	cmd := exec.Command(opts.Self, "project", "--root", l.Root, "--scene", opts.ScenePath,
		"--fixtures", opts.Fixtures, "--project", project)
	cmd.Env = l.Env(sock)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("seeding project %s: %w: %s", project, err, strings.TrimSpace(stderr.String()))
	}
	ids := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if k, id, ok := strings.Cut(line, " "); ok {
			ids[k] = id
		}
	}
	return ids, nil
}

// Reset kills the demo server and empties the root, keeping bin/ (the
// freshly built binaries live there).
func Reset(l Layout, t Tmux) error {
	if err := t.CheckOwner(); err != nil {
		return err
	}
	if t.Running() {
		if _, err := t.Run("kill-server"); err != nil {
			return fmt.Errorf("stopping demo server: %w", err)
		}
	}
	entries, err := os.ReadDir(l.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading demo root: %w", err)
	}
	for _, e := range entries {
		if e.Name() == "bin" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(l.Root, e.Name())); err != nil {
			return fmt.Errorf("clearing demo root: %w", err)
		}
	}
	return nil
}

// Down kills the demo server and removes the whole root.
func Down(l Layout) error {
	t := Tmux{Layout: l}
	if err := t.CheckOwner(); err != nil {
		return err
	}
	if t.Running() {
		if _, err := t.Run("kill-server"); err != nil {
			return fmt.Errorf("stopping demo server: %w", err)
		}
	}
	if err := os.RemoveAll(l.Root); err != nil {
		return fmt.Errorf("removing demo root: %w", err)
	}
	return nil
}

// LoadManifest reads the manifest `up` wrote.
func LoadManifest(l Layout) (*Manifest, error) {
	data, err := os.ReadFile(l.Manifest())
	if err != nil {
		return nil, fmt.Errorf("reading manifest (run `just demo` first): %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}
	return &m, nil
}

// Summary is a human-readable description of the manifest.
func (m *Manifest) Summary() string {
	var b strings.Builder
	projects := make([]string, 0, len(m.Sessions))
	for p := range m.Sessions {
		projects = append(projects, p)
	}
	sort.Strings(projects)
	for _, p := range projects {
		fmt.Fprintf(&b, "  %s (session %s)\n", p, m.Sessions[p])
		var keys []string
		for k, w := range m.Windows {
			if w.Project == p {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool { return windowNum(m.Windows[keys[i]].ID) < windowNum(m.Windows[keys[j]].ID) })
		for _, k := range keys {
			fmt.Fprintf(&b, "    %-6s %s\n", m.Windows[k].ID, k)
		}
	}
	return b.String()
}

// windowNum is N of a tmux window ID "@N" (0 when malformed).
func windowNum(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "@"))
	return n
}
