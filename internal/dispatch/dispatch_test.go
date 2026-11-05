package dispatch

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/hooks"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/worktree"
)

func TestBuildBranch_Issue(t *testing.T) {
	req := Request{
		AgentName: "go-engineer",
		Ticket:    42,
	}
	got := buildBranch(req)
	want := "agent/go-engineer/issue-42"
	if got != want {
		t.Errorf("buildBranch() = %q, want %q", got, want)
	}
}

func TestBuildBranch_AdHoc(t *testing.T) {
	req := Request{
		AgentName: "go-engineer",
		Ticket:    0,
	}
	got := buildBranch(req)

	// Should match pattern: agent/go-engineer/adhoc-<8hexchars>
	prefix := "agent/go-engineer/adhoc-"
	if len(got) != len(prefix)+8 {
		t.Errorf("buildBranch() = %q, want length %d (prefix + 8 hex chars)", got, len(prefix)+8)
	}
	if got[:len(prefix)] != prefix {
		t.Errorf("buildBranch() prefix = %q, want %q", got[:len(prefix)], prefix)
	}
	// Verify the slug is hex.
	slug := got[len(prefix):]
	for _, ch := range slug {
		if !('0' <= ch && ch <= '9') && !('a' <= ch && ch <= 'f') {
			t.Errorf("non-hex character %q in ad-hoc slug %q", ch, slug)
		}
	}
}

// TestBuildBranch_TicketWinsOverModeBare asserts that a ticket number takes
// priority over ModeBare's "session-" prefix -- an issue number is always the
// more useful identifier, even for an interactive bare session that happens
// to carry one.
func TestBuildBranch_TicketWinsOverModeBare(t *testing.T) {
	req := Request{
		AgentName: "go-engineer",
		Mode:      ModeBare,
		Ticket:    42,
	}
	got := buildBranch(req)
	want := "agent/go-engineer/issue-42"
	if got != want {
		t.Errorf("buildBranch() = %q, want %q", got, want)
	}
}

// TestBuildBranch_ModeBareNoTicket asserts ModeBare with no ticket gets the
// "session-" prefix so a human can distinguish an interactive orchestrator
// session's branch from real autonomous work at a glance.
func TestBuildBranch_ModeBareNoTicket(t *testing.T) {
	req := Request{
		AgentName: "go-engineer",
		Mode:      ModeBare,
	}
	got := buildBranch(req)

	prefix := "agent/go-engineer/session-"
	if len(got) != len(prefix)+8 {
		t.Errorf("buildBranch() = %q, want length %d (prefix + 8 hex chars)", got, len(prefix)+8)
	}
	if got[:len(prefix)] != prefix {
		t.Errorf("buildBranch() prefix = %q, want %q", got[:len(prefix)], prefix)
	}
}

// TestBuildBranch_ModeAdhocPrefix asserts ModeAdhoc keeps the historical
// "adhoc-" prefix (as does every other ticketless, non-bare mode).
func TestBuildBranch_ModeAdhocPrefix(t *testing.T) {
	req := Request{
		AgentName: "go-engineer",
		Mode:      ModeAdhoc,
	}
	got := buildBranch(req)

	prefix := "agent/go-engineer/adhoc-"
	if len(got) != len(prefix)+8 {
		t.Errorf("buildBranch() = %q, want length %d (prefix + 8 hex chars)", got, len(prefix)+8)
	}
	if got[:len(prefix)] != prefix {
		t.Errorf("buildBranch() prefix = %q, want %q", got[:len(prefix)], prefix)
	}
}

// TestResolveUseWorktree is a table test for the mode-default-with-override
// resolution logic at the top of Launch(). It exercises the same three-way
// decision inline (nil / explicit true / explicit false) since Launch()
// itself requires a live tmux session to run to completion.
func TestResolveUseWorktree(t *testing.T) {
	trueVal := true
	falseVal := false

	resolve := func(mode Mode, override *bool) bool {
		spec, ok := modes[mode]
		if !ok {
			t.Fatalf("mode %q not registered", mode)
		}
		useWorktree := spec.DefaultWorktree
		if override != nil {
			useWorktree = *override
		}
		return useWorktree
	}

	tests := []struct {
		name     string
		mode     Mode
		override *bool
		want     bool
	}{
		{"nil request yields ModeDispatch default (true)", ModeDispatch, nil, true},
		{"nil request yields ModeAdhoc default (true)", ModeAdhoc, nil, true},
		{"nil request yields ModeBare default (false)", ModeBare, nil, false},
		{"nil request yields ModeDiscuss default (true)", ModeDiscuss, nil, true},
		{"nil request yields ModeTriage default (true)", ModeTriage, nil, true},
		{"nil request yields ModeReview default (true)", ModeReview, nil, true},
		{"nil request yields ModeCustom default (true)", ModeCustom, nil, true},
		{"explicit true overrides ModeDispatch (already true)", ModeDispatch, &trueVal, true},
		{"explicit false overrides ModeDispatch (true -> false)", ModeDispatch, &falseVal, false},
		{"explicit false overrides ModeBare (already false)", ModeBare, &falseVal, false},
		{"explicit true overrides ModeBare (false -> true)", ModeBare, &trueVal, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolve(tc.mode, tc.override); got != tc.want {
				t.Errorf("resolveUseWorktree(%s, %v) = %v, want %v", tc.mode, tc.override, got, tc.want)
			}
		})
	}
}

// TestLaunchWorkDirResolution_BareStaysInMainCheckout exercises Launch's own
// steps 1-3 (mode lookup, useWorktree resolution, conditional
// worktree.Create) against a real git repo -- the same logic Launch() runs,
// short of the tmux launch itself which requires a live session and can't run
// in a unit test. It is the regression guard for the revert: a bare launch's
// resolved working directory must be the main checkout, never a
// ".worktrees/" path, while a dispatch launch (which still defaults to
// worktree-isolated) must land in one.
func TestLaunchWorkDirResolution_BareStaysInMainCheckout(t *testing.T) {
	repoDir := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
		}
	}
	gitRun("init")
	gitRun("config", "user.email", "test@example.com")
	gitRun("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "README.md")
	gitRun("commit", "-m", "initial commit")

	resolveWorkDir := func(mode Mode, req Request) string {
		t.Helper()
		spec, ok := modes[mode]
		if !ok {
			t.Fatalf("mode %q not registered", mode)
		}
		useWorktree := spec.DefaultWorktree
		if req.UseWorktree != nil {
			useWorktree = *req.UseWorktree
		}
		workDir := repoDir
		if useWorktree {
			branch := buildBranch(req)
			wtPath, err := worktree.Create(repoDir, branch)
			if err != nil {
				t.Fatalf("worktree.Create: %v", err)
			}
			workDir = wtPath
		}
		return workDir
	}

	t.Run("bare default resolves to main checkout", func(t *testing.T) {
		got := resolveWorkDir(ModeBare, Request{AgentName: "lead", Mode: ModeBare})
		if got != repoDir {
			t.Errorf("bare workDir = %q, want main checkout %q", got, repoDir)
		}
		if strings.Contains(got, ".worktrees") {
			t.Errorf("bare workDir = %q, should not contain .worktrees", got)
		}
	})

	t.Run("dispatch default resolves to a worktree", func(t *testing.T) {
		got := resolveWorkDir(ModeDispatch, Request{AgentName: "go-engineer", Mode: ModeDispatch, Ticket: 1})
		if got == repoDir {
			t.Errorf("dispatch workDir = %q, want a worktree distinct from main checkout", got)
		}
		if !strings.Contains(got, ".worktrees") {
			t.Errorf("dispatch workDir = %q, want a path under .worktrees", got)
		}
	})
}

