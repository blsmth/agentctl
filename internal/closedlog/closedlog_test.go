package closedlog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func setup(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	dir := filepath.Join(state, "closed")
	oldD, oldR := dirFn, rootFn
	dirFn = func() (string, error) { return dir, nil }
	rootFn = func() (string, error) { return state, nil }
	t.Cleanup(func() { dirFn, rootFn = oldD, oldR })
	return dir
}

func TestAppendLoadNewestFirstAndSince(t *testing.T) {
	dir := setup(t)
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for i, id := range []string{"@1", "@2", "@3"} {
		if err := Append(Record{ClosedAt: t0.Add(time.Duration(i) * time.Hour), WindowID: id, SessionID: "s" + id}); err != nil {
			t.Fatal(err)
		}
	}
	// One from last month, in its own file.
	if err := Append(Record{ClosedAt: t0.AddDate(0, -1, 0), WindowID: "@0", SessionID: "old"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "closed-2026-11.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	got, err := Load(t0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.WindowID)
	}
	if strings.Join(ids, ",") != "@3,@2,@1" {
		t.Errorf("Load order = %v, want newest first", ids)
	}
	if got, _ := Load(t0.Add(90 * time.Minute)); len(got) != 1 || got[0].WindowID != "@3" {
		t.Errorf("since filter = %+v", got)
	}
	if got, _ := Load(t0.AddDate(0, -2, 0)); len(got) != 4 || got[3].WindowID != "@0" {
		t.Errorf("across months = %+v", got)
	}
}

func TestLoadMissingAndCorrupt(t *testing.T) {
	dir := setup(t)
	if got, err := Load(time.Time{}); err != nil || len(got) != 0 {
		t.Fatalf("missing ledger = %v, %v", got, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "not json\n\n{\"closed_at\":\"2026-11-20T10:00:00Z\",\"window_id\":\"@5\"}\n{\"window_id\":\"@6\"}\n{\"closed_at\":\"2026-11-20T10:00:00Z\"\n"
	if err := os.WriteFile(filepath.Join(dir, "closed-2026-11.ndjson"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(time.Time{})
	if err != nil || len(got) != 1 || got[0].WindowID != "@5" {
		t.Errorf("Load = %+v, %v; want only the valid line", got, err)
	}
}

func TestLoadDedupesDoubleAppend(t *testing.T) {
	setup(t)
	r := Record{ClosedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC), WindowID: "@1", Session: "agentctl-x", SessionID: "u1"}
	for range 2 {
		if err := Append(r); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := Load(time.Time{}); len(got) != 1 {
		t.Errorf("double append shows %d records, want 1", len(got))
	}
}

func TestAppendTruncatesPrompt(t *testing.T) {
	setup(t)
	long := strings.Repeat("é", MaxPrompt*2)
	if err := Append(Record{WindowID: "@1", FirstPrompt: long}); err != nil {
		t.Fatal(err)
	}
	got, _ := Load(time.Time{})
	if len(got) != 1 || len([]rune(got[0].FirstPrompt)) != MaxPrompt || !strings.HasSuffix(got[0].FirstPrompt, "…") {
		t.Errorf("prompt = %q", got[0].FirstPrompt)
	}
	if got[0].ClosedAt.IsZero() {
		t.Error("ClosedAt not defaulted")
	}
}

func TestAppendConcurrentLinesStayWhole(t *testing.T) {
	setup(t)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = Append(Record{WindowID: "@1", ClosedAt: time.Now().Add(time.Duration(i) * time.Second), FirstPrompt: strings.Repeat("x", 200)})
		}()
	}
	wg.Wait()
	if got, _ := Load(time.Time{}); len(got) != 40 {
		t.Errorf("got %d records, want 40", len(got))
	}
}

func TestPrune(t *testing.T) {
	dir := setup(t)
	at := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	if err := Append(Record{ClosedAt: at, WindowID: "@1"}); err != nil {
		t.Fatal(err)
	}
	if err := Append(Record{ClosedAt: at.AddDate(0, -5, 0), WindowID: "@2"}); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, FileName(at.AddDate(0, -5, 0)))
	if err := os.Chtimes(old, at.AddDate(0, -4, 0), at.AddDate(0, -4, 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, FileName(at)), at, at); err != nil {
		t.Fatal(err)
	}
	n, err := Prune(at, MaxAge)
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v; want 1", n, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old file kept")
	}
	if got, _ := Load(time.Time{}); len(got) != 1 || got[0].WindowID != "@1" {
		t.Errorf("after prune = %+v", got)
	}
}

func TestCacheRereadsOnChange(t *testing.T) {
	setup(t)
	var c Cache
	t0 := time.Now().Add(-time.Hour)
	if got, err := c.Load(t0); err != nil || len(got) != 0 {
		t.Fatalf("empty = %v, %v", got, err)
	}
	if err := Append(Record{WindowID: "@1"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Load(t0); len(got) != 1 {
		t.Fatalf("after append = %d records, want 1", len(got))
	}
	if err := Append(Record{WindowID: "@2", SessionID: "x"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Load(t0); len(got) != 2 || got[0].WindowID != "@2" {
		t.Errorf("after second append = %+v", got)
	}
	if got, _ := c.Load(time.Now().Add(time.Minute)); len(got) != 0 {
		t.Errorf("since after both = %+v", got)
	}
}

func TestRecordHelpers(t *testing.T) {
	tests := []struct {
		r     Record
		title string
		state string
		proj  string
	}{
		{Record{ShortTitle: "Fix bug", FirstPrompt: "hello", Done: true, ProjectDir: "/x/agentctl"}, "Fix bug", "done", "agentctl"},
		{Record{FirstPrompt: "first\nsecond", State: "working", Session: "agentctl-foo"}, "first", "working", "foo"},
		{Record{Instance: "go-engineer-2"}, "go-engineer-2", "idle", ""},
	}
	for _, tt := range tests {
		if got := tt.r.Title(); got != tt.title {
			t.Errorf("Title = %q, want %q", got, tt.title)
		}
		if got := tt.r.FinalState(); got != tt.state {
			t.Errorf("FinalState = %q, want %q", got, tt.state)
		}
		if got := tt.r.Project(); got != tt.proj {
			t.Errorf("Project = %q, want %q", got, tt.proj)
		}
	}
}

func TestAgo(t *testing.T) {
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		d    time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{2 * time.Hour, "2h ago"},
		{72 * time.Hour, "3d ago"},
	}
	for _, tt := range tests {
		if got := Ago(at, at.Add(-tt.d)); got != tt.want {
			t.Errorf("Ago(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
