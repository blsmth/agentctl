package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/artifactkit"
	"github.com/blsmth/agentctl/internal/artifacts"
)

const goodSpec = `{"kind":"comparison","title":"Which queue?","blocks":[{"type":"text","text":"x </script><!-- y"}]}`

func TestArtifactRenderPublishes(t *testing.T) {
	h := newArtifactHarness(t)
	spec := writeTmp(t, "spec.json", goodSpec)
	var out bytes.Buffer
	if err := h.env.render(&out, ArtifactRenderOptions{Path: spec, Description: "d", Pin: true, NoNotify: true, JSON: true}); err != nil {
		t.Fatal(err)
	}
	var v artifactView
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("bad json %q: %v", out.String(), err)
	}
	if v.Title != "Which queue?" || v.Kind != "html" || !v.Pinned || v.Description != "d" || v.Agent != "go-engineer" {
		t.Errorf("view = %+v", v)
	}
	dir, err := h.env.store().RevDir(v.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, v.Entry))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `id="spec"`) || strings.Contains(string(data), "x </script><!--") {
		t.Error("stored page lacks the spec element or has an unescaped spec string")
	}
}

func TestArtifactRenderTitleOverride(t *testing.T) {
	h := newArtifactHarness(t)
	var out bytes.Buffer
	if err := h.env.render(&out, ArtifactRenderOptions{Path: writeTmp(t, "s.json", goodSpec), Title: "Custom", JSON: true}); err != nil {
		t.Fatal(err)
	}
	var v artifactView
	_ = json.Unmarshal(out.Bytes(), &v)
	if v.Title != "Custom" {
		t.Errorf("title = %q", v.Title)
	}
}

func TestArtifactRenderStdin(t *testing.T) {
	h := newArtifactHarness(t)
	e := h.env
	e.stdin = strings.NewReader(goodSpec)
	var out bytes.Buffer
	if err := e.render(&out, ArtifactRenderOptions{Path: "-", JSON: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Which queue?") {
		t.Errorf("out = %s", out.String())
	}
}

func TestArtifactRenderInvalidSpecIsNotPublished(t *testing.T) {
	h := newArtifactHarness(t)
	spec := writeTmp(t, "bad.json", `{"kind":"report","title":"T","blocks":[{"type":"chart","chart":"bar","labels":["a","b"],"series":[{"name":"s","values":[1]}]}]}`)
	var out bytes.Buffer
	err := h.env.render(&out, ArtifactRenderOptions{Path: spec})
	var errs artifactkit.Errors
	if err == nil || !errors.As(err, &errs) {
		t.Fatalf("err = %v, want validation errors", err)
	}
	if !strings.Contains(err.Error(), "blocks[0].series[0].values") || !strings.Contains(err.Error(), "sources") {
		t.Errorf("error lacks paths: %v", err)
	}
	list, lerr := h.env.store().List(artifacts.Filter{})
	if lerr != nil || len(list) != 0 {
		t.Errorf("published despite errors: %v %v", list, lerr)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestArtifactRenderOutWritesWithoutPublishing(t *testing.T) {
	h := newArtifactHarness(t)
	dest := filepath.Join(t.TempDir(), "sub", "page.html")
	var out bytes.Buffer
	if err := h.env.render(&out, ArtifactRenderOptions{Path: writeTmp(t, "s.json", goodSpec), Out: dest}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || !strings.Contains(string(data), "<!doctype html>") {
		t.Fatalf("output file: %v", err)
	}
	if !strings.Contains(out.String(), "not published") {
		t.Errorf("stdout = %q", out.String())
	}
	list, _ := h.env.store().List(artifacts.Filter{})
	if len(list) != 0 {
		t.Errorf("--out published: %v", list)
	}
	if err := h.env.render(&out, ArtifactRenderOptions{Path: writeTmp(t, "bad.json", `{}`), Out: filepath.Join(t.TempDir(), "x.html")}); err == nil {
		t.Error("--out wrote an invalid spec")
	}
}

func TestArtifactRenderUpdatePublishesRevision(t *testing.T) {
	h := newArtifactHarness(t)
	var out bytes.Buffer
	if err := h.env.render(&out, ArtifactRenderOptions{Path: writeTmp(t, "s.json", goodSpec), JSON: true}); err != nil {
		t.Fatal(err)
	}
	var first artifactView
	_ = json.Unmarshal(out.Bytes(), &first)

	v2 := strings.Replace(goodSpec, "Which queue?", "Which queue now?", 1)
	out.Reset()
	if err := h.env.render(&out, ArtifactRenderOptions{Path: writeTmp(t, "s2.json", v2), Update: first.ID, Note: "retitled", JSON: true}); err != nil {
		t.Fatal(err)
	}
	var second artifactView
	_ = json.Unmarshal(out.Bytes(), &second)
	if second.ID != first.ID || len(second.Revs) != 2 || second.Title != "Which queue now?" || second.Revs[1].Note != "retitled" {
		t.Errorf("after update: %+v", second)
	}
	list, _ := h.env.store().List(artifacts.Filter{})
	if len(list) != 1 {
		t.Errorf("update created a second artifact: %d", len(list))
	}
}

func TestArtifactRenderFlagConflicts(t *testing.T) {
	h := newArtifactHarness(t)
	spec := writeTmp(t, "s.json", goodSpec)
	for name, o := range map[string]ArtifactRenderOptions{
		"out+update":  {Path: spec, Out: "x.html", Update: "abc"},
		"note only":   {Path: spec, Note: "n"},
		"pin+update":  {Path: spec, Update: "abc", Pin: true},
		"no spec":     {},
		"missing":     {Path: filepath.Join(t.TempDir(), "nope.json")},
		"update miss": {Path: spec, Update: "doesnotexist"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := h.env.render(&bytes.Buffer{}, o); err == nil {
				t.Error("accepted")
			}
		})
	}
}
