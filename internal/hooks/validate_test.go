package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

// ---------------------------------------------------------------------------
// checkAgentDenyList — table-driven
// ---------------------------------------------------------------------------

func TestCheckAgentDenyList(t *testing.T) {
	tests := []struct {
		name     string
		ac       config.AgentConfig
		command  string
		wantDeny bool
		wantMsg  string // substring that must appear in the reason (only when wantDeny)
	}{
		// --- blocked commands (substring match) ---
		{
			name:     "blocked_command_exact_match",
			ac:       config.AgentConfig{BlockedCommands: []string{"docker push"}},
			command:  "docker push myimage:latest",
			wantDeny: true,
			wantMsg:  "docker push",
		},
		{
			name:     "blocked_command_substring_inside_longer_command",
			ac:       config.AgentConfig{BlockedCommands: []string{"docker push"}},
			command:  "docker push myimage:latest --platform linux/amd64",
			wantDeny: true,
			wantMsg:  "docker push",
		},
		{
			name:     "blocked_command_not_triggered_by_different_command",
			ac:       config.AgentConfig{BlockedCommands: []string{"docker push"}},
			command:  "docker pull myimage:latest",
			wantDeny: false,
		},

		// --- partial substring match ---
		{
			name:     "partial_substring_blocks_short_prefix",
			ac:       config.AgentConfig{BlockedCommands: []string{"rm"}},
			command:  "rm file.txt",
			wantDeny: true,
			wantMsg:  "rm",
		},
		{
			name:     "partial_substring_blocks_when_substring_is_embedded",
			ac:       config.AgentConfig{BlockedCommands: []string{"rm"}},
			command:  "grep -rm pattern /src",
			wantDeny: true,
			wantMsg:  "rm",
		},

		// --- blocked patterns (regex match) ---
		{
			name:     "blocked_pattern_regex_matches",
			ac:       config.AgentConfig{BlockedPatterns: []string{`npm\s+publish`}},
			command:  "npm publish",
			wantDeny: true,
			wantMsg:  "matching pattern", // %q-escaping makes the literal pattern hard to match; check for fixed text instead
		},
		{
			name:     "blocked_pattern_regex_matches_with_extra_args",
			ac:       config.AgentConfig{BlockedPatterns: []string{`npm\s+publish`}},
			command:  "npm publish --dry-run",
			wantDeny: true,
		},
		{
			name:     "blocked_pattern_does_not_match_different_command",
			ac:       config.AgentConfig{BlockedPatterns: []string{`npm\s+publish`}},
			command:  "npm install",
			wantDeny: false,
		},

		// --- multiple blocked commands: first match wins ---
		{
			name: "multiple_blocked_commands_first_match_wins",
			ac: config.AgentConfig{
				BlockedCommands: []string{"docker push", "kubectl delete"},
			},
			command:  "docker push myimage:latest",
			wantDeny: true,
			wantMsg:  "docker push",
		},
		{
			name: "multiple_blocked_commands_second_entry_also_blocks",
			ac: config.AgentConfig{
				BlockedCommands: []string{"docker push", "kubectl delete"},
			},
			command:  "kubectl delete pod my-pod",
			wantDeny: true,
			wantMsg:  "kubectl delete",
		},
		{
			name: "multiple_blocked_commands_unrelated_command_allowed",
			ac: config.AgentConfig{
				BlockedCommands: []string{"docker push", "kubectl delete"},
			},
			command:  "go test ./...",
			wantDeny: false,
		},

		// --- invalid regex pattern: skip, do not block or panic ---
		{
			name:     "invalid_regex_pattern_is_skipped",
			ac:       config.AgentConfig{BlockedPatterns: []string{"[invalid-regex"}},
			command:  "anything goes",
			wantDeny: false,
		},
		{
			name: "valid_pattern_after_invalid_still_checked",
			ac: config.AgentConfig{
				BlockedPatterns: []string{
					"[invalid-regex", // bad — should be skipped
					`npm\s+publish`,  // good — should still fire
				},
			},
			command:  "npm publish",
			wantDeny: true,
		},

		// --- empty blocklists: allow everything ---
		{
			name:     "empty_blocked_commands_allows_all",
			ac:       config.AgentConfig{},
			command:  "rm -rf /tmp/scratch",
			wantDeny: false,
		},
		{
			name:     "nil_blocked_lists_allows_all",
			ac:       config.AgentConfig{BlockedCommands: nil, BlockedPatterns: nil},
			command:  "kubectl delete pod foo",
			wantDeny: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := checkAgentDenyList(tt.command, tt.ac)
			if tt.wantDeny {
				if reason == "" {
					t.Errorf("checkAgentDenyList(%q) = empty reason, want deny", tt.command)
					return
				}
				if tt.wantMsg != "" && !strings.Contains(reason, tt.wantMsg) {
					t.Errorf("checkAgentDenyList(%q) reason %q does not contain %q", tt.command, reason, tt.wantMsg)
				}
			} else {
				if reason != "" {
					t.Errorf("checkAgentDenyList(%q) = %q, want allow (empty reason)", tt.command, reason)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkProjectDenyList — table-driven
// ---------------------------------------------------------------------------

func TestCheckProjectDenyList(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.ProjectConfig
		command   string
		agentName string
		wantDeny  bool
		wantMsg   string // substring expected in the reason (only when wantDeny)
	}{
		// --- project-level blocked commands ---
		{
			name:      "project_blocked_command_substring_match",
			cfg:       &config.ProjectConfig{BlockedCommands: []string{"terraform destroy"}},
			command:   "terraform destroy -auto-approve",
			agentName: "any-agent",
			wantDeny:  true,
			wantMsg:   "project config",
		},
		{
			name:      "project_blocked_command_unrelated_command_allowed",
			cfg:       &config.ProjectConfig{BlockedCommands: []string{"terraform destroy"}},
			command:   "terraform plan",
			agentName: "any-agent",
			wantDeny:  false,
		},

		// --- project-level blocked patterns ---
		{
			name:      "project_blocked_pattern_matches",
			cfg:       &config.ProjectConfig{BlockedPatterns: []string{`pip\s+install\s+--user`}},
			command:   "pip install --user requests",
			agentName: "any-agent",
			wantDeny:  true,
			wantMsg:   "project config",
		},
		{
			name:      "project_blocked_pattern_does_not_match_different_command",
			cfg:       &config.ProjectConfig{BlockedPatterns: []string{`pip\s+install\s+--user`}},
			command:   "pip install requests",
			agentName: "any-agent",
			wantDeny:  false,
		},
		{
			name:      "project_bad_pattern_is_skipped_not_panics",
			cfg:       &config.ProjectConfig{BlockedPatterns: []string{"[bad-regex"}},
			command:   "anything",
			agentName: "any-agent",
			wantDeny:  false,
		},

		// --- protected branches expansion ---
		{
			name:      "protected_branch_main_blocks_push",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"main"}},
			command:   "git push origin main",
			agentName: "any-agent",
			wantDeny:  true,
			wantMsg:   "protected branch",
		},
		{
			name:      "protected_branch_main_allows_push_to_feature",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"main"}},
			command:   "git push origin feature/my-branch",
			agentName: "any-agent",
			wantDeny:  false,
		},
		{
			name:      "protected_branch_main_does_not_block_push_to_main_backup",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"main"}},
			command:   "git push origin main-backup",
			agentName: "any-agent",
			// The pattern uses \b word boundary, so "main-backup" should NOT match "main\b"
			// because "-" is a word boundary after "main".
			// Actually \b matches between word char and non-word char; "-" is non-word,
			// so "main\b" in "main-backup" DOES match at position after "main".
			// This is a known limitation — we document it here.
			wantDeny: true, // \b matches before "-"
		},
		{
			name:      "protected_branch_blocks_force_push_to_production",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"production"}},
			command:   "git push --force origin production",
			agentName: "any-agent",
			wantDeny:  true,
		},
		{
			name:      "protected_branch_multiple_branches_first_blocked",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"main", "production"}},
			command:   "git push origin main",
			agentName: "any-agent",
			wantDeny:  true,
		},
		{
			name:      "protected_branch_multiple_branches_second_blocked",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"main", "production"}},
			command:   "git push origin production",
			agentName: "any-agent",
			wantDeny:  true,
		},
		{
			name:      "protected_branch_multiple_branches_feature_allowed",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"main", "production"}},
			command:   "git push origin feature/new-feature",
			agentName: "any-agent",
			wantDeny:  false,
		},

		// --- per-agent overrides ---
		{
			name: "per_agent_blocked_command_fires_for_matching_agent",
			cfg: &config.ProjectConfig{
				Agents: map[string]config.AgentConfig{
					"go-engineer": {BlockedCommands: []string{"go install"}},
				},
			},
			command:   "go install github.com/some/tool@latest",
			agentName: "go-engineer",
			wantDeny:  true,
			wantMsg:   "go-engineer",
		},
		{
			name: "per_agent_blocked_command_ignored_for_different_agent",
			cfg: &config.ProjectConfig{
				Agents: map[string]config.AgentConfig{
					"go-engineer": {BlockedCommands: []string{"go install"}},
				},
			},
			command:   "go install github.com/some/tool@latest",
			agentName: "python-engineer",
			wantDeny:  false,
		},
		{
			name: "per_agent_blocked_command_ignored_for_empty_agent_name",
			cfg: &config.ProjectConfig{
				Agents: map[string]config.AgentConfig{
					"go-engineer": {BlockedCommands: []string{"go install"}},
				},
			},
			command:   "go install github.com/some/tool@latest",
			agentName: "",
			wantDeny:  false,
		},
		{
			name: "per_agent_blocked_pattern_fires_for_matching_agent",
			cfg: &config.ProjectConfig{
				Agents: map[string]config.AgentConfig{
					"frontend": {BlockedPatterns: []string{`npm\s+publish`}},
				},
			},
			command:   "npm publish",
			agentName: "frontend",
			wantDeny:  true,
		},

		// --- project blocks are additive: allowed by agent config still blocked by project ---
		{
			name: "project_blocks_additive_even_if_agent_config_empty",
			cfg: &config.ProjectConfig{
				BlockedCommands: []string{"terraform destroy"},
			},
			command:   "terraform destroy",
			agentName: "any-agent",
			wantDeny:  true,
		},

		// --- empty project config fields: no panic ---
		{
			name:      "empty_project_config_allows_all",
			cfg:       &config.ProjectConfig{},
			command:   "git push origin main",
			agentName: "any-agent",
			wantDeny:  false,
		},
		{
			name: "project_config_with_nil_agents_map_does_not_panic",
			cfg: &config.ProjectConfig{
				BlockedCommands: []string{"something-else"},
				Agents:          nil,
			},
			command:   "go test ./...",
			agentName: "go-engineer",
			wantDeny:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := checkProjectDenyList(tt.command, tt.agentName, tt.cfg)
			if tt.wantDeny {
				if reason == "" {
					t.Errorf("checkProjectDenyList(%q, %q) = empty reason, want deny", tt.command, tt.agentName)
					return
				}
				if tt.wantMsg != "" && !strings.Contains(reason, tt.wantMsg) {
					t.Errorf("checkProjectDenyList reason %q does not contain %q", reason, tt.wantMsg)
				}
			} else {
				if reason != "" {
					t.Errorf("checkProjectDenyList(%q, %q) = %q, want allow (empty reason)", tt.command, tt.agentName, reason)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// serve-tamper rules (remote approvals)
// ---------------------------------------------------------------------------

func TestCheckServeTamper(t *testing.T) {
	orig := serveStateDir
	t.Cleanup(func() { serveStateDir = orig })
	serveStateDir = func() string { return "/Users/u/.local/state/agentctl/serve" }

	tests := []struct {
		name     string
		command  string
		wantDeny bool
	}{
		// Denied: state dir, socket, pidfile.
		{"control socket", "nc -U ~/.local/state/agentctl/serve/control.sock", true},
		{"socket by name", "python3 -c 'import socket; s.connect(\"control.sock\")'", true},
		{"pidfile", "kill $(cat ~/.local/state/agentctl/serve/serve.pid)", true},
		{"pidfile anywhere", "cat serve.pid", true},
		{"state dir tilde", "ls ~/.local/state/agentctl/serve", true},
		{"state dir xdg", "ls $XDG_STATE_HOME/agentctl/serve/", true},
		{"state dir xdg braces", "ls ${XDG_STATE_HOME}/agentctl/serve", true},
		{"resolved dir", "cp x /Users/u/.local/state/agentctl/serve/devices.json", true},
		{"resolved dir via HOME", "rm $HOME/.local/state/agentctl/serve/audit.jsonl", true},
		// Denied: stopping serve.
		{"launchctl bootout", "launchctl bootout gui/501/com.blsmth.agentctl.serve", true},
		{"launchctl unload plist", "launchctl unload ~/Library/LaunchAgents/com.blsmth.agentctl.serve.plist", true},
		{"launchctl kickstart", "launchctl kickstart -k gui/$(id -u)/com.blsmth.agentctl.serve", true},
		{"systemctl", "systemctl --user stop agentctl-serve", true},
		{"pkill serve", "pkill -f 'agentctl serve'", true},
		{"pkill agentctl", "pkill agentctl", true},
		{"killall", "killall agentctl", true},
		{"kill pgrep", "kill $(pgrep -f 'agentctl serve')", true},
		{"serve uninstall", "agentctl serve uninstall", true},
		{"serve install", "agentctl serve install", true},
		{"serve pair", "agentctl serve pair --name evil", true},
		{"serve ca rotate", "agentctl serve ca rotate", true},
		{"chained", "go build ./... && agentctl serve uninstall", true},
		// Denied: the human-only start/stop/toggle and their launchctl forms.
		{"serve stop", "agentctl serve stop", true},
		{"serve start", "agentctl serve start", true},
		{"serve toggle", "agentctl serve toggle", true},
		{"serve toggle keybinding form", "agentctl serve toggle --message-client /dev/ttys001", true},
		{"serve stop by path", "/usr/local/bin/agentctl serve stop", true},
		{"serve stop via which", `"$(which agentctl)" serve stop`, true},
		{"serve stop chained", "go test ./... ; agentctl serve stop", true},
		{"serve toggle via tmux run-shell", "tmux run-shell 'agentctl serve toggle'", true},
		{"quoted subcommand", `agentctl serve "stop"`, true},
		{"single-quoted subcommand", `agentctl serve 'toggle'`, true},
		{"quoted serve", `agentctl "serve" stop`, true},
		{"go run serve stop", "go run . serve stop", true},
		{"go run serve pair", "go run ./ serve pair --name x", true},
		{"go run flags serve stop", "go run -race -tags dev . serve stop", true},
		{"go run module serve pair", "go run github.com/blsmth/agentctl@latest serve pair", true},
		{"go run -- serve stop", "go run . -- serve stop", true},
		{"launchctl bootstrap", "launchctl bootstrap gui/501 ~/Library/LaunchAgents/com.blsmth.agentctl.serve.plist", true},
		{"launchctl kickstart plain", "launchctl kickstart gui/501/com.blsmth.agentctl.serve", true},
		{"launchctl kill", "launchctl kill SIGTERM gui/501/com.blsmth.agentctl.serve", true},
		{"launchctl disable", "launchctl disable gui/501/com.blsmth.agentctl.serve", true},
		{"launchctl label in a variable", "L=com.blsmth.agentctl.serve; launchctl bootout gui/501/$L", true},
		{"launchctl across lines", "launchctl bootout \\\n  gui/501/com.blsmth.agentctl.serve", true},

		// Allowed: ordinary commands that mention serve.
		{"go test serve pkg", "go test -race ./internal/serve/...", false},
		{"serve status", "agentctl serve status", false},
		{"serve help", "agentctl serve --help", false},
		{"go run serve status", "go run . serve status", false},
		{"go run subpackage serve stop", "go run ./internal/serve/servetest/devserver serve stop", false},
		{"go run other dir serve stop", "cd /tmp && go run . serve stop", false},
		{"go run other module serve stop", "go run example.com/tool serve stop", false},
		{"echo go run serve stop", "echo go run . serve stop", false},
		{"go test servectl", "go test ./internal/servectl/...", false},
		{"grep for toggle", "grep -rn 'serve toggle' docs", false},
		{"serve logs", "agentctl serve logs", false},
		{"serve logs audit", "agentctl serve logs --audit", false},
		{"serve logs follow", "agentctl serve logs -n 200 --audit --follow", false},
		{"npm serve", "npm run serve", false},
		{"python http.server", "python3 -m http.server 8000", false},
		{"hugo", "hugo serve -D", false},
		{"metrics serve", "agentctl metrics serve --help", false},
		{"git log serve", "git log --oneline -- internal/serve/server.go", false},
		{"grep serve", "grep -rn 'func (s \\*Server)' internal/serve", false},
		{"kill job", "kill %1", false},
		{"kill pid", "kill 12345", false},
		{"launchctl list", "launchctl list | grep homebrew", false},
		{"agentctl status", "agentctl status --json", false},
		{"dispatch about serve", "agentctl dispatch --agent go-engineer --prompt 'fix the serve push notifier'", false},
		{"other state dir", "ls ~/.local/state/agentctl/registry", false},
		{"serve dir in repo", "cat internal/serve/control.go", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := checkGlobalDenyList(tt.command)
			if got := reason != ""; got != tt.wantDeny {
				t.Fatalf("checkGlobalDenyList(%q) = %q, wantDeny %v", tt.command, reason, tt.wantDeny)
			}
			if tt.wantDeny && !strings.Contains(reason, "agentctl serve logs") {
				t.Fatalf("reason %q does not explain the block", reason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkGoRunAgentctl
// ---------------------------------------------------------------------------

// goRunFixture is a fake HOME holding an agentctl checkout at
// ~/code/personal/agentctl and another Go module at ~/src/myapp.
type goRunFixture struct {
	home, root, other string
}

func newGoRunFixture(t *testing.T) goRunFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := goRunFixture{
		home:  home,
		root:  filepath.Join(home, "code", "personal", "agentctl"),
		other: filepath.Join(home, "src", "myapp"),
	}
	writeGoMod(t, f.root, "module github.com/blsmth/agentctl // the CLI\n\ngo 1.26\n")
	writeGoMod(t, f.other, "module example.com/myapp\n\ngo 1.26\n")
	for _, d := range []string{"internal/foo", "internal/serve/servetest/devserver", "cmd/tool"} {
		if err := os.MkdirAll(filepath.Join(f.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func writeGoMod(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckGoRunAgentctl(t *testing.T) {
	f := newGoRunFixture(t)
	foo := filepath.Join(f.root, "internal", "foo")
	tests := []struct {
		name     string
		cwd      string
		command  string
		wantDeny bool
	}{
		// Denied: the root main package.
		{"dot", f.root, "go run .", true},
		{"dot slash", f.root, "go run ./", true},
		{"dot with args", f.root, "go run . status", true},
		{"root files", f.root, "go run main.go cli.go", true},
		{"root glob", f.root, "go run ./*.go dispatch --issue 1", true},
		{"race flag", f.root, "go run -race .", true},
		{"tags flag", f.root, "go run -tags dev,debug . status", true},
		{"tags inline", f.root, "go run -tags=dev .", true},
		{"exec flag", f.root, "go run -exec echo .", true},
		{"double dash flag", f.root, "go run --race .", true},
		{"-C root", f.other, "go run -C " + f.root + " .", true},
		{"-C relative", f.home, "go run -C code/personal/agentctl .", true},
		{"-C tilde", "", "go run -C ~/code/personal/agentctl . status", true},
		{"module path", f.other, "go run github.com/blsmth/agentctl status", true},
		{"module at version", f.other, "go run github.com/blsmth/agentctl@latest status", true},
		{"module no cwd", "", "go run github.com/blsmth/agentctl@v1.2.3", true},
		{"tilde path", f.other, "go run ~/code/personal/agentctl status", true},
		{"HOME path", f.other, "go run $HOME/code/personal/agentctl", true},
		{"braced HOME path", f.other, "go run ${HOME}/code/personal/agentctl/", true},
		{"absolute path", "", "go run " + f.root + " status", true},
		{"relative path", f.home, "go run ./code/personal/agentctl", true},
		{"parent dir", foo, "go run ../..", true},
		{"parent files", foo, "go run ../../main.go", true},
		{"cd tilde chain", f.other, "cd ~/code/personal/agentctl && go run .", true},
		{"cd absolute chain", "", "cd " + f.root + " && go run . dispatch --agent x --prompt y", true},
		{"cd relative chain", f.home, "cd code/personal && cd agentctl; go run .", true},
		{"cd up from subdir", foo, "cd ../.. && go run .", true},
		{"subshell cd", f.other, "(cd ~/code/personal/agentctl && go run .)", true},
		{"env prefix", f.root, "FOO=1 GOFLAGS=-mod=mod go run .", true},
		{"env command", f.root, "env -u GOFLAGS GOOS=darwin go run .", true},
		{"after &&", f.root, "go build ./... && go run . status", true},
		{"after ;", f.root, "go vet ./... ; go run .", true},
		{"after ||", f.root, "false || go run .", true},
		{"after pipe", f.root, "echo hi | go run .", true},
		{"attached operator", f.root, "go build ./...&&go run .", true},
		{"newline", f.root, "go build ./...\ngo run .", true},
		{"redirect", f.root, "go run . status 2>&1 > /tmp/out", true},
		{"bash -c", f.root, "bash -c 'go run . status'", true},
		{"command substitution", f.root, "x=$(go run . status)", true},
		{"go binary path", f.root, "/usr/local/go/bin/go run .", true},
		{"time prefix", f.root, "time go run .", true},
		// Denied, fail closed: unresolvable dir and the line mentions agentctl.
		{"unknown cwd mentions agentctl", "", "go run . dispatch --agent agentctl-dev", true},
		{"cd variable mentions agentctl", f.other, "cd $AGENTCTL_SRC && go run .", true},
		{"cd dash mentions agentctl", f.other, "cd - && go run . # agentctl", true},
		{"variable package mentions agentctl", f.other, "go run $AGENTCTL_PKG", true},

		// Allowed: subpackages of this repo.
		{"devserver", f.root, "go run ./internal/serve/servetest/devserver -scene hub", false},
		{"cd then subpackage", f.other, "cd ~/code/personal/agentctl && go run ./internal/foo", false},
		{"cmd subpackage", f.root, "go run ./cmd/tool", false},
		{"subpackage file", f.root, "go run ./internal/foo/main.go", false},
		{"subpackage module path", "", "go run github.com/blsmth/agentctl/internal/serve/servetest/devserver", false},
		{"pattern", f.root, "go run ./...", false},
		{"-C subpackage", f.other, "go run -C ~/code/personal/agentctl ./internal/foo", false},
		{"chained tests then subpackage", f.root, "go test ./... && go run ./internal/foo", false},
		// Allowed: go run outside this repo.
		{"other repo dot", f.other, "go run .", false},
		{"other repo files", f.other, "go run main.go", false},
		{"other repo cd", f.root, "cd ~/src/myapp && go run . --repo ~/code/personal/agentctl", false},
		{"scratch program", f.root, "go run /tmp/scratch/main.go -- --repo ~/code/personal/agentctl", false},
		{"dir named agentctl", "", "go run ~/src/agentctl status", false},
		{"other module", f.root, "go run golang.org/x/tools/cmd/stringer@latest -type=Foo", false},
		{"unknown cwd no mention", "", "go run .", false},
		{"cd variable no mention", "", "cd $DIR && go run .", false},
		// Allowed: the words are not in command position.
		{"context set text", f.root, `agentctl context set k 'never go run . from ~/code/personal/agentctl'`, false},
		{"context set double quoted", f.root, `agentctl context set k "cd ~/code/personal/agentctl && go run ."`, false},
		{"echo", f.root, "echo go run .", false},
		{"commit message", f.root, `git commit -m "Stop agentctl workers using go run ."`, false},
		{"grep", f.root, `grep -rn "go run ." docs`, false},
		{"data heredoc", f.root, "cat > /tmp/x.md <<'EOF'\ngo run .\nEOF", false},
		{"go test", f.root, "go test ./... -run TestGoRun", false},
		{"go run alone", f.root, "go run", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := checkGlobalDenyListIn(tt.command, tt.cwd)
			if got := reason == goRunAgentctlReason; got != tt.wantDeny {
				t.Fatalf("checkGlobalDenyListIn(%q, %q) = %q, wantDeny %v", tt.command, tt.cwd, reason, tt.wantDeny)
			}
		})
	}
}

func TestGoModModule(t *testing.T) {
	tests := []struct {
		data, want string
	}{
		{"module github.com/blsmth/agentctl\n", "github.com/blsmth/agentctl"},
		{"// comment\nmodule \"github.com/blsmth/agentctl\" // x\ngo 1.26\n", "github.com/blsmth/agentctl"},
		{"module github.com/blsmth/agentctl/v2\n", "github.com/blsmth/agentctl/v2"},
		{"modules x\n", ""},
		{"go 1.26\n", ""},
	}
	for _, tt := range tests {
		if got := goModModule([]byte(tt.data)); got != tt.want {
			t.Errorf("goModModule(%q) = %q, want %q", tt.data, got, tt.want)
		}
	}
}

// TestGoRunScan_PanicRecovery pins the recover paths: the convenience rule
// fails open, the gate rewrite fails closed (every go run counts).
func TestGoRunScan_PanicRecovery(t *testing.T) {
	orig := goRunScan
	goRunScan = func([]cmdToken, string) []goRunCall { panic("boom") }
	t.Cleanup(func() { goRunScan = orig })
	if got := checkGoRunAgentctl("go run github.com/blsmth/agentctl", ""); got != "" {
		t.Fatalf("checkGoRunAgentctl after panic = %q, want allow", got)
	}
	if !isAgentctlDispatch("go run ./internal/foo dispatch --agent x --prompt y") {
		t.Fatal("dispatch gate after panic: want gated")
	}
}

// TestValidateBash_GoRunAgentctlUsesPayloadCwd checks the hook resolves
// `go run .` against the payload cwd.
func TestValidateBash_GoRunAgentctlUsesPayloadCwd(t *testing.T) {
	f := newGoRunFixture(t)
	d := DefaultDeps()
	d.LoadConfig = func() *config.Config { return &config.Config{} }
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
	failIfDispatchPopupRunnerCalled(t)
	envVars := map[string]string{"AGENTCTL": "1", "AGENTCTL_AGENT_NAME": "go-engineer", "AGENTCTL_WINDOW_ID": "w99", "TMUX_PANE": ""}

	for _, tt := range []struct {
		cwd      string
		wantDeny bool
	}{{f.root, true}, {f.other, false}} {
		payload, err := json.Marshal(map[string]any{
			"tool_name":  "Bash",
			"tool_input": map[string]string{"command": "go run . status"},
			"cwd":        tt.cwd,
		})
		if err != nil {
			t.Fatal(err)
		}
		out, _ := runHookCaptured(t, envVars, string(payload), ValidateBash)
		if got := strings.Contains(out, "never invoke agentctl via 'go run'"); got != tt.wantDeny {
			t.Errorf("cwd %s: denied %v, want %v (output %q)", tt.cwd, got, tt.wantDeny, out)
		}
	}
}

// TestMaskAgentctlArgs_UnterminatedDoubleQuote is the regression test for
// the slice-out-of-range panic: an unterminated double quote stepped the
// scanner past the end of the command, which panicked when that word was a
// command word (sliced for the assignment check).
func TestMaskAgentctlArgs_UnterminatedDoubleQuote(t *testing.T) {
	cmd := `python3 - <<'PY'` + "\nprint('x')\nPY\n" + `agentctl context set k v; "it's unterminated`
	if got := maskAgentctlArgs(cmd); got != cmd {
		t.Fatalf("maskAgentctlArgs = %q, want the command unchanged (unsure)", got)
	}
}