func TestApplySpawnedByEnv_PopulatedFromEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-abc-123")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "@36")

	extraEnv := map[string]string{}
	applySpawnedByEnv(extraEnv)

	if got := extraEnv["AGENTCTL_SPAWNED_BY"]; got != "@36" {
		t.Errorf("AGENTCTL_SPAWNED_BY = %q, want %q", got, "@36")
	}
	if got := extraEnv["AGENTCTL_SPAWNED_BY_SESSION"]; got != "sess-abc-123" {
		t.Errorf("AGENTCTL_SPAWNED_BY_SESSION = %q, want %q", got, "sess-abc-123")
	}
}

func TestApplySpawnedByEnv_AbsentWhenEnvEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "")

	extraEnv := map[string]string{}
	applySpawnedByEnv(extraEnv)

	if v, ok := extraEnv["AGENTCTL_SPAWNED_BY"]; ok {
		t.Errorf("AGENTCTL_SPAWNED_BY should be absent, got %q", v)
	}
	if v, ok := extraEnv["AGENTCTL_SPAWNED_BY_SESSION"]; ok {
		t.Errorf("AGENTCTL_SPAWNED_BY_SESSION should be absent, got %q", v)
	}
}

// --- buildDispatchEnv / AGENTCTL_RUNTIME_DIR ---
//
// buildDispatchEnv's invariant is session ownership: a dispatched window's
// runtime dir must be the one whose paths.DirName() names the tmux session
// the window actually lives in -- the dispatcher's own paths.RuntimeDir(),
// inherited verbatim, whenever one is set. The target repo is only used as a
// fallback anchor when no runtime dir is available at all (e.g. dispatch
// invoked before paths.Init).
//
// withRuntimeDir is a small test helper that pins paths.RuntimeDir() (via
// paths.Init + AGENTCTL_RUNTIME_DIR) for the duration of one subtest and
// restores it afterward, since buildDispatchEnv now reads it as package
// state rather than taking it as a parameter.
func withRuntimeDir(t *testing.T, dir string) {
	t.Helper()
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	if dir == "" {
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")
		// Deliberately do not call paths.Init: RuntimeDir() stays "".
		return
	}
	t.Setenv("AGENTCTL_RUNTIME_DIR", dir)
	paths.Init(t.TempDir()) // baseDir is irrelevant once AGENTCTL_RUNTIME_DIR wins
}

func TestBuildDispatchEnv_RuntimeDir(t *testing.T) {
	tests := []struct {
		name          string
		req           Request
		useWorktree   bool
		workDir       string
		repoDir       string
		branch        string
		sessionRuntim string // paths.RuntimeDir() to pin; "" means leave unset
		wantRuntime   string
	}{
		{
			// This is the umbrella-directory regression from the live
			// umbrella incident: the launching session's project dir holds
			// several independent repos, so repoDir (one of those repos) is
			// NOT the session's own project dir. AGENTCTL_RUNTIME_DIR must
			// still be the session's runtime dir -- re-deriving it from
			// repoDir is exactly what produced the empty tabs row / missing
			// chrome bar pane.
			name:          "umbrella session dir: runtime dir is the SESSION's, not the target repo's",
			req:           Request{AgentName: "go-engineer", Mode: ModeAdhoc},
			useWorktree:   false,
			workDir:       "/Users/alice/src/acme/repo-b",
			repoDir:       "/Users/alice/src/acme/repo-b",
			branch:        "",
			sessionRuntim: filepath.Join("/Users/alice/src/acme", ".agentctl"),
			wantRuntime:   filepath.Join("/Users/alice/src/acme", ".agentctl"),
		},
		{
			name:          "non-worktree dispatch: runtime dir is the session's, not workDir's",
			req:           Request{AgentName: "go-engineer", Mode: ModeAdhoc},
			useWorktree:   false,
			workDir:       "/repo",
			repoDir:       "/repo",
			branch:        "",
			sessionRuntim: filepath.Join("/repo", ".agentctl"),
			wantRuntime:   filepath.Join("/repo", ".agentctl"),
		},
		{
			name:          "worktree dispatch: runtime dir is the session's, not the worktree's",
			req:           Request{AgentName: "go-engineer", Mode: ModeAdhoc},
			useWorktree:   true,
			workDir:       "/repo/.worktrees/agent-go-engineer-adhoc-1234",
			repoDir:       "/repo",
			branch:        "agent/go-engineer/adhoc-1234",
			sessionRuntim: filepath.Join("/repo", ".agentctl"),
			wantRuntime:   filepath.Join("/repo", ".agentctl"),
		},
		{
			// A cross-project --dir dispatch still records its state in the
			// LAUNCHING session's runtime dir -- that is the session that
			// owns the window, not the target project.
			name:          "--dir into another project: runtime dir follows the launching session, not the target project",
			req:           Request{AgentName: "devops-engineer", Mode: ModeAdhoc},
			useWorktree:   false,
			workDir:       "/Users/alice/src/acme/some-other-project",
			repoDir:       "/Users/alice/src/acme/some-other-project",
			branch:        "",
			sessionRuntim: filepath.Join("/Users/alice/code/mine", ".agentctl"),
			wantRuntime:   filepath.Join("/Users/alice/code/mine", ".agentctl"),
		},
		{
			// Empty-runtime-dir fallback: dispatch invoked with no resolvable
			// paths.RuntimeDir() (e.g. before paths.Init runs) falls back to
			// the target repo's own .agentctl, preserving the pre-existing
			// single-repo behavior.
			name:          "no session runtime dir: falls back to worktree.MainRepoRoot(repoDir)/.agentctl",
			req:           Request{AgentName: "devops-engineer", Mode: ModeDispatch, Ticket: 42},
			useWorktree:   true,
			workDir:       "/Users/alice/src/acme/some-other-project/.worktrees/agent-devops-engineer-issue-42",
			repoDir:       "/Users/alice/src/acme/some-other-project",
			branch:        "agent/devops-engineer/issue-42",
			sessionRuntim: "",
			wantRuntime:   filepath.Join("/Users/alice/src/acme/some-other-project", ".agentctl"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withRuntimeDir(t, tc.sessionRuntim)
			env := buildDispatchEnv(tc.req, config.AgentConfig{}, tc.workDir, tc.repoDir, tc.branch, tc.useWorktree)
			if got := env["AGENTCTL_RUNTIME_DIR"]; got != tc.wantRuntime {
				t.Errorf("AGENTCTL_RUNTIME_DIR = %q, want %q", got, tc.wantRuntime)
			}
		})
	}
}

