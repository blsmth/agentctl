package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ── DiscoverSkills ────────────────────────────────────────────────────────────

func TestDiscoverSkills_EmptyDirs(t *testing.T) {
	// Point to a temp dir that has no .claude/commands subdirectory.
	tmp := t.TempDir()
	// Provide a fake HOME so we don't pick up real global skills.
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	entries := DiscoverSkills(tmp)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for empty dirs, got %d", len(entries))
	}
}

func TestDiscoverSkills_GlobalOnly(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	// Create ~/.claude/commands/foo.md
	dir := filepath.Join(tmp, ".claude", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "foo.md"), "# foo\nSome content\n")
	writeFile(t, filepath.Join(dir, "bar.md"), "# bar\nOther content\n")

	entries := DiscoverSkills("")
	if len(entries) != 2 {
		t.Fatalf("expected 2 global entries, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Scope != SkillScopeGlobal {
			t.Errorf("expected global scope for %q, got %v", e.Name, e.Scope)
		}
		if e.Tokens <= 0 {
			t.Errorf("expected positive token count for %q", e.Name)
		}
	}
}

func TestDiscoverSkills_ProjectLocal(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	// Create .claude/commands/ inside a project dir (not home).
	projDir := filepath.Join(tmp, "myproject")
	cmdDir := filepath.Join(projDir, ".claude", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(cmdDir, "local-cmd.md"), "# local-cmd\nLocal only\n")

	entries := DiscoverSkills(projDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 project entry, got %d", len(entries))
	}
	if entries[0].Scope != SkillScopeProject {
		t.Errorf("expected project scope, got %v", entries[0].Scope)
	}
	if entries[0].Name != "local-cmd" {
		t.Errorf("expected name 'local-cmd', got %q", entries[0].Name)
	}
}

func TestDiscoverSkills_GlobalAndProject(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	globalDir := filepath.Join(tmp, ".claude", "commands")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	writeFile(t, filepath.Join(globalDir, "global-cmd.md"), "# global-cmd\n")

	projDir := filepath.Join(tmp, "proj")
	projCmdDir := filepath.Join(projDir, ".claude", "commands")
	if err := os.MkdirAll(projCmdDir, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	writeFile(t, filepath.Join(projCmdDir, "proj-cmd.md"), "# proj-cmd\n")

	entries := DiscoverSkills(projDir)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (1 global + 1 project), got %d", len(entries))
	}

	scopeCounts := map[SkillScope]int{}
	for _, e := range entries {
		scopeCounts[e.Scope]++
	}
	if scopeCounts[SkillScopeGlobal] != 1 {
		t.Errorf("expected 1 global skill, got %d", scopeCounts[SkillScopeGlobal])
	}
	if scopeCounts[SkillScopeProject] != 1 {
		t.Errorf("expected 1 project skill, got %d", scopeCounts[SkillScopeProject])
	}
}

func TestDiscoverSkills_IgnoresNonMD(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	dir := filepath.Join(tmp, ".claude", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "good.md"), "# good\n")
	writeFile(t, filepath.Join(dir, "ignored.txt"), "not a command\n")
	writeFile(t, filepath.Join(dir, "also.sh"), "#!/bin/bash\n")

	entries := DiscoverSkills("")
	if len(entries) != 1 || entries[0].Name != "good" {
		t.Errorf("expected only 'good' entry, got %v", entries)
	}
}

func TestDiscoverSkills_IgnoresSubdirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	dir := filepath.Join(tmp, ".claude", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "skill.md"), "# skill\n")
	// Create a subdirectory that should be ignored.
	subDir := filepath.Join(dir, "subdir.md") // .md dir
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	entries := DiscoverSkills("")
	if len(entries) != 1 {
		t.Errorf("expected 1 entry (directory should be ignored), got %d", len(entries))
	}
}

// ── SkillScope ────────────────────────────────────────────────────────────────

func TestSkillScope_String(t *testing.T) {
	tests := []struct {
		scope SkillScope
		want  string
	}{
		{SkillScopeGlobal, "global"},
		{SkillScopeProject, "project"},
		{SkillScope(99), "unknown"},
	}
	for _, tc := range tests {
		if got := tc.scope.String(); got != tc.want {
			t.Errorf("SkillScope(%d).String() = %q, want %q", tc.scope, got, tc.want)
		}
	}
}

// ── createSkillFile ───────────────────────────────────────────────────────────

func TestCreateSkillFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	path, err := createSkillFile("my-command")
	if err != nil {
		t.Fatalf("createSkillFile: %v", err)
	}
	if !strings.HasSuffix(path, "my-command.md") {
		t.Errorf("expected path to end with 'my-command.md', got %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading created file: %v", err)
	}
	if !strings.Contains(string(data), "my-command") {
		t.Errorf("created file should contain command name, got:\n%s", string(data))
	}
}

func TestCreateSkillFile_Duplicate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	if _, err := createSkillFile("duplicate-cmd"); err != nil {
		t.Fatalf("first createSkillFile: %v", err)
	}
	_, err := createSkillFile("duplicate-cmd")
	if err == nil {
		t.Error("expected error for duplicate command name, got nil")
	}
}

// ── isValidSkillNameChar ──────────────────────────────────────────────────────

func TestIsValidSkillNameChar(t *testing.T) {
	valid := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"
	for _, r := range valid {
		if !isValidSkillNameChar(r) {
			t.Errorf("expected %q to be valid", r)
		}
	}
	invalid := " !@#$%^&*()+=[]{}|;':\",./<>?\\"
	for _, r := range invalid {
		if isValidSkillNameChar(r) {
			t.Errorf("expected %q to be invalid", r)
		}
	}
}

// ── NewSkillsModel ────────────────────────────────────────────────────────────

func TestNewSkillsModel_EmptyState(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	m := NewSkillsModel(tmp)
	if len(m.allEntries) != 0 {
		t.Errorf("expected 0 entries for empty dirs, got %d", len(m.allEntries))
	}
}

func TestNewSkillsModel_WithEntries(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	dir := filepath.Join(tmp, ".claude", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "alpha.md"), "# alpha\n")
	writeFile(t, filepath.Join(dir, "beta.md"), "# beta\n")

	m := NewSkillsModel("")
	if len(m.allEntries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(m.allEntries))
	}
}

// ── SkillsModel navigation ────────────────────────────────────────────────────

func TestSkillsModel_Navigation(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	dir := filepath.Join(tmp, ".claude", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < 3; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("cmd%d.md", i)), fmt.Sprintf("# cmd%d\n", i))
	}

	m := NewSkillsModel("")
	if m.cursor != 0 {
		t.Errorf("initial cursor should be 0, got %d", m.cursor)
	}

	// Move down.
	nm, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = nm.(SkillsModel)
	if m.cursor != 1 {
		t.Errorf("after j, cursor should be 1, got %d", m.cursor)
	}

	// Move down again.
	nm, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = nm.(SkillsModel)
	if m.cursor != 2 {
		t.Errorf("after j, cursor should be 2, got %d", m.cursor)
	}

	// Can't go past the end.
	nm, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = nm.(SkillsModel)
	if m.cursor != 2 {
		t.Errorf("cursor should stay at 2, got %d", m.cursor)
	}

	// Move up.
	nm, _ = m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	m = nm.(SkillsModel)
	if m.cursor != 1 {
		t.Errorf("after k, cursor should be 1, got %d", m.cursor)
	}
}

func TestSkillsModel_NavigationAtZero(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	dir := filepath.Join(tmp, ".claude", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "cmd.md"), "# cmd\n")

	m := NewSkillsModel("")
	// Try moving up from 0 — should stay at 0.
	nm, _ := m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	m = nm.(SkillsModel)
	if m.cursor != 0 {
		t.Errorf("cursor should stay at 0, got %d", m.cursor)
	}
}

// ── SkillsModel filter ────────────────────────────────────────────────────────

func TestSkillsModel_Filter(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	dir := filepath.Join(tmp, ".claude", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "refactor.md"), "# refactor\n")
	writeFile(t, filepath.Join(dir, "review.md"), "# review\n")
	writeFile(t, filepath.Join(dir, "deploy.md"), "# deploy\n")

	m := NewSkillsModel("")
	if len(m.entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(m.entries))
	}

	// Activate search with '/'.
	nm, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = nm.(SkillsModel)
	if !m.searchMode {
		t.Error("expected search mode to be active after '/'")
	}

	// Type 're' to filter.
	for _, ch := range "re" {
		nm, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
		m = nm.(SkillsModel)
	}

	// Confirm with enter.
	nm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = nm.(SkillsModel)

	if m.searchMode {
		t.Error("expected search mode to be inactive after enter")
	}
	if len(m.entries) != 2 {
		// Should match refactor + review.
		t.Errorf("expected 2 entries after filtering 're', got %d: %v",
			len(m.entries), skillNames(m.entries))
	}

	// Escape clears filter.
	nm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = nm.(SkillsModel)
	if len(m.entries) != 3 {
		t.Errorf("expected 3 entries after esc, got %d", len(m.entries))
	}
}

