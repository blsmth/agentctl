package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestLoadSceneByName(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "demo")
	sc, err := loadScene(filepath.Join(root, "scenes"), "hub", filepath.Join(root, "transcripts"))
	if err != nil {
		t.Fatal(err)
	}
	if sc.name != "hub" || len(sc.groups) != 3 || len(sc.refs) != 6 {
		t.Errorf("scene = %q, %d groups, %d windows", sc.name, len(sc.groups), len(sc.refs))
	}
	if _, err := loadScene(filepath.Join(root, "scenes"), "nope", ""); err == nil {
		t.Error("want an error for a missing scene")
	}
}

func TestRealizeDirsCreatesProjectDirs(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "demo")
	sc, err := loadScene(filepath.Join(root, "scenes"), "hub", filepath.Join(root, "transcripts"))
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(t.TempDir(), "code")
	if err := sc.realizeDirs(base); err != nil {
		t.Fatal(err)
	}
	for _, g := range sc.groups {
		if fi, err := os.Stat(g.Dir); err != nil || !fi.IsDir() || filepath.Dir(g.Dir) != base {
			t.Errorf("%s: dir %s not created under %s (%v)", g.Project, g.Dir, base, err)
		}
	}
}
