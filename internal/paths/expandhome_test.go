package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "tilde prefix", in: "~/code/project", want: filepath.Join(home, "code/project")},
		{name: "absolute path", in: "/absolute/path", want: "/absolute/path"},
		{name: "relative path", in: "relative/path", want: "relative/path"},
		{name: "empty", in: "", want: ""},
		{name: "bare tilde no slash", in: "~", want: "~"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExpandHome(tt.in); got != tt.want {
				t.Errorf("ExpandHome(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