// ── SkillsModel quit ──────────────────────────────────────────────────────────

func TestSkillsModel_QuitOnQ(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	m := NewSkillsModel("")
	nm, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	m = nm.(SkillsModel)
	if !m.quitting {
		t.Error("expected quitting=true after q")
	}
	if cmd == nil {
		t.Error("expected tea.Quit cmd after q")
	}
}

func TestSkillsModel_QuitOnCtrlC(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	m := NewSkillsModel("")
	nm, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl, Text: "ctrl+c"})
	m = nm.(SkillsModel)
	if !m.quitting {
		t.Error("expected quitting=true after ctrl+c")
	}
	if cmd == nil {
		t.Error("expected tea.Quit cmd after ctrl+c")
	}
}

// ── SkillsModel View ─────────────────────────────────────────────────────────

func TestSkillsModel_ViewEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	m := NewSkillsModel(tmp)
	m.termWidth = 120
	m.termHeight = 40
	m.splitPane.SetSize(120, 30)

	view := m.buildView()
	if view == "" {
		t.Error("expected non-empty View() for empty state")
	}
	plain := stripANSI(view)
	if !strings.Contains(plain, "No commands") {
		t.Errorf("empty state view should mention 'No commands', got:\n%s", plain)
	}
}

func TestSkillsModel_ViewWithEntries(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	dir := filepath.Join(tmp, ".claude", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "my-skill.md"), "# My Skill\n\nDoes something.\n")

	m := NewSkillsModel("")
	m.termWidth = 120
	m.termHeight = 40
	m.splitPane.SetSize(120, 30)
	m.preview.SetSize(114, 10)

	view := m.buildView()
	plain := stripANSI(view)
	if !strings.Contains(plain, "my-skill") {
		t.Errorf("expected 'my-skill' in view, got:\n%s", plain)
	}
	if !strings.Contains(plain, "global") {
		t.Errorf("expected scope 'global' in view, got:\n%s", plain)
	}
}

func TestSkillsModel_ViewProjectScope(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	// Create a project command (no global commands).
	projDir := filepath.Join(tmp, "proj")
	cmdDir := filepath.Join(projDir, ".claude", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(cmdDir, "proj-skill.md"), "# Proj Skill\n")

	m := NewSkillsModel(projDir)
	m.termWidth = 120
	m.termHeight = 40
	m.splitPane.SetSize(120, 30)

	view := m.buildView()
	plain := stripANSI(view)
	if !strings.Contains(plain, "proj-skill") {
		t.Errorf("expected 'proj-skill' in view, got:\n%s", plain)
	}
	if !strings.Contains(plain, "project") {
		t.Errorf("expected scope 'project' in view, got:\n%s", plain)
	}
}

// ── SkillsModel hint bar ──────────────────────────────────────────────────────

func TestSkillsModel_HintBarShown(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	m := NewSkillsModel(tmp)
	m.termWidth = 120
	m.termHeight = 40
	m.splitPane.SetSize(120, 30)

	view := m.buildView()
	plain := stripANSI(view)
	for _, key := range []string{"j/k", "n", "e", "q"} {
		if !strings.Contains(plain, key) {
			t.Errorf("hint bar missing key %q in:\n%s", key, plain)
		}
	}
}

// ── WindowSizeMsg ─────────────────────────────────────────────────────────────

func TestSkillsModel_WindowSizeMsg(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	m := NewSkillsModel(tmp)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = nm.(SkillsModel)
	if m.termWidth != 100 || m.termHeight != 30 {
		t.Errorf("expected termWidth=100 termHeight=30, got %d/%d", m.termWidth, m.termHeight)
	}
}

// ── EditTarget ────────────────────────────────────────────────────────────────

func TestSkillsModel_EditTargetEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	m := NewSkillsModel(tmp)
	if m.EditTarget() != "" {
		t.Errorf("expected empty EditTarget, got %q", m.EditTarget())
	}
}

func TestSkillsModel_EditTargetSetOnE(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	dir := filepath.Join(tmp, ".claude", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "editable.md"), "# editable\n")

	m := NewSkillsModel("")
	nm, _ := m.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	m = nm.(SkillsModel)
	if m.EditTarget() == "" {
		t.Error("expected EditTarget to be set after pressing 'e'")
	}
	if !strings.HasSuffix(m.EditTarget(), "editable.md") {
		t.Errorf("EditTarget should end with 'editable.md', got %q", m.EditTarget())
	}
}

// ── Test helpers ──────────────────────────────────────────────────────────────

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile %q: %v", path, err)
	}
}

func skillNames(entries []SkillEntry) []string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return names
}
