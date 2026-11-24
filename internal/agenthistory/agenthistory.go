// Package agenthistory keeps a hidden git history of the agent definitions
// under ~/.claude/agents and ~/.claude/shared (and, per project, of
// <repo>/.claude/agents), so every change an improve session or a human
// makes can be inspected and reverted.
//
// Each tracked root has its own store: a git directory owned by agentctl
// under the state dir, with the root as its work tree. No .git is ever
// created inside the tracked tree, every git call passes explicit
// --git-dir/--work-tree through the runGit seam, and only the listed
// pathspecs are ever staged, so other files under ~/.claude are untouched.
// History is append-only: a revert is a new snapshot.
package agenthistory

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/stamp"
)

// MaxFileSize is the largest file the store tracks; bigger ones are skipped.
const MaxFileSize = 1 << 20

// DefaultAuthor is the commit author of every snapshot.
const DefaultAuthor = "agentctl"

// ErrGitMissing is returned when no git binary is on PATH.
var ErrGitMissing = errors.New("git not found on PATH")

// ErrNoSnapshots is returned when the store exists but holds no commit yet.
var ErrNoSnapshots = errors.New("no snapshots yet")

// ErrNotTracked is returned for a path outside the store's tracked paths.
var ErrNotTracked = errors.New("path is not tracked by the agent history")

// Seams for tests.
var (
	lookPath = exec.LookPath
	// runGit runs git with args (already carrying --git-dir/--work-tree) and
	// returns stdout. env is appended to a scrubbed environment.
	runGit = defaultRunGit
	// stateDir resolves the agentctl state dir lazily.
	stateDir = config.StateDir
	// logf writes a log line.
	logf = logging.Log
)

// Store is one tracked root.
type Store struct {
	GitDir   string   `json:"git_dir"`   // bare-style git dir owned by agentctl
	WorkTree string   `json:"work_tree"` // the tracked root
	Paths    []string `json:"paths"`     // tracked pathspecs, relative to WorkTree
	// NameDir is the directory a bare name resolves into ("reviewer" is
	// <NameDir>/reviewer.md); Paths[0] when empty.
	NameDir string `json:"name_dir,omitempty"`
}

// Store names, for --store and run records.
const (
	NameHome   = "agents"
	NameConfig = "config"
)

// Entry is one commit in the history.
type Entry struct {
	Rev     string
	Short   string
	Author  string
	Date    string
	Subject string
}

// HomePaths are the tracked pathspecs under ~/.claude: nothing else there is
// ever staged.
var HomePaths = []string{"agents", "shared", "CLAUDE.md", "settings.json"}

// ConfigPaths are the tracked pathspecs under the agentctl global config dir.
// Nothing else in it (secrets, tokens) is ever staged.
var ConfigPaths = []string{"config.json", "templates"}

// HomeStore is the store for ~/.claude/agents, ~/.claude/shared,
// ~/.claude/CLAUDE.md and ~/.claude/settings.json.
func HomeStore() (*Store, error) {
	home := claudehome.HomeDir()
	if home == "" {
		return nil, errors.New("home directory unknown")
	}
	sd, err := stateDir()
	if err != nil {
		return nil, fmt.Errorf("resolving state dir: %w", err)
	}
	return &Store{GitDir: filepath.Join(sd, "agent-history.git"), WorkTree: home, Paths: slices.Clone(HomePaths), NameDir: "agents"}, nil
}

// ConfigStore is the store for the agentctl global config (config.json and
// templates/), a second git dir beside the home store.
func ConfigStore() (*Store, error) {
	dir, err := config.GlobalDir()
	if err != nil {
		return nil, fmt.Errorf("resolving config dir: %w", err)
	}
	sd, err := stateDir()
	if err != nil {
		return nil, fmt.Errorf("resolving state dir: %w", err)
	}
	return &Store{GitDir: filepath.Join(sd, "agent-history-config.git"), WorkTree: dir, Paths: slices.Clone(ConfigPaths), NameDir: "templates"}, nil
}

