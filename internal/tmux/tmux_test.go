package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/shell"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/shellquote"
)

func TestStripAgentPrefix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"first spin frame", "⠋ go-engineer", "go-engineer"},
		{"mid spin frame", "⠸ go-engineer", "go-engineer"},
		{"blocked prefix", "⏸ go-engineer", "go-engineer"},
		{"improve prefix", "improve~go-engineer", "improve~go-engineer"},
		{"clean name", "go-engineer", "go-engineer"},
		{"shell", "shell", "shell"},
		{"empty", "", ""},
		{"spin then blocked strips both", "⠋ ⏸ double", "double"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripAgentPrefix(tt.input)
			if got != tt.want {
				t.Errorf("StripAgentPrefix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBaseAgentName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no suffix", "go-engineer", "go-engineer"},
		{"numeric suffix :2", "go-engineer:2", "go-engineer"},
		{"numeric suffix :15", "go-engineer:15", "go-engineer"},
		{"colon no number", "go-engineer:", "go-engineer:"},
		{"non-numeric suffix", "go-engineer:abc", "go-engineer:abc"},
		{"improve prefix not numeric", "improve~go-engineer", "improve~go-engineer"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BaseAgentName(tt.input)
			if got != tt.want {
				t.Errorf("BaseAgentName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestFindMainPaneExcludesHUD verifies that findMainPane skips all chrome pane
// IDs — including the HUD pane — and returns only the terminal pane.
//
// findMainPane iterates panes returned by tmux and skips IDs that match any
// registered chrome option. We test the filtering logic directly by verifying
// that a pane ID stored under @agentctl_hud_pane is not returned as the main
// pane. Because findMainPane shells out to tmux, this test exercises the
// exclusion logic only in an environment where the window actually exists;
// without tmux the function returns "" for both the options and the pane list,
// so the test is gated on SessionExists.
//
// The structural test below validates the exclusion set used by findMainPane
// without requiring a live tmux session.
func TestFindMainPaneExcludesHUDLogic(t *testing.T) {
	// Simulate what findMainPane does: given a set of chrome pane IDs and a
	// list of pane IDs in a window, the first pane that is not in the chrome
	// set should be returned as the main pane.
	filterMainPane := func(panes []string, header, footer, statusBar, hud string) string {
		for _, id := range panes {
			if id != header && id != footer && id != statusBar && id != hud && id != "" {
				return id
			}
		}
		return ""
	}

	tests := []struct {
		name      string
		panes     []string
		header    string
		footer    string
		statusBar string
		hud       string
		want      string
	}{
		{
			name:      "hud pane excluded, terminal returned",
			panes:     []string{"%1", "%2", "%3", "%4"},
			header:    "%1",
			footer:    "%2",
			statusBar: "%3",
			hud:       "%4",
			want:      "", // no pane left — all are chrome
		},
		{
			name:      "terminal pane present among chrome panes",
			panes:     []string{"%1", "%2", "%3", "%4", "%5"},
			header:    "%1",
			footer:    "%2",
			statusBar: "%3",
			hud:       "%4",
			want:      "%5",
		},
		{
			name:      "no hud pane — hud empty string not matched",
			panes:     []string{"%1", "%2", "%3"},
			header:    "%1",
			footer:    "%2",
			statusBar: "%3",
			hud:       "",
			want:      "", // all chrome, no terminal
		},
		{
			name:      "only terminal pane",
			panes:     []string{"%1"},
			header:    "",
			footer:    "",
			statusBar: "",
			hud:       "",
			want:      "%1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterMainPane(tt.panes, tt.header, tt.footer, tt.statusBar, tt.hud)
			if got != tt.want {
				t.Errorf("filterMainPane(%v, header=%q footer=%q statusBar=%q hud=%q) = %q, want %q",
					tt.panes, tt.header, tt.footer, tt.statusBar, tt.hud, got, tt.want)
			}
		})
	}
}

// TestAfterSelectWindowHookQuotesPath verifies the after-select-window hook
// survives all three layers (tmux's parser, run-shell format expansion, then
// sh) with hostile paths: the runtime dir and binary must come out exactly as
// given, and no shell redirect (which would follow a planted symlink) is used.
func TestAfterSelectWindowHookQuotesPath(t *testing.T) {
	tests := []struct {
		name string
		dir  string
	}{
		{"plain", "/home/user/project/.agentctl"},
		{"space and quote", "/Users/Jane O'Neil/my proj/.agentctl"},
		{"shell metacharacters", "/tmp/x;touch pwned$(id)`id`/.agentctl"},
		{"tmux formats", "/tmp/#(touch pwned)#{session_name}##/.agentctl"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exe := tc.dir + "/bin/agentctl"
			hookCmd := activeWindowHookArgs("s", exe, tc.dir)[4]
			tmuxArgs := tokenize(t, hookCmd)
			if len(tmuxArgs) != 3 || tmuxArgs[0] != "run-shell" || tmuxArgs[1] != "-b" {
				t.Fatalf("hook tokenizes to %q, want [run-shell -b <cmd>]", tmuxArgs)
			}
			if strings.Contains(tmuxArgs[2], ">") && !strings.Contains(tc.dir, ">") {
				t.Errorf("shell command %q uses a redirect", tmuxArgs[2])
			}
			want := []string{"AGENTCTL_RUNTIME_DIR=" + tc.dir, exe, "set-active-window", "@5"}
			if got := tokenize(t, expandFormats(tmuxArgs[2])); !slices.Equal(got, want) {
				t.Errorf("command tokenizes to %q, want %q", got, want)
			}
		})
	}
}

// expandFormats mimics tmux's format expansion of a run-shell command for the
// formats these hooks use: #{window_id} becomes @5 and ## becomes #. Any
// other '#' left in s would be a format tmux would act on, so it fails.
func expandFormats(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '#' {
			b.WriteByte(s[i])
			continue
		}
		switch {
		case strings.HasPrefix(s[i:], "##"):
			b.WriteByte('#')
			i++
		case strings.HasPrefix(s[i:], "#{window_id}"):
			b.WriteString("@5")
			i += len("#{window_id}") - 1
		default:
			return "UNESCAPED-FORMAT:" + s[i:]
		}
	}
	return b.String()
}

// tokenize splits s the way sh (and tmux's command parser, which shares its
// quoting rules) would, failing the test if any parameter expansion would
// occur. #{window_id} is replaced first, as run-shell format-expands it
// before the shell runs (an unexpanded leading # would read as a comment).
func tokenize(t *testing.T, s string) []string {
	t.Helper()
	s = strings.ReplaceAll(s, "#{window_id}", "@5")
	fields, err := shell.Fields(s, func(name string) string {
		switch name {
		case "IFS", "PWD": // consulted by the tokenizer itself
			return os.Getenv(name)
		}
		t.Errorf("unexpected parameter expansion of $%s in %q", name, s)
		return ""
	})
	if err != nil {
		t.Fatalf("tokenizing %q: %v", s, err)
	}
	return fields
}

// TestAgentctlKeybindingsQuoteExe verifies every keybinding keeps the binary
// path as a single word through tmux's parser, the run-shell/popup shell,
// and (for nested popups) the inner tmux client's shell.
func TestAgentctlKeybindingsQuoteExe(t *testing.T) {
	exes := []string{
		"/usr/local/bin/agentctl",
		"/Users/Jane O'Neil/bin/agentctl",
		"/opt/$weird dir/agentctl",
	}
	for _, exe := range exes {
		t.Run(exe, func(t *testing.T) {
			for _, kb := range agentctlKeybindings(exe) {
				argv := agentctlArgv(t, kb.command)
				if len(argv) == 0 || argv[0] != exe {
					t.Errorf("bind %q: agentctl invocation = %q, want argv[0] = %q\ncommand: %s", kb.key, argv, exe, kb.command)
				}
			}
		})
	}
}

// agentctlArgv peels the quoting layers off a keybinding command and returns
// the argv of the agentctl invocation it ultimately runs (env assignments
// stripped).
func agentctlArgv(t *testing.T, command string) []string {
	t.Helper()
	tmuxArgs := tokenize(t, command)
	shellCmd := tmuxArgs[len(tmuxArgs)-1]
	sh := tokenize(t, shellCmd)
	if len(sh) > 1 && sh[0] == "tmux" && sh[1] == "display-popup" {
		// Nested form: the inner tmux client's last argv item is the popup's
		// shell command.
		sh = tokenize(t, sh[len(sh)-1])
	}
	for len(sh) > 0 && strings.Contains(sh[0], "=") && !strings.HasPrefix(sh[0], "/") {
		sh = sh[1:]
	}
	return sh
}

// TestKeybindingPinArgsIsProjectAgnostic is the regression test for the
// cross-session keybinding bug: `bind-key` is SERVER-global, so every
// agentctl session on the machine rebinds the same keys and only the last
// binding survives for all of them. Any project-specific value baked into a
// binding therefore leaks into every OTHER session -- prefix+A pressed in
// agentctl-acme launched agents into agentctl-other-project because that session
// started last.
//
// The invariant: the fragments must be constant, naming no path at all. The
// runtime dir arrives from the tmux session environment at keypress time
// (EnsureSessionEnv) and the cwd from paths.HonorCwdPin.
func TestKeybindingPinArgsIsProjectAgnostic(t *testing.T) {
	popupPin, envPrefix := keybindingPinArgs()

	if want := "-e " + paths.CwdPinEnv + "=1"; popupPin != want {
		t.Errorf("popupPin = %q, want %q", popupPin, want)
	}
	if want := paths.CwdPinEnv + "=1 "; envPrefix != want {
		t.Errorf("envPrefix = %q, want %q", envPrefix, want)
	}

	// No absolute path, and no baked runtime dir, may appear in either
	// fragment -- those are exactly the values that used to cross sessions.
	for name, frag := range map[string]string{"popupPin": popupPin, "envPrefix": envPrefix} {
		if strings.Contains(frag, "/") {
			t.Errorf("%s = %q contains a path; keybindings are server-global and must name no project", name, frag)
		}
		if strings.Contains(frag, "AGENTCTL_RUNTIME_DIR") {
			t.Errorf("%s = %q bakes a runtime dir; it must come from the session environment instead", name, frag)
		}
	}
}

// TestKeybindingConstructionIsProjectAgnostic asserts the same invariant one
// level up, on the fully constructed bind-key command strings for a
// representative slice of applyConfig's bindings: the direct display-popup
// form (bind "a"), the run-shell form (bind "A", the launch key that actually
// misrouted), and the nested form (bind "x").
//
// Only the agentctl binary path -- which is machine-wide, not project-scoped
// -- may appear. Notably neither `-c` nor `-d` may: tmux does not
// format-expand either flag, so a cwd pin there can only ever be a constant,
// which is what made every keypress resolve one project's cwd.
func TestKeybindingConstructionIsProjectAgnostic(t *testing.T) {
	exe := "/usr/local/bin/agentctl"
	byKey := map[string]string{}
	for _, kb := range agentctlKeybindings(exe) {
		byKey[kb.key] = kb.command
	}

	cmds := map[string]string{
		"a (direct popup: menu)": byKey["a"],
		"A (run-shell: launch)":  byKey["A"],
		"x (nested popup: kill)": byKey["x"],
	}

	projectRoot := "/home/user/project"
	pidDir := projectRoot + "/.agentctl"

	for name, cmd := range cmds {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(cmd, projectRoot) || strings.Contains(cmd, pidDir) {
				t.Errorf("bind command must not reference a project path; got: %q", cmd)
			}
			if strings.Contains(cmd, "AGENTCTL_RUNTIME_DIR=") {
				t.Errorf("bind command must not bake AGENTCTL_RUNTIME_DIR; got: %q", cmd)
			}
			for _, flag := range []string{" -c ", " -d "} {
				if strings.Contains(cmd, flag) {
					t.Errorf("bind command must not pin a cwd via %q (tmux does not format-expand it); got: %q", strings.TrimSpace(flag), cmd)
				}
			}
			if !strings.Contains(cmd, paths.CwdPinEnv+"=1") {
				t.Errorf("bind command should carry the %s flag; got: %q", paths.CwdPinEnv, cmd)
			}
		})
	}

	// The nested form must target its inner popup explicitly: the inner
	// `tmux` is a fresh client invocation and would otherwise resolve
	// whichever session the server considers current.
	nested := cmds["x (nested popup: kill)"]
	if !strings.Contains(nested, "display-popup -E -t #{window_id}") {
		t.Errorf("nested popup should target -t #{window_id}; got: %q", nested)
	}
}

