package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- resolveBranch --------------------------------------------------------

func TestResolveBranch(t *testing.T) {
	// Deterministic random suffix for the fallback-path assertions.
	orig := randomSuffixFn
	defer func() { randomSuffixFn = orig }()
	randomSuffixFn = func() string { return "deadbeef" }

	tests := []struct {
		name       string
		branch     string
		branchName string
		nameField  string
		sessionID  string
		want       string // exact match; "" means "just check non-empty + prefix"
	}{
		{
			name:   "explicit branch wins over everything",
			branch: "feature/explicit",
			want:   "feature/explicit",
		},
		{
			name:       "explicit branch wins even with branchName and name present",
			branch:     "feature/explicit",
			branchName: "feature/alias",
			nameField:  "agent-deadbeef",
			sessionID:  "test-1234",
			want:       "feature/explicit",
		},
		{
			name:       "branchName alias used when branch absent",
			branchName: "feature/alias",
			want:       "feature/alias",
		},
		{
			name:       "branchName wins over name when both present",
			branchName: "feature/alias",
			nameField:  "agent-deadbeef",
			want:       "feature/alias",
		},
		{
			name:      "name wins over session_id",
			nameField: "agent-deadbeef",
			sessionID: "test-1234",
			want:      "worktree/agent-deadbeef",
		},
		{
			name:      "name alone",
			nameField: "agent-a6a3ff01c3c89551d",
			want:      "worktree/agent-a6a3ff01c3c89551d",
		},
		{
			name:      "name empty falls back to a session-derived branch",
			sessionID: "abcdef1234567890",
			want:      "worktree/abcdef12-deadbeef",
		},
		{
			name:      "short session_id used as-is in the fallback",
			sessionID: "ab12",
			want:      "worktree/ab12-deadbeef",
		},
		{
			name: "everything empty still returns a non-empty generated name",
			want: "", // checked for non-empty + prefix below
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveBranch(tt.branch, tt.branchName, tt.nameField, tt.sessionID)
			if got == "" {
				t.Fatalf("resolveBranch() returned empty string, must never happen")
			}
			if tt.want != "" && got != tt.want {
				t.Errorf("resolveBranch() = %q, want %q", got, tt.want)
			}
			if tt.want == "" && !strings.HasPrefix(got, "worktree/") {
				t.Errorf("resolveBranch() fallback = %q, want worktree/<something>", got)
			}
		})
	}
}

// TestResolveBranch_FallbackIsCollisionProof is the direct regression test
// for the shared-session-ID collision: two invocations sharing the same session_id (as every
// concurrent agent in one Claude Code session does) but with no `name` field
// must NOT resolve to the same branch. Before this fix, the fallback was
// "worktree/" + sessionID[:8] with no randomness -- constant for the whole
// session, so worktree.Create's directory-reuse fast path silently handed
// every concurrent agent the same working tree.
func TestResolveBranch_FallbackIsCollisionProof(t *testing.T) {
	orig := randomSuffixFn
	defer func() { randomSuffixFn = orig }()

	seq := []string{"aaaaaaaa", "bbbbbbbb"}
	i := 0
	randomSuffixFn = func() string {
		v := seq[i%len(seq)]
		i++
		return v
	}

	const sameSessionID = "00000000-0000-4000-8000-000000000003"
	b1 := resolveBranch("", "", "", sameSessionID)
	b2 := resolveBranch("", "", "", sameSessionID)

	if b1 == b2 {
		t.Fatalf("resolveBranch() returned identical branches for two calls with the same session_id and no name: %q", b1)
	}
}

// --- worktreeCreate: malformed input never panics -------------------------

