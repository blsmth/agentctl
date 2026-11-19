package phoneview

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAddRemoveActive(t *testing.T) {
	dir := t.TempDir()
	if Active(dir, "@1") {
		t.Fatal("active before add")
	}
	if err := Add(dir, "@1", "v-a"); err != nil {
		t.Fatal(err)
	}
	if err := Add(dir, "@1", "v-b"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "@1", "v-a"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("marker stat: %v %v", fi, err)
	}
	if !Active(dir, "@1") || Active(dir, "@2") {
		t.Fatal("Active wrong")
	}
	if err := Remove(dir, "@1", "v-a"); err != nil || !Active(dir, "@1") {
		t.Fatalf("one connection left: err %v active %v", err, Active(dir, "@1"))
	}
	if err := Remove(dir, "@1", "v-b"); err != nil || Active(dir, "@1") {
		t.Fatalf("all closed: err %v", err)
	}
	if err := Remove(dir, "@1", "v-b"); err != nil {
		t.Fatalf("double remove: %v", err)
	}
}

func TestInvalidNames(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range [][2]string{{"@1", "../x"}, {"1", "v"}, {"@1/..", "v"}, {"@1", ""}} {
		if Add(dir, tc[0], tc[1]) == nil {
			t.Errorf("Add(%q, %q) accepted", tc[0], tc[1])
		}
	}
	if Active("", "@1") {
		t.Error("empty dir active")
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	_ = Add(dir, "@1", "live")
	_ = Add(dir, "@1", "dead")
	_ = Add(dir, "@2", "dead2")
	if n := Prune(dir, func(v string) bool { return v == "live" }); n != 2 {
		t.Fatalf("pruned %d, want 2", n)
	}
	if !Active(dir, "@1") || Active(dir, "@2") {
		t.Fatal("wrong markers survived")
	}
	if n := Prune(dir, nil); n != 1 || Active(dir, "@1") {
		t.Fatalf("clear all: %d", n)
	}
}

func TestPaneMarkerTTL(t *testing.T) {
	dir := t.TempDir()
	v := PaneViewer("dev.1/x")
	if v != "pane-dev_1_x" {
		t.Fatalf("PaneViewer = %q", v)
	}
	if err := Touch(dir, "@1", "plain"); err == nil {
		t.Fatal("Touch accepted a non-snapshot viewer")
	}
	if err := Touch(dir, "@1", v); err != nil {
		t.Fatal(err)
	}
	if !Active(dir, "@1") {
		t.Fatal("fresh pane marker not active")
	}
	old := time.Now().Add(-2 * PaneTTL)
	if err := os.Chtimes(filepath.Join(dir, "@1", v), old, old); err != nil {
		t.Fatal(err)
	}
	if Active(dir, "@1") {
		t.Fatal("expired pane marker active")
	}
	// A live terminal marker never expires by age.
	if err := Add(dir, "@1", "term-a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "@1", "term-a"), old, old); err != nil {
		t.Fatal(err)
	}
	if !Active(dir, "@1") {
		t.Fatal("old terminal marker expired")
	}
	if n := PruneExpired(dir); n != 1 {
		t.Fatalf("PruneExpired = %d, want 1", n)
	}
	if !Active(dir, "@1") {
		t.Fatal("terminal marker pruned")
	}
}