// TestBuildDispatchEnv_UmbrellaSessionDir is the regression test for the live
// umbrella incident: a tmux session's project dir is an umbrella holding
// several independent repos (so it is NOT itself a git repo), and dispatch
// targets one of the inner repos. AGENTCTL_RUNTIME_DIR must equal the
// SESSION's runtime dir, never repoDir/.agentctl -- re-deriving it from the
// inner repo is exactly what produced the empty tabs row / 0% ctx chrome bar
// (and, when the inner repo's basename didn't collide with the session's,
// the missing chrome bar pane entirely).
func TestBuildDispatchEnv_UmbrellaSessionDir(t *testing.T) {
	sessionRuntimeDir := filepath.Join("/Users/alice/src/acme", ".agentctl")
	withRuntimeDir(t, sessionRuntimeDir)

	req := Request{AgentName: "go-engineer", Mode: ModeAdhoc}
	repoDir := "/Users/alice/src/acme/repo-b" // its own repo, NOT the session's project dir
	env := buildDispatchEnv(req, config.AgentConfig{}, repoDir, repoDir, "", false)

	if got := env["AGENTCTL_RUNTIME_DIR"]; got != sessionRuntimeDir {
		t.Errorf("AGENTCTL_RUNTIME_DIR = %q, want the session's runtime dir %q (not repoDir/.agentctl = %q)",
			got, sessionRuntimeDir, filepath.Join(repoDir, ".agentctl"))
	}
}

