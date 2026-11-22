package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/tmux"
)

// benchConfig writes realistic (multi-KB) global and project config files so
// a config read costs what it does on a real machine.
func benchConfig(b *testing.B) {
	b.Helper()
	dir := b.TempDir()
	var agents []string
	for i := 0; i < 40; i++ {
		agents = append(agents, fmt.Sprintf(`"agent-%d": {"model": "opus", "dir": "/tmp/x/%d", "permission_mode": "auto"}`, i, i))
	}
	body := `{"theme": "catppuccin-mocha", "agents": {` + strings.Join(agents, ",") + `}}`
	global := filepath.Join(dir, "global.json")
	project := filepath.Join(dir, "project.json")
	for _, p := range []string{global, project} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	config.SetConfigPathForTest(project)
	config.SetGlobalConfigPathForTest(global)
}

// BenchmarkChromeBarView is one frame. Bubble Tea calls View after every
// message, so this runs on each spinner, prefix and reload tick.
func BenchmarkChromeBarView(b *testing.B) {
	benchConfig(b)
	m := newChromeBarModel("@1", "", nil, func() bool { return false })
	m.width = 200
	for i := 0; i < 20; i++ {
		m.tabs = append(m.tabs, TabInfo{Name: fmt.Sprintf("agent-%d", i), State: "idle", WindowID: fmt.Sprintf("@%d", i)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}

// BenchmarkChromeBarRebuild is one rebuild after a windowstate write, in a
// windows dir holding a few live records and many records of closed windows.
func BenchmarkChromeBarRebuild(b *testing.B) {
	benchConfig(b)
	dir := b.TempDir()
	wdir := filepath.Join(dir, "windows")
	if err := os.MkdirAll(wdir, 0o700); err != nil {
		b.Fatal(err)
	}
	var wins []tmux.WindowInfo
	pad := strings.Repeat("x", 2000)
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("@%d", i)
		if i < 20 {
			wins = append(wins, tmux.WindowInfo{ID: id, Name: fmt.Sprintf("agent-%d", i)})
		}
		body := fmt.Sprintf(`{"state":"idle","instance":"agent-%d","last_text":%q}`, i, pad)
		if err := os.WriteFile(filepath.Join(wdir, id+".json"), []byte(body), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "active-window"), []byte("@1\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	m := newChromeBarModel("@1", dir, func() ([]tmux.WindowInfo, error) { return wins, nil }, func() bool { return false })
	m.rebuild(true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.rebuild(false)
	}
}
