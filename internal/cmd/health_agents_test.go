package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
)

func TestCheckAgentTools(t *testing.T) {
	full := "Read, Bash(git *, ls), CronCreate,CronList , CronDelete, Monitor, ToolSearch"
	tests := []struct {
		name   string
		agents []agent.Agent
		want   []string // substrings, one per finding
	}{
		{"orchestrator no tools", []agent.Agent{{Name: "o", FilePath: "/o.md", Role: agent.RoleOrchestrator}}, nil},
		{"orchestrator with tools", []agent.Agent{{Name: "o", FilePath: "/o.md", Role: agent.RoleOrchestrator, Tools: "Read, ToolSearch"}}, []string{"remove it"}},
		{"worker cron without ToolSearch", []agent.Agent{{Name: "w", FilePath: "/w.md", Tools: "Read, CronCreate, Monitor"}}, []string{"without ToolSearch"}},
		{"worker full set", []agent.Agent{{Name: "w", FilePath: "/w.md", Tools: full}}, nil},
		{"spaces and pattern", []agent.Agent{{Name: "w", FilePath: "/w.md", Tools: "  Bash(git *) ,  CronList  "}}, []string{"CronList"}},
		{"pattern only", []agent.Agent{{Name: "w", FilePath: "/w.md", Tools: "Bash(Monitor *)"}}, nil},
		{"empty tools", []agent.Agent{{Name: "w", FilePath: "/w.md", Tools: "  "}}, nil},
		{"builtin skipped", []agent.Agent{{Name: "Explore", Tools: "CronList"}}, nil},
		{"orchestrator both", []agent.Agent{{Name: "o", FilePath: "/o.md", Role: agent.RoleOrchestrator, Tools: "Monitor"}}, []string{"remove it", "without ToolSearch"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkAgentTools(tc.agents)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d findings %+v, want %d", len(got), got, len(tc.want))
			}
			for i, w := range tc.want {
				if !strings.Contains(got[i].Message, w) {
					t.Errorf("finding %d = %q, want substring %q", i, got[i].Message, w)
				}
				if got[i].FilePath == "" || got[i].Agent == "" {
					t.Errorf("finding %d missing name/path: %+v", i, got[i])
				}
			}
		})
	}
}

func writeAgentFile(t *testing.T, dir, name, front string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".md")
	body := "---\nname: " + name + "\ndescription: d\n" + front + "---\nbody\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPrintAgentToolsHealth(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	old := agent.RegistryDir
	agent.RegistryDir = global
	t.Cleanup(func() { agent.RegistryDir = old })

	t.Run("no agents", func(t *testing.T) {
		var buf bytes.Buffer
		printAgentToolsHealth(&buf, project)
		if !strings.Contains(buf.String(), "ok") || strings.Contains(buf.String(), "warn") {
			t.Errorf("output = %q", buf.String())
		}
	})

	writeAgentFile(t, global, "good", "tools: Read\n")
	bad := writeAgentFile(t, filepath.Join(project, ".claude", "agents"), "brandon", "role: orchestrator\ntools: Read, CronList\n")
	// Global brandon is clean but shadowed by the local one.
	writeAgentFile(t, global, "brandon", "role: orchestrator\n")

	var buf bytes.Buffer
	printAgentToolsHealth(&buf, project)
	out := buf.String()
	for _, want := range []string{"warn", "brandon", bad, "remove it", "without ToolSearch"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "good ") {
		t.Errorf("clean agent flagged:\n%s", out)
	}
}
