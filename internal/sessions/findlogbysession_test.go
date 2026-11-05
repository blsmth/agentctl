package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFindLogBySessionIDIn(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	tests := []struct {
		name     string
		projects []string // project dirs containing <id>.jsonl
		wantIn   int      // index into projects of expected match, -1 for ""
	}{
		{"zero matches", nil, -1},
		{"one match", []string{"-a"}, 0},
		{"two matches", []string{"-a", "-b"}, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			// An unrelated session must never match.
			if err := os.MkdirAll(filepath.Join(root, "-z"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "-z", "other.jsonl"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			for _, p := range tt.projects {
				if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, p, id+".jsonl"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want := ""
			if tt.wantIn >= 0 {
				want = filepath.Join(root, tt.projects[tt.wantIn], id+".jsonl")
			}
			if got := FindLogBySessionIDIn(root, id); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
	t.Run("empty and glob-y ids", func(t *testing.T) {
		root := t.TempDir()
		for _, bad := range []string{"", "*", "../x"} {
			if got := FindLogBySessionIDIn(root, bad); got != "" {
				t.Errorf("id %q: got %q, want empty", bad, got)
			}
		}
	})
}

func TestFindLatestLogInProject_Scoped(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"-a", "-b"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		line := `{"agentSetting":"lead"}` + "\n"
		if err := os.WriteFile(filepath.Join(root, d, d[1:]+".jsonl"), []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := FindLatestLogInProject(filepath.Join(root, "-a"), "lead", time.Time{}, nil)
	if want := filepath.Join(root, "-a", "a.jsonl"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
