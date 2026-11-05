// Package paths provides a single source of truth for the agentctl runtime
// directory and its subdirectories. All instance-specific state lives under
// $CWD/.agentctl/, where CWD is the directory where agentctl was launched.
// This provides natural per-project isolation without needing an explicit
// AGENTCTL_INSTANCE env var for path disambiguation.
package paths

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/blsmth/agentctl/internal/safefs"
)

var (
	runtimeDir string
	once       sync.Once

	dataDir     string
	dataDirOnce sync.Once
)

// runtimeDirName is the basename every runtime directory must have. DirName()
// derives the project name as basename(dirname(runtimeDir)), which is only
// correct when runtimeDir actually ends in this element.
const runtimeDirName = ".agentctl"

// Init sets the runtime directory to baseDir/.agentctl. It does not create it;
// see EnsureRuntimeDir.
// baseDir is typically the CWD where agentctl was launched. Call once at
// startup from main before any other Init functions.
//
// If AGENTCTL_RUNTIME_DIR is set to a non-empty value, it is used directly as
// the runtime directory instead of baseDir/.agentctl. Unlike baseDir, this is
// the full path to a ".agentctl" directory, not a project base to join onto.
// This exists so a process whose CWD does not match the project it belongs to
// (e.g. a dispatched agent running inside a git worktree, or a dispatching
// process itself running from an unrelated directory) can still resolve the
// correct, single runtime directory for its project: the env var is set once
// at tmux window launch time and, unlike CWD, survives every subsequent `cd`
// inside that window. When the env var wins, baseDir/.agentctl is never
// created.
//
// Use Init for every invocation that acts on behalf of an ALREADY-RUNNING
// session -- hooks, chrome bars, monitors, keybinding-spawned commands,
// dispatch. Those must follow the env var even when it disagrees with cwd,
// because that is the whole point of the pin. Session-STARTING invocations
// want InitForSession instead; see its doc comment.
func Init(baseDir string) {
	once.Do(func() { initRuntimeDir(baseDir, false) })
}

// InitForSession is Init for the invocation that starts or attaches a
// project's own tmux session (bare `agentctl`, which routes to
// runDefaultSession).
//
// It differs from Init in exactly one way: an inherited
// AGENTCTL_RUNTIME_DIR is honored only when baseDir actually belongs to the
// project that runtime dir anchors. When baseDir sits outside it, the env
// var is a stale inheritance rather than a deliberate pin, and cwd wins.
//
// This matters because AGENTCTL_RUNTIME_DIR is exported into every pane of
// every agentctl session and deliberately survives `cd`. Without the
// ownership test, launching agentctl from a shell inside an existing session
// -- `cd ~/src/acme/widgetco-infra && agentctl` from the agentctl-acme shell
// window, or the same call from an agent's Bash tool -- inherits the parent's
// runtime dir, so SetSessionName resolves the PARENT's session name and the
// launch silently attaches to that session instead of starting the project's
// own. An umbrella project directory holding many sibling repos (the
// ~/src/acme layout) hits this on essentially every launch, because
// "cd into a repo and start it" is the normal workflow there.
//
// Note this is NOT the same situation as a dispatched window, which inherits
// the dispatcher's runtime dir on purpose: that window really is created in,
// monitored by, and chrome-rendered by the launching session, so its state
// belongs in the launching session's runtime dir. Dispatch keeps using Init.
// See internal/dispatch.buildDispatchEnv for how dispatch pins the runtime dir.
func InitForSession(baseDir string) {
	once.Do(func() { initRuntimeDir(baseDir, true) })
}

// initRuntimeDir resolves the runtime directory. It is the shared
// body of Init and InitForSession and must only be called inside once.Do.
//
// requireOwnership makes an inherited AGENTCTL_RUNTIME_DIR conditional on it
// anchoring a project that contains baseDir.
func initRuntimeDir(baseDir string, requireOwnership bool) {
	if envDir := normalizeRuntimeDir(os.Getenv("AGENTCTL_RUNTIME_DIR")); envDir != "" {
		if !requireOwnership || ownsDir(envDir, baseDir) {
			runtimeDir = envDir
			return
		}
	}
	runtimeDir = filepath.Join(baseDir, runtimeDirName)
}

// EnsureRuntimeDir creates the runtime directory (and its self-ignoring
// .gitignore) if absent. Init only RESOLVES the directory so that read-only
// invocations (--help, version, status, ...) leave nothing behind in the
// cwd; creation happens here, on first use by a writer. LogDir and
// SessionLogDir call it, so the first log write creates the directory.
// Errors are non-fatal and ignored. Safe to call repeatedly.
func EnsureRuntimeDir() {
	if runtimeDir == "" {
		return
	}
	ensureRuntimeDir(runtimeDir)
}

// selfIgnoreContents makes the runtime dir invisible to git: a `*` inside
// .agentctl/ ignores every sibling and the .gitignore itself.
const selfIgnoreContents = "# created by agentctl: runtime state, not for version control\n*\n"

