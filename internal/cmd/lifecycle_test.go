package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/reconcile"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestRestartScript(t *testing.T) {
	tests := []struct {
		name string
		exe  string
		want string
	}{
		{"plain", "/usr/local/bin/agentctl", "exec '/usr/local/bin/agentctl'\n"},
		{"spaces", "/Users/a b/bin/agentctl", "exec '/Users/a b/bin/agentctl'\n"},
		{"quote", "/tmp/it's/agentctl", `exec '/tmp/it'\''s/agentctl'` + "\n"},
		{"metachars", "/tmp/$(rm -rf x);y/agentctl", "exec '/tmp/$(rm -rf x);y/agentctl'\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := restartScript(tt.exe)
			if !strings.HasPrefix(got, "#!/bin/sh\n") || !strings.Contains(got, tt.want) {
				t.Errorf("script = %q, want to contain %q", got, tt.want)
			}
			if strings.Contains(got, "go build") || strings.Contains(got, "go list") {
				t.Error("restart script must not build from source")
			}
		})
	}
}

func TestWriteRestartScript(t *testing.T) {
	path, err := writeRestartScript("/opt/my agentctl/agentctl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("script not executable: %v", fi.Mode())
	}
	b, _ := os.ReadFile(path)
	if string(b) != restartScript("/opt/my agentctl/agentctl") {
		t.Errorf("unexpected script contents %q", b)
	}
}

func TestCascadeNames(t *testing.T) {
	lw := func(id, instance, parent, parentSID, sid string) reconcile.LiveWindow {
		return reconcile.LiveWindow{WindowID: id, Record: windowstate.WindowState{
			Instance: instance, SessionID: sid, SpawnedBy: parent, SpawnedBySession: parentSID,
		}}
	}
	wins := []reconcile.LiveWindow{
		lw("@2", "go-engineer-3", "@1", "P", "C"),
		lw("@3", "code-reviewer-1", "@2", "C", "R"),
		lw("@4", "stranger", "@1", "OTHER", "S"),
	}
	got := cascadeNames("@1", "P", wins)
	want := []string{"go-engineer-3", "code-reviewer-1"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("cascadeNames = %v, want %v", got, want)
	}
	if got := cascadeNames("@4", "S", wins); len(got) != 0 {
		t.Errorf("childless window: %v", got)
	}
}