// TestWorktreeCreate_MalformedInputDoesNotPanic feeds worktreeCreate several
// unparseable or wrong-shaped payloads. Every case below os.Chdir's into a
// real repo first, so resolveRepoDir's os.Getwd fallback always has a valid
// repo to work with -- meaning worktreeCreate genuinely should succeed (exit
// 0, non-empty stdout) in every case, not merely avoid panicking. A test that
// only logs on a non-zero exit code can never fail for anything but a
// literal panic; assert the real expected outcome instead.
func TestWorktreeCreate_MalformedInputDoesNotPanic(t *testing.T) {
	repoDir := setupFakeRemoteForHooks(t)

	inputs := []string{
		"",
		"not json at all",
		"{",
		`{"cwd": 123}`, // wrong type for a string field
	}

	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			var out, errOut bytes.Buffer
			// Malformed/wrong-typed input leaves worktreeCreateInput.Cwd
			// empty, so resolveRepoDir falls back to os.Getwd -- chdir into
			// repoDir so that fallback resolves to a real repo.
			restoreDir := chdir(t, repoDir)
			defer restoreDir()

			code := worktreeCreate(strings.NewReader(in), &out, &errOut)
			if code != 0 {
				t.Fatalf("worktreeCreate(%q) = %d, want 0; stderr=%s", in, code, errOut.String())
			}
			if out.Len() == 0 {
				t.Fatalf("worktreeCreate(%q) exited 0 with empty stdout", in)
			}

			path := strings.TrimSpace(out.String())
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("worktreeCreate(%q) stdout path %q does not exist: %v", in, path, err)
			}
		})
	}
}

// --- worktreeCreate: unknown fields are ignored ----------------------------

