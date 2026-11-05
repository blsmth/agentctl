package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/llm"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/windowstate"
)

type fakeCompleter struct {
	out   string
	err   error
	calls int
}

func (f *fakeCompleter) Complete(context.Context, string) (string, error) {
	f.calls++
	return f.out, f.err
}

// journalFixture sets up a temp HOME, config with an ollama provider, a window
// with a session log, and a worktree-style runtime dir. It returns home and the
// project root that paths.ProjectDir() must resolve to.
func journalFixture(t *testing.T, provider string) (home, project string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	project = filepath.Join(root, "proj")
	worktree := filepath.Join(project, ".worktrees", "agent-foo-issue-7")
	for _, d := range []string{home, worktree} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	// Dispatched agent: cwd is the worktree, runtime dir belongs to the project.
	t.Setenv("AGENTCTL_RUNTIME_DIR", filepath.Join(project, ".agentctl"))
	paths.ResetForTest()
	paths.Init(worktree)
	t.Cleanup(paths.ResetForTest)
	t.Chdir(worktree)

	cfgPath := filepath.Join(root, "config.json")
	body := `{"llm":{"provider":"` + provider + `","ollama":{"model":"m"}}}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// llm.* is only honored from the global layer.
	config.SetConfigPathForTest(cfgPath)
	config.SetGlobalConfigPathForTest(cfgPath)

	winDir := filepath.Join(root, "windows")
	if err := os.MkdirAll(winDir, 0o755); err != nil {
		t.Fatal(err)
	}
	windowstate.SetDirForTest(winDir)

	logPath := filepath.Join(root, "sess1.jsonl")
	line := `{"type":"assistant","message":{"model":"claude-x","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n"
	if err := os.WriteFile(logPath, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := windowstate.Write("@1", windowstate.WindowState{SessionID: "sess1", LogPath: logPath, Ticket: "7"}); err != nil {
		t.Fatal(err)
	}
	return home, project
}

func useCompleter(t *testing.T, c llm.Completer) {
	t.Helper()
	orig := newCompleterFunc
	newCompleterFunc = func(config.LLMConfig) (llm.Completer, error) { return c, nil }
	t.Cleanup(func() { newCompleterFunc = orig })
}

func TestGenerateWorkJournal_WritesMarkdownAndSidecar(t *testing.T) {
	home, project := journalFixture(t, "ollama")
	useCompleter(t, &fakeCompleter{out: "  - Did the thing\n"})

	if err := generateWorkJournal("foo", "sess1", "@1"); err != nil {
		t.Fatalf("generateWorkJournal: %v", err)
	}

	dir := filepath.Join(home, ".local", "share", "agentctl", "journal")
	md, err := os.ReadFile(filepath.Join(dir, "foo.md"))
	if err != nil || !strings.Contains(string(md), "- Did the thing") {
		t.Fatalf("md = %q, err = %v", md, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "entries.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 jsonl line, got %d", len(lines))
	}
	var rec journalRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	// Project must be the parent project, not the worktree cwd.
	if rec.Project != project {
		t.Errorf("project = %q, want %q (cwd-derived would be the worktree)", rec.Project, project)
	}
	if cwd, _ := os.Getwd(); strings.HasPrefix(rec.Project, cwd) {
		t.Errorf("project %q derived from cwd %q", rec.Project, cwd)
	}
	if rec.Agent != "foo" || rec.SessionID != "sess1" || rec.Ticket != "7" ||
		rec.Turns != 1 || rec.Model != "claude-x" || rec.Summary != "- Did the thing" || rec.Timestamp == "" {
		t.Errorf("unexpected record: %+v", rec)
	}
}

func TestGenerateWorkJournal_FailedCompletionWritesNothing(t *testing.T) {
	tests := []struct {
		name string
		c    *fakeCompleter
	}{
		{"error", &fakeCompleter{err: errors.New("boom")}},
		{"empty", &fakeCompleter{out: ""}},
		{"whitespace", &fakeCompleter{out: " \n\t "}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := journalFixture(t, "ollama")
			useCompleter(t, tc.c)

			if err := generateWorkJournal("foo", "sess1", "@1"); err == nil {
				t.Fatal("expected error")
			}
			if _, err := os.Stat(filepath.Join(home, ".local", "share", "agentctl", "journal")); !os.IsNotExist(err) {
				t.Errorf("journal dir must not exist after failure; stat err = %v", err)
			}
		})
	}
}

func TestGenerateWorkJournal_DisabledWithoutProvider(t *testing.T) {
	home, _ := journalFixture(t, "")
	f := &fakeCompleter{out: "x"}
	useCompleter(t, f)

	if err := generateWorkJournal("foo", "sess1", "@1"); err != nil {
		t.Fatalf("disabled journaling must not error: %v", err)
	}
	if f.calls != 0 {
		t.Errorf("completer called %d times while disabled", f.calls)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "agentctl", "journal")); !os.IsNotExist(err) {
		t.Errorf("journal dir must not exist; stat err = %v", err)
	}
}

func TestWriteJournalEntry_RejectsBlankSummary(t *testing.T) {
	home := t.TempDir()
	for _, s := range []string{"", "  \n\t"} {
		if err := writeJournalEntry(home, "a", "s", "p", "", &sessions.Summary{}, s); err == nil {
			t.Errorf("summary %q: expected error", s)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(err) {
		t.Errorf("nothing should be written; stat err = %v", err)
	}
}

func TestJournalTimeout(t *testing.T) {
	cases := map[string]string{"": "30s", "bogus": "30s", "-5s": "30s", "12s": "12s"}
	for in, want := range cases {
		var c config.LLMConfig
		c.Ollama.Timeout = in
		if got := journalTimeout(c).String(); got != want {
			t.Errorf("journalTimeout(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestJournalDir_XDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	if got, want := journalDir(home), filepath.Join(home, ".local", "share", "agentctl", "journal"); got != want {
		t.Errorf("default journalDir = %q, want %q", got, want)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	if got, want := journalDir(home), filepath.Join(xdg, "agentctl", "journal"); got != want {
		t.Errorf("xdg journalDir = %q, want %q", got, want)
	}
}

// TestJournalDir_MigratesLegacy verifies a journal in the old config-dir
// location is moved to the data dir once, and an existing data-dir journal wins.
func TestJournalDir_MigratesLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	legacy := filepath.Join(home, ".config", "agentctl", "journal")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "a.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "share", "agentctl", "journal")
	if got := journalDir(home); got != want {
		t.Errorf("journalDir = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(want, "a.md")); err != nil {
		t.Errorf("journal not migrated: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy journal should be gone, err=%v", err)
	}
}
