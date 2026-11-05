package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

func TestProjectDirs(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	tests := []struct {
		name   string
		agents map[string]config.AgentConfig
		first  []string
		want   []string
	}{
		{
			name:  "first dirs in order, empties and duplicates dropped",
			first: []string{"/repo", "", "/cwd", "/repo"},
			want:  []string{"/repo", "/cwd"},
		},
		{
			name:   "agent dir expanded and appended",
			agents: map[string]config.AgentConfig{"a": {Dir: "~/proj"}, "b": {}},
			first:  []string{"/cwd"},
			want:   []string{"/cwd", filepath.Join(home, "proj")},
		},
		{
			name:   "agent dir equal to a first dir is not repeated",
			agents: map[string]config.AgentConfig{"a": {Dir: "/cwd"}},
			first:  []string{"/cwd"},
			want:   []string{"/cwd"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Agents: tt.agents}
			got := ProjectDirs(cfg, tt.first...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ProjectDirs() = %v, want %v", got, tt.want)
			}
		})
	}
}
