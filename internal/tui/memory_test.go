package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ── DiscoverMemoryFiles ───────────────────────────────────────────────────────

func TestDiscoverMemoryFiles_HandlesAllTypes(t *testing.T) {
	// Create a temp directory tree that looks like a project with all memory
	// file types.
	tmp := t.TempDir()

	// Create project CLAUDE.md and .claude/CLAUDE.md and .claude/settings.json
	if err := os.WriteFile(filepath.Join(tmp, "CLAUDE.md"), []byte("# project memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("# project .claude memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"key":"val"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Change into the tmp dir so DiscoverMemoryFiles picks up project files.
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	entries := DiscoverMemoryFiles()

	// We should have at least the 3 project files we created.
	projectPaths := map[string]bool{}
	for _, e := range entries {
		projectPaths[e.Path] = true
	}

	wantFiles := []string{
		filepath.Join(tmp, "CLAUDE.md"),
		filepath.Join(tmp, ".claude", "CLAUDE.md"),
		filepath.Join(tmp, ".claude", "settings.json"),
	}
	for _, want := range wantFiles {
		// Resolve symlinks on both sides (macOS /var → /private/var).
		wantResolved, err := filepath.EvalSymlinks(want)
		if err != nil {
			wantResolved = want
		}
		// Also add the resolved real path into the projectPaths set for lookup.
		for path := range projectPaths {
			resolved, _ := filepath.EvalSymlinks(path)
			projectPaths[resolved] = true
		}
		if !projectPaths[wantResolved] && !projectPaths[want] {
			t.Errorf("expected %q to be discovered, got %v", want, entries)
		}
	}
}

func TestDiscoverMemoryFiles_MissingFilesGraceful(t *testing.T) {
	// Point at a temp dir that has no memory files at all.
	tmp := t.TempDir()

	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	// Override HOME to a directory with no .claude subdir so the global file
	// is also absent.
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	entries := DiscoverMemoryFiles()
	// Should return empty (no panic, no error).
	for _, e := range entries {
		// If any entry is returned it must exist on disk.
		if _, err := os.Stat(e.Path); err != nil {
			t.Errorf("entry %q does not exist on disk: %v", e.Path, err)
		}
	}
}

func TestDiscoverMemoryFiles_NoDuplicates(t *testing.T) {
	// When the same path would be added twice, it must appear only once.
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "CLAUDE.md"), []byte("# proj"), 0o644); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	entries := DiscoverMemoryFiles()
	seen := map[string]int{}
	for _, e := range entries {
		seen[e.Path]++
	}
	for path, count := range seen {
		if count > 1 {
			t.Errorf("path %q appears %d times, want 1", path, count)
		}
	}
}

func TestDiscoverMemoryFiles_CompositionOrder(t *testing.T) {
	// Global files should appear before project files in the Order field.
	tmp := t.TempDir()
	homeDir := filepath.Join(tmp, "home")
	projDir := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(homeDir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".claude", "CLAUDE.md"), []byte("# global"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "CLAUDE.md"), []byte("# project"), 0o644); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", "")

	entries := DiscoverMemoryFiles()

	globalIdx := -1
	projectIdx := -1
	for _, e := range entries {
		if e.Scope == "global" {
			globalIdx = e.Order
		}
		if e.Scope == "project" && strings.HasSuffix(e.Path, "CLAUDE.md") {
			projectIdx = e.Order
		}
	}
	if globalIdx < 0 || projectIdx < 0 {
		t.Skip("could not find both global and project entries")
	}
	if globalIdx >= projectIdx {
		t.Errorf("expected global (order=%d) before project (order=%d)", globalIdx, projectIdx)
	}
}

// ── resolveJSONPrompt ─────────────────────────────────────────────────────────

func TestResolveJSONPrompt_FormatsJSON(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	raw := `{"foo":"bar","num":42}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	entry := MemoryEntry{Path: path, Scope: "project"}
	rp := resolveJSONPrompt(entry)
	if rp == nil {
		t.Fatal("expected non-nil ResolvedPrompt")
	}
	// Content should be a fenced JSON block.
	if !strings.Contains(rp.Sections[0].Content, "```json") {
		t.Errorf("expected fenced JSON block, got: %s", rp.Sections[0].Content)
	}
	// Pretty-printed JSON should contain key.
	if !strings.Contains(rp.Sections[0].Content, `"foo"`) {
		t.Errorf("expected JSON key in content, got: %s", rp.Sections[0].Content)
	}
	// Token count must be positive.
	if rp.Tokens.Total <= 0 {
		t.Errorf("expected positive token count, got %d", rp.Tokens.Total)
	}
}

func TestResolveJSONPrompt_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	raw := `not valid json`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	entry := MemoryEntry{Path: path, Scope: "project"}
	rp := resolveJSONPrompt(entry)
	if rp == nil {
		t.Fatal("expected non-nil ResolvedPrompt even for invalid JSON")
	}
	// Should fall back to raw content.
	if !strings.Contains(rp.Sections[0].Content, raw) {
		t.Errorf("expected raw content fallback, got: %s", rp.Sections[0].Content)
	}
}

// ── buildCompositePrompt ──────────────────────────────────────────────────────

func TestBuildCompositePrompt_Empty(t *testing.T) {
	rp := buildCompositePrompt(nil)
	if rp != nil {
		t.Errorf("expected nil for empty entries, got %+v", rp)
	}
}

