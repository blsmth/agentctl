package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	ctxstore "github.com/blsmth/agentctl/internal/context"
)

func newContextTestModel(t *testing.T, entries map[string]ctxstore.Entry) ContextModel {
	t.Helper()
	ctxstore.SetPathForTest(t.TempDir())
	for k, e := range entries {
		if err := ctxstore.Set(k, e); err != nil {
			t.Fatal(err)
		}
	}
	m := NewContextModel()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return next.(ContextModel)
}

func ctxKey(m ContextModel, k string) ContextModel {
	next, _ := m.Update(tea.KeyPressMsg{Text: k, Code: []rune(k)[0]})
	return next.(ContextModel)
}

func ctxKeys(m ContextModel, s string) ContextModel {
	for _, r := range s {
		m = ctxKey(m, string(r))
	}
	return m
}

func TestContextModel_RendersColumnsAndSortsNewestFirst(t *testing.T) {
	m := newContextTestModel(t, map[string]ctxstore.Entry{
		"older": {Content: "old body", Agent: "go-engineer", Tags: []string{"spec"}},
		"newer": {Content: "new body"},
	})
	// Make "older" older on disk.
	old := time.Now().Add(-3 * 24 * time.Hour)
	if err := chtimesContext("older", old); err != nil {
		t.Fatal(err)
	}
	m.reload()
	if m.entries[0].Key != "newer" {
		t.Fatalf("want newest first, got %q", m.entries[0].Key)
	}
	out := m.buildView()
	for _, want := range []string{"KEY", "AGENT", "TAGS", "SIZE", "UPDATED", "go-engineer", "spec", "—", "3d", "tok"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}

func TestContextModel_EmptyState(t *testing.T) {
	m := newContextTestModel(t, nil)
	out := m.buildView()
	if !strings.Contains(out, "context store is empty") || !strings.Contains(out, "context set") {
		t.Errorf("want friendly empty state, got:\n%s", out)
	}
}

func TestContextModel_MissingStoreDir(t *testing.T) {
	ctxstore.SetPathForTest(t.TempDir() + "/does/not/exist")
	m := NewContextModel()
	if m.loadErr != nil {
		t.Fatalf("missing dir must not error: %v", m.loadErr)
	}
	if !strings.Contains(m.buildView(), "empty") {
		t.Error("want empty state")
	}
}

func TestContextModel_Filter(t *testing.T) {
	m := newContextTestModel(t, map[string]ctxstore.Entry{
		"alpha": {Content: "a", Agent: "reviewer"},
		"beta":  {Content: "b", Tags: []string{"urgent"}},
		"gamma": {Content: "c"},
	})
	tests := []struct {
		query string
		want  []string
	}{
		{"alp", []string{"alpha"}},
		{"review", []string{"alpha"}},
		{"urg", []string{"beta"}},
		{"zzz", nil},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			m := m
			m.searchQuery = tt.query
			m.applyFilter()
			var got []string
			for _, e := range m.entries {
				got = append(got, e.Key)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got %v want %v", got, tt.want)
			}
		})
	}

	m = ctxKey(m, "/")
	m = ctxKeys(m, "urg")
	if len(m.entries) != 1 {
		t.Fatalf("live filter: got %d entries", len(m.entries))
	}
	if !strings.Contains(m.buildView(), "beta") {
		t.Error("filtered view missing beta")
	}
}

func TestContextModel_DeleteConfirm(t *testing.T) {
	m := newContextTestModel(t, map[string]ctxstore.Entry{"only": {Content: "x"}})

	// n cancels.
	m = ctxKey(m, "d")
	if m.confirmKey != "only" || !strings.Contains(m.buildView(), "Delete \"only\"? y/n") {
		t.Fatalf("expected confirm prompt, got %q", m.confirmKey)
	}
	m = ctxKey(m, "n")
	if _, ok, _ := ctxstore.Get("only"); !ok {
		t.Fatal("entry deleted despite cancel")
	}

	// y deletes.
	m = ctxKey(ctxKey(m, "d"), "y")
	if _, ok, _ := ctxstore.Get("only"); ok {
		t.Fatal("entry still present after y")
	}
	if len(m.entries) != 0 {
		t.Errorf("want empty list, got %d", len(m.entries))
	}
}

func TestContextModel_DeletedByOtherProcess(t *testing.T) {
	m := newContextTestModel(t, map[string]ctxstore.Entry{
		"a": {Content: "x"},
		"b": {Content: "y"},
	})
	key := m.selectedKey()
	m = ctxKey(m, "d")
	if err := ctxstore.Delete(key); err != nil { // another process wins the race
		t.Fatal(err)
	}
	m = ctxKey(m, "y")
	if !strings.Contains(m.toast, "already deleted") {
		t.Errorf("want toast, got %q", m.toast)
	}
	if len(m.entries) != 1 {
		t.Errorf("want reload to 1 entry, got %d", len(m.entries))
	}

	// Gone before preview (navigation).
	m = newContextTestModel(t, map[string]ctxstore.Entry{"a": {Content: "x"}, "b": {Content: "y"}})
	other := m.entries[1].Key
	if err := ctxstore.Delete(other); err != nil {
		t.Fatal(err)
	}
	m = ctxKey(m, "j")
	if !strings.Contains(m.toast, "no longer exists") || len(m.entries) != 1 {
		t.Errorf("toast=%q entries=%d", m.toast, len(m.entries))
	}
}

func TestContextModel_ReloadPicksUpNewEntries(t *testing.T) {
	m := newContextTestModel(t, nil)
	if err := ctxstore.Set("fresh", ctxstore.Entry{Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	m = ctxKey(m, "r")
	if len(m.entries) != 1 {
		t.Fatalf("want 1 entry after reload, got %d", len(m.entries))
	}
}

func TestContextModel_LongContentStaysInLayout(t *testing.T) {
	long := strings.Repeat("word ", 5000) + "\x1b[31mred\x1b[0m\n" + strings.Repeat("x", 3000)
	m := newContextTestModel(t, map[string]ctxstore.Entry{"big": {Content: long, Agent: "a"}})
	out := m.buildView()
	if strings.Contains(out, "\x1b[31mred") {
		t.Error("stored escape sequence leaked into the view")
	}
	lines := strings.Split(out, "\n")
	if len(lines) > m.termHeight {
		t.Errorf("view has %d lines, terminal %d", len(lines), m.termHeight)
	}
	for _, sz := range [][2]int{{30, 12}, {20, 8}, {200, 60}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		_ = next.(ContextModel).buildView() // must not panic
	}
}

func TestContextModel_Quit(t *testing.T) {
	m := newContextTestModel(t, nil)
	for _, k := range []tea.KeyPressMsg{{Text: "q", Code: 'q'}, {Code: tea.KeyEscape}} {
		next, cmd := m.Update(k)
		if !next.(ContextModel).Quitting() || cmd == nil {
			t.Errorf("%v should quit", k)
		}
	}
}

func chtimesContext(key string, ts time.Time) error {
	return osChtimes(ctxstore.Dir()+"/"+key+".md", ts)
}

var osChtimes = func(p string, ts time.Time) error { return os.Chtimes(p, ts, ts) }
