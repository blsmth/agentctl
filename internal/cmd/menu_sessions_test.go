package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/tui"
)

func TestMenuSessions(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	typed := write("typed.jsonl", `{"type":"user","message":{"role":"user","content":"fix the flaky test"}}`+"\n")
	empty := write("empty.jsonl", `{"type":"assistant","message":{"content":"hi"}}`+"\n")
	now := time.Now()
	all := []sessions.Session{
		{ID: "older", EndTime: now.Add(-12 * time.Minute), LogFile: empty},
		{ID: "live", EndTime: now.Add(-time.Minute), LogFile: typed},
		{ID: "newer", StartTime: now.Add(-11 * time.Minute), LogFile: typed},
	}
	got := menuSessions(all, map[string]bool{"live": true}, func(s sessions.Session) string { return "named-" + s.ID })
	if len(got) != 2 || got[0].ID != "newer" || got[1].ID != "older" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Preview != "fix the flaky test" {
		t.Errorf("preview = %q", got[0].Preview)
	}
	if got[1].Preview != "named-older" {
		t.Errorf("fallback preview = %q", got[1].Preview)
	}
}

func TestMenuSection(t *testing.T) {
	cwd := "/repo"
	tests := []struct {
		name string
		a    agent.Agent
		want tui.MenuSection
	}{
		{"cwd project agent", agent.Agent{Scope: agent.ScopeLocal, FilePath: "/repo/.claude/agents/brandon.md"}, tui.SectionProject},
		{"symlink into current repo", agent.Agent{Scope: agent.ScopeLocal, SymTarget: "/repo/.claude/agents/x.md", FilePath: "/repo/.claude/agents/x.md"}, tui.SectionProject},
		{"symlink into other repo", agent.Agent{Scope: agent.ScopeLocal, SymTarget: "/other/.claude/agents/ha.md", FilePath: "/other/.claude/agents/ha.md"}, tui.SectionOther},
		{"sibling dir prefix is not cwd", agent.Agent{Scope: agent.ScopeLocal, FilePath: "/repo2/.claude/agents/a.md"}, tui.SectionOther},
		{"global registry", agent.Agent{Scope: agent.ScopeGlobal, FilePath: "/home/u/.claude/agents/a.md"}, tui.SectionUser},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := menuSection(tc.a, cwd); got != tc.want {
				t.Errorf("section = %v, want %v", got, tc.want)
			}
		})
	}
}