// TestBuildDispatchEnv_EmptyRuntimeDirFallback verifies that when the
// dispatching process has no resolvable paths.RuntimeDir() at all (e.g.
// dispatch invoked before paths.Init has run), buildDispatchEnv falls back to
// worktree.MainRepoRoot(repoDir)/.agentctl -- preserving the pre-existing single-repo
// behavior exactly.
func TestBuildDispatchEnv_EmptyRuntimeDirFallback(t *testing.T) {
	withRuntimeDir(t, "")

	req := Request{AgentName: "go-engineer", Mode: ModeAdhoc}
	repoDir := t.TempDir()
	if out, err := exec.Command("git", "-C", repoDir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	env := buildDispatchEnv(req, config.AgentConfig{}, repoDir, repoDir, "", false)

	want := filepath.Join(worktree.MainRepoRoot(repoDir), ".agentctl")
	if got := env["AGENTCTL_RUNTIME_DIR"]; got != want {
		t.Errorf("AGENTCTL_RUNTIME_DIR = %q, want fallback %q", got, want)
	}
}

func TestBuildDispatchEnv_OtherKeysUnaffected(t *testing.T) {
	// No session runtime dir pinned -- exercises the fallback path so this
	// test's expectations don't depend on ambient paths.RuntimeDir() state
	// left behind by other tests in the package.
	withRuntimeDir(t, "")

	req := Request{
		AgentName:   "go-engineer",
		Mode:        ModeDispatch,
		Ticket:      7,
		TicketTitle: "fix the thing",
		RepoSlug:    "blsmth/agentctl",
	}
	env := buildDispatchEnv(req, config.AgentConfig{}, "/repo/.worktrees/x", "/repo", "agent/go-engineer/issue-7", true)

	wantKV := map[string]string{
		"AGENTCTL_REPO":         "blsmth/agentctl",
		"AGENTCTL_WORKTREE":     "/repo/.worktrees/x",
		"AGENTCTL_BRANCH":       "agent/go-engineer/issue-7",
		"AGENTCTL_TICKET":       "7",
		"AGENTCTL_TICKET_TITLE": "fix the thing",
		"AGENTCTL_MODE":         "dispatch",
		"AGENTCTL_RUNTIME_DIR":  filepath.Join("/repo", ".agentctl"),
	}
	for k, want := range wantKV {
		if got := env[k]; got != want {
			t.Errorf("env[%q] = %q, want %q", k, got, want)
		}
	}
}

// TestBuildDispatchEnv_AnchorSubdirectoryNormalizedToRepoRoot exercises the
// empty-runtime-dir FALLBACK path only (no session paths.RuntimeDir() set):
// a dispatcher whose cwd (or --dir) was itself a subdirectory of a git repo
// -- e.g. it had cd-ed into <repo>/.agentctl/logs to read logs before
// dispatching -- must still fall back to AGENTCTL_RUNTIME_DIR =
// <repo-root>/.agentctl, never <subdir>/.agentctl. An un-normalized runtime
// dir under a subdirectory produces a bogus tmux session name downstream
// (paths.DirName() returns the subdirectory's own basename), and every tmux
// call thereafter fails. When a session runtime dir IS available this
// subdirectory normalization is moot -- buildDispatchEnv inherits the
// session's runtime dir verbatim and never touches repoDir at all; see
// TestBuildDispatchEnv_RuntimeDir for that behavior.
func TestBuildDispatchEnv_AnchorSubdirectoryNormalizedToRepoRoot(t *testing.T) {
	withRuntimeDir(t, "") // force the fallback path

	repoRoot := t.TempDir()
	// Resolve symlinks up front (macOS temp dirs are under /var, a symlink to
	// /private/var) so the comparison below matches what `git -C` -- which
	// resolves paths for real -- actually returns.
	repoRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if out, err := exec.Command("git", "-C", repoRoot, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	subdir := filepath.Join(repoRoot, ".agentctl", "logs")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	req := Request{AgentName: "go-engineer", Mode: ModeAdhoc}
	env := buildDispatchEnv(req, config.AgentConfig{}, subdir, subdir, "", false)

	want := filepath.Join(repoRoot, ".agentctl")
	if got := env["AGENTCTL_RUNTIME_DIR"]; got != want {
		t.Errorf("AGENTCTL_RUNTIME_DIR = %q, want %q (repo root, not subdirectory)", got, want)
	}
}

// TestMainRepoRoot_NonGitDirFallsBackUnchanged verifies MainRepoRoot degrades
// gracefully -- rather than erroring the dispatch -- when dir is not inside a
// git repository at all.
func TestMainRepoRoot_NonGitDirFallsBackUnchanged(t *testing.T) {
	dir := t.TempDir()
	if got := worktree.MainRepoRoot(dir); got != dir {
		t.Errorf("worktree.MainRepoRoot(%q) = %q, want unchanged", dir, got)
	}
}

// TestWorktreeDispatch_SingleRuntimeDir is an end-to-end (minus tmux itself)
// regression test for the split-brain bug: it proves that once
// AGENTCTL_RUNTIME_DIR is threaded through, a worktree-dispatched agent's own
// process (hooks, its own future dispatches, etc.) resolves paths.RuntimeDir()
// to the exact same directory as the dispatching process and the monitor --
// i.e. exactly ONE .agentctl/windows/ directory backs a given window, never
// two. This can't exercise real tmux here, but it exercises the full
// env-var contract that fixes the bug: buildDispatchEnv's output is what
// tmux.LaunchAgent forwards as a real env var into the launched window, and
// paths.Init is what every process (dispatcher, hook, monitor) calls on
// startup.
func TestWorktreeDispatch_SingleRuntimeDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	repoDir := t.TempDir()
	workDir := filepath.Join(repoDir, ".worktrees", "agent-go-engineer-adhoc-1234")
	req := Request{AgentName: "go-engineer", Mode: ModeAdhoc}
	branch := "agent/go-engineer/adhoc-1234"

	// Simulate the dispatching process itself: it has already run paths.Init
	// for its own session/project (repoDir here, since this is the
	// single-repo case) before Launch() ever calls buildDispatchEnv. This is
	// the actual invariant under test post-fix: buildDispatchEnv inherits
	// paths.RuntimeDir() from the live dispatcher process rather than
	// re-deriving anything from repoDir/workDir.
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(repoDir)
	dispatcherRuntimeDir := paths.RuntimeDir()

	extraEnv := buildDispatchEnv(req, config.AgentConfig{}, workDir, repoDir, branch, true)
	runtimeDirEnvValue := extraEnv["AGENTCTL_RUNTIME_DIR"]
	if runtimeDirEnvValue == "" {
		t.Fatal("buildDispatchEnv did not set AGENTCTL_RUNTIME_DIR")
	}
	if runtimeDirEnvValue != dispatcherRuntimeDir {
		t.Fatalf("buildDispatchEnv AGENTCTL_RUNTIME_DIR = %q, want the dispatcher's own paths.RuntimeDir() %q",
			runtimeDirEnvValue, dispatcherRuntimeDir)
	}

	// Simulate the monitor: cwd is forced to repoDir (as tmux.applyConfig
	// does via `tmux run-shell -c projectRoot`), no env var override needed
	// or present.
	paths.ResetForTest()
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(repoDir)
	monitorRuntimeDir := paths.RuntimeDir()

	// Simulate a hook running inside the dispatched agent's window: cwd is
	// the WORKTREE (the actual split-brain trigger), but AGENTCTL_RUNTIME_DIR
	// is set on the window's environment exactly as buildDispatchEnv computed
	// it, and survives every `cd` a hook's subprocess might do.
	paths.ResetForTest()
	t.Setenv("AGENTCTL_RUNTIME_DIR", runtimeDirEnvValue)
	paths.Init(workDir)
	hookRuntimeDir := paths.RuntimeDir()

	if hookRuntimeDir != monitorRuntimeDir {
		t.Fatalf("split-brain reproduced: hook runtime dir %q != monitor runtime dir %q",
			hookRuntimeDir, monitorRuntimeDir)
	}
	wantDir := filepath.Join(repoDir, ".agentctl")
	if hookRuntimeDir != wantDir {
		t.Errorf("runtime dir = %q, want %q", hookRuntimeDir, wantDir)
	}

	// The worktree must NOT have gotten its own .agentctl -- that's the
	// second file the bug produced.
	if _, err := os.Stat(filepath.Join(workDir, ".agentctl")); err == nil {
		t.Error("a second .agentctl/ was created under the worktree -- split-brain not fixed")
	}
}

func TestModeLogPrefix(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want string
	}{
		{"issue", Request{Ticket: 42}, "#42"},
		{"adhoc", Request{Ticket: 0}, "adhoc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := modeLogPrefix(tc.req)
			if got != tc.want {
				t.Errorf("modeLogPrefix() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLaunch_UnknownMode(t *testing.T) {
	_, err := Launch(Request{
		AgentName: "test",
		Mode:      "nonexistent",
		WorkDir:   "/tmp",
	})
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
	if got := err.Error(); got != `unknown dispatch mode: "nonexistent"` {
		t.Errorf("error = %q, want unknown mode error", got)
	}
}

func TestLaunch_MissingWorkDir(t *testing.T) {
	_, err := Launch(Request{
		AgentName: "test",
		Mode:      ModeDispatch,
	})
	if err == nil {
		t.Fatal("expected error for missing WorkDir")
	}
}

func TestModeBare_Spec(t *testing.T) {
	spec, ok := modes[ModeBare]
	if !ok {
		t.Fatal("ModeBare not registered in modes map")
	}
	t.Run("not autonomous", func(t *testing.T) {
		if spec.Autonomous {
			t.Error("ModeBare should not be autonomous")
		}
	})
	t.Run("does not include ticket body", func(t *testing.T) {
		if spec.IncludeTicketBody {
			t.Error("ModeBare should not include ticket body")
		}
	})
	t.Run("empty default user message", func(t *testing.T) {
		req := Request{AgentName: "go-engineer", Mode: ModeBare}
		if msg := spec.DefaultUserMessage(req); msg != "" {
			t.Errorf("ModeBare default user message should be empty, got %q", msg)
		}
	})
	t.Run("empty system instructions", func(t *testing.T) {
		req := Request{AgentName: "go-engineer", Mode: ModeBare}
		if instr := spec.SystemInstructions(req); instr != "" {
			t.Errorf("ModeBare system instructions should be empty, got %q", instr)
		}
	})
}

func TestModeDefaultModel_AppliedWhenAgentConfigEmpty(t *testing.T) {
	// ModeDispatch has DefaultModel = "sonnet".
	spec := modes[ModeDispatch]
	if spec.DefaultModel != "sonnet" {
		t.Fatalf("expected ModeDispatch.DefaultModel = %q, got %q", "sonnet", spec.DefaultModel)
	}

	// Simulate the model-resolution logic from Launch().
	ac := config.AgentConfig{Model: ""}
	req := Request{Model: ""}
	if req.Model != "" {
		ac.Model = req.Model
	} else if ac.Model == "" && spec.DefaultModel != "" {
		ac.Model = spec.DefaultModel
	}

	if ac.Model != "sonnet" {
		t.Errorf("expected model = %q after applying mode default, got %q", "sonnet", ac.Model)
	}
}

func TestModeDefaultModel_RequestOverridesAll(t *testing.T) {
	spec := modes[ModeDispatch] // DefaultModel = "sonnet"
	ac := config.AgentConfig{Model: "opus"}
	req := Request{Model: "haiku"}

	if req.Model != "" {
		ac.Model = req.Model
	} else if ac.Model == "" && spec.DefaultModel != "" {
		ac.Model = spec.DefaultModel
	}

	if ac.Model != "haiku" {
		t.Errorf("expected Request.Model to override all, got %q", ac.Model)
	}
}

func TestModeDefaultModel_AgentConfigTakesPrecedenceOverModeDefault(t *testing.T) {
	spec := modes[ModeDispatch] // DefaultModel = "sonnet"
	ac := config.AgentConfig{Model: "opus"}
	req := Request{Model: ""}

	if req.Model != "" {
		ac.Model = req.Model
	} else if ac.Model == "" && spec.DefaultModel != "" {
		ac.Model = spec.DefaultModel
	}

	// Agent config had "opus" — mode default should NOT override it.
	if ac.Model != "opus" {
		t.Errorf("expected agent config model to take precedence over mode default, got %q", ac.Model)
	}
}

// --- BuildAgentSettingsJSON ---

func TestBuildAgentSettingsJSON_Structure(t *testing.T) {
	ac := config.AgentConfig{}
	data, err := BuildAgentSettingsJSON(ac, true)
	if err != nil {
		t.Fatalf("BuildAgentSettingsJSON() unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("BuildAgentSettingsJSON() returned empty data")
	}

	// Parse into a generic map so we can inspect the hook keys.
	var parsed struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("BuildAgentSettingsJSON() invalid JSON: %v", err)
	}

	wantHooks := []string{"PreToolUse", "PostToolUse", "SessionStart", "SessionEnd", "Notification"}
	for _, key := range wantHooks {
		if _, ok := parsed.Hooks[key]; !ok {
			t.Errorf("BuildAgentSettingsJSON(): missing hook key %q", key)
		}
	}
}

func TestBuildAgentSettingsJSON_CommandsContainBinaryPath(t *testing.T) {
	ac := config.AgentConfig{}
	data, err := BuildAgentSettingsJSON(ac, true)
	if err != nil {
		t.Fatalf("BuildAgentSettingsJSON() unexpected error: %v", err)
	}

	// The hook commands must reference an agentctl binary path.
	// Since os.Executable() returns the test binary path during tests,
	// just verify that the JSON contains "hook-pre-tool" and "hook-post-tool".
	s := string(data)
	for _, sub := range []string{"hook-pre-tool", "hook-post-tool", "hook-session-start"} {
		if !strings.Contains(s, sub) {
			t.Errorf("BuildAgentSettingsJSON(): JSON missing expected hook subcommand %q", sub)
		}
	}
}

func TestBuildAgentSettingsJSON_AutoAcceptPermissions(t *testing.T) {
	ac := config.AgentConfig{AutoAccept: true}
	data, err := BuildAgentSettingsJSON(ac, true)
	if err != nil {
		t.Fatalf("BuildAgentSettingsJSON() unexpected error: %v", err)
	}

	var parsed struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	found := false
	for _, a := range parsed.Permissions.Allow {
		if a == "Bash(*)" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("BuildAgentSettingsJSON() with AutoAccept=true: expected Bash(*) in allow list, got %v", parsed.Permissions.Allow)
	}
}

// TestBuildAgentSettingsJSON_AutoAcceptAllowRules is a table-driven check that
// the auto_accept allow list contains only valid Claude Code permission
// rules. Claude Code rejects a bare "mcp__*" allow rule (an allow pattern
// must name the scope it widens — globs are only permitted in the tool
// position after a literal "mcp__<server>__" prefix), which produced a
// settings warning banner on every agent launch. See #<issue>.
func TestBuildAgentSettingsJSON_AutoAcceptAllowRules(t *testing.T) {
	ac := config.AgentConfig{AutoAccept: true}
	data, err := BuildAgentSettingsJSON(ac, true)
	if err != nil {
		t.Fatalf("BuildAgentSettingsJSON() unexpected error: %v", err)
	}

	var parsed struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	tests := []struct {
		name string
		want bool
		rule string
	}{
		{name: "contains Bash(*)", want: true, rule: "Bash(*)"},
		{name: "contains Write(*)", want: true, rule: "Write(*)"},
		{name: "contains NotebookEdit(*)", want: true, rule: "NotebookEdit(*)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := false
			for _, a := range parsed.Permissions.Allow {
				if a == tt.rule {
					got = true
					break
				}
			}
			if got != tt.want {
				t.Errorf("allow list contains %q = %v, want %v (allow=%v)", tt.rule, got, tt.want, parsed.Permissions.Allow)
			}
		})
	}

	t.Run("no invalid mcp__ wildcard rule", func(t *testing.T) {
		for _, a := range parsed.Permissions.Allow {
			if strings.HasPrefix(a, "mcp__") {
				t.Errorf("allow list contains invalid mcp__ rule %q; Claude Code rejects a bare mcp__* allow rule", a)
			}
		}
	})
}

// TestBuildAgentSettingsJSON_StopHookPresent guards against the historical
// bug where dispatch's hand-maintained hook map omitted "Stop" entirely (it
// had "StopFailure", which made the omission look intentional — it wasn't).
// Without a Stop hook, hook-check-done never fires, windowstate.Done is
// never set, there's no done badge, dispatch.HandleComplete never runs, and
// `dispatch --wait` hangs forever. See #<issue>.
func TestBuildAgentSettingsJSON_StopHookPresent(t *testing.T) {
	ac := config.AgentConfig{}
	data, err := BuildAgentSettingsJSON(ac, true)
	if err != nil {
		t.Fatalf("BuildAgentSettingsJSON() unexpected error: %v", err)
	}

	var parsed struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("BuildAgentSettingsJSON() invalid JSON: %v", err)
	}

	raw, ok := parsed.Hooks["Stop"]
	if !ok {
		t.Fatal("BuildAgentSettingsJSON(): missing \"Stop\" hook key")
	}
	if !strings.Contains(string(raw), "hook-check-done") {
		t.Errorf("BuildAgentSettingsJSON(): \"Stop\" hook does not reference hook-check-done, got %s", raw)
	}
}

// TestBuildAgentSettingsJSON_MatchesCanonicalEventSet is the drift guard:
// the set of hook event names rendered into dispatched agents' settings.json
// must exactly equal the set in the canonical internal/hooks map. This is
// what makes it impossible to silently reintroduce a second, drifted hook
// map — any addition/removal to hooks.HooksConfig() must be reflected here
// automatically since both now render from the same underlying function.
func TestBuildAgentSettingsJSON_MatchesCanonicalEventSet(t *testing.T) {
	ac := config.AgentConfig{}
	data, err := BuildAgentSettingsJSON(ac, true)
	if err != nil {
		t.Fatalf("BuildAgentSettingsJSON() unexpected error: %v", err)
	}

	var parsed struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("BuildAgentSettingsJSON() invalid JSON: %v", err)
	}

	got := make(map[string]bool, len(parsed.Hooks))
	for k := range parsed.Hooks {
		got[k] = true
	}

	canonical := hooks.HooksConfig()
	want := make(map[string]bool, len(canonical))
	for k := range canonical {
		want[k] = true
	}

	for k := range want {
		if !got[k] {
			t.Errorf("BuildAgentSettingsJSON(): missing canonical event %q (dispatch hook map has drifted from internal/hooks)", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("BuildAgentSettingsJSON(): unexpected event %q not present in canonical internal/hooks map", k)
		}
	}
}

func TestBuildAgentSettingsJSON_BlockedCommands(t *testing.T) {
	ac := config.AgentConfig{BlockedCommands: []string{"rm -rf"}}
	data, err := BuildAgentSettingsJSON(ac, true)
	if err != nil {
		t.Fatalf("BuildAgentSettingsJSON() unexpected error: %v", err)
	}

	var parsed struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(parsed.Permissions.Deny) == 0 {
		t.Error("BuildAgentSettingsJSON() with BlockedCommands: expected non-empty deny list")
	}
}

// --- WriteWorktreeSettings ---

func TestWriteWorktreeSettings_CreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	ac := config.AgentConfig{}

	if err := WriteWorktreeSettings(tmpDir, filepath.Dir(tmpDir), ac); err != nil {
		t.Fatalf("WriteWorktreeSettings() unexpected error: %v", err)
	}

	settingsPath := filepath.Join(tmpDir, ".claude", "settings.local.json")
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		t.Fatalf("WriteWorktreeSettings(): file not created at %s", settingsPath)
	}
}

