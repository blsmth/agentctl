package main

import (
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
