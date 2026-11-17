package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
)

func TestBuildMenuAgentsInstancesMostRecentFirst(t *testing.T) {
	running := map[string]string{"@1": "go-engineer", "@2": "go-engineer:2", "@3": "go-engineer:3", "@4": "other"}
	activity := map[string]int64{"@1": 100, "@2": 300, "@3": 200}
	got := buildMenuAgents(&config.Config{}, []agent.Agent{{Name: "go-engineer"}}, running, activity)
	for _, a := range got {
		if a.Name != "go-engineer" {
			continue
		}
		if s := strings.Join(a.Instances, ","); s != "go-engineer:2,go-engineer:3,go-engineer" {
			t.Errorf("instances = %s, want most recently active first", s)
		}
		return
	}
	t.Fatal("go-engineer not in menu")
}

func TestWindowActivityReadsWindowsSubdir(t *testing.T) {
	rt := t.TempDir()
	dir := filepath.Join(rt, "windows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for id, rec := range map[string]string{
		"@2":  `{"updated_at":"2026-01-01T00:00:00Z","last_tool_time":3000000000}`,
		"@10": `{"updated_at":"2026-01-01T00:00:00Z","last_tool_time":1000000000}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(rec), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	act := windowActivity(rt)
	if act["@2"] != 3000000000 || act["@10"] == 0 {
		t.Fatalf("activity = %v", act)
	}
	running := map[string]string{"@10": "go-engineer", "@2": "go-engineer:2"}
	got := buildMenuAgents(&config.Config{}, []agent.Agent{{Name: "go-engineer"}}, running, act)
	for _, a := range got {
		if a.Name == "go-engineer" {
			if len(a.Instances) != 2 || a.Instances[0] != "go-engineer:2" {
				t.Errorf("instances = %v, want most recent (@2) first", a.Instances)
			}
			return
		}
	}
	t.Fatal("go-engineer not in menu")
}

func TestBuildMenuAgentsNumericWindowFallback(t *testing.T) {
	running := map[string]string{"@10": "go-engineer:10", "@2": "go-engineer:2"}
	got := buildMenuAgents(&config.Config{}, []agent.Agent{{Name: "go-engineer"}}, running, map[string]int64{})
	for _, a := range got {
		if a.Name == "go-engineer" {
			if s := strings.Join(a.Instances, ","); s != "go-engineer:2,go-engineer:10" {
				t.Errorf("instances = %s, want numeric window order", s)
			}
			return
		}
	}
	t.Fatal("go-engineer not in menu")
}
