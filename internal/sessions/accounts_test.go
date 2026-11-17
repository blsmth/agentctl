package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

// twoAccountDirs configures two accounts in a temp global config and returns
// their config dirs.
func twoAccountDirs(t *testing.T) (string, string) {
	t.Helper()
	tmp := t.TempDir()
	a, b := filepath.Join(tmp, "a"), filepath.Join(tmp, "b")
	cfgPath := filepath.Join(tmp, "config.json")
	body := fmt.Sprintf(`{"accounts":{"a":{"config_dir":%q},"b":{"config_dir":%q}},"default_account":"a"}`, a, b)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config.SetConfigPathForTest(cfgPath)
	t.Cleanup(func() { config.SetConfigPathForTest("") })
	t.Setenv("CLAUDE_CONFIG_DIR", a)
	SetClaudeDirForTest("")
	return a, b
}

func writeLog(t *testing.T, configDir, slug, sid, agent string) string {
	t.Helper()
	dir := filepath.Join(configDir, "projects", slug)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sid+".jsonl")
	line := fmt.Sprintf(`{"type":"user","agentSetting":%q,"timestamp":"2026-01-01T00:00:00Z"}`+"\n", agent)
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSessionsSearchEveryAccount(t *testing.T) {
	a, b := twoAccountDirs(t)
	pa := writeLog(t, a, "-w", "sid-a", "eng")
	pb := writeLog(t, b, "-w", "sid-b", "eng")

	if got := FindLogBySessionID("sid-a"); got != pa {
		t.Errorf("FindLogBySessionID(sid-a) = %q, want %q", got, pa)
	}
	if got := FindLogBySessionID("sid-b"); got != pb {
		t.Errorf("FindLogBySessionID(sid-b) = %q, want %q", got, pb)
	}
	if roots := ProjectsRoots(); len(roots) != 2 {
		t.Errorf("ProjectsRoots = %v, want both accounts", roots)
	}
	ss, err := FindSessionsForProject("/w", 0)
	if err != nil || len(ss) != 2 {
		t.Errorf("FindSessionsForProject = %d sessions, %v; want 2", len(ss), err)
	}
}