func TestWriteWorktreeSettings_ValidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	ac := config.AgentConfig{}

	if err := WriteWorktreeSettings(tmpDir, filepath.Dir(tmpDir), ac); err != nil {
		t.Fatalf("WriteWorktreeSettings() unexpected error: %v", err)
	}

	settingsPath := filepath.Join(tmpDir, ".claude", "settings.local.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var parsed struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("WriteWorktreeSettings(): invalid JSON at %s: %v", settingsPath, err)
	}

	if _, ok := parsed.Hooks["PreToolUse"]; !ok {
		t.Error("WriteWorktreeSettings(): settings.local.json missing PreToolUse hook")
	}
}

func TestWriteWorktreeSettings_CreatesClaudeDir(t *testing.T) {
	tmpDir := t.TempDir()
	ac := config.AgentConfig{}

	// Ensure the .claude dir does not pre-exist.
	claudeDir := filepath.Join(tmpDir, ".claude")
	if _, err := os.Stat(claudeDir); !os.IsNotExist(err) {
		t.Fatal("precondition failed: .claude already exists")
	}

	if err := WriteWorktreeSettings(tmpDir, filepath.Dir(tmpDir), ac); err != nil {
		t.Fatalf("WriteWorktreeSettings() unexpected error: %v", err)
	}

	if _, err := os.Stat(claudeDir); os.IsNotExist(err) {
		t.Fatalf("WriteWorktreeSettings(): .claude directory not created at %s", claudeDir)
	}
}

