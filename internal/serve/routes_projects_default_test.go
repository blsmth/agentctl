package serve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
)

func TestProjectsDefaultAgent(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("HOME", cfgHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	globalDir := filepath.Join(cfgHome, ".config", "agentctl")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCfg := func(dir, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeCfg(globalDir, `{"default_agent":"Plan"}`)

	group := func(name, project string) fleetwatch.Group {
		rt := filepath.Join(t.TempDir(), ".agentctl")
		if err := os.MkdirAll(rt, 0o755); err != nil {
			t.Fatal(err)
		}
		if project != "" {
			writeCfg(rt, project)
		}
		g := testGroup()
		g.Session, g.Project, g.Dir, g.RuntimeDir = "agentctl-"+name, name, "/code/"+name, rt
		return g
	}
	e := newReadEnv(t)
	e.fleet.set(
		group("override", `{"default_agent":"Explore"}`),
		group("fallback", ``),
		group("invalid", `{"default_agent":"bad name!"}`),
		group("unknown", `{"default_agent":"no-such-agent"}`),
	)

	var got struct {
		Projects []projectInfo `json:"projects"`
	}
	if err := json.Unmarshal(e.get("/v1/projects").Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"override": "Explore", "fallback": "Plan", "invalid": "Plan", "unknown": "Plan"}
	if len(got.Projects) != len(want) {
		t.Fatalf("projects = %+v", got.Projects)
	}
	for _, p := range got.Projects {
		if p.DefaultAgent != want[p.Name] {
			t.Errorf("%s: default_agent = %q, want %q", p.Name, p.DefaultAgent, want[p.Name])
		}
	}
}
