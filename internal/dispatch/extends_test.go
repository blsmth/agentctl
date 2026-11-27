package dispatch

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/tmux/launchtest"
)

// agentMD is an agent definition with optional extends and harness.
func agentMD(name, extends, harnessName, body string) string {
	fm := "---\nname: " + name + "\ndescription: " + name + "\n"
	if extends != "" {
		fm += "extends: " + extends + "\n"
	}
	if harnessName != "" {
		fm += "harness: " + harnessName + "\n"
	}
	return fm + "---\n" + body + "\n"
}

// writeFiles writes name -> content files into dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if len(files) == 0 {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// launchedSystemFile returns the system prompt text a captured launch
// appends, for Claude (--append-system-prompt-file) or pi
// (--append-system-prompt).
func launchedSystemFile(t *testing.T, l launchtest.Launch) string {
	t.Helper()
	if sysFileRe.MatchString(l.Command) {
		return launchedSystemPrompt(t, l)
	}
	argv := fields(t, l.AgentCommand())
	i := slices.Index(argv, "--append-system-prompt")
	if i < 0 || i+1 >= len(argv) {
		t.Fatalf("no system prompt in %q", argv)
	}
	data, err := os.ReadFile(argv[i+1])
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestLaunch_ExtendsReachesSystemPrompt launches agents with and without
// extends and checks what the system prompt file carries of their
// definitions: with --agent (Claude) the base body under its own heading
// and never the own body (Claude Code loads that file itself); without it
// (pi) the whole composed persona; and each shared include once.
func TestLaunch_ExtendsReachesSystemPrompt(t *testing.T) {
	const (
		baseText   = "BASE-PERSONA-TEXT"
		childText  = "CHILD-PERSONA-TEXT"
		commonRule = "COMMON-SHARED-RULE"
		childRule  = "CHILD-SHARED-RULE"
	)
	baseMD := agentMD("base-eng", "", "", baseText+"\n@shared/common.md")
	childBody := childText + "\n@shared/common.md\n@shared/child.md"
	shared := map[string]string{"common.md": commonRule, "child.md": childRule}

	tests := []struct {
		name    string
		global  map[string]string // registry files, name -> content
		project map[string]string // project .claude/agents files
		agent   string
		want    []string // in the system prompt
		notWant []string // absent from it
		once    []string // exactly once
		warn    string   // stderr warning substring, "" for none
	}{
		{
			name:    "standalone agent",
			global:  map[string]string{"solo.md": agentMD("solo", "", "", "SOLO-TEXT\n@shared/common.md")},
			agent:   "solo",
			want:    []string{"## Shared Conventions", commonRule},
			notWant: []string{"## Base agent", "SOLO-TEXT", "## Agent definition"},
		},
		{
			name:    "child extends global base",
			global:  map[string]string{"base-eng.md": baseMD, "child.md": agentMD("child", "base-eng", "", childBody)},
			agent:   "child",
			want:    []string{"## Base agent: base-eng", baseText, "overrides anything here that conflicts", childRule},
			notWant: []string{childText, "## Agent definition"},
			once:    []string{commonRule, baseText},
		},
		{
			name:    "project child extends global base",
			global:  map[string]string{"base-eng.md": baseMD},
			project: map[string]string{"child.md": agentMD("child", "base-eng", "", childBody)},
			agent:   "child",
			want:    []string{"## Base agent: base-eng", baseText, childRule},
			notWant: []string{childText},
			once:    []string{commonRule},
		},
		{
			name:    "unresolvable base",
			global:  map[string]string{"child.md": agentMD("child", "no-such-base", "", childBody)},
			agent:   "child",
			want:    []string{"## Shared Conventions", commonRule, childRule},
			notWant: []string{"## Base agent", childText},
		},
		{
			name:    "global child refuses project base shadowing global base",
			global:  map[string]string{"base-eng.md": baseMD, "child.md": agentMD("child", "base-eng", "", childBody)},
			project: map[string]string{"base-eng.md": agentMD("base-eng", "", "", "SMUGGLED-TEXT")},
			agent:   "child",
			want:    []string{childRule},
			notWant: []string{"## Base agent", "SMUGGLED-TEXT", baseText},
			warn:    "outside your global agents dir",
		},
		{
			name:    "global child refuses project-only base",
			global:  map[string]string{"child.md": agentMD("child", "base-eng", "", childBody)},
			project: map[string]string{"base-eng.md": agentMD("base-eng", "", "", "SMUGGLED-TEXT")},
			agent:   "child",
			notWant: []string{"## Base agent", "SMUGGLED-TEXT"},
			warn:    "outside your global agents dir",
		},
		{
			name:    "project child with project base shadowing global base",
			global:  map[string]string{"base-eng.md": baseMD},
			project: map[string]string{"base-eng.md": agentMD("base-eng", "", "", "LOCAL-BASE-TEXT"), "child.md": agentMD("child", "base-eng", "", childBody)},
			agent:   "child",
			want:    []string{"## Base agent: base-eng", "LOCAL-BASE-TEXT"},
			notWant: []string{baseText, childText},
			warn:    "shadows your global agent",
		},
		{
			name:    "non-Claude harness gets the composed persona",
			global:  map[string]string{"base-eng.md": baseMD, "child.md": agentMD("child", "base-eng", "pi", childBody)},
			agent:   "child",
			want:    []string{"## Agent definition: child", baseText, "## Local Overrides", childText, childRule},
			notWant: []string{"## Base agent"},
			once:    []string{commonRule, baseText, childText},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			no := false
			env := setupGolden(t, `{}`)
			writeFiles(t, filepath.Join(env.Home, ".claude", "agents"), tt.global)
			writeFiles(t, filepath.Join(env.Project, ".claude", "agents"), tt.project)
			writeFiles(t, filepath.Join(env.Home, ".claude", "shared"), shared)
			var warn bytes.Buffer
			prevWarn := shadowWarnOut
			shadowWarnOut = &warn
			t.Cleanup(func() { shadowWarnOut = prevWarn })

			rec := launchtest.Capture(t)
			if _, err := Launch(Request{
				AgentName: tt.agent, Mode: ModeAdhoc, CustomPrompt: "say hi", RepoSlug: "o/r",
				WorkDir: env.Project, UseWorktree: &no, Background: true,
			}); err != nil {
				t.Fatalf("Launch: %v", err)
			}
			sys := launchedSystemFile(t, rec.Only())
			for _, w := range tt.want {
				if !strings.Contains(sys, w) {
					t.Errorf("system prompt lacks %q:\n%s", w, sys)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(sys, w) {
					t.Errorf("system prompt has %q:\n%s", w, sys)
				}
			}
			for _, w := range tt.once {
				if n := strings.Count(sys, w); n != 1 {
					t.Errorf("system prompt has %q %d times, want once:\n%s", w, n, sys)
				}
			}
			if tt.warn == "" && warn.Len() != 0 {
				t.Errorf("unexpected warning: %q", warn.String())
			}
			if tt.warn != "" && !strings.Contains(warn.String(), tt.warn) {
				t.Errorf("warning = %q, want it to contain %q", warn.String(), tt.warn)
			}
		})
	}
}

// TestLaunch_ExtendsBaseInEveryMode checks the base body reaches the system
// prompt file of every launch mode, ahead of the shared conventions.
func TestLaunch_ExtendsBaseInEveryMode(t *testing.T) {
	no := false
	reviewPR := samplePullRequest()
	reqs := []Request{
		{Mode: ModeDispatch, Ticket: 7, TicketTitle: "Fix it", TicketBody: "body"},
		{Mode: ModeAdhoc, CustomPrompt: "say hi"},
		{Mode: ModeAdhoc, CustomPrompt: "nightly", ScheduledBy: "nightly"},
		{Mode: ModeReviewPR, ReviewPR: &reviewPR},
		{Mode: ModeBare},
		{Mode: ModeBare, CustomPrompt: "hello"},
	}
	for _, r := range reqs {
		t.Run(string(r.Mode)+"/"+r.CustomPrompt, func(t *testing.T) {
			env := setupGolden(t, `{}`)
			writeFiles(t, filepath.Join(env.Home, ".claude", "agents"), map[string]string{
				"base-eng.md": agentMD("base-eng", "", "", "BASE-TEXT\n@shared/common.md"),
				"child.md":    agentMD("child", "base-eng", "", "CHILD-TEXT"),
			})
			writeFiles(t, filepath.Join(env.Home, ".claude", "shared"), map[string]string{"common.md": "COMMON"})
			rec := launchtest.Capture(t)
			req := r
			req.AgentName, req.WorkDir, req.UseWorktree, req.Background, req.RepoSlug = "child", env.Project, &no, true, "o/r"
			if _, err := Launch(req); err != nil {
				t.Fatalf("Launch: %v", err)
			}
			sys := launchedSystemFile(t, rec.Only())
			b, s := strings.Index(sys, "## Base agent: base-eng"), strings.Index(sys, "## Shared Conventions")
			if b < 0 || s < 0 || b > s || !strings.Contains(sys, "BASE-TEXT") || strings.Contains(sys, "CHILD-TEXT") {
				t.Errorf("system prompt (base at %d, shared at %d):\n%s", b, s, sys)
			}
		})
	}
}

// TestAgentSystemPrompt checks the exported form improve uses.
func TestAgentSystemPrompt(t *testing.T) {
	env := setupGolden(t, `{}`)
	writeFiles(t, filepath.Join(env.Home, ".claude", "agents"), map[string]string{
		"base-eng.md": agentMD("base-eng", "", "", "BASE-TEXT"),
		"child.md":    agentMD("child", "base-eng", "", "CHILD-TEXT"),
	})
	cfg := emptyConfig()
	if got := AgentSystemPrompt(cfg, "child", true, env.Project); !strings.HasPrefix(got, "## Base agent: base-eng") || strings.Contains(got, "CHILD-TEXT") {
		t.Errorf("with --agent = %q", got)
	}
	if got := AgentSystemPrompt(cfg, "child", false, env.Project); !strings.Contains(got, "BASE-TEXT") || !strings.Contains(got, "CHILD-TEXT") {
		t.Errorf("without --agent = %q", got)
	}
	if got := AgentSystemPrompt(cfg, "golden", true, env.Project); got != "" {
		t.Errorf("standalone with no includes = %q, want empty", got)
	}
	if got := AgentSystemPrompt(cfg, "missing", true, env.Project); got != "" {
		t.Errorf("unknown agent = %q, want empty", got)
	}
}
