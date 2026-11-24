package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/closedlog"
)

func stubRecent(t *testing.T, dirs ...string) {
	t.Helper()
	orig := fleetRecentLedger
	t.Cleanup(func() { fleetRecentLedger = orig })
	fleetRecentLedger = func(time.Time) []closedlog.Record {
		recs := make([]closedlog.Record, len(dirs))
		for i, d := range dirs {
			recs[i] = closedlog.Record{ProjectDir: d}
		}
		return recs
	}
}

func mkdirs(t *testing.T, names ...string) []string {
	t.Helper()
	root := t.TempDir()
	var out []string
	for _, n := range names {
		p := filepath.Join(root, n)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// openWithRecent opens the prompt and delivers the recent-dirs load.
func openWithRecent(t *testing.T, m FleetModel) (FleetModel, *newSessionCalls) {
	t.Helper()
	m, cmd := hubKey(t, m, fleetNewSessionKey)
	if cmd == nil {
		t.Fatal("opening the prompt returned no load command")
	}
	next, _ := m.Update(cmd())
	return nsSettled(next.(FleetModel)), nil
}

func TestLoadRecentDirsFilters(t *testing.T) {
	ds := mkdirs(t, "a", "b", "c", "repo/.worktrees/w1")
	link := filepath.Join(t.TempDir(), "a-link")
	if err := os.Symlink(ds[0], link); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "gone")
	recs := []closedlog.Record{
		{ProjectDir: ds[1]}, {ProjectDir: link}, {ProjectDir: ds[3]}, {ProjectDir: missing},
		{ProjectDir: ds[0]}, {ProjectDir: ds[1]}, {ProjectDir: ""},
	}
	got := loadRecentDirs(ds[2], recs)
	want := []string{ds[2], ds[1], link}
	if !slices.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestRecentExcludesLiveGroups(t *testing.T) {
	home := t.TempDir()
	ds := mkdirs(t, "live", "idle")
	link := filepath.Join(t.TempDir(), "live-link")
	if err := os.Symlink(ds[0], link); err != nil {
		t.Fatal(err)
	}
	stubNewSession(t, "", home)
	stubRecent(t, link, ds[1])
	m := foldModel(t)
	m.groups[0].ProjectDir = ds[0]
	m, _ = openWithRecent(t, m)
	if got := m.recentEntries(); !slices.Equal(got, []string{ds[1]}) {
		t.Errorf("entries = %v, want only %s", got, ds[1])
	}
}

func TestRecentShowsTildeAndCapsAtEight(t *testing.T) {
	home := t.TempDir()
	names := []string{}
	for i := 0; i < 12; i++ {
		names = append(names, filepath.Join("p", string(rune('a'+i))))
	}
	var ds []string
	for _, n := range names {
		p := filepath.Join(home, n)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		ds = append(ds, p)
	}
	stubNewSession(t, "", home)
	stubRecent(t, ds...)
	m, _ := openWithRecent(t, foldModel(t))
	if n := len(m.recentEntries()); n != fleetRecentMax {
		t.Fatalf("%d entries, want %d", n, fleetRecentMax)
	}
	out := ansi.Strip(m.render())
	if !strings.Contains(out, "~/p/a") || strings.Contains(out, home) {
		t.Errorf("entries not ~-shortened:\n%s", out)
	}
	if n := len(m.nsPanelLines()); n != 2+1+fleetRecentMax {
		t.Errorf("panel rows = %d", n)
	}
}

func TestRecentNoEntriesLooksLikeToday(t *testing.T) {
	home := t.TempDir()
	stubNewSession(t, "", home)
	stubRecent(t)
	m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
	if n := len(m.nsPanelLines()); n != 3 {
		t.Errorf("panel has %d rows with no recent dirs, want border+input+border", n)
	}
	for _, h := range m.newSessionHints() {
		if h.key == "↑↓" {
			t.Error("recent hint shown with no entries")
		}
	}
}

func TestRecentKeyboardSelection(t *testing.T) {
	home := t.TempDir()
	ds := mkdirs(t, "alpha", "beta", "gamma")
	calls := stubNewSession(t, "", home)
	stubRecent(t, ds...)
	m, _ := openWithRecent(t, foldModel(t))
	if got := m.recentEntries(); len(got) != 3 {
		t.Fatalf("entries %v", got)
	}
	m, _ = hubKey(t, m, "down")
	m, _ = hubKey(t, m, "down")
	if m.dirPrompt.hl != 1 {
		t.Fatalf("hl = %d, want 1", m.dirPrompt.hl)
	}
	m, _ = hubKey(t, m, "up")
	m, _ = hubKey(t, m, "up")
	m, _ = hubKey(t, m, "up")
	if m.dirPrompt.hl != -1 {
		t.Fatalf("hl = %d after up past the top", m.dirPrompt.hl)
	}
	m, _ = hubKey(t, m, "down")
	m, _ = hubKey(t, m, "down")
	m, cmd := hubKey(t, m, "enter")
	if cmd == nil {
		t.Fatal("no command")
	}
	cmd()
	if !slices.Equal(calls.dirs, []string{ds[1]}) {
		t.Errorf("helper dirs = %v, want %v", calls.dirs, ds[1])
	}
}

func TestRecentTypedTextWinsWithoutHighlight(t *testing.T) {
	home := t.TempDir()
	ds := mkdirs(t, "alpha", "beta")
	typed := mkdirs(t, "typed")[0]
	calls := stubNewSession(t, "", home)
	stubRecent(t, ds...)
	m, _ := openWithRecent(t, foldModel(t))
	m = typeDir(t, m, typed)
	_, cmd := hubKey(t, m, "enter")
	cmd()
	if !slices.Equal(calls.dirs, []string{typed}) {
		t.Errorf("helper dirs = %v, want typed %s", calls.dirs, typed)
	}
}

func TestRecentTypingFilters(t *testing.T) {
	home := t.TempDir()
	ds := mkdirs(t, "alpha", "beta", "gamma")
	stubNewSession(t, "", home)
	stubRecent(t, ds...)
	m, _ := openWithRecent(t, foldModel(t))
	m, _ = hubKey(t, m, "down")
	m = typeDir(t, m, "bet")
	if got := m.recentEntries(); !slices.Equal(got, []string{ds[1]}) {
		t.Errorf("entries after typing = %v", got)
	}
	if m.dirPrompt.hl != -1 {
		t.Errorf("highlight survived typing: %d", m.dirPrompt.hl)
	}
	m = typeDir(t, m, "zzz")
	if len(m.recentEntries()) != 0 {
		t.Error("non-matching text still lists entries")
	}
}

func TestRecentClickOpens(t *testing.T) {
	home := t.TempDir()
	ds := mkdirs(t, "alpha", "beta")
	calls := stubNewSession(t, "", home)
	stubRecent(t, ds...)
	m, _ := openWithRecent(t, foldModel(t))
	x, y, _, _ := m.nsPanelRect()
	x += 3
	y++ // the input row
	// The input itself keeps the dropdown open and opens nothing.
	next, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	if cmd != nil || !next.(FleetModel).newSessionOpen() {
		t.Fatal("click on the input changed the prompt")
	}
	next, cmd = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y + 2})
	if cmd == nil {
		t.Fatal("click on an entry returned no command")
	}
	cmd()
	if !slices.Equal(calls.dirs, []string{ds[1]}) {
		t.Errorf("helper dirs = %v, want %s", calls.dirs, ds[1])
	}
	if next.(FleetModel).newSessionOpen() {
		t.Error("prompt still open")
	}
}

func TestTildePath(t *testing.T) {
	for _, c := range []struct{ in, home, want string }{
		{"/h/x/y", "/h", "~/x/y"}, {"/h", "/h", "~"}, {"/hh/x", "/h", "/hh/x"}, {"/a", "", "/a"},
	} {
		if got := tildePath(c.in, c.home); got != c.want {
			t.Errorf("tildePath(%q,%q) = %q, want %q", c.in, c.home, got, c.want)
		}
	}
}

func TestRecentIncludesHubOrigin(t *testing.T) {
	home := t.TempDir()
	ds := mkdirs(t, "origin", "other")
	stubNewSession(t, ds[0], home)
	stubRecent(t, ds[1], ds[0])
	m, _ := openWithRecent(t, foldModel(t))
	if got := m.recentEntries(); !slices.Equal(got, ds) {
		t.Errorf("entries = %v, want %v", got, ds)
	}
}