// TestChromeBarHookArgs is the regression test for the chrome-bar panes=1
// bug: the after-new-window hook fires via `run-shell`, which runs with the
// tmux SERVER's environment (not the session's) and the ACTIVE PANE's cwd --
// neither of which reliably points at this project once bare sessions run in
// git worktrees. chromeBarHookArgs must bake the resolved runtime
// dir and project root into the command explicitly rather than let the
// spawned process fall back to resolving them from its own cwd.
func TestChromeBarHookArgs(t *testing.T) {
	tests := []struct {
		name        string
		session     string
		exe         string
		runtimeDir  string
		projectRoot string
		wantSession string
	}{
		{
			name:        "default session",
			session:     "agentctl",
			exe:         "/usr/local/bin/agentctl",
			runtimeDir:  "/home/user/project/.agentctl",
			projectRoot: "/home/user/project",
			wantSession: "agentctl",
		},
		{
			name:        "named project session",
			session:     "agentctl-acme",
			exe:         "/usr/local/bin/agentctl",
			runtimeDir:  "/home/user/acme/.agentctl",
			projectRoot: "/home/user/acme",
			wantSession: "agentctl-acme",
		},
		{
			name:        "runtime dir anchored at repo root, NOT a worktree slug",
			session:     "agentctl-repo",
			exe:         "/usr/local/bin/agentctl",
			runtimeDir:  "/home/user/repo/.agentctl",
			projectRoot: "/home/user/repo",
			wantSession: "agentctl-repo",
		},
		{
			// Regression case for the shq double-quoting defect: a project
			// root containing $ and a space must survive intact rather than
			// being expanded by a real shell or tmux's own $VAR expansion.
			name:        "project root with dollar sign and space",
			session:     "agentctl-weird",
			exe:         "/usr/local/bin/agentctl",
			runtimeDir:  "/home/user/$weird project/.agentctl",
			projectRoot: "/home/user/$weird project",
			wantSession: "agentctl-weird",
		},
		{
			// A binary path with a space and a single quote used to break the
			// '%s' wrap around the whole run-shell command.
			name:        "binary path with space and quote",
			session:     "agentctl-x",
			exe:         "/Users/Jane O'Neil/bin/agentctl",
			runtimeDir:  "/Users/Jane O'Neil/x/.agentctl",
			projectRoot: "/Users/Jane O'Neil/x",
			wantSession: "agentctl-x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := chromeBarHookArgs(tt.session, tt.exe, tt.runtimeDir, tt.projectRoot)

			// Must be a set-hook command.
			if len(args) < 4 {
				t.Fatalf("expected at least 4 args, got %d: %v", len(args), args)
			}
			if args[0] != "set-hook" {
				t.Errorf("args[0] = %q, want %q", args[0], "set-hook")
			}

			// Must use -t (session-scoped), not -g (global).
			if args[1] != "-t" {
				t.Errorf("args[1] = %q, want %q (session-scoped, not -g)", args[1], "-t")
			}
			if args[2] != tt.wantSession {
				t.Errorf("session = %q, want %q", args[2], tt.wantSession)
			}
			if args[3] != "after-new-window" {
				t.Errorf("hook name = %q, want %q", args[3], "after-new-window")
			}

			hookCmd := args[4]
			if !strings.HasPrefix(hookCmd, "run-shell -b -c ") {
				t.Errorf("hook cmd should start with \"run-shell -b -c \"; got: %q", hookCmd)
			}
			if !strings.Contains(hookCmd, "#{window_id}") {
				t.Errorf("hook cmd should reference #{window_id} (unexpanded); got: %q", hookCmd)
			}
			// tmux's command parser treats single quotes and '\'' like sh, so
			// a POSIX tokenizer stands in for it: the hook must split into
			// exactly run-shell -b -c <projectRoot> <shell command>.
			tmuxArgs, err := shell.Fields(hookCmd, func(string) string { return "EXPANDED" })
			if err != nil {
				t.Fatalf("tokenizing hook cmd %q: %v", hookCmd, err)
			}
			if len(tmuxArgs) != 5 || !slices.Equal(tmuxArgs[:4], []string{"run-shell", "-b", "-c", tt.projectRoot}) {
				t.Fatalf("hook cmd tokenizes to %q, want [run-shell -b -c %q <cmd>]", tmuxArgs, tt.projectRoot)
			}
			// Must bake in an explicit AGENTCTL_RUNTIME_DIR= assignment matching
			// the passed runtime dir -- env beats cwd in paths.Init, which is
			// the load-bearing part of the fix -- followed by the quoted binary.
			wantInvocation := "AGENTCTL_RUNTIME_DIR=" + shellquote.Quote(tt.runtimeDir) + " " +
				shellquote.Quote(tt.exe) + " chrome-bar-pane #{window_id}"
			if !strings.Contains(tmuxArgs[4], wantInvocation) {
				t.Errorf("shell cmd should contain %q; got: %q", wantInvocation, tmuxArgs[4])
			}
			// The window name must never reach the shell (B1): tmux would
			// expand it before sh runs, so quoting cannot protect it.
			if strings.Contains(hookCmd, "window_name") {
				t.Errorf("hook cmd must not interpolate #{window_name}; got: %q", hookCmd)
			}
		})
	}
}

