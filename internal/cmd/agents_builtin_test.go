package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
)

func TestBuiltinAgentNames(t *testing.T) {
	got := builtinAgentNames(nil)
	if !slices.Contains(got, "general-purpose") || !slices.Contains(got, "Explore") || !slices.IsSorted(got) {
		t.Fatalf("builtinAgentNames(nil) = %v, want sorted list with the built-ins", got)
	}
	got = builtinAgentNames([]agent.Agent{{Name: "Explore"}})
	if slices.Contains(got, "Explore") {
		t.Errorf("a discovered agent must shadow its built-in, got %v", got)
	}
}

func writeProjectAgent(t *testing.T, name string) {
	t.Helper()
	proj := t.TempDir()
	dir := filepath.Join(proj, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: project agent\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(proj)
}

func TestPrintAgentsTable_JSONListsBuiltinsAfterDiscovered(t *testing.T) {
	writeProjectAgent(t, "Explore")
	out := captureStdout(t, func() {
		if err := PrintAgentsTable(true); err != nil {
			t.Fatal(err)
		}
	})
	var rows []agentsJSONRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	firstBuiltin, explore := -1, 0
	var sawGP bool
	for i, r := range rows {
		if r.Scope == builtinScope {
			if firstBuiltin < 0 {
				firstBuiltin = i
			}
			if r.Tokens != 0 || r.Model != "" {
				t.Errorf("builtin row %+v: want no model/tokens", r)
			}
			sawGP = sawGP || r.Name == "general-purpose"
		} else if firstBuiltin >= 0 {
			t.Errorf("discovered agent %q listed after a built-in", r.Name)
		}
		if r.Name == "Explore" {
			explore++
			if r.Scope == builtinScope {
				t.Errorf("project Explore must shadow the built-in")
			}
		}
	}
	if !sawGP {
		t.Errorf("general-purpose built-in missing: %s", out)
	}
	if explore != 1 {
		t.Errorf("Explore listed %d times, want 1", explore)
	}
}

func TestPrintAgentsTable_TableShowsBuiltinRows(t *testing.T) {
	t.Chdir(t.TempDir())
	out := captureStdout(t, func() {
		if err := PrintAgentsTable(false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "general-purpose") || !strings.Contains(out, "builtin") {
		t.Errorf("table lacks a builtin row:\n%s", out)
	}
}
