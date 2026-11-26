package serve

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/worktree"
)

// The phone's "Open project", the hub's "+ open" over HTTP: GET
// /v1/projects/recent lists the same recent project dirs as the hub's
// dropdown (closedlog.RecentDirs, minus projects with a live group), and
// POST /v1/projects/open runs `agentctl open --detach` for the dir's main
// repo root, which starts its session detached when it is not running.
// No tmux client is ever switched or attached.

// openProjectSpawn is the production open: the `agentctl open --detach`
// subprocess. A var so TestMain can make an unstubbed test fail instead of
// spawning.
var openProjectSpawn = runOpenDetached

func spawnOpenProject(dir string) (string, error) { return openProjectSpawn(dir) }

// userHomeDir expands a leading "~" in an open request.
var userHomeDir = os.UserHomeDir

func (s *Server) registerOpen(rt *Router) {
	if rt.allowWrite {
		rt.Feature("open-project")
	}
	rt.Device("GET /v1/projects/recent", s.handleRecentProjects)
	rt.Write("POST /v1/projects/open", writeRoute(s, "open-project", s.handleOpenProject))
}

// recentProject is one entry of GET /v1/projects/recent.
type recentProject struct {
	Dir string `json:"dir"`
	// Label is Dir with the laptop's home dir shortened to "~".
	Label string `json:"label"`
}

// recentProjectsBody is the answer of GET /v1/projects/recent. Error is set,
// with an empty list, when the ledger could not be read; the phone still
// offers the path field.
type recentProjectsBody struct {
	Dirs  []recentProject `json:"dirs"`
	Error string          `json:"error,omitempty"`
}

func (s *Server) handleRecentProjects(w http.ResponseWriter, _ *http.Request) {
	body := recentProjectsBody{Dirs: []recentProject{}}
	recs, err := s.closedRecs(s.now().Add(-closedlog.RecentWindow))
	if err != nil {
		logging.Warn("serve: reading the closed ledger: %v", err)
		body.Error = "the closed ledger could not be read"
		writeJSON(w, http.StatusOK, body)
		return
	}
	var live []string
	if groups, err := s.fleet.Fleet(); err != nil {
		logging.Warn("serve: loading fleet for /v1/projects/recent: %v", err)
	} else {
		for _, g := range groups {
			live = append(live, g.Dir)
		}
	}
	home, _ := userHomeDir()
	for _, d := range closedlog.WithoutDirs(closedlog.RecentDirs("", recs, isDirPath), live) {
		body.Dirs = append(body.Dirs, recentProject{Dir: d, Label: tildeDir(d, home)})
	}
	writeJSON(w, http.StatusOK, body)
}

// isDirPath reports an existing directory.
func isDirPath(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// tildeDir shortens a leading home dir to "~".
func tildeDir(p, home string) string {
	switch {
	case home == "":
		return p
	case p == home:
		return "~"
	case strings.HasPrefix(p, home+string(filepath.Separator)):
		return "~" + p[len(home):]
	}
	return p
}

// openProjectIn is the body of POST /v1/projects/open.
type openProjectIn struct {
	Dir string `json:"dir"`
}

// OpenProjectResult is the answer of POST /v1/projects/open: the project's
// session and whether it was already running before the request.
type OpenProjectResult struct {
	Session string `json:"session"`
	Dir     string `json:"dir"`
	Running bool   `json:"running"`
}

// resolveRequestDir checks an open request's dir: "~" and "~/..." expand to
// the laptop's home dir, and the result must be an absolute path to an
// existing directory. Relative paths are refused: the server has no
// meaningful working directory.
func resolveRequestDir(arg string) (string, error) {
	p := strings.TrimSpace(arg)
	switch {
	case p == "":
		return "", errors.New("enter a project directory")
	case p == "~" || strings.HasPrefix(p, "~/"):
		home, err := userHomeDir()
		if err != nil || home == "" {
			return "", errors.New("cannot expand ~: the laptop's home directory is unknown")
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	case strings.HasPrefix(p, "~"):
		return "", fmt.Errorf("unsupported path %s: use ~/ or an absolute path", p)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%s is not an absolute path", p)
	}
	p = filepath.Clean(p)
	fi, err := os.Stat(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("%s does not exist", p)
	case err != nil:
		return "", fmt.Errorf("%s cannot be read", p)
	case !fi.IsDir():
		return "", fmt.Errorf("%s is not a directory", p)
	}
	return p, nil
}

func (s *Server) handleOpenProject(_ *http.Request, in openProjectIn, a *auditEntry) outcome {
	dir, err := resolveRequestDir(in.Dir)
	if err != nil {
		return failure(http.StatusBadRequest, codeBadRequest, err.Error())
	}
	a.Project = dir
	release, ok := s.opens.acquire(closedlog.DirKey(dir))
	if !ok {
		return failure(http.StatusConflict, codeBusy, "this project is already being opened")
	}
	defer release()
	running := map[string]bool{}
	if groups, err := s.fleet.Fleet(); err == nil {
		for _, g := range groups {
			running[g.Session] = true
		}
	}
	s.auditAttempt(*a)
	stdout, err := s.openProject(dir)
	if err != nil {
		logging.Warn("serve: opening %s: %v", dir, err)
		return failure(http.StatusBadGateway, codeActionFailed, "could not open "+dir+": "+err.Error())
	}
	session := lastLine(stdout)
	a.Session = session
	s.bus.Refresh()
	return success(OpenProjectResult{Session: session, Dir: dir, Running: session != "" && running[session]})
}

// lastLine is the last non-empty line of out, trimmed.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// subprocessError is the error of a failed agentctl helper: the last line it
// printed to stderr without Kong's "Error: " prefix, or err when it printed
// nothing.
func subprocessError(stderr string, err error) error {
	msg := lastLine(stderr)
	if msg = strings.TrimPrefix(msg, "Error: "); msg != "" {
		return errors.New(msg)
	}
	return err
}

// runOpenDetached runs `agentctl open --detach` for dir's project the way
// the hub runs its helpers: in a git repo the main repo root (the same
// resolution open itself does), with cwd and the runtime dir pinned to it
// (closedlog.ResumeEnv), so the helper ensures the session in process and
// never creates a runtime dir in a subdirectory or inherits serve's pin. It
// returns the helper's stdout (the session name). open initializes the
// project when needed and starts its session detached, refusing a
// same-named session of another directory; --detach never switches or
// attaches a client.
func runOpenDetached(dir string) (string, error) {
	exe, err := selfpath.Spawnable()
	if err != nil {
		return "", fmt.Errorf("locating agentctl: %w", err)
	}
	root := dir
	if _, err := worktree.Toplevel(dir); err == nil {
		if r := worktree.MainRepoRoot(dir); r != "" {
			root = filepath.Clean(r)
		}
	}
	c := exec.Command(exe, "open", "--detach", root)
	c.Dir = root
	c.Env = closedlog.ResumeEnv(root, os.Environ())
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return "", subprocessError(stderr.String(), err)
	}
	return stdout.String(), nil
}
