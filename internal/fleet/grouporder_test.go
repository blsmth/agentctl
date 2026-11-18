package fleet

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func ogroup(session, project, dir string) Group {
	return Group{Session: session, Project: project, ProjectDir: dir}
}

func groupKeys(gs []Group) []string {
	var out []string
	for _, g := range gs {
		out = append(out, GroupKey(g))
	}
	return out
}

func TestGroupKey(t *testing.T) {
	if got := GroupKey(ogroup("s", "p", "/a/p")); got != "/a/p" {
		t.Errorf("got %q", got)
	}
	if got := GroupKey(ogroup("s", "p", "")); got != "s" {
		t.Errorf("got %q, want session fallback", got)
	}
}

func TestOrderGroupsBy(t *testing.T) {
	a, b, c := ogroup("sa", "alpha", "/x/alpha"), ogroup("sb", "beta", "/x/beta"), ogroup("sc", "gamma", "/x/gamma")
	tests := []struct {
		name   string
		groups []Group
		saved  []string
		want   []string
	}{
		{"no saved is alphabetical", []Group{c, a, b}, nil, []string{"/x/alpha", "/x/beta", "/x/gamma"}},
		{"saved order wins", []Group{a, b, c}, []string{"/x/gamma", "/x/alpha", "/x/beta"}, []string{"/x/gamma", "/x/alpha", "/x/beta"}},
		{"new group goes after saved, alphabetical", []Group{c, b, a}, []string{"/x/gamma"}, []string{"/x/gamma", "/x/alpha", "/x/beta"}},
		{"stale keys ignored", []Group{a, b}, []string{"/x/gone", "/x/beta", "/x/alpha"}, []string{"/x/beta", "/x/alpha"}},
		{"duplicate saved key uses first slot", []Group{a, b}, []string{"/x/beta", "/x/alpha", "/x/beta"}, []string{"/x/beta", "/x/alpha"}},
		{"no groups", nil, []string{"/x/a"}, nil},
		{"same basename tie broken by session", []Group{ogroup("s2", "app", "/y/app"), ogroup("s1", "app", "/z/app")}, nil, []string{"/z/app", "/y/app"}},
		{"same basename, distinct saved keys", []Group{ogroup("s1", "app", "/z/app"), ogroup("s2", "app", "/y/app")}, []string{"/y/app", "/z/app"}, []string{"/y/app", "/z/app"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := groupKeys(OrderGroupsBy(tt.groups, tt.saved))
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeGroupOrder(t *testing.T) {
	tests := []struct {
		name        string
		saved, live []string
		want        []string
	}{
		{"no stale", []string{"a", "b"}, []string{"b", "a"}, []string{"b", "a"}},
		{"stale keeps slot after predecessor", []string{"a", "x", "b"}, []string{"b", "a"}, []string{"b", "a", "x"}},
		{"stale first stays first", []string{"x", "a", "b"}, []string{"b", "a"}, []string{"x", "b", "a"}},
		{"nothing saved", nil, []string{"a"}, []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MergeGroupOrder(tt.saved, tt.live); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGroupOrderFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "group-order.json")

	if got := ReadGroupOrder(path); got != nil {
		t.Errorf("missing file: got %v", got)
	}
	if err := WriteGroupOrder(path, []string{"/b", "/a"}); err != nil {
		t.Fatal(err)
	}
	if got := ReadGroupOrder(path); !slices.Equal(got, []string{"/b", "/a"}) {
		t.Errorf("round trip: got %v", got)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadGroupOrder(path); got != nil {
		t.Errorf("corrupt file: got %v, want nil", got)
	}
}

func TestLoadGroupOrderUsesStateDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path, err := GroupOrderPath()
	if err != nil {
		t.Fatal(err)
	}
	if got := LoadGroupOrder(); got != nil {
		t.Errorf("got %v before any write", got)
	}
	if err := WriteGroupOrder(path, []string{"/q"}); err != nil {
		t.Fatal(err)
	}
	if got := LoadGroupOrder(); !slices.Equal(got, []string{"/q"}) {
		t.Errorf("got %v", got)
	}
}