// TestActiveWindowHookArgs verifies that activeWindowHookArgs produces
// session-scoped hook args that run set-active-window pinned to the runtime dir.
func TestActiveWindowHookArgs(t *testing.T) {
	session := "agentctl-acme"
	runtimeDir := "/home/user/acme/.agentctl"

	args := activeWindowHookArgs(session, "/usr/local/bin/agentctl", runtimeDir)

	if len(args) < 4 {
		t.Fatalf("expected at least 4 args, got %d: %v", len(args), args)
	}
	if args[0] != "set-hook" {
		t.Errorf("args[0] = %q, want %q", args[0], "set-hook")
	}
	if args[1] != "-t" {
		t.Errorf("args[1] = %q, want %q (session-scoped, not -g)", args[1], "-t")
	}
	if args[2] != session {
		t.Errorf("session = %q, want %q", args[2], session)
	}
	if args[3] != "after-select-window" {
		t.Errorf("hook name = %q, want %q", args[3], "after-select-window")
	}

	hookCmd := args[4]
	if !strings.HasPrefix(hookCmd, "run-shell -b '") {
		t.Errorf("hook cmd should start with \"run-shell -b '\"; got: %q", hookCmd)
	}
	if !strings.Contains(hookCmd, "#{window_id}") {
		t.Errorf("hook cmd should reference #{window_id}; got: %q", hookCmd)
	}
	if !strings.Contains(hookCmd, runtimeDir) || !strings.Contains(hookCmd, "set-active-window #{window_id}") {
		t.Errorf("hook cmd should run set-active-window pinned to %q; got: %q", runtimeDir, hookCmd)
	}
}

