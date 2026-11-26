package closedlog

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func mkRecentDirs(t *testing.T, names ...string) []string {
	t.Helper()
	root := t.TempDir()
	out := make([]string, 0, len(names))
	for _, n := range names {
		p := filepath.Join(root, n)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func statDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func TestRecentDirs(t *testing.T) {
	ds := mkRecentDirs(t, "a", "b", "c", "repo/.worktrees/w1")
	link := filepath.Join(t.TempDir(), "a-link")
	if err := os.Symlink(ds[0], link); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "gone")
	recs := []Record{
		{ProjectDir: ds[1]}, {ProjectDir: link}, {ProjectDir: ds[3]}, {ProjectDir: missing},
		{ProjectDir: ds[0]}, {ProjectDir: ds[1]}, {ProjectDir: ""}, {ProjectDir: "relative/dir"},
	}
	tests := []struct {
		name   string
		origin string
		recs   []Record
		want   []string
	}{
		{"origin first, deduped, worktrees and gone dropped", ds[2], recs, []string{ds[2], ds[1], link}},
		{"no origin", "", recs, []string{ds[1], link}},
		{"empty", "", nil, nil},
		{"origin only", ds[2], nil, []string{ds[2]}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RecentDirs(tt.origin, tt.recs, statDir); !slices.Equal(got, tt.want) {
				t.Errorf("got %v\nwant %v", got, tt.want)
			}
		})
	}
}

func TestRecentDirsScanBound(t *testing.T) {
	dir := mkRecentDirs(t, "late")[0]
	recs := make([]Record, RecentScan)
	for i := range recs {
		recs[i] = Record{ProjectDir: "/nonexistent-recent-scan"}
	}
	recs = append(recs, Record{ProjectDir: dir})
	if got := RecentDirs("", recs, statDir); len(got) != 0 {
		t.Errorf("got %v, want nothing past the scan bound", got)
	}
}

func TestWithoutDirs(t *testing.T) {
	ds := mkRecentDirs(t, "live", "idle")
	link := filepath.Join(t.TempDir(), "live-link")
	if err := os.Symlink(ds[0], link); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		dirs, exclude []string
		want          []string
	}{
		{"symlink spelling excluded", []string{link, ds[1]}, []string{ds[0]}, []string{ds[1]}},
		{"nothing excluded", []string{ds[0], ds[1]}, nil, []string{ds[0], ds[1]}},
		{"empty exclude entry ignored", []string{ds[1]}, []string{""}, []string{ds[1]}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WithoutDirs(tt.dirs, tt.exclude); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsWorktreePath(t *testing.T) {
	for p, want := range map[string]bool{
		"/r/.worktrees/x": true, "/r/.worktrees": true, "/r/worktrees/x": false, "/r": false,
	} {
		if got := IsWorktreePath(p); got != want {
			t.Errorf("IsWorktreePath(%q) = %v, want %v", p, got, want)
		}
	}
}
