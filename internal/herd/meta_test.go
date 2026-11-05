package herd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The parent link is only meaningful for dispatched windows. A bare/menu
// launch records whatever AGENTCTL_WINDOW_ID happened to hold — a tmux
// session-global, last-writer-wins value that names the most recently created
// window, not the launcher. SpawnedBySession is the discriminator because only
// the dispatch path has a session UUID to record.
func TestMetaParentTrusted(t *testing.T) {
	tests := []struct {
		name        string
		meta        Meta
		wantTrusted bool
		wantParent  string
	}{
		{
			name:        "dispatched window has both fields",
			meta:        Meta{Mode: "adhoc", SpawnedBy: "@305", SpawnedBySession: "1663c405-dead-beef"},
			wantTrusted: true,
			wantParent:  "@305",
		},
		{
			name:        "bare window records a parent but no session",
			meta:        Meta{Mode: "bare", SpawnedBy: "@281"},
			wantTrusted: false,
			wantParent:  "", // must NOT surface @281
		},
		{
			name:        "no parent recorded at all",
			meta:        Meta{Mode: "bare"},
			wantTrusted: false,
			wantParent:  "",
		},
		{
			name:        "dispatch mode with ticket",
			meta:        Meta{Mode: "dispatch", SpawnedBy: "@100", SpawnedBySession: "abc"},
			wantTrusted: true,
			wantParent:  "@100",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.meta.ParentTrusted(); got != tt.wantTrusted {
				t.Errorf("ParentTrusted() = %v, want %v", got, tt.wantTrusted)
			}
			if got := tt.meta.Parent(); got != tt.wantParent {
				t.Errorf("Parent() = %q, want %q", got, tt.wantParent)
			}
		})
	}
}

// Regression guard for the specific bug this encodes: a bare window whose
// SpawnedBy points at an unrelated window must never render that value as a
// parent, however plausible it looks.
func TestMetaParent_BareNeverLeaksSpawnedBy(t *testing.T) {
	m := Meta{Mode: "bare", SpawnedBy: "@281", SpawnedBySession: ""}
	if p := m.Parent(); p != "" {
		t.Fatalf("Parent() leaked untrustworthy value %q for a bare window", p)
	}
	if m.SpawnedBy == "" {
		t.Fatal("fixture no longer carries a SpawnedBy; the test proves nothing")
	}
}

func TestMetaDispatched(t *testing.T) {
	tests := []struct {
		mode string
		want bool
	}{
		{"adhoc", true},
		{"dispatch", true},
		{"custom", true},
		{"bare", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := (Meta{Mode: tt.mode}).Dispatched(); got != tt.want {
			t.Errorf("Meta{Mode:%q}.Dispatched() = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

func TestLoadMeta(t *testing.T) {
	runtimeDir := t.TempDir()
	windowsDir := filepath.Join(runtimeDir, "windows")
	if err := os.MkdirAll(windowsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(name string, v map[string]any) {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(windowsDir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("@10.json", map[string]any{
		"mode": "adhoc", "spawned_by": "@9", "spawned_by_session": "uuid-9",
		"branch": "agent/go-engineer/adhoc-1", "ticket": "42",
	})
	write("@11.json", map[string]any{"mode": "bare", "spawned_by": "@10"})
	// Non-.json siblings and corrupt records must be skipped, not fatal:
	// the windows dir also holds .lock files written by the flock helper.
	write("@12.json", nil)
	if err := os.WriteFile(filepath.Join(windowsDir, "@10.json.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(windowsDir, "@13.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := LoadMeta(Group{RuntimeDir: runtimeDir})

	if len(got) != 3 {
		t.Fatalf("LoadMeta() returned %d entries (%v), want 3", len(got), keysOf(got))
	}
	if m := got["@10"]; m.Parent() != "@9" || m.Branch != "agent/go-engineer/adhoc-1" || m.Ticket != "42" {
		t.Errorf("@10 = %+v, want parent @9 with branch and ticket", m)
	}
	if m := got["@11"]; m.Parent() != "" {
		t.Errorf("@11 (bare) leaked parent %q", m.Parent())
	}
	if _, ok := got["@10.json"]; ok {
		t.Error("lock file was parsed as a window record")
	}
	if _, ok := got["@13"]; ok {
		t.Error("corrupt record was returned rather than skipped")
	}
}

// A group with no runtime dir (a session whose snapshot was missing) must
// still yield a usable empty map rather than a nil one callers index into.
func TestLoadMeta_NoRuntimeDir(t *testing.T) {
	got := LoadMeta(Group{})
	if got == nil {
		t.Fatal("LoadMeta() returned nil map")
	}
	if len(got) != 0 {
		t.Errorf("LoadMeta() = %v, want empty", got)
	}
	if m := got["@1"]; m.Parent() != "" {
		t.Errorf("indexing an absent window returned %+v", m)
	}
}

func keysOf(m map[string]Meta) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
