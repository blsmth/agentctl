package agent

import (
	"reflect"
	"testing"
)

func TestSplitByProject(t *testing.T) {
	dir := "/work/proj"
	local := func(name string) Agent {
		return Agent{Name: name, Scope: ScopeLocal, FilePath: dir + "/.claude/agents/" + name + ".md"}
	}
	glob := func(name string) Agent { return Agent{Name: name, FilePath: "/home/u/.claude/agents/" + name + ".md"} }
	other := Agent{Name: "x", Scope: ScopeLocal, FilePath: "/work/other/.claude/agents/x.md"}
	sibling := Agent{Name: "y", Scope: ScopeLocal, FilePath: "/work/proj2/.claude/agents/y.md"}

	tests := []struct {
		name     string
		in       []Agent
		wantProj []string
		wantRest []string
	}{
		{"project sorted first", []Agent{glob("b"), local("zed"), other, local("alpha"), sibling}, []string{"alpha", "zed"}, []string{"b", "x", "y"}},
		{"no project agents keeps order", []Agent{glob("b"), glob("a")}, nil, []string{"b", "a"}},
		{"empty", nil, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, r := SplitByProject(tt.in, dir)
			if !reflect.DeepEqual(names(p), tt.wantProj) || !reflect.DeepEqual(names(r), tt.wantRest) {
				t.Errorf("got %v / %v, want %v / %v", names(p), names(r), tt.wantProj, tt.wantRest)
			}
		})
	}
}

func TestInProjectEmptyDir(t *testing.T) {
	if InProject(Agent{Scope: ScopeLocal, FilePath: "/a/.claude/agents/x.md"}, "") {
		t.Error("empty dir must match nothing")
	}
}

func TestProjectLabel(t *testing.T) {
	if got := ProjectLabel("/work/agentctl/"); got != "agentctl" {
		t.Errorf("got %q", got)
	}
}

func names(as []Agent) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Name)
	}
	return out
}
