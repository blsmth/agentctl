package registry

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// isolate points both $HOME and the package's directory override at a fresh
// temp dir, so no test can touch the developer's real ~/.config/agentctl.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	dir := filepath.Join(home, ".config", "agentctl", "registry")
	SetDirForTest(dir)
	t.Cleanup(func() { SetDirForTest("") })
	return dir
}

func TestRegisterAndList(t *testing.T) {
	isolate(t)

	e := Entry{
		SessionName: "agentctl-myproject",
		ProjectDir:  "/tmp/myproject",
		RuntimeDir:  "/tmp/myproject/.agentctl",
		Host:        "local",
		PID:         1234,
		StartedAt:   1000,
		LastSeen:    1000,
	}
	if err := Register(e); err != nil {
		t.Fatalf("Register: %v", err)
	}

	entries, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List returned %d entries, want 1", len(entries))
	}
	if entries[0] != e {
		t.Errorf("List returned %+v, want %+v", entries[0], e)
	}
}

func TestRegisterOverwritesOnReRegister(t *testing.T) {
	isolate(t)

	e1 := Entry{SessionName: "agentctl-foo", PID: 1, LastSeen: 1000}
	e2 := Entry{SessionName: "agentctl-foo", PID: 2, LastSeen: 2000}

	if err := Register(e1); err != nil {
		t.Fatalf("Register e1: %v", err)
	}
	if err := Register(e2); err != nil {
		t.Fatalf("Register e2: %v", err)
	}

	entries, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List returned %d entries, want 1 (overwrite, not append)", len(entries))
	}
	if entries[0].PID != 2 {
		t.Errorf("entry PID = %d, want 2 (latest write should win)", entries[0].PID)
	}
}

func TestDeregister(t *testing.T) {
	isolate(t)

	e := Entry{SessionName: "agentctl-bar", LastSeen: 1000}
	if err := Register(e); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := Deregister("agentctl-bar"); err != nil {
		t.Fatalf("Deregister present: %v", err)
	}

	entries, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("List returned %d entries after deregister, want 0", len(entries))
	}

	// Deregistering an absent session must be a no-op, not an error.
	if err := Deregister("agentctl-never-existed"); err != nil {
		t.Errorf("Deregister absent: %v", err)
	}
}

func TestHeartbeatUpdatesLastSeenOnly(t *testing.T) {
	isolate(t)

	e := Entry{
		SessionName: "agentctl-baz",
		ProjectDir:  "/tmp/baz",
		RuntimeDir:  "/tmp/baz/.agentctl",
		Host:        "local",
		PID:         42,
		StartedAt:   1000,
		LastSeen:    1000,
	}
	if err := Register(e); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := Heartbeat("agentctl-baz"); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	entries, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List returned %d entries, want 1", len(entries))
	}
	got := entries[0]
	if got.LastSeen == e.LastSeen {
		t.Errorf("LastSeen unchanged after Heartbeat")
	}
	if got.ProjectDir != e.ProjectDir || got.RuntimeDir != e.RuntimeDir ||
		got.Host != e.Host || got.PID != e.PID || got.StartedAt != e.StartedAt {
		t.Errorf("Heartbeat modified fields other than LastSeen: got %+v, want same as %+v (except LastSeen)", got, e)
	}
}

func TestHeartbeatMissingEntryErrors(t *testing.T) {
	isolate(t)

	if err := Heartbeat("agentctl-nonexistent"); err == nil {
		t.Error("Heartbeat on missing entry should return an error")
	}
}

func TestListSkipsCorruptFile(t *testing.T) {
	dir := isolate(t)

	good := Entry{SessionName: "agentctl-good", LastSeen: 1000}
	if err := Register(good); err != nil {
		t.Fatalf("Register good: %v", err)
	}

	// Write a truncated/corrupt JSON file directly.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	corruptPath := filepath.Join(dir, "agentctl-corrupt.json")
	if err := os.WriteFile(corruptPath, []byte(`{"session_name": "agentctl-corrupt", "pid":`), 0o644); err != nil {
		t.Fatalf("writing corrupt file: %v", err)
	}

	entries, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List returned %d entries, want 1 (corrupt file skipped)", len(entries))
	}
	if entries[0].SessionName != "agentctl-good" {
		t.Errorf("List returned %+v, want the good entry", entries[0])
	}
}

func TestPrune(t *testing.T) {
	isolate(t)

	now := time.Now().Unix()
	fresh := Entry{SessionName: "agentctl-fresh", LastSeen: now}
	stale := Entry{SessionName: "agentctl-stale", LastSeen: now - 3600}
	deadButRecent := Entry{SessionName: "agentctl-dead", LastSeen: now}

	for _, e := range []Entry{fresh, stale, deadButRecent} {
		if err := Register(e); err != nil {
			t.Fatalf("Register %s: %v", e.SessionName, err)
		}
	}

	t.Run("age and liveness", func(t *testing.T) {
		live := map[string]bool{"agentctl-fresh": true, "agentctl-stale": true}
		surviving, err := Prune(live, 30*time.Second)
		if err != nil {
			t.Fatalf("Prune: %v", err)
		}
		if len(surviving) != 1 || surviving[0].SessionName != "agentctl-fresh" {
			t.Errorf("Prune surviving = %+v, want only agentctl-fresh (stale by age, dead by liveness)", surviving)
		}

		entries, _ := List()
		if len(entries) != 1 {
			t.Errorf("List after Prune returned %d entries, want 1", len(entries))
		}
	})
}

func TestPruneNilLiveSkipsLivenessCheck(t *testing.T) {
	isolate(t)

	now := time.Now().Unix()
	fresh := Entry{SessionName: "agentctl-fresh2", LastSeen: now}
	stale := Entry{SessionName: "agentctl-stale2", LastSeen: now - 3600}
	for _, e := range []Entry{fresh, stale} {
		if err := Register(e); err != nil {
			t.Fatalf("Register %s: %v", e.SessionName, err)
		}
	}

	surviving, err := Prune(nil, 30*time.Second)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(surviving) != 1 || surviving[0].SessionName != "agentctl-fresh2" {
		t.Errorf("Prune(nil live) surviving = %+v, want only agentctl-fresh2 (age-only pruning)", surviving)
	}
}

func TestDirCreatedOnWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	dir := filepath.Join(home, ".config", "agentctl", "registry")
	SetDirForTest(dir)
	t.Cleanup(func() { SetDirForTest("") })

	if _, err := os.Stat(dir); err == nil {
		t.Fatalf("registry dir should not exist yet: %s", dir)
	}

	got := Dir()
	if got != dir {
		t.Errorf("Dir() = %q, want %q", got, dir)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("Dir() must not create the directory (read-only callers)")
	}
	if _, err := List(); err != nil {
		t.Fatalf("List on missing dir: %v", err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("List() must not create the directory")
	}
	if err := Register(Entry{SessionName: "agentctl-x"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("Register did not create the directory: %v", err)
	}
}
