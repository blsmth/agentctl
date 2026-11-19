package sweeprun

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestArgs(t *testing.T) {
	if got, want := Args("agentctl-x", true), []string{"sweep", "--session", "agentctl-x", "--json", "--dry-run"}; !slices.Equal(got, want) {
		t.Errorf("dry run args = %v, want %v", got, want)
	}
	if got, want := Args("agentctl-x", false), []string{"sweep", "--session", "agentctl-x", "--json"}; !slices.Equal(got, want) {
		t.Errorf("apply args = %v, want %v", got, want)
	}
}

func TestEnvPinsRuntimeDirAndDropsIdentity(t *testing.T) {
	parent := []string{
		"PATH=/bin", "TMUX_PANE=%9", "AGENTCTL_WINDOW_ID=@1", "AGENTCTL_SESSION_ID=s",
		"AGENTCTL_INSTANCE=3", "AGENTCTL_RUNTIME_DIR=/other/.agentctl", "AGENTCTL_KEY_WINDOW=@2", "AGENTCTL_KEY_CLIENT=c",
	}
	got := Env(Target{Session: "agentctl-p", RuntimeDir: "/code/p/.agentctl"}, parent)
	want := []string{"PATH=/bin", "TMUX_PANE=%9", "AGENTCTL_RUNTIME_DIR=/code/p/.agentctl"}
	if !slices.Equal(got, want) {
		t.Errorf("Env = %v, want %v", got, want)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		t    Target
		ok   bool
	}{
		{"project session", Target{Session: "agentctl-p", RuntimeDir: "/code/p/.agentctl"}, true},
		{"empty", Target{RuntimeDir: "/code/p/.agentctl"}, false},
		{"hub", Target{Session: HubSession, RuntimeDir: "/code/p/.agentctl"}, false},
		{"target spec", Target{Session: "agentctl-p:1", RuntimeDir: "/code/p/.agentctl"}, false},
		{"flag", Target{Session: "-x", RuntimeDir: "/code/p/.agentctl"}, false},
		{"relative runtime", Target{Session: "agentctl-p", RuntimeDir: "p/.agentctl"}, false},
		{"not .agentctl", Target{Session: "agentctl-p", RuntimeDir: "/code/p"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.t.Validate()
			if (err == nil) != tt.ok {
				t.Fatalf("Validate = %v, want ok=%v", err, tt.ok)
			}
			if err != nil && !errors.Is(err, ErrBadTarget) {
				t.Errorf("err = %v, want ErrBadTarget", err)
			}
		})
	}
}

func TestParse(t *testing.T) {
	good := []byte(`{"session":"s","dry_run":true,"closed":[{"window_id":"@1","name":"a","status":"done"}],"attention":[],"in_progress":[],"skipped":[]}`)
	rep, err := Parse("s", good, nil, nil)
	if err != nil || len(rep.Closed) != 1 || rep.Closed[0].Name != "a" || !rep.DryRun {
		t.Fatalf("Parse = %+v, %v", rep, err)
	}
	if _, err := Parse("other", good, nil, nil); err == nil {
		t.Error("report for another session accepted")
	}
	if _, err := Parse("s", []byte("not json"), nil, nil); err == nil {
		t.Error("garbage accepted")
	}
	_, err = Parse("s", nil, []byte("warn\nagentctl: no tmux session \"s\"\n"), errors.New("exit status 1"))
	if err == nil || !strings.Contains(err.Error(), `no tmux session "s"`) || strings.Contains(err.Error(), "agentctl:") {
		t.Errorf("failed run err = %v", err)
	}
	_, err = Parse("s", nil, nil, errors.New("exit status 2"))
	if err == nil || !strings.Contains(err.Error(), "exit status 2") {
		t.Errorf("silent failure err = %v", err)
	}
}

func TestPlan(t *testing.T) {
	row := func(n string) Row { return Row{Name: n} }
	tests := []struct {
		rep  Report
		want string
	}{
		{Report{}, "nothing to sweep"},
		{Report{Closed: []Row{row("a"), row("b"), row("c")}, Attention: []Row{row("d"), row("e")}, InProgress: []Row{row("f")}},
			"close 3 (a, b, c), 2 need attention, 1 in progress"},
		{Report{Attention: []Row{row("d")}}, "1 needs attention"},
	}
	for _, tt := range tests {
		if got := tt.rep.Plan(); got != tt.want {
			t.Errorf("Plan = %q, want %q", got, tt.want)
		}
	}
}

func TestFailed(t *testing.T) {
	rep := Report{Closed: []Row{{Name: "a"}, {Name: "b", Errors: []string{"boom"}}}}
	if f := rep.Failed(); len(f) != 1 || f[0].Name != "b" {
		t.Errorf("Failed = %+v", f)
	}
}

// Run must refuse inside a test binary rather than spawn it.
func TestRunRefusesInTestBinary(t *testing.T) {
	_, err := Run(context.Background(), Target{Session: "agentctl-p", RuntimeDir: "/code/p/.agentctl"}, true)
	if err == nil {
		t.Fatal("Run spawned from a test binary")
	}
}

func TestRunRejectsBadTarget(t *testing.T) {
	if _, err := Run(context.Background(), Target{Session: HubSession, RuntimeDir: "/x/.agentctl"}, true); !errors.Is(err, ErrBadTarget) {
		t.Fatalf("err = %v, want ErrBadTarget", err)
	}
}
