package hooks

import "testing"

func TestSyncSessionMeta(t *testing.T) {
	orig := spawnMetaSyncFn
	t.Cleanup(func() { spawnMetaSyncFn = orig })
	var got []string
	spawnMetaSyncFn = func(id string) error { got = append(got, id); return nil }

	syncSessionMeta("")
	syncSessionMeta("abc-123")
	if len(got) != 1 || got[0] != "abc-123" {
		t.Errorf("spawned for %v, want [abc-123]", got)
	}
}
