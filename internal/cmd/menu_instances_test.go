package cmd

import (
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