func TestModes_AllRegistered(t *testing.T) {
	wantDefaultModels := map[Mode]string{
		ModeDispatch: "sonnet",
		ModeAdhoc:    "",
		ModeDiscuss:  "sonnet",
		ModeTriage:   "sonnet",
		ModeReview:   "opus",
		ModeCustom:   "",
		ModeBare:     "",
	}

	// wantDefaultWorktree is an explicit, per-mode expectation table -- not a
	// blanket "every mode is true" check -- so that a future change to ANY
	// mode's default (in either direction) trips a named assertion here.
	// ModeBare is the sole false: bare launches the interactive orchestrator,
	// which must run in the main checkout (see the DefaultWorktree comment on
	// ModeBare in modes.go for why).
	wantDefaultWorktree := map[Mode]bool{
		ModeDispatch: true,
		ModeAdhoc:    true,
		ModeDiscuss:  true,
		ModeTriage:   true,
		ModeReview:   true,
		ModeCustom:   true,
		ModeBare:     false,
	}

	for _, mode := range []Mode{ModeDispatch, ModeAdhoc, ModeDiscuss, ModeTriage, ModeReview, ModeCustom, ModeBare} {
		t.Run(string(mode), func(t *testing.T) {
			spec, ok := modes[mode]
			if !ok {
				t.Fatalf("mode %q not registered in modes map", mode)
			}
			if spec.SystemInstructions == nil {
				t.Error("SystemInstructions is nil")
			}
			if spec.DefaultUserMessage == nil {
				t.Error("DefaultUserMessage is nil")
			}
			if want := wantDefaultModels[mode]; spec.DefaultModel != want {
				t.Errorf("DefaultModel = %q, want %q", spec.DefaultModel, want)
			}
			if want, ok := wantDefaultWorktree[mode]; !ok {
				t.Fatalf("no DefaultWorktree expectation registered for mode %q", mode)
			} else if spec.DefaultWorktree != want {
				t.Errorf("DefaultWorktree = %v, want %v", spec.DefaultWorktree, want)
			}
		})
	}
}

// --- BuildAgentSettingsJSON: includeHooks ---

// TestBuildAgentSettingsJSON_IncludeHooksFalse is the regression guard for
// the duplicate-hooks bug: when includeHooks is false the "hooks" key must
// be absent from the JSON entirely (not an empty map, which Claude Code
// would still treat as a hooks-registering source), while permissions
// derived from AllowedPermissions and BlockedCommands are unaffected.
func TestBuildAgentSettingsJSON_IncludeHooksFalse(t *testing.T) {
	ac := config.AgentConfig{
		AllowedPermissions: []string{"WebFetch(*)"},
		BlockedCommands:    []string{"rm -rf"},
	}
	data, err := BuildAgentSettingsJSON(ac, false)
	if err != nil {
		t.Fatalf("BuildAgentSettingsJSON() unexpected error: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("BuildAgentSettingsJSON() invalid JSON: %v", err)
	}
	if _, ok := raw["hooks"]; ok {
		t.Errorf("BuildAgentSettingsJSON(includeHooks=false): JSON contains a \"hooks\" key, want it fully absent: %s", data)
	}

	var parsed struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("BuildAgentSettingsJSON() invalid JSON: %v", err)
	}
	if len(parsed.Permissions.Allow) == 0 {
		t.Error("BuildAgentSettingsJSON(includeHooks=false): expected non-empty allow list from AllowedPermissions")
	}
	if len(parsed.Permissions.Deny) == 0 {
		t.Error("BuildAgentSettingsJSON(includeHooks=false): expected non-empty deny list from BlockedCommands")
	}
}

