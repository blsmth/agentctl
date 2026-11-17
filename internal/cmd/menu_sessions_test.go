package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/sessions"
)

func TestMenuSessions(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	typed := write("typed.jsonl", `{"type":"user","message":{"role":"user","content":"fix the flaky test"}}`+"\n")
	empty := write("empty.jsonl", `{"type":"assistant","message":{"content":"hi"}}`+"\n")
	now := time.Now()
	all := []sessions.Session{
		{ID: "older", EndTime: now.Add(-12 * time.Minute), LogFile: empty},
		{ID: "live", EndTime: now.Add(-time.Minute), LogFile: typed},
		{ID: "newer", StartTime: now.Add(-11 * time.Minute), LogFile: typed},
	}
	got := menuSessions(all, map[string]bool{"live": true}, func(s sessions.Session) string { return "named-" + s.ID })
	if len(got) != 2 || got[0].ID != "newer" || got[1].ID != "older" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Preview != "fix the flaky test" {
		t.Errorf("preview = %q", got[0].Preview)
	}
	if got[1].Preview != "named-older" {
		t.Errorf("fallback preview = %q", got[1].Preview)
	}
}