// StoreByName returns the store for NameHome (or ""), NameConfig, or an
// absolute project dir (its .claude/agents store).
func StoreByName(name string) (*Store, error) {
	if filepath.IsAbs(name) {
		return ProjectStore(name)
	}
	switch name {
	case "", NameHome:
		return HomeStore()
	case NameConfig:
		return ConfigStore()
	}
	return nil, fmt.Errorf("unknown store %q (want %s or %s)", name, NameHome, NameConfig)
}

// StoreForPath picks the store an absolute path belongs to: the config store
// for a path under the agentctl config dir, a project's store (named by its
// canonical project dir) for a path under some <project>/.claude/agents, the
// home store otherwise.
func StoreForPath(arg string) string {
	if filepath.IsAbs(arg) {
		if dir, err := config.GlobalDir(); err == nil {
			for _, d := range []string{dir, CanonicalRoot(dir)} {
				for _, a := range []string{arg, canonicalPath(arg)} {
					if rel, err := filepath.Rel(d, a); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
						return NameConfig
					}
				}
			}
		}
		// The root keeps the spelling given: ProjectStore canonicalizes it
		// and falls back to a store keyed by that spelling.
		if root, ok := projectRootOf(arg); ok && !isHomeRoot(root) {
			return root
		}
		if root, ok := projectRootOf(canonicalPath(arg)); ok && !isHomeRoot(root) {
			return root
		}
	}
	return NameHome
}

// ProjectStore is the store for <repo>/.claude/agents, keyed by the repo path.
func ProjectStore(repo string) (*Store, error) {
	if !filepath.IsAbs(repo) {
		return nil, fmt.Errorf("project dir %q is not absolute", repo)
	}
	sd, err := stateDir()
	if err != nil {
		return nil, fmt.Errorf("resolving state dir: %w", err)
	}
	literal := filepath.Clean(repo)
	repo = CanonicalRoot(repo)
	gitDir := projectGitDir(sd, repo)
	if literal != repo && !gitDirExists(gitDir) {
		// Stores made before keys were canonical used the path as spelled.
		// Keep using one, so its history is not orphaned.
		if legacy := projectGitDir(sd, literal); gitDirExists(legacy) {
			gitDir = legacy
		}
	}
	return &Store{
		GitDir:   gitDir,
		WorkTree: repo,
		Paths:    []string{filepath.Join(".claude", "agents")},
	}, nil
}

func projectGitDir(sd, root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(sd, "agent-history", fmt.Sprintf("%x-%s.git", sum[:4], filepath.Base(root)))
}

func gitDirExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "HEAD"))
	return err == nil
}

func defaultRunGit(env []string, stdin string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = append(scrubbedEnv(), env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("git %s: %w: %s", firstNonFlag(args), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func firstNonFlag(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
			return a
		}
	}
	return ""
}

// scrubbedEnv drops git's repository selectors and the user's git config so
// the surrounding shell (or a test) cannot redirect or reconfigure the store.
func scrubbedEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
}

// git runs one git command against the store.
func (s *Store) git(env []string, stdin string, args ...string) ([]byte, error) {
	full := append([]string{"--no-optional-locks", "--git-dir=" + s.GitDir, "--work-tree=" + s.WorkTree}, args...)
	return runGit(env, stdin, full...)
}

// lock takes the store's exclusive flock and returns its release.
func (s *Store) lock() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.GitDir), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.GitDir+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close() //nolint:errcheck
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
		f.Close()                                   //nolint:errcheck
	}, nil
}

func (s *Store) hasHead() bool {
	_, err := s.git(nil, "", "rev-parse", "--verify", "-q", "HEAD")
	return err == nil
}

// ensureInit creates the git dir on first use. It reports whether it did.
func (s *Store) ensureInit() (bool, error) {
	if _, err := os.Stat(filepath.Join(s.GitDir, "HEAD")); err == nil {
		os.Chmod(s.GitDir, 0o700) //nolint:errcheck // harden stores made before this
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(s.GitDir), 0o700); err != nil {
		return false, err
	}
	if _, err := runGit(nil, "", "--no-optional-locks", "--git-dir="+s.GitDir, "--work-tree="+s.WorkTree,
		"init", "-q", "--initial-branch=main", "--shared=0600"); err != nil {
		return false, fmt.Errorf("initialising history store: %w", err)
	}
	// The stores hold config and settings: owner only, whatever the umask.
	if err := os.Chmod(s.GitDir, 0o700); err != nil {
		return false, err
	}
	return true, nil
}

