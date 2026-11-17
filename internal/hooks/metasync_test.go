package hooks

import (
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/paths"
)

type metaSpawn struct{ id, transcript string }

func stubMetaSpawn(t *testing.T) *[]metaSpawn {
	t.Helper()
	orig := spawnMetaSyncFn
	t.Cleanup(func() { spawnMetaSyncFn = orig })
	var got []metaSpawn
	spawnMetaSyncFn = func(id, tr string) error { got = append(got, metaSpawn{id, tr}); return nil }
	return &got
}

func TestSyncSessionMeta(t *testing.T) {
	got := stubMetaSpawn(t)
	syncSessionMeta("", "")
	syncSessionMeta("abc-123", "/t/abc-123.jsonl")
	if len(*got) != 1 || (*got)[0] != (metaSpawn{"abc-123", "/t/abc-123.jsonl"}) {
		t.Errorf("spawned %v", *got)
	}
}

func TestSyncSessionMetaThrottled(t *testing.T) {
	got := stubMetaSpawn(t)
	t.Cleanup(paths.Bind(t.TempDir()))
	now := time.Now()

	syncSessionMetaThrottled("s1", "/t/s1.jsonl", now)
	syncSessionMetaThrottled("s1", "/t/s1.jsonl", now.Add(time.Minute)) // throttled
	syncSessionMetaThrottled("s2", "", now.Add(time.Minute))            // other session unaffected
	syncSessionMetaThrottled("s1", "/t/s1.jsonl", now.Add(metaSyncInterval+time.Second))
	syncSessionMetaThrottled("../evil", "", now)

	want := []metaSpawn{{"s1", "/t/s1.jsonl"}, {"s2", ""}, {"s1", "/t/s1.jsonl"}}
	if len(*got) != len(want) {
		t.Fatalf("spawns = %v, want %v", *got, want)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Errorf("spawn %d = %v, want %v", i, (*got)[i], want[i])
		}
	}
}

// The Stop hook passes the transcript path through, skips stop_hook_active
// re-entry, and syncs at most once per interval per session.
func TestCheckDone_MetaSync(t *testing.T) {
	stopEnv(t, "@82", "discuss")
	t.Cleanup(paths.Bind(t.TempDir()))
	got := stubMetaSpawn(t)

	in := `{"session_id":"sess-A","transcript_path":"/p/sess-A.jsonl","last_assistant_message":"x"}`
	runHookCaptured(t, nil, in, CheckDone)
	if len(*got) != 1 || (*got)[0] != (metaSpawn{"sess-A", "/p/sess-A.jsonl"}) {
		t.Fatalf("first stop spawns = %v", *got)
	}
	runHookCaptured(t, nil, in, CheckDone) // within the interval
	if len(*got) != 1 {
		t.Errorf("throttled stop spawned again: %v", *got)
	}

	reentry := `{"stop_hook_active":true,"session_id":"sess-B","transcript_path":"/p/sess-B.jsonl"}`
	runHookCaptured(t, nil, reentry, CheckDone)
	if len(*got) != 1 {
		t.Errorf("stop_hook_active re-entry spawned: %v", *got)
	}
}
