package cmd

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
)

func TestUsableDefaultAgent(t *testing.T) {
	known := []agent.Agent{{Name: "go-engineer"}}
	tests := []struct {
		name     string
		def      string
		discErr  error
		want     string
		wantWarn bool
	}{
		{name: "unset", def: "", want: ""},
		{name: "discovered", def: "go-engineer", want: "go-engineer"},
		{name: "claude built-in", def: "Plan", want: "Plan"},
		{name: "plugin agent", def: "my-plugin:reviewer", want: "my-plugin:reviewer"},
		{name: "malformed plugin agent", def: "a:b:c", want: "", wantWarn: true},
		{name: "plugin agent with payload", def: "p:$(id)", want: "", wantWarn: true},
		{name: "not discovered", def: "rust-engineer", want: "", wantWarn: true},
		{name: "shell payload", def: "x$(touch /tmp/p)", want: "", wantWarn: true},
		{name: "path traversal", def: "../../x", want: "", wantWarn: true},
		{name: "discovery error", def: "go-engineer", discErr: errors.New("boom"), want: "", wantWarn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubDiscovery(t, known, tt.discErr)
			var w bytes.Buffer
			got := usableDefaultAgent(&config.Config{DefaultAgent: tt.def}, &w)
			if got != tt.want {
				t.Errorf("usableDefaultAgent(%q) = %q, want %q", tt.def, got, tt.want)
			}
			if warned := strings.Contains(w.String(), "ignoring default_agent"); warned != tt.wantWarn {
				t.Errorf("warning = %q, wantWarn %v", w.String(), tt.wantWarn)
			}
		})
	}
}

func TestLessThenRemoveQuotesPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a b;$(touch x)'q", "log.txt")
	cmd := lessThenRemove(path)
	// Swap the pager for echo to see exactly what the shell passes it.
	cmd = strings.Replace(cmd, "less -R ", "printf %s ", 1)
	cmd = strings.Replace(cmd, "; rm -f ", "; : ", 1)
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil {
		t.Fatalf("running %q: %v", cmd, err)
	}
	if string(out) != path {
		t.Errorf("pager got %q, want %q", out, path)
	}
}