// skipList walks the tracked paths without following symlinks and returns
// the files over MaxFileSize and the nested git repos as exclude pathspecs,
// plus the repos' paths relative to the work tree. A tracked path that is
// itself a git repo is reported as a nested repo too.
func (s *Store) skipList() (excludes, repos []string) {
	for _, p := range s.Paths {
		root := filepath.Join(s.WorkTree, p)
		fi, err := os.Lstat(root)
		if err != nil {
			continue
		}
		if !fi.IsDir() {
			if fi.Mode().IsRegular() && fi.Size() > MaxFileSize {
				excludes = append(excludes, ":(exclude,literal)"+filepath.ToSlash(p))
			}
			continue
		}
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
			if err != nil || d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() {
				// A nested git repo (a checked-out project under agents/) makes
				// "git add" fail outright, so it is left out of the history.
				if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
					if rel, err := filepath.Rel(s.WorkTree, path); err == nil {
						rel = filepath.ToSlash(rel)
						repos = append(repos, rel)
						excludes = append(excludes, ":(exclude,literal)"+rel)
					}
					return filepath.SkipDir
				}
				return nil
			}
			if fi, err := d.Info(); err == nil && fi.Size() > MaxFileSize {
				if rel, err := filepath.Rel(s.WorkTree, path); err == nil {
					excludes = append(excludes, ":(exclude,literal)"+filepath.ToSlash(rel))
				}
			}
			return nil
		})
	}
	return excludes, repos
}

// SkippedRepos lists the nested git repos (paths relative to the work tree)
// the store leaves out of its history.
func (s *Store) SkippedRepos() []string {
	_, repos := s.skipList()
	return repos
}

// logSkippedRepos logs each skipped repo once per path per day, deduped by a
// stamp in the state dir (housekeeping runs in a fresh process every few
// minutes).
func (s *Store) logSkippedRepos(repos []string) {
	if len(repos) == 0 {
		return
	}
	sd, err := stateDir()
	if err != nil {
		return
	}
	dir := filepath.Join(sd, "agent-history-skips")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	for _, r := range repos {
		sum := sha256.Sum256([]byte(s.GitDir + "\x00" + r))
		if stamp.Try(filepath.Join(dir, fmt.Sprintf("%x.stamp", sum[:8])), 24*time.Hour, time.Now()) {
			logf("agent-history: skipping nested git repo %s", filepath.Join(s.WorkTree, filepath.FromSlash(r)))
		}
	}
}

