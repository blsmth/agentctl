package fleet

import (
	"os"
	"path/filepath"

	"github.com/blsmth/agentctl/internal/registry"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestWho(t *testing.T) {
	ix := NewIndex(nil, nil)
	start := time.Date(2026, 11, 24, 9, 5, 0, 0, time.Local).Unix()
	tests := []struct {
		name string
		row  Row
		want string
	}{
		{"titled", Row{AgentState: state.AgentState{AgentName: "go-engineer", ShortTitle: "esc fix in hub", SessionID: "a3f9c0de"}}, `go-engineer "esc fix in hub" (a3f9)`},
		{"bare, started time", Row{AgentState: state.AgentState{AgentName: "go-engineer", StartTime: start, SessionID: "a3f9c0de"}}, "go-engineer · started 09:05 (a3f9)"},
		{"launching, no session id", Row{AgentState: state.AgentState{AgentName: "go-engineer", ShortTitle: "x"}}, `go-engineer "x"`},
		{"nothing known", Row{AgentState: state.AgentState{WindowID: "@9"}}, "session"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Who(ix, tt.row); got != tt.want {
				t.Errorf("Who = %q, want %q", got, tt.want)
			}
		})
	}
	if got := Who(nil, Row{AgentState: state.AgentState{AgentName: "a", SessionID: "abcdef"}}); got != "a (abcd)" {
		t.Errorf("nil index: %q", got)
	}
}

func TestRowFromRecordStartTime(t *testing.T) {
	r := RowFromRecord("@3", windowstate.WindowState{AgentName: "a", StartedAt: 1234})
	if r.WindowID != "@3" || r.StartTime != 1234 {
		t.Errorf("row = %+v", r.AgentState)
	}
}

func TestLoadHandleIndexNeedsNoTmux(t *testing.T) {
	regDir, project := t.TempDir(), t.TempDir()
	registry.SetDirForTest(regDir)
	t.Cleanup(func() { registry.SetDirForTest("") })
	rt := filepath.Join(project, ".agentctl")
	if err := registry.Register(registry.Entry{SessionName: "agentctl-p", ProjectDir: project, RuntimeDir: rt}); err != nil {
		t.Fatal(err)
	}
	dir := windowstate.WindowsDirFor(rt)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"session_id":"a3f9c0de-0001","agent_name":"go-engineer"}`)
	if err := os.WriteFile(filepath.Join(dir, "@5.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "") // any tmux spawn would fail loudly
	ix := LoadHandleIndex()
	s, err := ix.Resolve("a3f9")
	if err != nil || s.Row.WindowID != "@5" {
		t.Fatalf("resolve = %+v, %v", s, err)
	}
}