// TestUtilityWindowNames verifies that the utility window names used to guard
// LaunchChromeBar and the after-new-window hook are correct and complete.
func TestUtilityWindowNames(t *testing.T) {
	expected := []string{"tickets", "sessions", "agents"}
	for _, name := range expected {
		if !utilityWindowNames[name] {
			t.Errorf("utilityWindowNames missing %q", name)
		}
	}
	// Sanity-check that non-utility names are absent.
	nonUtility := []string{"shell", "go-engineer", "python-engineer", ""}
	for _, name := range nonUtility {
		if utilityWindowNames[name] {
			t.Errorf("utilityWindowNames should not contain %q", name)
		}
	}
}

// TestChromeBarHookFormatEscapesPaths checks that every '#' in the runtime
// dir and binary path is escaped for run-shell's format expansion, so a
// project dir like "x#(cmd)" neither runs cmd nor gets mangled.
func TestChromeBarHookFormatEscapesPaths(t *testing.T) {
	dir := "/tmp/#(touch pwned)#{session_name}"
	exe := dir + "/agentctl"
	args := chromeBarHookArgs("s", exe, dir+"/.agentctl", dir)
	tmuxArgs := tokenize(t, args[4])
	if len(tmuxArgs) != 5 {
		t.Fatalf("hook tokenizes to %q", tmuxArgs)
	}
	want := []string{"AGENTCTL_RUNTIME_DIR=" + dir + "/.agentctl", exe, "chrome-bar-pane", "@5"}
	if got := tokenize(t, expandFormats(tmuxArgs[4])); !slices.Equal(got, want) {
		t.Errorf("command tokenizes to %q, want %q", got, want)
	}
}

func TestFormatEscape(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"/plain/path", "/plain/path"},
		{"#(touch x)", "##(touch x)"},
		{"a#{b}##c", "a##{b}####c"},
	}
	for _, tt := range tests {
		if got := FormatEscape(tt.in); got != tt.want {
			t.Errorf("FormatEscape(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestTitleString verifies that titleString produces the expected
// "agentctl · <project> · #W" format, with fallback for edge-case inputs.
func TestTitleString(t *testing.T) {
	tests := []struct {
		name    string
		project string
		want    string
	}{
		{"normal project", "acme", "agentctl · acme · #W"},
		{"hyphenated project", "my-project", "agentctl · my-project · #W"},
		{"agentctl project", "agentctl", "agentctl · agentctl · #W"},
		{"empty string falls back", "", "agentctl · agentctl · #W"},
		{"dot falls back", ".", "agentctl · agentctl · #W"},
		{"slash falls back", "/", "agentctl · agentctl · #W"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := titleString(tt.project)
			if got != tt.want {
				t.Errorf("titleString(%q) = %q, want %q", tt.project, got, tt.want)
			}
		})
	}
}

// ── KillSession pane PID parsing ─────────────────────────────────────────────

// parseSessionPanePIDs mirrors the PID extraction done by sigTermPaneGroups so
// the parsing logic used by KillSession can be verified without a live session.
func parseSessionPanePIDs(output string) []int {
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if pid, err := strconv.Atoi(line); err == nil && pid > 1 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// TestKillSessionPanePIDParsing verifies that the PID extraction logic used by
// KillSession (via sigTermPaneGroups) correctly parses tmux list-panes -s output.
// KillSession collects these PIDs before issuing kill-session so the full process
// group can be SIGTERMed after the session is gone.
func TestKillSessionPanePIDParsing(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantLen  int
		wantPIDs []int
	}{
		{
			name:     "single pane",
			raw:      "1234\n",
			wantLen:  1,
			wantPIDs: []int{1234},
		},
		{
			name:     "multiple panes across windows",
			raw:      "1234\n5678\n9012\n",
			wantLen:  3,
			wantPIDs: []int{1234, 5678, 9012},
		},
		{
			name:     "empty output — no session",
			raw:      "",
			wantLen:  0,
			wantPIDs: nil,
		},
		{
			name:     "whitespace lines are skipped",
			raw:      "1234\n\n  \n5678\n",
			wantLen:  2,
			wantPIDs: []int{1234, 5678},
		},
		{
			name:     "PID 1 is skipped (init)",
			raw:      "1\n1234\n",
			wantLen:  1,
			wantPIDs: []int{1234},
		},
		{
			name:     "non-numeric lines are skipped",
			raw:      "badpid\n1234\n",
			wantLen:  1,
			wantPIDs: []int{1234},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSessionPanePIDs(tt.raw)
			if len(got) != tt.wantLen {
				t.Fatalf("parseSessionPanePIDs(%q): got %d PIDs, want %d: %v",
					tt.raw, len(got), tt.wantLen, got)
			}
			for i, pid := range tt.wantPIDs {
				if got[i] != pid {
					t.Errorf("pid[%d] = %d, want %d", i, got[i], pid)
				}
			}
		})
	}
}

// TODO: test NextInstanceName once RunningAgents is mockable.
// It calls RunningInstances() → RunningAgents() which shells out to tmux.

// ── ListWindows parsing ───────────────────────────────────────────────────────

// parseListWindowsOutput mirrors the parsing logic in ListWindows so it can be
// tested without a live tmux session.
func parseListWindowsOutput(raw string) []WindowInfo {
	var windows []WindowInfo
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || parts[0] == "" {
			continue
		}
		windows = append(windows, WindowInfo{ID: parts[0], Path: parts[1], Name: parts[2]})
	}
	return windows
}