// TestBuildAgentSettingsJSON_IncludeHooksTrue asserts hooks are present and
// that hook commands are prefixed with the resolved binary path when
// includeHooks is true.
func TestBuildAgentSettingsJSON_IncludeHooksTrue(t *testing.T) {
	ac := config.AgentConfig{}
	data, err := BuildAgentSettingsJSON(ac, true)
	if err != nil {
		t.Fatalf("BuildAgentSettingsJSON() unexpected error: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("BuildAgentSettingsJSON() invalid JSON: %v", err)
	}
	if _, ok := raw["hooks"]; !ok {
		t.Fatal("BuildAgentSettingsJSON(includeHooks=true): missing \"hooks\" key")
	}

	exe, _ := os.Executable()
	if exe == "" {
		t.Skip("os.Executable() unavailable in this environment")
	}
	if !strings.Contains(string(data), exe) {
		t.Errorf("BuildAgentSettingsJSON(includeHooks=true): hook commands do not reference resolved exe %q, got %s", exe, data)
	}
}

// --- projectHasAgentctlHooks ---

func TestProjectHasAgentctlHooks(t *testing.T) {
	tests := []struct {
		name    string
		content string // empty means "do not write a settings file at all"
		want    bool
	}{
		{
			name:    "agentctl hook registered",
			content: `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"/usr/local/bin/agentctl hook-pre-tool"}]}]}}`,
			want:    true,
		},
		{
			name:    "hooks present but not agentctl",
			content: `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"some-other-tool notify"}]}]}}`,
			want:    false,
		},
		{
			name:    "no hooks key",
			content: `{"permissions":{"allow":["Bash(*)"]}}`,
			want:    false,
		},
		{
			name:    "malformed JSON",
			content: `{not valid json`,
			want:    false,
		},
		{
			name:    "missing file",
			content: "", // no file written
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.content != "" {
				claudeDir := filepath.Join(dir, ".claude")
				if err := os.MkdirAll(claudeDir, 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				path := filepath.Join(claudeDir, "settings.local.json")
				if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}

			if got := projectHasAgentctlHooks(dir); got != tt.want {
				t.Errorf("projectHasAgentctlHooks(%q) = %v, want %v", dir, got, tt.want)
			}
		})
	}
}

// --- ancestorHasAgentctlHooks ---

// TestAncestorHasAgentctlHooks covers the ancestor-walk itself, independent
// of the WriteWorktreeSettings/writeAgentSettings callers that use it: hooks
// registered at the immediate directory, one level up, at the boundary
// itself (two levels up here), nowhere in the chain, and a hooks block
// present but not agentctl's -- which must NOT count, mirroring
// projectHasAgentctlHooks's own "agentctl hook-" substring requirement.
func TestAncestorHasAgentctlHooks(t *testing.T) {
	agentctlHooks := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"/usr/local/bin/agentctl hook-pre-tool"}]}]}}`
	nonAgentctlHooks := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"some-other-tool notify"}]}]}}`

	writeSettings := func(t *testing.T, dir, content string) {
		t.Helper()
		claudeDir := filepath.Join(dir, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	tests := []struct {
		name        string
		hooksAt     int // index into dirs below: 0=immediate (start), 1=parent, 2=grandparent/boundary; -1=nowhere
		nonAgentctl bool
		want        bool
	}{
		{name: "hooks in immediate dir", hooksAt: 0, want: true},
		{name: "hooks in parent", hooksAt: 1, want: true},
		{name: "hooks in grandparent (the boundary itself)", hooksAt: 2, want: true},
		{name: "hooks nowhere in the chain", hooksAt: -1, want: false},
		{name: "non-agentctl hooks block must not count", hooksAt: 0, nonAgentctl: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			boundary := t.TempDir()
			parent := filepath.Join(boundary, "level1")
			start := filepath.Join(parent, "level2") // mirrors <repoRoot>/.worktrees/<slug>
			if err := os.MkdirAll(start, 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}

			dirs := []string{start, parent, boundary}
			if tt.hooksAt >= 0 {
				content := agentctlHooks
				if tt.nonAgentctl {
					content = nonAgentctlHooks
				}
				writeSettings(t, dirs[tt.hooksAt], content)
			}

			if got := ancestorHasAgentctlHooks(start, boundary); got != tt.want {
				t.Errorf("ancestorHasAgentctlHooks(%q, %q) = %v, want %v", start, boundary, got, tt.want)
			}
		})
	}
}

// TestAncestorHasAgentctlHooks_StartEqualsBoundary asserts the non-worktree
// case (writeAgentSettings's callers pass workDir as its own boundary):
// exactly one directory is checked and the walk terminates immediately,
// matching projectHasAgentctlHooks's single-directory behavior.
func TestAncestorHasAgentctlHooks_StartEqualsBoundary(t *testing.T) {
	dir := t.TempDir()
	if got := ancestorHasAgentctlHooks(dir, dir); got != false {
		t.Errorf("ancestorHasAgentctlHooks(dir, dir) with no settings file = %v, want false", got)
	}

	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"/usr/local/bin/agentctl hook-pre-tool"}]}]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := ancestorHasAgentctlHooks(dir, dir); got != true {
		t.Errorf("ancestorHasAgentctlHooks(dir, dir) with agentctl hooks at dir = %v, want true", got)
	}
}

// --- Single-hooks-source regression (the bug this ticket fixes) ---

// TestDispatchWorktreePath_SingleHooksSource is the regression test that must
// never come back: for a worktree dispatch, the --settings payload
// (writeAgentSettings) and the worktree's project-local settings.local.json
// (WriteWorktreeSettings) must not both carry hooks. Claude Code merges
// hooks from every settings source it loads rather than letting one
// override another, so if both files register the full hook map, every
// hook fires twice per tool call.
//
// This is the scenario with NO conflicting ancestor: a fresh worktree
// directory whose parent chain carries no other agentctl hooks at all. See
// TestDispatchWorktreePath_SingleHooksSource_AncestorAlreadyHasHooks below
// for the actual production bug (the nested-worktree case), which this version of the test
// cannot catch by construction -- it never gave the worktree an ancestor
// settings source to conflict with.
func TestDispatchWorktreePath_SingleHooksSource(t *testing.T) {
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(t.TempDir())

	// Pin execPath to something that actually looks like the "agentctl"
	// binary -- the real os.Executable() during `go test` resolves to the
	// test binary, which would never satisfy projectHasAgentctlHooks's
	// "agentctl hook-" substring check and would make this test pass for the
	// wrong reason (or fail to catch a real regression).
	prevExecPath := execPath
	execPath = func() (string, error) { return "/tmp/fake-home/.local/bin/agentctl", nil }
	t.Cleanup(func() { execPath = prevExecPath })

	repoRoot := t.TempDir()
	workDir := filepath.Join(repoRoot, ".worktrees", "slug")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	ac := config.AgentConfig{}

	// Mirror the order dispatch.Launch() now uses: the worktree settings file
	// (the reliable source under bug #37730) is written first, then the
	// --settings file, which checks workDir and its ancestors up to repoRoot
	// for already-registered agentctl hooks and omits them if found.
	if err := WriteWorktreeSettings(workDir, repoRoot, ac); err != nil {
		t.Fatalf("WriteWorktreeSettings() unexpected error: %v", err)
	}
	settingsPath, _ := writeAgentSettings("go-engineer", ac, workDir, repoRoot)
	if settingsPath == "" {
		t.Fatal("writeAgentSettings() returned empty path")
	}

	worktreeData, err := os.ReadFile(filepath.Join(workDir, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatalf("reading worktree settings: %v", err)
	}
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading --settings file: %v", err)
	}

	worktreeHasHooks := jsonHasHooksKey(t, worktreeData)
	settingsHasHooks := jsonHasHooksKey(t, settingsData)

	if !worktreeHasHooks {
		t.Error("worktree settings.local.json must carry hooks -- it is the reliable source under bug #37730")
	}
	if worktreeHasHooks && settingsHasHooks {
		t.Fatal("regression: both the worktree settings and the --settings file carry hooks -- every hook will fire twice per tool call")
	}
}