func TestWorktreeCreate_UnknownFieldsIgnored(t *testing.T) {
	repoDir := setupFakeRemoteForHooks(t)

	payload := map[string]any{
		"cwd":               repoDir,
		"branch":            "feature/unknown-fields-ok",
		"some_future_field": "whatever",
		"nested":            map[string]any{"a": 1},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out, errOut bytes.Buffer
	code := worktreeCreate(bytes.NewReader(raw), &out, &errOut)
	if code != 0 {
		t.Fatalf("worktreeCreate() = %d, stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "feature-unknown-fields-ok") {
		t.Errorf("expected output to reference the slugged branch dir, got %q", out.String())
	}
}

// --- worktreeCreate: output encoding ---------------------------------------

// TestWorktreeCreate_OutputIsBarePathNotJSON is the regression pin for the
// JSON-envelope bug: this hook once emitted a {"hookSpecificOutput": ...} JSON envelope,
// which the consumer treated as a literal relative path and joined to the
// repo root, aborting the agent launch. This is the SECOND time an
// output-format guess has broken this hook (the first was a Noop wiring that wrote nothing), so this
// test asserts the negative as loudly as the positive: stdout must contain
// none of the punctuation or field names a JSON envelope would introduce.
func TestWorktreeCreate_OutputIsBarePathNotJSON(t *testing.T) {
	repoDir := setupFakeRemoteForHooks(t)

	payload := map[string]any{
		"cwd":    repoDir,
		"branch": "feature/bare-path-output",
	}
	raw, _ := json.Marshal(payload)

	var out, errOut bytes.Buffer
	code := worktreeCreate(bytes.NewReader(raw), &out, &errOut)
	if code != 0 {
		t.Fatalf("worktreeCreate() = %d, stderr=%s", code, errOut.String())
	}

	path := strings.TrimSpace(out.String())
	if path == "" {
		t.Fatal("worktreeCreate() produced empty stdout")
	}
	if !filepath.IsAbs(path) {
		t.Errorf("stdout = %q, want an absolute path", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("stdout path %q does not exist: %v", path, err)
	}

	for _, forbidden := range []string{"{", "}", `"`, "hookSpecificOutput"} {
		if strings.Contains(out.String(), forbidden) {
			t.Errorf("stdout contains %q, which means this is a JSON envelope again -- only a bare path is accepted (see the regression doc above); got stdout=%q", forbidden, out.String())
		}
	}
}

// --- worktreeCreate: concurrent agents must not collide -------------------

// TestWorktreeCreate_ConcurrentAgentsGetDistinctWorktrees is the end-to-end
// regression test for the shared-session-ID collision. Two WorktreeCreate invocations sharing the
// same session_id -- exactly what every concurrent agent dispatched within
// one Claude Code session sends -- but with different `name` values must
// produce two distinct worktrees. Before this fix, both branch names were
// derived solely from session_id, so the second invocation silently reused
// the first agent's worktree directory (worktree.Create's directory-reuse
// fast path), handing two agents the same working tree.
func TestWorktreeCreate_ConcurrentAgentsGetDistinctWorktrees(t *testing.T) {
	repoDir := setupFakeRemoteForHooks(t)

	const sharedSessionID = "00000000-0000-4000-8000-000000000003"

	payload1 := map[string]any{
		"cwd":        repoDir,
		"session_id": sharedSessionID,
		"name":       "agent-a6a3ff01c3c89551d",
	}
	payload2 := map[string]any{
		"cwd":        repoDir,
		"session_id": sharedSessionID,
		"name":       "agent-ab0748d9194c2615e",
	}

	raw1, _ := json.Marshal(payload1)
	raw2, _ := json.Marshal(payload2)

	var out1, errOut1, out2, errOut2 bytes.Buffer
	if code := worktreeCreate(bytes.NewReader(raw1), &out1, &errOut1); code != 0 {
		t.Fatalf("worktreeCreate() agent 1 = %d, stderr=%s", code, errOut1.String())
	}
	if code := worktreeCreate(bytes.NewReader(raw2), &out2, &errOut2); code != 0 {
		t.Fatalf("worktreeCreate() agent 2 = %d, stderr=%s", code, errOut2.String())
	}

	path1 := strings.TrimSpace(out1.String())
	path2 := strings.TrimSpace(out2.String())

	if path1 == "" || path2 == "" {
		t.Fatalf("expected non-empty paths, got path1=%q path2=%q", path1, path2)
	}
	if path1 == path2 {
		t.Fatalf("two agents sharing session_id %q but different names got the SAME worktree path %q -- this is exactly the shared-session-ID collision", sharedSessionID, path1)
	}

	if _, err := os.Stat(path1); err != nil {
		t.Errorf("agent 1 worktree %q does not exist: %v", path1, err)
	}
	if _, err := os.Stat(path2); err != nil {
		t.Errorf("agent 2 worktree %q does not exist: %v", path2, err)
	}
}

// TestWorktreeCreate_ConcurrentAgentsNoNameFallbackIsCollisionProof covers
// the last-resort fallback: same session_id, no `name` field at all (a case
// not observed against any known Claude Code version, but still must not
// collide, per resolveBranch's collision-proof fallback).
func TestWorktreeCreate_ConcurrentAgentsNoNameFallbackIsCollisionProof(t *testing.T) {
	repoDir := setupFakeRemoteForHooks(t)

	seq := []string{"aaaa1111", "bbbb2222"}
	i := 0
	orig := randomSuffixFn
	defer func() { randomSuffixFn = orig }()
	randomSuffixFn = func() string {
		v := seq[i%len(seq)]
		i++
		return v
	}

	const sharedSessionID = "test-1234"
	payload := map[string]any{
		"cwd":        repoDir,
		"session_id": sharedSessionID,
	}
	raw, _ := json.Marshal(payload)

	var out1, errOut1, out2, errOut2 bytes.Buffer
	if code := worktreeCreate(bytes.NewReader(raw), &out1, &errOut1); code != 0 {
		t.Fatalf("worktreeCreate() first call = %d, stderr=%s", code, errOut1.String())
	}
	if code := worktreeCreate(bytes.NewReader(raw), &out2, &errOut2); code != 0 {
		t.Fatalf("worktreeCreate() second call = %d, stderr=%s", code, errOut2.String())
	}

	path1 := strings.TrimSpace(out1.String())
	path2 := strings.TrimSpace(out2.String())

	if path1 == path2 {
		t.Fatalf("two calls sharing session_id %q with no name still collided on path %q", sharedSessionID, path1)
	}
	if _, err := os.Stat(path1); err != nil {
		t.Errorf("first worktree %q does not exist: %v", path1, err)
	}
	if _, err := os.Stat(path2); err != nil {
		t.Errorf("second worktree %q does not exist: %v", path2, err)
	}
}

// --- worktreeCreate: THE regression test for the empty-stdout Noop wiring ---

// TestWorktreeCreate_NeverExitsZeroWithEmptyStdout is the direct regression
// test for the empty-stdout bug: a Noop wiring exited 0 with zero bytes on stdout, which is
// the one outcome a caller cannot act on. A successful create must always
// write a non-empty payload.
func TestWorktreeCreate_NeverExitsZeroWithEmptyStdout(t *testing.T) {
	repoDir := setupFakeRemoteForHooks(t)

	payload := map[string]any{
		"cwd":    repoDir,
		"branch": "feature/regression-966",
	}
	raw, _ := json.Marshal(payload)

	var out, errOut bytes.Buffer
	code := worktreeCreate(bytes.NewReader(raw), &out, &errOut)

	if code != 0 {
		t.Fatalf("worktreeCreate() = %d, want 0; stderr=%s", code, errOut.String())
	}
	if out.Len() == 0 {
		t.Fatal("worktreeCreate() exited 0 with EMPTY stdout -- this is exactly the empty-stdout bug")
	}
}

// --- worktreeCreate: settings-injection failure is non-fatal ---------------

func TestWorktreeCreate_SettingsInjectionFailureStillSucceeds(t *testing.T) {
	repoDir := setupFakeRemoteForHooks(t)

	orig := writeWorktreeSettingsFn
	defer func() { writeWorktreeSettingsFn = orig }()
	writeWorktreeSettingsFn = func(worktreeDir, repoDir string) error {
		return errBoom
	}

	payload := map[string]any{
		"cwd":    repoDir,
		"branch": "feature/settings-fail",
	}
	raw, _ := json.Marshal(payload)

	var out, errOut bytes.Buffer
	code := worktreeCreate(bytes.NewReader(raw), &out, &errOut)
	if code != 0 {
		t.Fatalf("worktreeCreate() = %d, want 0 despite settings failure; stderr=%s", code, errOut.String())
	}
	if out.Len() == 0 {
		t.Fatal("worktreeCreate() must still print the path when settings injection fails")
	}
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }

// --- SetWriteWorktreeSettingsFn default is safe ----------------------------

func TestWriteWorktreeSettingsFnDefaultDoesNotPanic(t *testing.T) {
	// Exercise the REAL package default -- defaultWriteWorktreeSettings --
	// not a stub the test installs itself. This is what runs in production
	// whenever main.go hasn't wired SetWriteWorktreeSettingsFn (e.g. hooks
	// invoked outside the normal agentctl-managed launch path).
	if err := defaultWriteWorktreeSettings(t.TempDir(), t.TempDir()); err != nil {
		t.Fatalf("defaultWriteWorktreeSettings returned error: %v", err)
	}
}

// --- Round-trip against a temp git repo: create -> settings planted -> remove

func TestWorktreeCreateThenRemove_RoundTrip(t *testing.T) {
	repoDir := setupFakeRemoteForHooks(t)

	planted := false
	orig := writeWorktreeSettingsFn
	defer func() { writeWorktreeSettingsFn = orig }()
	writeWorktreeSettingsFn = func(worktreeDir, repoDir string) error {
		planted = true
		claudeDir := filepath.Join(worktreeDir, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), []byte("{}"), 0o644)
	}

	createPayload := map[string]any{
		"cwd":    repoDir,
		"branch": "feature/round-trip",
	}
	raw, _ := json.Marshal(createPayload)

	var out, errOut bytes.Buffer
	code := worktreeCreate(bytes.NewReader(raw), &out, &errOut)
	if code != 0 {
		t.Fatalf("worktreeCreate() = %d, stderr=%s", code, errOut.String())
	}
	if !planted {
		t.Fatal("expected writeWorktreeSettingsFn to be invoked")
	}

	worktreePath := strings.TrimSpace(out.String())

	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree does not exist after create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, ".claude", "settings.local.json")); err != nil {
		t.Fatalf(".claude/settings.local.json not planted: %v", err)
	}

	// Now remove it.
	removePayload := map[string]any{"worktree_path": worktreePath}
	rraw, _ := json.Marshal(removePayload)
	worktreeRemove(bytes.NewReader(rraw))

	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree to be gone after remove, stat err=%v", err)
	}
}