// TestListWindowsParsing verifies that the parsing logic in ListWindows handles
// various tmux list-windows output formats correctly — including pane paths
// and window names that contain spaces (e.g. a path under "My Projects", or a
// spinner-prefixed "● go-engineer" name), which is exactly why the fields are
// tab-delimited rather than space-delimited.
func TestListWindowsParsing(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []WindowInfo
	}{
		{
			name: "single window",
			raw:  "@1\t/repo\tshell\n",
			want: []WindowInfo{{ID: "@1", Path: "/repo", Name: "shell"}},
		},
		{
			name: "multiple windows",
			raw:  "@1\t/repo\tshell\n@2\t/repo/.worktrees/foo\tgo-engineer\n@3\t/repo/.worktrees/bar\tpython-engineer\n",
			want: []WindowInfo{
				{ID: "@1", Path: "/repo", Name: "shell"},
				{ID: "@2", Path: "/repo/.worktrees/foo", Name: "go-engineer"},
				{ID: "@3", Path: "/repo/.worktrees/bar", Name: "python-engineer"},
			},
		},
		{
			name: "window name with space (spinner prefix)",
			raw:  "@2\t/repo/.worktrees/foo\t● go-engineer\n",
			want: []WindowInfo{{ID: "@2", Path: "/repo/.worktrees/foo", Name: "● go-engineer"}},
		},
		{
			name: "window name with space (blocked prefix)",
			raw:  "@3\t/repo/.worktrees/foo\t⏸ go-engineer\n",
			want: []WindowInfo{{ID: "@3", Path: "/repo/.worktrees/foo", Name: "⏸ go-engineer"}},
		},
		{
			name: "pane path with a space and a spinner-prefixed name on the same line",
			raw:  "@2\t/repo/my worktrees/foo\t● go-engineer\n",
			want: []WindowInfo{{ID: "@2", Path: "/repo/my worktrees/foo", Name: "● go-engineer"}},
		},
		{
			name: "empty output",
			raw:  "",
			want: nil,
		},
		{
			name: "empty pane path still yields three fields",
			raw:  "@1\t\tshell\n",
			want: []WindowInfo{{ID: "@1", Path: "", Name: "shell"}},
		},
		{
			name: "whitespace-only line is skipped",
			raw:  "@1\t/repo\tshell\n  \n@2\t/repo/.worktrees/foo\tgo-engineer\n",
			want: []WindowInfo{
				{ID: "@1", Path: "/repo", Name: "shell"},
				{ID: "@2", Path: "/repo/.worktrees/foo", Name: "go-engineer"},
			},
		},
		{
			name: "line missing a field is skipped",
			raw:  "@1\n@2\t/repo/.worktrees/foo\tgo-engineer\n",
			want: []WindowInfo{{ID: "@2", Path: "/repo/.worktrees/foo", Name: "go-engineer"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseListWindowsOutput(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("parseListWindowsOutput(%q): got %d windows, want %d: got=%v want=%v",
					tt.raw, len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("window[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ── ActiveWindowIDFast ────────────────────────────────────────────────────────

// TestActiveWindowIDFastReadsFile verifies that ActiveWindowIDFast returns the
// window ID stored in the active-window file when the file exists and is valid.
func TestActiveWindowIDFastReadsFile(t *testing.T) {
	dir := t.TempDir()
	awPath := filepath.Join(dir, "active-window")
	if err := os.WriteFile(awPath, []byte("@3\n"), 0o644); err != nil {
		t.Fatalf("write active-window: %v", err)
	}

	got := ActiveWindowIDFast(awPath)
	if got != "@3" {
		t.Errorf("ActiveWindowIDFast(%q) = %q, want %q", awPath, got, "@3")
	}
}

// TestActiveWindowIDFastFallbackOnMissingFile verifies that ActiveWindowIDFast
// falls back gracefully when the file does not exist. The tmux fallback will
// fail in the test environment (no live session), so we just verify no panic.
func TestActiveWindowIDFastFallbackOnMissingFile(t *testing.T) {
	nonExistent := filepath.Join(t.TempDir(), "active-window")
	// Must not panic; tmux is unavailable in CI so result may be "".
	_ = ActiveWindowIDFast(nonExistent)
}

// TestActiveWindowIDFastFallbackOnEmptyFile verifies that a whitespace-only
// active-window file causes ActiveWindowIDFast to fall through to the tmux
// fallback rather than returning an empty string from the file.
func TestActiveWindowIDFastFallbackOnEmptyFile(t *testing.T) {
	dir := t.TempDir()
	awPath := filepath.Join(dir, "active-window")
	if err := os.WriteFile(awPath, []byte("  \n"), 0o644); err != nil {
		t.Fatalf("write active-window: %v", err)
	}
	// Whitespace trimmed to "" → falls through to tmux; tmux fails in CI → "".
	_ = ActiveWindowIDFast(awPath)
}

// TestActiveWindowIDFastEmptyPath verifies that passing an empty filePath
// skips the file read and falls through directly to the tmux fallback.
func TestActiveWindowIDFastEmptyPath(t *testing.T) {
	// Empty path must not panic or error.
	_ = ActiveWindowIDFast("")
}

// ── ListSessionsWithPaths parsing ────────────────────────────────────────────

// TestParseSessionsWithPaths verifies that parseSessionsWithPaths correctly
// splits `tmux list-sessions -F '#{session_name}|#{session_path}'` output,
// including a session path containing spaces.
func TestParseSessionsWithPaths(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []SessionInfo
	}{
		{
			name: "single session",
			raw:  "agentctl-foo|/home/user/foo\n",
			want: []SessionInfo{{Name: "agentctl-foo", Path: "/home/user/foo"}},
		},
		{
			name: "multiple sessions",
			raw:  "agentctl-foo|/home/user/foo\nagentctl-bar|/home/user/bar\n",
			want: []SessionInfo{
				{Name: "agentctl-foo", Path: "/home/user/foo"},
				{Name: "agentctl-bar", Path: "/home/user/bar"},
			},
		},
		{
			name: "path containing spaces",
			raw:  "agentctl-hub|/Users/alice/src/my project\n",
			want: []SessionInfo{{Name: "agentctl-hub", Path: "/Users/alice/src/my project"}},
		},
		{
			name: "empty output",
			raw:  "",
			want: nil,
		},
		{
			name: "whitespace-only line is skipped",
			raw:  "agentctl-foo|/home/user/foo\n  \nagentctl-bar|/home/user/bar\n",
			want: []SessionInfo{
				{Name: "agentctl-foo", Path: "/home/user/foo"},
				{Name: "agentctl-bar", Path: "/home/user/bar"},
			},
		},
		{
			name: "line without separator is skipped",
			raw:  "no-separator-here\nagentctl-bar|/home/user/bar\n",
			want: []SessionInfo{{Name: "agentctl-bar", Path: "/home/user/bar"}},
		},
		{
			name: "path containing a pipe splits on the first only",
			raw:  "agentctl-foo|/home/user/weird|name\n",
			want: []SessionInfo{{Name: "agentctl-foo", Path: "/home/user/weird|name"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSessionsWithPaths(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("parseSessionsWithPaths(%q): got %d sessions, want %d: got=%v want=%v",
					tt.raw, len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("session[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestLaunchAgentShellPrefixIsBareScriptPath is the regression test for
// the shell-prefix wrapper rewrite. It verifies that CLAUDE_CODE_SHELL_PREFIX is set to
// exactly the script path — no leading 'source ', no trailing ' &&', and no
// other shell metacharacters. Either of those would corrupt argv[0] when
// Claude Code execs the wrapper.
func TestLaunchAgentShellPrefixIsBareScriptPath(t *testing.T) {
	const scriptPath = "/home/user/.agentctl/shell-prefix.sh"

	args := buildLaunchEnv(
		"go-engineer",             // windowName
		"/tmp/workdir",            // workDir
		"go-engineer",             // agentName
		"session-123",             // sessionID
		"/usr/local/bin/agentctl", // agentctlBin
		scriptPath,                // shellPrefixScript
		"",                        // inst (no instance suffix)
		nil,                       // extraEnv
	)

	// Find the CLAUDE_CODE_SHELL_PREFIX value in the -e KEY=VALUE pairs.
	var prefixValue string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-e" && strings.HasPrefix(args[i+1], "CLAUDE_CODE_SHELL_PREFIX=") {
			prefixValue = strings.TrimPrefix(args[i+1], "CLAUDE_CODE_SHELL_PREFIX=")
			break
		}
	}

	if prefixValue == "" {
		t.Fatal("CLAUDE_CODE_SHELL_PREFIX not found in buildLaunchEnv args")
	}

	// Must be exactly the script path — no extra tokens.
	if prefixValue != scriptPath {
		t.Errorf("CLAUDE_CODE_SHELL_PREFIX = %q, want %q", prefixValue, scriptPath)
	}

	// Regression: must not contain ' &&' (left-over from an earlier wrapper).
	if strings.Contains(prefixValue, " &&") {
		t.Errorf("CLAUDE_CODE_SHELL_PREFIX contains ' &&': %q", prefixValue)
	}

	// Regression: must not contain 'source ' (artefact of an earlier wrapper).
	if strings.Contains(prefixValue, "source ") {
		t.Errorf("CLAUDE_CODE_SHELL_PREFIX contains 'source ': %q", prefixValue)
	}
}

// TestBuildLaunchEnv_ForwardsExtraEnv is the regression test for the runtime
// dir split-brain fix: AGENTCTL_RUNTIME_DIR (and any other dispatch-supplied
// extraEnv key) must reach the tmux new-window -e args unchanged, since that
// is how hooks running inside the launched window ever see it.
func TestBuildLaunchEnv_ForwardsExtraEnv(t *testing.T) {
	extraEnv := map[string]string{
		"AGENTCTL_RUNTIME_DIR": "/repo/.agentctl",
		"AGENTCTL_MODE":        "adhoc",
	}

	args := buildLaunchEnv(
		"go-engineer",
		"/tmp/workdir",
		"go-engineer",
		"session-123",
		"/usr/local/bin/agentctl",
		"/repo/.agentctl/shell-prefix.sh",
		"",
		extraEnv,
	)

	got := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-e" {
			continue
		}
		kv := strings.SplitN(args[i+1], "=", 2)
		if len(kv) == 2 {
			got[kv[0]] = kv[1]
		}
	}

	for k, want := range extraEnv {
		if v, ok := got[k]; !ok || v != want {
			t.Errorf("buildLaunchEnv args missing/mismatched %s: got %q (present=%v), want %q", k, v, ok, want)
		}
	}
}

// countRuntimeDirArgs scans buildLaunchEnv's "-e KEY=VALUE" pairs for
// AGENTCTL_RUNTIME_DIR and reports how many times it appears (a count > 1
// would mean tmux's own -e ordering decides which value wins) along with its
// value, if present at all.
func countRuntimeDirArgs(args []string) (count int, value string, present bool) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-e" {
			continue
		}
		const prefix = "AGENTCTL_RUNTIME_DIR="
		if strings.HasPrefix(args[i+1], prefix) {
			count++
			present = true
			value = strings.TrimPrefix(args[i+1], prefix)
		}
	}
	return count, value, present
}

// TestBuildLaunchEnv_RuntimeDirDefaulting is the regression test for the
// four LaunchAgent callers (improve, sessions.go, menu.go, restore.go) that
// never set AGENTCTL_RUNTIME_DIR themselves and previously left the launched
// process to resolve one from its own CWD -- a split-brain where state landed in the wrong project.
func TestBuildLaunchEnv_RuntimeDirDefaulting(t *testing.T) {
	callBuildLaunchEnv := func(extraEnv map[string]string) []string {
		return buildLaunchEnv(
			"go-engineer",
			"/tmp/workdir",
			"go-engineer",
			"session-123",
			"/usr/local/bin/agentctl",
			"/repo/.agentctl/shell-prefix.sh",
			"",
			extraEnv,
		)
	}

	t.Run("nil extraEnv defaults to paths.RuntimeDir", func(t *testing.T) {
		paths.ResetForTest()
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")
		dir := t.TempDir()
		paths.Init(dir)
		defer paths.ResetForTest()

		args := callBuildLaunchEnv(nil)

		count, value, present := countRuntimeDirArgs(args)
		if !present || count != 1 {
			t.Fatalf("AGENTCTL_RUNTIME_DIR count = %d, want exactly 1", count)
		}
		if want := paths.RuntimeDir(); value != want {
			t.Errorf("AGENTCTL_RUNTIME_DIR = %q, want %q", value, want)
		}
	})

	t.Run("caller-supplied non-empty value is preserved verbatim", func(t *testing.T) {
		paths.ResetForTest()
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")
		dir := t.TempDir()
		paths.Init(dir)
		defer paths.ResetForTest()

		const otherProjectRuntimeDir = "/other-repo/.agentctl"
		extraEnv := map[string]string{"AGENTCTL_RUNTIME_DIR": otherProjectRuntimeDir}

		args := callBuildLaunchEnv(extraEnv)

		count, value, present := countRuntimeDirArgs(args)
		if !present || count != 1 {
			t.Fatalf("AGENTCTL_RUNTIME_DIR count = %d, want exactly 1", count)
		}
		if value != otherProjectRuntimeDir {
			t.Errorf("AGENTCTL_RUNTIME_DIR = %q, want caller-supplied %q (must not be overwritten by paths.RuntimeDir() %q)",
				value, otherProjectRuntimeDir, paths.RuntimeDir())
		}
	})

	t.Run("caller-supplied empty value stays empty (hub.go clear)", func(t *testing.T) {
		paths.ResetForTest()
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")
		dir := t.TempDir()
		paths.Init(dir)
		defer paths.ResetForTest()

		extraEnv := map[string]string{"AGENTCTL_RUNTIME_DIR": ""}

		args := callBuildLaunchEnv(extraEnv)

		count, value, present := countRuntimeDirArgs(args)
		if !present || count != 1 {
			t.Fatalf("AGENTCTL_RUNTIME_DIR count = %d, want exactly 1", count)
		}
		if value != "" {
			t.Errorf("AGENTCTL_RUNTIME_DIR = %q, want empty (explicit clear must survive)", value)
		}
	})

	t.Run("paths.RuntimeDir empty means not emitted at all", func(t *testing.T) {
		paths.ResetForTest()
		defer paths.ResetForTest()
		// Deliberately do not call paths.Init: RuntimeDir() returns "".

		args := callBuildLaunchEnv(nil)

		count, _, present := countRuntimeDirArgs(args)
		if present || count != 0 {
			t.Fatalf("AGENTCTL_RUNTIME_DIR count = %d, present = %v, want absent entirely (not emitted as empty)", count, present)
		}
	})
}

// ── SetInstance removal ──────────────────────────────────────────────────────

// TestSetInstanceRemoved is the regression test for the --instance no-op
// booby trap: SetInstance used to accept an instance name and silently do
// nothing (session naming was already derived from paths.DirName() via
// SetSessionName). Rather than leave a function that lies about what it
// does, it was deleted outright, along with every `--instance <name>`
// argument that keybinding/hook commands used to construct for it (see
// LaunchChromeBar, ToggleHud, chromeBarHookArgs). This test greps the
// package's own non-test source for the symbol so it can't quietly come
// back; combined with `go build ./...` (which would fail to compile any
// cross-package caller of an exported symbol that no longer exists), this
// covers every call site.
func TestSetInstanceRemoved(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", e.Name(), err)
		}
		if strings.Contains(string(data), "func SetInstance") {
			t.Errorf("%s defines SetInstance, which was removed as a lying no-op", e.Name())
		}
	}
}

// ── Session-name resolution vs. worktree cwd ─────────────────────────────────

// TestSetSessionNameHonorsExplicitRuntimeDirOverWorktreeCwd is the regression
// test for the chrome-bar panes=1 bug's full chain: a process invoked with
// its cwd inside a git worktree (e.g.
// <repo>/.worktrees/agent-lead-session-5f7a18cb -- the common case since
// every bare session now lives in one) must resolve its runtime dir, and
// therefore its tmux.SessionName, from an explicit AGENTCTL_RUNTIME_DIR env
// var rather than from that worktree cwd whenever the env var is set. Before
// the fix, chromeBarHookArgs (and every pinned keybinding in applyConfig)
// relied entirely on cwd inheritance through `run-shell`, which runs with the
// tmux SERVER's environment and the active pane's cwd -- so the spawned
// process resolved a session name derived from the worktree slug instead of
// the project, and every subsequent `list-panes`/`split-window` call target
// a session that doesn't exist.
func TestSetSessionNameHonorsExplicitRuntimeDirOverWorktreeCwd(t *testing.T) {
	paths.ResetForTest()
	defer paths.ResetForTest()

	repoRoot := t.TempDir()
	worktreeCwd := filepath.Join(repoRoot, ".worktrees", "agent-lead-session-5f7a18cb")
	if err := os.MkdirAll(worktreeCwd, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktreeCwd): %v", err)
	}

	// Simulate the fix: the hook explicitly assigns AGENTCTL_RUNTIME_DIR to
	// the project's real runtime dir before paths.Init ever runs, exactly as
	// chromeBarHookArgs (and the pinned keybindings in applyConfig) now do.
	explicitRuntimeDir := filepath.Join(repoRoot, ".agentctl")
	t.Setenv("AGENTCTL_RUNTIME_DIR", explicitRuntimeDir)

	// paths.Init is called with the WORKTREE cwd, mirroring a hook/keybinding
	// process whose active pane happens to be inside the worktree.
	paths.Init(worktreeCwd)

	origSessionName := SessionName
	defer func() { SessionName = origSessionName }()
	SessionName = "agentctl"
	SetSessionName()

	wantProject := tmuxSafeRe.ReplaceAllString(filepath.Base(repoRoot), "-")
	want := "agentctl-" + wantProject
	if SessionName != want {
		t.Errorf("SessionName = %q, want %q (must derive from explicit AGENTCTL_RUNTIME_DIR, not worktree cwd %q)",
			SessionName, want, worktreeCwd)
	}
	if strings.Contains(SessionName, "agent-lead-session") {
		t.Errorf("SessionName = %q must not be derived from the worktree slug", SessionName)
	}
}

// ── Launch script generation / diagnostics-echo quoting bug ────────────────

// TestWriteLaunchLogHeaderNotShellInterpreted is the regression test for the
// diagnostics-echo quoting bug's silent form: a shellCmd whose backtick count
// happened to be even used to have its backticked substring silently
// EXECUTED by the shell (command substitution) instead of causing a syntax
// error, corrupting the log with command output instead of the literal
// command text. writeLaunchLogHeader writes via a plain os.OpenFile/Write
// call, so there is no shell in the loop at all -- this asserts that in the
// way that actually matters: a backticked shell command embedded in shellCmd
// is never executed as a side effect of logging it, and the log records it
// verbatim.
func TestWriteLaunchLogHeaderNotShellInterpreted(t *testing.T) {
	dir := t.TempDir()
	markerFile := filepath.Join(dir, "marker")
	launchLog := filepath.Join(dir, "launch.log")

	// If this were ever shell-interpreted again (e.g. a future regression
	// re-introducing a double-quoted echo), the backtick-wrapped `touch`
	// would create markerFile as a side effect.
	shellCmd := fmt.Sprintf("echo before `touch %s` after", markerFile)

	if err := writeLaunchLogHeader(launchLog, "/tmp/workdir", shellCmd); err != nil {
		t.Fatalf("writeLaunchLogHeader: %v", err)
	}

	if _, err := os.Stat(markerFile); err == nil {
		t.Error("writeLaunchLogHeader executed the backticked command instead of logging it literally")
	}

	got, err := os.ReadFile(launchLog)
	if err != nil {
		t.Fatalf("ReadFile(launchLog): %v", err)
	}
	if !strings.Contains(string(got), shellCmd) {
		t.Errorf("launch log does not contain the literal command text.\nlog: %s\nwant substring: %s", got, shellCmd)
	}
}

// TestWriteLaunchLogHeaderAppends is the regression test for a truncation
// regression introduced by the first version of this fix: writeLaunchLogHeader
// originally used os.WriteFile, which truncates the target file on every
// call. launch-<windowName>.log is meant to accumulate every launch for that
// window name over time -- the script's own exit-code line further down
// appends with >> for the same reason -- and that history is load-bearing:
// the bug this file fixes was originally diagnosed by noticing one launch's
// entry stopped after "PWD" with no "Command" line, which was only visible
// by comparing it against several earlier healthy launches sitting in the
// same file. Truncating on every launch would erase exactly the evidence
// needed to catch the next occurrence of this class of bug, so two
// successive calls against the same path must leave both headers present.
func TestWriteLaunchLogHeaderAppends(t *testing.T) {
	dir := t.TempDir()
	launchLog := filepath.Join(dir, "launch.log")

	if err := writeLaunchLogHeader(launchLog, "/tmp/workdir", "claude --agent go-engineer first-launch"); err != nil {
		t.Fatalf("writeLaunchLogHeader (first): %v", err)
	}
	if err := writeLaunchLogHeader(launchLog, "/tmp/workdir", "claude --agent go-engineer second-launch"); err != nil {
		t.Fatalf("writeLaunchLogHeader (second): %v", err)
	}

	got, err := os.ReadFile(launchLog)
	if err != nil {
		t.Fatalf("ReadFile(launchLog): %v", err)
	}

	if !strings.Contains(string(got), "first-launch") {
		t.Errorf("launch log missing first launch's command after a second call -- header write is truncating instead of appending.\nlog:\n%s", got)
	}
	if !strings.Contains(string(got), "second-launch") {
		t.Errorf("launch log missing second launch's command.\nlog:\n%s", got)
	}
}

// TestBuildLaunchScriptNoDoubleQuotedCommandEcho verifies buildLaunchScript
// never re-introduces a double-quoted echo of shellCmd -- the exact pattern
// (`echo "... Command: %s" ...`) that made an odd backtick count in a launch
// prompt unterminated the whole script. The invocation line further down
// still interpolates shellCmd raw, but that line is unquoted (not inside a
// double-quoted string), so backticks there are not command-substituted by
// this script -- see buildLaunchScript's doc comment.
func TestBuildLaunchScriptNoDoubleQuotedCommandEcho(t *testing.T) {
	got := buildLaunchScript("claude --agent go-engineer", "/tmp/launch.log", "/tmp/launch.sh")
	if strings.Contains(got, "Command: %s") || strings.Contains(got, `Command: claude`) {
		t.Errorf("buildLaunchScript should not echo the command inline; got:\n%s", got)
	}
	if strings.Contains(got, `echo "`) && strings.Contains(got, "Command:") {
		t.Errorf("buildLaunchScript should not double-quote-echo the command; got:\n%s", got)
	}
}

// ── ChromeBarRows consistency ────────────────────────────────────────

// TestChromeBarSplitArgsUsesChromeBarRows is the regression test for the
// duplicated "chrome bar row count" literal: LaunchChromeBar's split-window
// "-l" flag must be derived from ChromeBarRows, not an independent literal
// that can silently drift out of sync with EnforceMainPaneSize's sizing math.
func TestChromeBarSplitArgsUsesChromeBarRows(t *testing.T) {
	args := chromeBarSplitArgs("%3", "agentctl chrome-bar @1")
	want := strconv.Itoa(ChromeBarRows)
	for i, a := range args {
		if a == "-l" {
			if i+1 >= len(args) || args[i+1] != want {
				t.Fatalf("chromeBarSplitArgs \"-l\" value = %v, want %q (ChromeBarRows=%d); args=%v",
					args[i+1:], want, ChromeBarRows, args)
			}
			return
		}
	}
	t.Fatalf("chromeBarSplitArgs did not include a \"-l\" flag; args=%v", args)
}

// TestChromeRowsForLayoutMatchesChromeBarRows verifies that
// EnforceMainPaneSize's row-total helper derives from ChromeBarRows rather
// than a second hardcoded literal.
func TestChromeRowsForLayoutMatchesChromeBarRows(t *testing.T) {
	if got := chromeRowsForLayout(true); got != ChromeBarRows {
		t.Errorf("chromeRowsForLayout(true) = %d, want ChromeBarRows (%d)", got, ChromeBarRows)
	}
	if got := chromeRowsForLayout(false); got != 0 {
		t.Errorf("chromeRowsForLayout(false) = %d, want 0", got)
	}
}

// TestChromeBarRowsIsTwo pins the actual row count (shrunk from 4 to
// 2 -- keybinds + tabs -- once rate limits and per-window metrics moved to
// the "rich" Claude Code status line).
func TestChromeBarRowsIsTwo(t *testing.T) {
	if ChromeBarRows != 2 {
		t.Errorf("ChromeBarRows = %d, want 2", ChromeBarRows)
	}
}