// TestDispatchWorktreePath_SingleHooksSource_AncestorAlreadyHasHooks is the
// actual production reproduction for the hook-doubling bug: an
// agentctl worktree at <repoRoot>/.worktrees/<slug>/ is nested INSIDE the
// repo, and Claude Code's project-local settings resolution walks up the
// directory tree from cwd -- so a hand-maintained
// <repoRoot>/.claude/settings.local.json that already registers agentctl
// hooks (exactly what a real agentctl checkout has -- see
// ancestorHasAgentctlHooks's doc comment for the empirical verification) is
// a SECOND hooks source Claude Code loads in addition to whatever this
// dispatch writes into the worktree.
//
// The predecessor version of this test built the worktree in
// a bare t.TempDir() with no hook-carrying ancestor, so it could never
// observe this failure mode -- it shipped green and production still
// doubled every hook. This version plants the ancestor deliberately and
// asserts the total count of hook-registering sources visible from the
// worktree (repoRoot's own file + the worktree's own file + the --settings
// file) is exactly 1, not the "no two of these three" check the old test
// ran.
func TestDispatchWorktreePath_SingleHooksSource_AncestorAlreadyHasHooks(t *testing.T) {
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(t.TempDir())

	prevExecPath := execPath
	execPath = func() (string, error) { return "/tmp/fake-home/.local/bin/agentctl", nil }
	t.Cleanup(func() { execPath = prevExecPath })

	// repoRoot mirrors a real agentctl checkout: it has its own hand-maintained
	// .claude/settings.local.json that already registers agentctl hooks,
	// exactly like the main checkout in the bug report.
	repoRoot := t.TempDir()
	repoClaudeDir := filepath.Join(repoRoot, ".claude")
	if err := os.MkdirAll(repoClaudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	handMaintained := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"~/.local/bin/agentctl hook-pre-tool"}]}]}}`
	if err := os.WriteFile(filepath.Join(repoClaudeDir, "settings.local.json"), []byte(handMaintained), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// workDir is a REAL subdirectory of repoRoot, matching worktree.Create's
	// actual layout (<repoRoot>/.worktrees/<slug>) -- not an unrelated
	// t.TempDir() with no relationship to repoRoot.
	workDir := filepath.Join(repoRoot, ".worktrees", "agent-go-engineer-issue-1002")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	ac := config.AgentConfig{}

	// Exact call order dispatch.Launch() uses.
	if err := WriteWorktreeSettings(workDir, repoRoot, ac); err != nil {
		t.Fatalf("WriteWorktreeSettings() unexpected error: %v", err)
	}
	settingsPath, _ := writeAgentSettings("go-engineer", ac, workDir, repoRoot)
	if settingsPath == "" {
		t.Fatal("writeAgentSettings() returned empty path")
	}

	worktreeData, err := os.ReadFile(filepath.Join(workDir, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatalf("reading worktree settings: %v", err)
	}
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading --settings file: %v", err)
	}

	sources := 0
	if jsonHasHooksKey(t, []byte(handMaintained)) {
		sources++ // the ancestor -- always true here, spelled out for clarity
	}
	if jsonHasHooksKey(t, worktreeData) {
		sources++
	}
	if jsonHasHooksKey(t, settingsData) {
		sources++
	}

	if sources != 1 {
		t.Fatalf("hook-registering sources visible from the worktree = %d, want exactly 1 (ancestor=%v worktree=%v settings=%v)",
			sources, true, jsonHasHooksKey(t, worktreeData), jsonHasHooksKey(t, settingsData))
	}
}

// TestDispatchNonWorktreePath_SkipsHooksWhenProjectAlreadyHasThem asserts the
// mirror case for a plain, non-worktree launch: when the working directory's
// own project-local .claude/settings.local.json already registers agentctl
// hooks (e.g. the hand-maintained file in the main repo checkout),
// WriteAgentSettings must not duplicate them into the --settings file.
func TestDispatchNonWorktreePath_SkipsHooksWhenProjectAlreadyHasThem(t *testing.T) {
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(t.TempDir())

	workDir := t.TempDir()
	claudeDir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	handMaintained := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"~/.local/bin/agentctl hook-pre-tool"}]}]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), []byte(handMaintained), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	settingsPath := WriteAgentSettings("go-engineer", config.AgentConfig{}, workDir)
	if settingsPath == "" {
		t.Fatal("WriteAgentSettings() returned empty path")
	}
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading --settings file: %v", err)
	}
	if jsonHasHooksKey(t, settingsData) {
		t.Error("WriteAgentSettings(): --settings file carries hooks even though the project-local file already registers them")
	}
}

// TestDispatchNonWorktreePath_IncludesHooksWhenProjectHasNone asserts hooks
// still fire at all for a plain launch when there is no project-local
// registration to rely on -- the fail-safe direction for
// projectHasAgentctlHooks (missing/malformed file => false => hooks
// included) must actually result in a --settings file that carries hooks.
func TestDispatchNonWorktreePath_IncludesHooksWhenProjectHasNone(t *testing.T) {
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(t.TempDir())

	workDir := t.TempDir() // no .claude/settings.local.json at all

	settingsPath := WriteAgentSettings("go-engineer", config.AgentConfig{}, workDir)
	if settingsPath == "" {
		t.Fatal("WriteAgentSettings() returned empty path")
	}
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading --settings file: %v", err)
	}
	if !jsonHasHooksKey(t, settingsData) {
		t.Error("WriteAgentSettings(): --settings file has no hooks and no project-local file covers this workDir -- hooks would never fire")
	}
}

// jsonHasHooksKey reports whether data's top-level JSON object has a "hooks"
// key at all (regardless of its value).
func jsonHasHooksKey(t *testing.T, data []byte) bool {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	_, ok := raw["hooks"]
	return ok
}

func TestBuildDispatchEnvOmitsEmptyRepo(t *testing.T) {
	withRuntimeDir(t, "")
	env := buildDispatchEnv(Request{AgentName: "go-engineer", Mode: ModeAdhoc}, config.AgentConfig{}, "/w", "/w", "", false)
	if v, ok := env["AGENTCTL_REPO"]; ok {
		t.Errorf("AGENTCTL_REPO = %q, want unset when RepoSlug is empty", v)
	}
}