func TestBuildCompositePrompt_ConcatenatesInOrder(t *testing.T) {
	tmp := t.TempDir()
	file1 := filepath.Join(tmp, "first.md")
	file2 := filepath.Join(tmp, "second.md")
	if err := os.WriteFile(file1, []byte("# First\ncontent A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, []byte("# Second\ncontent B"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := []MemoryEntry{
		{Path: file1, Scope: "global", Order: 0},
		{Path: file2, Scope: "project", Order: 1},
	}
	rp := buildCompositePrompt(entries)
	if rp == nil {
		t.Fatal("expected non-nil composite prompt")
	}
	// Both files' content must appear in the composite.
	if !strings.Contains(rp.Expanded, "First") {
		t.Errorf("expected 'First' in composite, got: %s", rp.Expanded)
	}
	if !strings.Contains(rp.Expanded, "Second") {
		t.Errorf("expected 'Second' in composite, got: %s", rp.Expanded)
	}
	// Total tokens should sum contributions.
	if rp.Tokens.Total <= 0 {
		t.Errorf("expected positive total tokens, got %d", rp.Tokens.Total)
	}
	// Sections should have one entry per file section.
	if len(rp.Sections) < 2 {
		t.Errorf("expected at least 2 sections, got %d", len(rp.Sections))
	}
}

func TestBuildCompositePrompt_WithJSON(t *testing.T) {
	tmp := t.TempDir()
	mdFile := filepath.Join(tmp, "CLAUDE.md")
	jsonFile := filepath.Join(tmp, "settings.json")
	if err := os.WriteFile(mdFile, []byte("# Memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := map[string]string{"apiKey": "test"}
	b, _ := json.Marshal(payload)
	if err := os.WriteFile(jsonFile, b, 0o644); err != nil {
		t.Fatal(err)
	}

	entries := []MemoryEntry{
		{Path: mdFile, Scope: "global", Order: 0},
		{Path: jsonFile, Scope: "project", Order: 1},
	}
	rp := buildCompositePrompt(entries)
	if rp == nil {
		t.Fatal("expected non-nil composite prompt")
	}
	if !strings.Contains(rp.Expanded, "Memory") {
		t.Errorf("expected markdown content in composite")
	}
	if !strings.Contains(rp.Expanded, "apiKey") {
		t.Errorf("expected JSON content in composite")
	}
}

// ── MemoryModel ───────────────────────────────────────────────────────────────

func TestNewMemoryModel_DoesNotPanic(t *testing.T) {
	// InitStyles must be called first to populate tableStyles (used by
	// initMemoryStyles which is called during InitStyles).
	InitStyles()
	m := NewMemoryModel()
	// Basic sanity: model is usable.
	_ = m.View()
}

func TestMemoryModel_CompositeToggle(t *testing.T) {
	InitStyles()
	m := NewMemoryModel()
	if m.compositeMode {
		t.Error("expected compositeMode to be false initially")
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	mm := updated.(MemoryModel)
	if !mm.compositeMode {
		t.Error("expected compositeMode to be true after pressing 'c'")
	}

	updated, _ = mm.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	mm = updated.(MemoryModel)
	if mm.compositeMode {
		t.Error("expected compositeMode to be false after pressing 'c' again")
	}
}

func TestMemoryModel_QuitKeys(t *testing.T) {
	InitStyles()

	tests := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"q", tea.KeyPressMsg{Code: 'q', Text: "q"}},
		{"esc", tea.KeyPressMsg{Code: tea.KeyEscape}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMemoryModel()
			updated, cmd := m.Update(tt.key)
			mm := updated.(MemoryModel)
			if !mm.quitting {
				t.Error("expected quitting=true")
			}
			if cmd == nil {
				t.Error("expected a quit command")
			}
		})
	}
}

func TestMemoryModel_SearchMode(t *testing.T) {
	InitStyles()
	m := NewMemoryModel()

	// Enter search mode with '/'.
	updated, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	mm := updated.(MemoryModel)
	if !mm.searchMode {
		t.Error("expected searchMode=true after '/'")
	}

	// Press esc to exit search mode.
	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	mm = updated.(MemoryModel)
	if mm.searchMode {
		t.Error("expected searchMode=false after esc")
	}
}

func TestMemoryModel_NavigationClamped(t *testing.T) {
	InitStyles()
	m := NewMemoryModel()
	initial := m.cursor

	// Pressing up at position 0 should not go below 0.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	mm := updated.(MemoryModel)
	if mm.cursor < 0 {
		t.Errorf("cursor went negative: %d", mm.cursor)
	}
	_ = initial
}

func TestMemoryModel_WindowResize(t *testing.T) {
	InitStyles()
	m := NewMemoryModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	mm := updated.(MemoryModel)
	if mm.termWidth != 200 || mm.termHeight != 50 {
		t.Errorf("expected 200x50, got %dx%d", mm.termWidth, mm.termHeight)
	}
}

func TestShortenMemoryPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	tests := []struct {
		input string
		want  string
	}{
		{filepath.Join(home, ".claude", "CLAUDE.md"), "~/.claude/CLAUDE.md"},
		{"/tmp/foo/bar.md", "/tmp/foo/bar.md"},
	}
	for _, tt := range tests {
		got := shortenMemoryPath(tt.input)
		if got != tt.want {
			t.Errorf("shortenMemoryPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
