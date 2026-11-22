package serve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
)

func TestDiscoveredAgentsOrchestrator(t *testing.T) {
	reg := t.TempDir()
	proj := t.TempDir()
	oldReg := agent.RegistryDir
	agent.RegistryDir = reg
	t.Cleanup(func() { agent.RegistryDir = oldReg })
	t.Setenv("HOME", t.TempDir())

	write := func(dir, name, fm string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte("---\n"+fm+"---\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(reg, "boss", "name: boss\ndescription: d\nrole: orchestrator\n")
	write(reg, "worker", "name: worker\ndescription: d\n")
	write(filepath.Join(proj, ".claude", "agents"), "deputy", "name: deputy\ndescription: d\nextends: boss\n")

	got, err := discoveredAgents{}.Agents([]string{proj})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"boss": true, "worker": false, "deputy": true}
	for _, a := range got {
		if w, ok := want[a.Name]; ok {
			if a.Orchestrator != w {
				t.Errorf("%s orchestrator = %v, want %v", a.Name, a.Orchestrator, w)
			}
			delete(want, a.Name)
		}
	}
	if len(want) != 0 {
		t.Errorf("agents not listed: %v", want)
	}
}
