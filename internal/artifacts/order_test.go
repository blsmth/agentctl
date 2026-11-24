package artifacts

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func ids(list []Meta) []string {
	out := []string{}
	for _, m := range list {
		out = append(out, m.ID)
	}
	return out
}

func TestApplyOrder(t *testing.T) {
	mk := func(id, dir string) Meta { return Meta{ID: id, ProjectDir: dir} }
	list := []Meta{mk("n1", "/p"), mk("x1", "/q"), mk("a", "/p"), mk("b", "/p"), mk("n0", "/p"), mk("x2", "/q")}
	tests := []struct {
		name  string
		order map[string][]string
		want  []string
	}{
		{"no order", nil, []string{"n1", "x1", "a", "b", "n0", "x2"}},
		{"saved only", map[string][]string{"/p": {"b", "a", "n1", "n0"}}, []string{"b", "x1", "a", "n1", "n0", "x2"}},
		{"new first, then saved; unknown dropped", map[string][]string{"/p": {"gone", "b", "a"}}, []string{"n1", "x1", "n0", "b", "a", "x2"}},
		{"duplicates count once", map[string][]string{"/q": {"x2", "x2", "x1"}}, []string{"n1", "x2", "a", "b", "n0", "x1"}},
		{"other project untouched", map[string][]string{"/zzz": {"a"}}, []string{"n1", "x1", "a", "b", "n0", "x2"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ids(ApplyOrder(list, tc.order)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestSetOrderAndList(t *testing.T) {
	s := newStore(t)
	clock := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return clock }
	src := write(t, filepath.Join(t.TempDir(), "a.txt"), "a")
	add := func(dir string) Meta {
		clock = clock.Add(time.Hour)
		m, err := s.Add(AddOptions{Source: Source{Path: src}, Origin: Origin{ProjectDir: dir, Project: filepath.Base(dir)}})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	a, b, c := add("/r/p"), add("/r/p"), add("/r/p")
	o := add("/r/other")

	// Newest first before any order is saved.
	got, _ := s.List(Filter{})
	if want := []string{o.ID, c.ID, b.ID, a.ID}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("default %v want %v", ids(got), want)
	}
	// Unknown id, another project's id and a duplicate are dropped.
	stored, err := s.SetOrder("/r/p", []string{a.ID, "zzzzzzzzzzzzzzzzzzzzzzzzzz", o.ID, c.ID, a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{a.ID, c.ID, b.ID}; !reflect.DeepEqual(stored, want) {
		t.Fatalf("stored %v want %v", stored, want)
	}
	got, _ = s.List(Filter{})
	if want := []string{o.ID, a.ID, c.ID, b.ID}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("ordered %v want %v", ids(got), want)
	}
	// A new artifact comes first; a removed one drops out.
	d := add("/r/p")
	if _, err := s.Remove(c.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.List(Filter{Project: "/r/p"})
	if want := []string{d.ID, a.ID, b.ID}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("after add/remove %v want %v", ids(got), want)
	}
	// Saving another project prunes the vanished id from the file.
	if _, err := s.SetOrder("/r/other", []string{o.ID}); err != nil {
		t.Fatal(err)
	}
	root, _ := s.Dir()
	if got := readOrder(root)["/r/p"]; !reflect.DeepEqual(got, []string{a.ID, b.ID}) {
		t.Errorf("saved p = %v", got)
	}
	// An empty list forgets the project's order.
	if _, err := s.SetOrder("/r/p", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := readOrder(root)["/r/p"]; ok {
		t.Error("order not forgotten")
	}
	fi, err := os.Stat(filepath.Join(root, orderFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("order file: %v %v", fi, err)
	}
}

func TestListIgnoresCorruptOrder(t *testing.T) {
	s := newStore(t)
	src := write(t, filepath.Join(t.TempDir(), "a.txt"), "a")
	m, _ := s.Add(AddOptions{Source: Source{Path: src}})
	root, _ := s.Dir()
	if err := os.WriteFile(filepath.Join(root, orderFile), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(Filter{})
	if err != nil || len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("got %v %v", got, err)
	}
}
