package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverRole(t *testing.T) {
	files := map[string]string{
		"lead.md":      "---\nname: lead\nrole: orchestrator\n---\nLead body",
		"worker.md":    "---\nname: worker\nrole: worker\n---\nWorker body",
		"plain.md":     "---\nname: plain\n---\nPlain body",
		"bogus.md":     "---\nname: bogus\nrole: manager\n---\nBogus body",
		"sub-lead.md":  "---\nname: sub-lead\nextends: lead\n---\nInherits the role",
		"sub-work.md":  "---\nname: sub-work\nextends: lead\nrole: worker\n---\nChild wins",
		"sub-plain.md": "---\nname: sub-plain\nextends: plain\nrole: orchestrator\n---\nChild sets it",
	}
	dir := t.TempDir()
	setRegistryDir(t, dir)
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	agents, err := DiscoverAll(emptyConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range agents {
		got[a.Name] = a.Role
	}
	tests := []struct {
		name, want string
	}{
		{"lead", RoleOrchestrator},
		{"worker", RoleWorker},
		{"plain", ""},
		{"bogus", ""}, // unknown value: logged and ignored
		{"sub-lead", RoleOrchestrator},
		{"sub-work", RoleWorker},
		{"sub-plain", RoleOrchestrator},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role, ok := got[tt.name]
			if !ok {
				t.Fatalf("agent %s not discovered", tt.name)
			}
			if role != tt.want {
				t.Errorf("Role = %q, want %q", role, tt.want)
			}
		})
	}
}

func TestNormalizeRole(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"orchestrator", RoleOrchestrator},
		{"worker", RoleWorker},
		{"Orchestrator", ""},
		{"lead", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := NormalizeRole(tt.in, "test.md"); got != tt.want {
				t.Errorf("NormalizeRole(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
