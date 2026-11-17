package dispatch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/tmux/launchtest"
	"github.com/blsmth/agentctl/internal/windowstate"
)

const piAgentMD = "---\nname: golden\ndescription: golden test agent\nharness: pi\n---\nYou are the golden test agent.\n@shared/conv.md\n"

// setupPi is setupGolden with a pi agent (harness from frontmatter), an
// expandable shared include, and a dispatching orchestrator.
func setupPi(t *testing.T, cfg string) *launchtest.Env {
	t.Helper()
	env := launchtest.Setup(t, cfg, map[string]string{"golden": piAgentMD})
	shared := filepath.Join(env.Home, ".claude", "shared")
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "conv.md"), []byte("Always write tests."), 0o600); err != nil {
		t.Fatal(err)
	}
	prevOrch, prevParent, prevExec := resolveOrchestratorFn, parentAccountFn, execPath
	resolveOrchestratorFn = func() Orchestrator { return Orchestrator{WindowID: "@26", Pane: "%777"} }
	parentAccountFn = func() string { return "" }
	execPath = func() (string, error) { return "/opt/bin/agentctl", nil }
	t.Cleanup(func() { resolveOrchestratorFn, parentAccountFn, execPath = prevOrch, prevParent, prevExec })
	return env
}

// TestLaunch_PiHarness checks a pi dispatch end to end (minus tmux): the pi
// command line, the bridge extension on disk, no Claude settings, the
// harness recorded in the window env, and a system prompt that carries the
// persona but does not teach SendMessage.
func TestLaunch_PiHarness(t *testing.T) {
	no := false
	env := setupPi(t, `{"agents": {"golden": {"model": "ollama/gemma4", "effort_level": "high", "permission_mode": "plan"}}}`)
	rec := launchtest.Capture(t)
	res, err := Launch(Request{
		AgentName: "golden", Mode: ModeAdhoc, CustomPrompt: "say hi", WorkDir: env.Project,
		UseWorktree: &no, Background: true,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if res.Harness != harness.Pi {
		t.Errorf("Result.Harness = %q", res.Harness)
	}
	l := rec.Only()
	argv := fields(t, l.Command)
	ext := filepath.Join(env.Project, ".agentctl", "launch", harness.PiExtensionFile)
	if argv[0] != "pi" || !slices.Contains(argv, "--session-id") || argv[slices.Index(argv, "-e")+1] != ext {
		t.Fatalf("command = %q", argv)
	}
	for _, want := range [][]string{{"--model", "ollama/gemma4"}, {"--thinking", "high"}, {"--", "say hi"}} {
		i := slices.Index(argv, want[0])
		if i < 0 || i+1 >= len(argv) || argv[i+1] != want[1] {
			t.Errorf("command lacks %q: %q", want, argv)
		}
	}
	for _, unwanted := range []string{"--agent", "--permission-mode", "--settings", "--append-system-prompt-file"} {
		if slices.Contains(argv, unwanted) {
			t.Errorf("pi command has Claude flag %s: %q", unwanted, argv)
		}
	}
	if _, err := os.Stat(ext); err != nil {
		t.Errorf("bridge extension not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.Project, ".agentctl", "settings", "golden.json")); err == nil {
		t.Error("Claude settings written for a pi launch")
	}
	if l.Env[harness.EnvHarness] != "pi" {
		t.Errorf("%s = %q", harness.EnvHarness, l.Env[harness.EnvHarness])
	}
	for k := range l.Env {
		if strings.HasPrefix(k, "CLAUDE_CODE_") || k == "DISABLE_AUTOUPDATER" {
			t.Errorf("pi window got Claude env %s", k)
		}
	}
	if ws, _ := windowstate.Read("@9001"); ws.Harness != "pi" {
		t.Errorf("windowstate harness = %q", ws.Harness)
	}

	prompt := argv[slices.Index(argv, "--append-system-prompt")+1]
	data, err := os.ReadFile(prompt)
	if err != nil {
		t.Fatal(err)
	}
	sys := string(data)
	for _, want := range []string{"## Agent definition: golden", "You are the golden test agent.", "Always write tests.", "### Reporting", "agentctl context get"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	for _, unwanted := range []string{"SendMessage", "ListAgents", "Claude Code does not expand"} {
		if strings.Contains(sys, unwanted) {
			t.Errorf("system prompt mentions %q", unwanted)
		}
	}
}

// TestLaunch_HarnessSelection covers the override and error paths.
func TestLaunch_HarnessSelection(t *testing.T) {
	no := false
	tests := []struct {
		name    string
		cfg     string
		req     Request
		wantCmd string
		wantErr string
	}{
		{"flag overrides frontmatter", `{}`, Request{Harness: "claude"}, "claude", ""},
		{"config overrides frontmatter", `{"agents": {"golden": {"harness": "claude"}}}`, Request{}, "claude", ""},
		{"unknown flag", `{}`, Request{Harness: "codex"}, "", `unknown harness "codex"`},
		{"unknown config", `{"agents": {"golden": {"harness": "nope"}}}`, Request{}, "", `unknown harness "nope"`},
		{"account flag on pi", `{}`, Request{Account: "work"}, "", "the pi harness has no accounts"},
		{"unknown backend on pi", `{}`, Request{Backend: "ollama"}, "", `backend "ollama" is not defined`},
		{"backend none is fine on pi", `{}`, Request{Backend: BackendNone}, "pi", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupPi(t, tt.cfg)
			rec := launchtest.Capture(t)
			req := tt.req
			req.AgentName, req.Mode, req.WorkDir, req.UseWorktree, req.Background = "golden", ModeBare, env.Project, &no, true
			_, err := Launch(req)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Launch err = %v, want %q", err, tt.wantErr)
				}
				if n := len(rec.Launches()); n != 0 {
					t.Errorf("%d windows launched despite the error", n)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := fields(t, rec.Only().Command)[0]; got != tt.wantCmd {
				t.Errorf("launched %s, want %s", got, tt.wantCmd)
			}
		})
	}
}

// TestRestore_Pi relaunches a recorded pi window on pi, reopening its
// session by ID.
func TestRestore_Pi(t *testing.T) {
	env := setupPi(t, `{}`)
	rec := launchtest.Capture(t)
	if _, err := Restore(RestoreRequest{
		AgentName: "golden", InstanceName: "golden:2", SessionID: "sid-1", WorkDir: env.Project,
		LogPath: "/s/2026_sid-1.jsonl", Harness: "pi", SessionName: "golden-proj-2",
	}); err != nil {
		t.Fatal(err)
	}
	l := rec.Only()
	argv := fields(t, l.Command)
	if argv[0] != "pi" || argv[slices.Index(argv, "--session-id")+1] != "sid-1" {
		t.Errorf("restore command = %q", argv)
	}
	if l.Env[harness.EnvHarness] != "pi" {
		t.Errorf("env harness = %q", l.Env[harness.EnvHarness])
	}
	if _, err := Restore(RestoreRequest{AgentName: "golden", WorkDir: env.Project, Harness: "gone"}); err == nil {
		t.Error("want error restoring an unknown harness")
	}
}

// TestPrepareBackendFor_PiNous: a nous backend on pi is resolved and
// preflighted (catalog, key available) but never routed through Claude's
// env or settings: no ANTHROPIC_* vars, no Claude settings-layer check, and
// the key travels only as the backend-token command.
func TestPrepareBackendFor_PiNous(t *testing.T) {
	nousEnvSetup(t, map[string]string{"nous": "sk-test-not-sent"})
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	// A Claude credential that would block a Claude launch on nous.
	home := os.Getenv("HOME")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"env":{"ANTHROPIC_API_KEY":"sk-x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	prevExec := execPath
	execPath = func() (string, error) { return "/opt/bin/agentctl", nil }
	t.Cleanup(func() { execPath = prevExec })
	srv := catalogServer(t, 200, nousCatalogJSON, nil)
	cfg := &config.Config{Backends: map[string]config.BackendConfig{
		"nous": {Kind: "nous", BaseURL: srv.URL, Model: "qwen/qwen3-coder-next", Models: &config.BackendModels{Haiku: "qwen/qwen3-max"}},
	}}

	ac := config.AgentConfig{}
	if _, err := PrepareBackendFor(cfg, harness.For(harness.Claude), &ac, "nous", "", t.TempDir(), map[string]string{}); err == nil {
		t.Fatal("claude launch must refuse the shadowing credential")
	}

	ac = config.AgentConfig{}
	env := map[string]string{}
	hb, err := PrepareBackendFor(cfg, harness.For(harness.Pi), &ac, "nous", "", t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	if hb == nil || hb.Name != "nous" || hb.BaseURL != srv.URL || hb.ContextWindow != 262144 || hb.AuthToken != "" {
		t.Fatalf("backend = %+v", hb)
	}
	if hb.KeyCommand != "'/opt/bin/agentctl' backend-token 'nous'" {
		t.Errorf("KeyCommand = %q", hb.KeyCommand)
	}
	if !slices.Contains(hb.Models, "qwen/qwen3-max") || hb.Models[0] != "qwen/qwen3-coder-next" {
		t.Errorf("models = %q", hb.Models)
	}
	if ac.Backend != "nous" || ac.Model != "qwen/qwen3-coder-next" {
		t.Errorf("ac = backend %q model %q", ac.Backend, ac.Model)
	}
	if len(env) != 1 || env[EnvBackend] != "nous" {
		t.Errorf("env = %v, want only %s", env, EnvBackend)
	}

	// Preflight still runs: a model missing from the catalog fails.
	ac = config.AgentConfig{}
	if _, err := PrepareBackendFor(cfg, harness.For(harness.Pi), &ac, "nous", "qwen/nope", t.TempDir(), map[string]string{}); err == nil {
		t.Error("want catalog error for an unknown model")
	}
	// No backend: nothing to register.
	ac = config.AgentConfig{}
	if hb, err := PrepareBackendFor(cfg, harness.For(harness.Pi), &ac, BackendNone, "", t.TempDir(), map[string]string{}); err != nil || hb != nil {
		t.Errorf("BackendNone = %+v, %v", hb, err)
	}
}

// TestLaunch_PiOllamaBackend runs a pi dispatch against an Ollama backend
// from the agent's config end to end (minus tmux).
func TestLaunch_PiOllamaBackend(t *testing.T) {
	no := false
	srv := tagsServer(t, 200, `{"models":[{"name":"qwen3:8b","model":"qwen3:8b"}]}`)
	env := setupPi(t, `{"backends": {"local": {"base_url": "`+srv.URL+`", "model": "qwen3:8b"}}, "agents": {"golden": {"backend": "local"}}}`)
	rec := launchtest.Capture(t)
	if _, err := Launch(Request{AgentName: "golden", Mode: ModeBare, WorkDir: env.Project, UseWorktree: &no, Background: true}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	l := rec.Only()
	argv := fields(t, l.Command)
	for _, want := range [][]string{{"--provider", "agentctl-local"}, {"--model", "qwen3:8b"}} {
		i := slices.Index(argv, want[0])
		if i < 0 || argv[i+1] != want[1] {
			t.Errorf("command lacks %q: %q", want, argv)
		}
	}
	if l.Env[EnvBackend] != "local" || !strings.Contains(l.Env[harness.EnvPiProvider], `"baseUrl":"`+srv.URL+`/v1"`) {
		t.Errorf("env backend=%q provider=%s", l.Env[EnvBackend], l.Env[harness.EnvPiProvider])
	}
	for k := range l.Env {
		if strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "CLAUDE_CODE_") {
			t.Errorf("pi window got Claude backend env %s", k)
		}
	}
	if ws, _ := windowstate.Read("@9001"); ws.Backend != "local" {
		t.Errorf("windowstate backend = %q", ws.Backend)
	}
}

// TestRestore_PiKeepsBackend: a pi window restored with its recorded backend
// registers it again.
func TestRestore_PiKeepsBackend(t *testing.T) {
	srv := tagsServer(t, 200, `{"models":[{"name":"qwen3:8b","model":"qwen3:8b"}]}`)
	env := setupPi(t, `{"backends": {"local": {"base_url": "`+srv.URL+`", "model": "qwen3:8b"}}}`)
	rec := launchtest.Capture(t)
	if _, err := Restore(RestoreRequest{AgentName: "golden", SessionID: "sid-1", WorkDir: env.Project, Harness: "pi", Backend: "local"}); err != nil {
		t.Fatal(err)
	}
	l := rec.Only()
	if argv := fields(t, l.Command); !slices.Contains(argv, "agentctl-local") || l.Env[EnvBackend] != "local" || l.Env[harness.EnvPiProvider] == "" {
		t.Errorf("restore = %q env %v", argv, l.Env)
	}
}