// ensureRuntimeDir creates dir (0700: it holds pending tool commands, logs,
// and settings) and, if absent, a self-ignoring .gitignore in it so client
// repos never show .agentctl/ as untracked. An existing .gitignore is never
// overwritten. A dir that is a symlink is left alone (nothing is created
// through it); an existing real dir created by an older version is
// tightened to 0700. Errors are non-fatal and ignored.
func ensureRuntimeDir(dir string) {
	if err := safefs.MkdirAll(dir, dir, 0o700); err != nil {
		return
	}
	_ = os.Chmod(dir, 0o700)
	f, err := safefs.OpenNoFollow(dir, filepath.Join(dir, ".gitignore"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return
	}
	f.WriteString(selfIgnoreContents) //nolint:errcheck
	f.Close()                         //nolint:errcheck
}

// normalizeRuntimeDir repairs an AGENTCTL_RUNTIME_DIR value that names a
// project root rather than the project's ".agentctl" directory, by appending
// the missing element.
//
// Everything downstream assumes runtimeDir ends in ".agentctl": DirName()
// reads basename(dirname(runtimeDir)) to name the tmux session, and every
// subdirectory helper (LogDir, WindowsDir, AuditDir, ...) joins onto it. A
// value that stops one level short silently breaks both -- it resolves to the
// GRANDPARENT's basename (so `~/src/acme/some-repo` names the session
// "agentctl-acme", pointing every tmux call at an unrelated session) and it
// scatters logs/, windows/, audit/ and friends directly into the repo root.
// Hand-set values are the usual source; the mistake is easy to make because
// the variable reads like it wants a project directory.
//
// An empty value is returned unchanged rather than normalized: an explicit
// empty assignment is how StartHubSession deliberately clears an inherited
// runtime dir (see internal/tmux/hub.go), and callers rely on distinguishing
// "unset/cleared" from "set to something".
func normalizeRuntimeDir(dir string) string {
	if dir == "" {
		return ""
	}
	// Clean first: a trailing separator would otherwise survive into every
	// path joined onto the result, and Base would compare against a
	// non-normalized final element.
	dir = filepath.Clean(dir)
	if filepath.Base(dir) == runtimeDirName {
		return dir
	}
	return filepath.Join(dir, runtimeDirName)
}

// worktreesDirName is the directory agentctl creates per-agent git worktrees
// under, relative to the project root.
const worktreesDirName = ".worktrees"

// ownsDir reports whether baseDir belongs to the project anchored by
// runtimeDir, i.e. whether a session started from baseDir would be that same
// project's session.
//
// Containment alone is not the test, because the interesting case is an
// UMBRELLA project directory: ~/src/monorepo is itself an agentctl project (it has
// its own .agentctl and session) while also holding many sibling repos. Every
// one of those repos is contained in it yet is emphatically its own project,
// and treating containment as ownership is precisely the bug this function
// exists to prevent. So a contained directory is owned only when it is NOT a
// project in its own right:
//
//   - baseDir == projectRoot                  -> owned (the plain case)
//   - <projectRoot>/.worktrees/<branch>       -> owned (agentctl's own worktree
//     for this project; it carries a .git file, so the check below would
//     otherwise mistake it for a separate project)
//   - <projectRoot>/some/subdir               -> owned, if not a project root.
//     Launching from a subdirectory should join the enclosing project's
//     session rather than mint a session (and a stray .agentctl) per
//     subdirectory.
//   - <projectRoot>/sibling-repo              -> NOT owned; it has its own
//     .git or .agentctl and deserves its own session.
//   - anything outside projectRoot            -> NOT owned.
//
// Paths are compared after symlink evaluation so that a project reached
// through a symlinked path (/tmp vs /private/tmp on macOS, or a symlinked
// checkout) is not misjudged as foreign.
func ownsDir(runtimeDir, baseDir string) bool {
	projectRoot := resolvePath(filepath.Dir(runtimeDir))
	baseDir = resolvePath(baseDir)

	rel, err := filepath.Rel(projectRoot, baseDir)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if segments := strings.Split(rel, string(filepath.Separator)); segments[0] == worktreesDirName {
		return true
	}
	return !isProjectRoot(baseDir)
}

// isProjectRoot reports whether dir looks like the root of a project that
// warrants its own agentctl session: it either already has a runtime dir, or
// it is a git checkout (.git is a directory in a normal clone and a file in a
// worktree or submodule, so only existence is checked).
func isProjectRoot(dir string) bool {
	for _, marker := range []string{runtimeDirName, ".git"} {
		if _, err := os.Lstat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// resolvePath returns the absolute, symlink-resolved form of path, falling
// back to the cleaned input when either step fails (a runtime dir that does
// not exist yet is normal and must not defeat the comparison).
func resolvePath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// RuntimeDir returns the base .agentctl/ directory.
func RuntimeDir() string { return runtimeDir }

// ProjectDir returns the project root -- the parent of .agentctl/, i.e. the
// directory a session was started from. Returns "" if Init has not been
// called.
func ProjectDir() string {
	if runtimeDir == "" {
		return ""
	}
	return filepath.Dir(runtimeDir)
}

// CwdPinEnv marks a process that was spawned by a tmux keybinding and must
// relocate itself into the project root before doing anything cwd-sensitive.
//
// It carries no value of its own -- deliberately. A keybinding cannot carry a
// project-specific value at all: `bind-key` is SERVER-global, so every
// agentctl session on the machine binds the same keys and whichever session
// started last wins for all of them. The only per-session channel that
// survives that is the tmux SESSION environment, which both `run-shell` jobs
// and `display-popup` panes inherit, so AGENTCTL_RUNTIME_DIR arrives that way
// (see tmux.EnsureSessionEnv) and this flag says only "trust it over your
// cwd". The obvious alternative -- `run-shell -c` / `display-popup -d` with
// #{session_name} spliced in -- does not work: tmux format-expands a
// run-shell COMMAND but not its -c flag, and display-popup expands neither
// its command nor -d/-e (verified on tmux 3.6a).
const CwdPinEnv = "AGENTCTL_PIN_CWD"

// HonorCwdPin chdirs the process into ProjectDir() when CwdPinEnv is set, and
// unsets the variable so the pin can never be inherited by a child process
// (an agent launched from a keybinding runs in a worktree on purpose, and
// must not be dragged back to the project root).
//
// Call once, immediately after Init/InitForSession: the destination is
// derived from the resolved runtime dir, and when that came from cwd rather
// than the environment the chdir is a no-op by construction.
func HonorCwdPin() {
	if os.Getenv(CwdPinEnv) == "" {
		return
	}
	os.Unsetenv(CwdPinEnv) //nolint:errcheck
	dir := ProjectDir()
	if dir == "" {
		return
	}
	os.Chdir(dir) //nolint:errcheck // best-effort: a missing project root leaves cwd as-is
}

// LogDir returns .agentctl/logs/. Returns "" if Init has not been called.
func LogDir() string {
	if runtimeDir == "" {
		return ""
	}
	EnsureRuntimeDir()
	return filepath.Join(runtimeDir, "logs")
}

// SessionLogDir returns .agentctl/logs/sessions/. Returns "" if Init has not been called.
func SessionLogDir() string {
	if runtimeDir == "" {
		return ""
	}
	EnsureRuntimeDir()
	return filepath.Join(runtimeDir, "logs", "sessions")
}

// WindowsDir returns .agentctl/windows/. Returns "" if Init has not been called.
func WindowsDir() string {
	if runtimeDir == "" {
		return ""
	}
	return filepath.Join(runtimeDir, "windows")
}

// SettingsDir returns .agentctl/settings/. Returns "" if Init has not been called.
func SettingsDir() string {
	if runtimeDir == "" {
		return ""
	}
	return filepath.Join(runtimeDir, "settings")
}

// AuditDir returns .agentctl/audit/, where internal/audit writes one JSONL
// file per Claude Code session (<session-id>.jsonl). Returns "" if Init has
// not been called.
func AuditDir() string {
	if runtimeDir == "" {
		return ""
	}
	return filepath.Join(runtimeDir, "audit")
}

// InboxDir returns .agentctl/inbox/, the root of the durable mailbox store
// (see internal/mailbox and docs/agent-messaging-design.md). Returns "" if
// Init has not been called.
func InboxDir() string {
	if runtimeDir == "" {
		return ""
	}
	return filepath.Join(runtimeDir, "inbox")
}

// DirName returns the basename of the project directory (the parent of
// .agentctl/). This is used for display purposes (tmux session name, menu bar).
// Returns "" if Init has not been called.
func DirName() string {
	if runtimeDir == "" {
		return ""
	}
	return filepath.Base(filepath.Dir(runtimeDir))
}

// DataDir returns ~/.local/share/agentctl, creating it if needed.
// This follows XDG data directory conventions and stores derived/computed data
// (not user-editable config). It operates independently of Init/RuntimeDir.
func DataDir() string {
	dataDirOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			dataDir = filepath.Join(os.TempDir(), "agentctl-data")
		} else {
			dataDir = filepath.Join(home, ".local", "share", "agentctl")
		}
		os.MkdirAll(dataDir, 0o755) //nolint:errcheck
	})
	return dataDir
}

// SessionDataDir returns the directory where session metadata sidecar files are
// stored: ~/.local/share/agentctl/sessions/
func SessionDataDir() string {
	d := filepath.Join(DataDir(), "sessions")
	os.MkdirAll(d, 0o755) //nolint:errcheck
	return d
}

// ResetForTest resets the once guard so Init can be called again in tests.
// Only for use in tests.
func ResetForTest() {
	once = sync.Once{}
	runtimeDir = ""
}

// ResetDataDirForTest resets the data directory so DataDir/SessionDataDir can
// be called again in tests.
func ResetDataDirForTest() {
	dataDirOnce = sync.Once{}
	dataDir = ""
}

// ExpandHome expands a leading "~/" in path to the user's home directory.
// Any other path (absolute, relative, bare "~", empty) is returned
// unchanged, as is the input when the home directory cannot be resolved.
func ExpandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
