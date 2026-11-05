package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// setSharedDir overrides SharedDir for the duration of the test and restores
// it on cleanup, mirroring setRegistryDir in agent_test.go.
func setSharedDir(t *testing.T, dir string) {
	t.Helper()
	orig := SharedDir
	SharedDir = dir
	t.Cleanup(func() { SharedDir = orig })
}

func writeSharedFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExpandSharedRefs(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		files       map[string]string
		wantExpand  []string // substrings expected in expanded, in order
		wantMissing []string
	}{
		{
			name: "single ref",
			body: "See @shared/foo.md for conventions.",
			files: map[string]string{
				"foo.md": "Foo content.",
			},
			wantExpand: []string{"Foo content."},
		},
		{
			name: "multiple refs in body order",
			body: "First @shared/a.md then @shared/b.md.",
			files: map[string]string{
				"a.md": "Content A.",
				"b.md": "Content B.",
			},
			wantExpand: []string{"Content A.", "Content B."},
		},
		{
			name: "missing ref reported while others still expand",
			body: "@shared/present.md and @shared/absent.md",
			files: map[string]string{
				"present.md": "Present content.",
			},
			wantExpand:  []string{"Present content."},
			wantMissing: []string{"shared/absent.md"},
		},
		{
			name: "nested ref one level deep",
			body: "@shared/outer.md",
			files: map[string]string{
				"outer.md": "Outer content. @shared/inner.md",
				"inner.md": "Inner content.",
			},
			wantExpand: []string{"Outer content.", "Inner content."},
		},
		{
			name: "self-referential cycle terminates",
			body: "@shared/cycle.md",
			files: map[string]string{
				"cycle.md": "Cycle content. @shared/cycle.md",
			},
			wantExpand: []string{"Cycle content."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			setSharedDir(t, dir)
			for name, content := range tt.files {
				writeSharedFile(t, dir, name, content)
			}

			expanded, missing := ExpandSharedRefs(tt.body)

			// Order matters: each wanted substring must appear, and earlier
			// wants must appear before later ones.
			pos := -1
			for _, want := range tt.wantExpand {
				idx := strings.Index(expanded, want)
				if idx == -1 {
					t.Errorf("expanded missing %q\n\ngot:\n%s", want, expanded)
					continue
				}
				if idx < pos {
					t.Errorf("expanded out of order: %q appears before an earlier expected substring\n\ngot:\n%s", want, expanded)
				}
				pos = idx
			}

			if len(tt.wantMissing) == 0 {
				if len(missing) != 0 {
					t.Errorf("missing = %v, want empty", missing)
				}
			} else if !reflect.DeepEqual(missing, tt.wantMissing) {
				t.Errorf("missing = %v, want %v", missing, tt.wantMissing)
			}
		})
	}
}

// TestExpandSharedRefs_DepthCapped verifies a chain of legitimate (non-cyclic)
// nested includes deeper than maxSharedRefDepth is not fully expanded: refs
// beyond the cap are simply never looked up (they appear in neither the
// expanded output nor missing).
func TestExpandSharedRefs_DepthCapped(t *testing.T) {
	dir := t.TempDir()
	setSharedDir(t, dir)

	// Chain: level1 -> level2 -> level3 -> level4 -> level5.
	// Depth 1 = level1 (from body), depth 2 = level2, depth 3 = level3.
	// level4 is referenced at depth 4, which exceeds maxSharedRefDepth (3),
	// so it and everything beyond it must never be read.
	writeSharedFile(t, dir, "level1.md", "L1 @shared/level2.md")
	writeSharedFile(t, dir, "level2.md", "L2 @shared/level3.md")
	writeSharedFile(t, dir, "level3.md", "L3 @shared/level4.md")
	writeSharedFile(t, dir, "level4.md", "L4 @shared/level5.md")
	writeSharedFile(t, dir, "level5.md", "L5")

	expanded, missing := ExpandSharedRefs("@shared/level1.md")

	for _, want := range []string{"L1", "L2", "L3"} {
		if !strings.Contains(expanded, want) {
			t.Errorf("expanded missing %q (within depth cap), got:\n%s", want, expanded)
		}
	}
	for _, notWant := range []string{"L4", "L5"} {
		if strings.Contains(expanded, notWant) {
			t.Errorf("expanded contains %q, which is beyond maxSharedRefDepth and should never be read", notWant)
		}
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty -- refs beyond the depth cap were never looked up, so they must not be reported missing either", missing)
	}
}

func TestExpandSharedRefs_NoRefs(t *testing.T) {
	dir := t.TempDir()
	setSharedDir(t, dir)

	expanded, missing := ExpandSharedRefs("Just plain text, no includes.")
	if expanded != "" {
		t.Errorf("expanded = %q, want empty string", expanded)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
}