// --- worktreeRemove: alias resolution --------------------------------------

func TestResolveWorktreePath(t *testing.T) {
	tests := []struct {
		name  string
		input worktreeRemoveInput
		want  string
	}{
		{
			name:  "worktree_path preferred",
			input: worktreeRemoveInput{WorktreePath: "/a", WorktreePathCamel: "/b", Cwd: "/c"},
			want:  "/a",
		},
		{
			name:  "worktreePath camelCase alias",
			input: worktreeRemoveInput{WorktreePathCamel: "/b", Cwd: "/c"},
			want:  "/b",
		},
		{
			name:  "falls back to cwd",
			input: worktreeRemoveInput{Cwd: "/c"},
			want:  "/c",
		},
		{
			name:  "everything empty",
			input: worktreeRemoveInput{},
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveWorktreePath(tt.input); got != tt.want {
				t.Errorf("resolveWorktreePath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorktreeRemove_MalformedInputDoesNotPanic(t *testing.T) {
	for _, in := range []string{"", "not json", "{", `{"cwd": 123}`} {
		t.Run(in, func(t *testing.T) {
			worktreeRemove(strings.NewReader(in)) // must not panic
		})
	}
}

// --- test helpers -----------------------------------------------------------

// setupFakeRemoteForHooks creates a bare "remote" repo with one commit on
// main and returns a local clone whose origin points to the bare repo, so
// worktree.Create's "fetch origin main" + branch-from-origin/main flow works
// end to end. Mirrors internal/worktree/worktree_test.go's helper (kept as
// its own copy since that helper is unexported in a different package).
func setupFakeRemoteForHooks(t *testing.T) (localRepo string) {
	t.Helper()

	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	local := filepath.Join(tmp, "local")
	staging := filepath.Join(tmp, "staging")

	gitRunForHooks(t, tmp, "init", "--bare", bare)

	gitRunForHooks(t, tmp, "init", staging)
	gitRunForHooks(t, staging, "config", "user.email", "test@example.com")
	gitRunForHooks(t, staging, "config", "user.name", "Test")
	gitRunForHooks(t, staging, "remote", "add", "origin", bare)

	readme := filepath.Join(staging, "README.md")
	if err := os.WriteFile(readme, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunForHooks(t, staging, "add", "README.md")
	gitRunForHooks(t, staging, "commit", "-m", "initial commit")
	gitRunForHooks(t, staging, "push", "origin", "HEAD:main")

	cmd := exec.Command("git", "clone", bare, local)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git clone: %v\n%s", err, string(out))
	}
	gitRunForHooks(t, local, "config", "user.email", "test@example.com")
	gitRunForHooks(t, local, "config", "user.name", "Test")

	return local
}

func gitRunForHooks(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

// chdir changes the working directory for the duration of a test and
// returns a restore function. Used to exercise resolveRepoDir's os.Getwd
// fallback path when cwd isn't present in the hook payload.
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("os.Chdir(%s): %v", dir, err)
	}
	return func() {
		_ = os.Chdir(orig)
	}
}

// --- Dispatch("hook-worktree-create") routing, exercised in a subprocess ---

// TestDispatchRoutesToWorktreeCreate confirms Dispatch("hook-worktree-create")
// is wired to WorktreeCreate() (registration-drift coverage that
// TestDispatchCoversAllHooks in hooks_test.go deliberately skips for this
// subcmd, since WorktreeCreate calls os.Exit and bypasses the AGENTCTL
// guard). Runs the real hook end-to-end in a subprocess re-exec of this test
// binary, so the os.Exit(0) is safely contained and doesn't touch this
// process's real working directory.
func TestDispatchRoutesToWorktreeCreate(t *testing.T) {
	if os.Getenv("AGENTCTL_TEST_WORKTREE_CREATE_HELPER") == "1" {
		Dispatch("hook-worktree-create")
		return
	}

	repoDir := setupFakeRemoteForHooks(t)
	payload := `{"cwd":"` + repoDir + `","branch":"feature/dispatch-routing"}`

	cmd := exec.Command(os.Args[0], "-test.run=TestDispatchRoutesToWorktreeCreate")
	cmd.Env = append(os.Environ(), "AGENTCTL_TEST_WORKTREE_CREATE_HELPER=1")
	cmd.Stdin = strings.NewReader(payload)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 0 {
			t.Fatalf("subprocess exited non-zero: %v\noutput:\n%s", err, out)
		}
	}
	if !strings.Contains(string(out), "feature-dispatch-routing") {
		t.Errorf("subprocess output missing expected worktree path, got:\n%s", out)
	}
	if strings.Contains(string(out), "hookSpecificOutput") {
		t.Errorf("subprocess output is a JSON envelope, want a bare path, got:\n%s", out)
	}
}