// stage stages every change under the tracked paths: additions and edits
// (forced, since project agent dirs are gitignored in their repo) and
// deletions of paths that no longer exist.
func (s *Store) stage() error {
	skip, repos := s.skipList()
	s.logSkippedRepos(repos)
	for _, p := range s.Paths {
		slash := filepath.ToSlash(p)
		if slices.Contains(repos, slash) {
			continue // the tracked path is itself a git repo
		}
		if _, err := os.Lstat(filepath.Join(s.WorkTree, p)); err != nil {
			if _, err := s.git(nil, "", "rm", "-r", "--cached", "-q", "--ignore-unmatch", "--", slash); err != nil {
				return err
			}
			continue
		}
		args := append([]string{"add", "-A", "-f", "--", slash}, skip...)
		if _, err := s.git(nil, "", args...); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot commits any drift under the tracked paths. reason says what
// triggered it (it becomes the commit subject); author defaults to "agentctl".
// The first call creates the store with the current contents as the baseline.
// It returns the new revision, or "" when nothing changed.
func (s *Store) Snapshot(reason, author string) (string, error) {
	return s.SnapshotBody(reason, "", author)
}

// SnapshotBody is Snapshot with a commit message body (a rationale).
func (s *Store) SnapshotBody(reason, body, author string) (string, error) {
	if _, err := lookPath("git"); err != nil {
		return "", ErrGitMissing
	}
	if author == "" {
		author = DefaultAuthor
	}
	if reason == "" {
		reason = "snapshot"
	}
	if fi, err := os.Stat(s.WorkTree); err != nil || !fi.IsDir() {
		return "", nil // nothing to track yet
	}
	unlock, err := s.lock()
	if err != nil {
		return "", fmt.Errorf("locking history store: %w", err)
	}
	defer unlock()
	return s.snapshotLocked(reason, body, author)
}

func (s *Store) snapshotLocked(reason, body, author string) (string, error) {
	fresh, err := s.ensureInit()
	if err != nil {
		return "", err
	}
	if err := s.stage(); err != nil {
		return "", fmt.Errorf("staging: %w", err)
	}
	baseline := !s.hasHead()
	if !baseline {
		if _, err := s.git(nil, "", "diff", "--cached", "--quiet"); err == nil {
			return "", nil
		}
	}
	msg := reason
	if fresh || baseline {
		msg = "baseline: " + reason
	}
	if body = strings.TrimSpace(body); body != "" {
		msg += "\n\n" + body
	}
	email := strings.ReplaceAll(strings.ToLower(author), " ", "-") + "@agentctl.local"
	env := []string{
		"GIT_AUTHOR_NAME=" + author, "GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + DefaultAuthor, "GIT_COMMITTER_EMAIL=agentctl@agentctl.local",
	}
	args := []string{"-c", "commit.gpgsign=false", "commit", "-q", "--no-verify", "-m", msg}
	if baseline {
		args = append(args, "--allow-empty")
	}
	if _, err := s.git(env, "", args...); err != nil {
		return "", fmt.Errorf("committing: %w", err)
	}
	out, err := s.git(nil, "", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// revRE accepts a hash, HEAD, or a HEAD~N style name; nothing that git could
// read as an option.
var revRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.~^]*$`)

func checkRev(rev string) error {
	if !revRE.MatchString(rev) || strings.Contains(rev, "..") {
		return fmt.Errorf("invalid revision %q", rev)
	}
	return nil
}

// Resolve turns an agent name or a path into a tracked path relative to the
// work tree. "reviewer" is agents/reviewer.md; an absolute path must be under
// the work tree; a relative path is taken from the work tree.
func (s *Store) Resolve(arg string) (string, error) {
	if arg == "" {
		return "", errors.New("empty name")
	}
	rel := arg
	if slices.Contains(s.Paths, arg) {
		return arg, nil // a tracked file or dir named exactly
	}
	nameDir := s.NameDir
	if nameDir == "" {
		nameDir = s.Paths[0]
	}
	switch {
	case filepath.IsAbs(arg):
		r, err := filepath.Rel(s.WorkTree, arg)
		if err != nil || !s.underTracked(r) {
			// Another spelling of the same file: symlinked dirs, or a
			// linked worktree's copy of a project path.
			r, err = filepath.Rel(s.WorkTree, canonicalPath(arg))
		}
		if err != nil {
			return "", fmt.Errorf("%w: %s", ErrNotTracked, arg)
		}
		rel = r
	case !strings.ContainsRune(arg, '/') && !strings.HasSuffix(arg, ".md"):
		rel = filepath.Join(nameDir, arg+".md")
	case !strings.ContainsRune(arg, '/'):
		rel = filepath.Join(nameDir, arg)
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: %s", ErrNotTracked, arg)
	}
	for _, p := range s.Paths {
		p = filepath.ToSlash(p)
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return rel, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrNotTracked, arg)
}

// underTracked reports whether rel (relative to the work tree) is one of the
// tracked paths or under one.
func (s *Store) underTracked(rel string) bool {
	rel = filepath.ToSlash(filepath.Clean(rel))
	for _, p := range s.Paths {
		p = filepath.ToSlash(p)
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

func (s *Store) ready() error {
	if _, err := lookPath("git"); err != nil {
		return ErrGitMissing
	}
	if _, err := os.Stat(filepath.Join(s.GitDir, "HEAD")); err != nil {
		return errors.New("no agent history yet (it is created by the first housekeeping snapshot)")
	}
	if !s.hasHead() {
		return ErrNoSnapshots
	}
	return nil
}

// Log returns the history, newest first, optionally limited to one path
// (from Resolve) and to limit entries (0 = all).
func (s *Store) Log(path string, limit int) ([]Entry, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	args := []string{"log", "--format=%H%x1f%an%x1f%aI%x1f%s"}
	if limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", limit))
	}
	args = append(args, "--")
	if path != "" {
		args = append(args, path)
	} else {
		args = append(args, s.Paths...)
	}
	out, err := s.git(nil, "", args...)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) != 4 {
			continue
		}
		entries = append(entries, Entry{Rev: f[0], Short: f[0][:min(8, len(f[0]))], Author: f[1], Date: f[2], Subject: f[3]})
	}
	return entries, nil
}

// Diff shows rev (default HEAD) against the current files, for path ("" =
// everything tracked).
func (s *Store) Diff(path, rev string) (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	if rev == "" {
		rev = "HEAD"
	}
	if err := checkRev(rev); err != nil {
		return "", err
	}
	args := []string{"diff", rev, "--"}
	if path != "" {
		args = append(args, path)
	} else {
		args = append(args, s.Paths...)
	}
	out, err := s.git(nil, "", args...)
	return string(out), err
}

// Revert restores path to its content at rev and records that as a new
// snapshot. With no rev it undoes the latest change to path (the version
// before the newest commit touching it). Drift is snapshotted first, so
// nothing is lost. It returns the revision restored from and the new
// snapshot's revision.
func (s *Store) Revert(path, rev, author string) (from, snap string, err error) {
	if _, err := lookPath("git"); err != nil {
		return "", "", ErrGitMissing
	}
	if author == "" {
		author = DefaultAuthor
	}
	if rev != "" {
		if err := checkRev(rev); err != nil {
			return "", "", err
		}
	}
	if fi, err := os.Lstat(filepath.Join(s.WorkTree, path)); err == nil && fi.IsDir() {
		return "", "", fmt.Errorf("%s is a directory; revert one file at a time", path)
	}
	unlock, err := s.lock()
	if err != nil {
		return "", "", fmt.Errorf("locking history store: %w", err)
	}
	defer unlock()
	if _, err := s.snapshotLocked("before revert of "+path, "", author); err != nil {
		return "", "", err
	}
	if rev == "" {
		out, err := s.git(nil, "", "log", "-n1", "--format=%H", "--", path)
		latest := strings.TrimSpace(string(out))
		if err != nil || latest == "" {
			return "", "", fmt.Errorf("no history for %s", path)
		}
		// The baseline records existing state, not a change to undo.
		parents, err := s.git(nil, "", "rev-list", "--parents", "-n1", latest)
		if err != nil {
			return "", "", err
		}
		if len(strings.Fields(string(parents))) < 2 {
			return "", "", fmt.Errorf("no change to undo for %s (only the baseline)", path)
		}
		rev = latest + "~1"
	}
	out, err := s.git(nil, "", "rev-parse", "--verify", "-q", rev+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("unknown revision %q", rev)
	}
	from = strings.TrimSpace(string(out))
	if from != "" && s.existsAt(from, path) {
		if _, err := s.git(nil, "", "checkout", from, "--", path); err != nil {
			return "", "", err
		}
	} else {
		if _, err := s.git(nil, "", "rm", "-r", "-f", "-q", "--ignore-unmatch", "--", path); err != nil {
			return "", "", err
		}
	}
	label := "nothing"
	if from != "" {
		label = from[:min(8, len(from))]
	}
	snap, err = s.snapshotLocked(fmt.Sprintf("revert %s to %s", path, label), "", author)
	return from, snap, err
}

func (s *Store) existsAt(rev, path string) bool {
	_, err := s.git(nil, "", "cat-file", "-e", rev+":"+path)
	return err == nil
}

// Head returns the current revision, "" when the store has no commit yet.
func (s *Store) Head() string {
	out, err := s.git(nil, "", "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ChangedFiles lists the tracked files (relative to the work tree) that differ
// between two revisions.
func (s *Store) ChangedFiles(from, to string) ([]string, error) {
	if err := checkRev(from); err != nil {
		return nil, err
	}
	if err := checkRev(to); err != nil {
		return nil, err
	}
	out, err := s.git(nil, "", "diff", "--name-only", "-z", from, to, "--")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}
