package llmusage

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/tokens"
)

func setup(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	dir := filepath.Join(state, "llm-usage")
	oldD, oldR := dirFn, rootFn
	dirFn = func() (string, error) { return dir, nil }
	rootFn = func() (string, error) { return state, nil }
	t.Cleanup(func() { dirFn, rootFn = oldD, oldR })
	return dir
}

func TestAppendLoadRoundTripAndPerms(t *testing.T) {
	dir := setup(t)
	c := 0.001
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for _, r := range []Record{
		{TS: t0, Feature: "titles", Backend: "nous", Model: "m/x", PromptTokens: 100, CompletionTokens: 5, CostUSD: &c, Window: "@3", Project: "agentctl", OK: true},
		{TS: t0.Add(time.Hour), Feature: "titles", Model: "m/x", Window: "@4", Error: "timeout"},
	} {
		if err := Append(r); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(filepath.Join(dir, "usage-2026-11.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", di.Mode().Perm())
	}
	got, err := Load(t0)
	if err != nil || len(got) != 2 {
		t.Fatalf("Load = %v, %v", got, err)
	}
	if got[0].CostUSD == nil || *got[0].CostUSD != c || !got[0].OK || got[1].OK || got[1].Error != "timeout" || got[1].CostUSD != nil {
		t.Errorf("records = %+v", got)
	}
	if later, _ := Load(t0.Add(30 * time.Minute)); len(later) != 1 || later[0].Window != "@4" {
		t.Errorf("since filter = %+v", later)
	}
}

func TestAppendConcurrentLinesStayWhole(t *testing.T) {
	setup(t)
	var wg sync.WaitGroup
	ts := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Append(Record{TS: ts, Feature: "titles", Model: "m", OK: true}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, _ := Load(ts.Add(-time.Hour))
	if len(got) != 40 {
		t.Errorf("loaded %d of 40 lines", len(got))
	}
}

func TestLoadSkipsMalformedAndSpansMonths(t *testing.T) {
	dir := setup(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "usage-2026-10.ndjson"),
		[]byte("not json\n{\"ts\":\"2026-10-31T23:00:00Z\",\"feature\":\"titles\",\"model\":\"m\",\"ok\":true}\n"), 0o600)
	_ = Append(Record{TS: time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC), Feature: "titles", Model: "m", OK: true})
	got, _ := Load(time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC))
	if len(got) != 2 {
		t.Fatalf("got %d records", len(got))
	}
}

func TestPrune(t *testing.T) {
	dir := setup(t)
	_ = Append(Record{TS: time.Now(), Feature: "titles", Model: "m", OK: true})
	oldPath := filepath.Join(dir, "usage-2026-01.ndjson")
	_ = os.WriteFile(oldPath, []byte("{}\n"), 0o600)
	old := time.Now().Add(-91 * 24 * time.Hour)
	_ = os.Chtimes(oldPath, old, old)
	n, err := Prune(time.Now(), MaxAge)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old file still present")
	}
	if got, _ := Load(time.Now().Add(-time.Hour)); len(got) != 1 {
		t.Error("current file was pruned")
	}
}

func TestCost(t *testing.T) {
	tokens.SetOverrides(map[string]tokens.ModelOverride{"x/priced": {InputPerMTok: 1, OutputPerMTok: 4, CacheReadPerMTok: 0.1}}, 0)
	t.Cleanup(func() { tokens.SetOverrides(nil, 0) })

	// 1000 prompt of which 400 cached: 600*1 + 400*0.1 + 50*4 = 840 / 1e6.
	c := Cost("x/priced", 1000, 50, 400)
	if c == nil || *c < 0.00083999 || *c > 0.00084001 {
		t.Fatalf("cost = %v", c)
	}
	if Cost("totally/unknown-model", 1000, 50, 0) != nil {
		t.Error("unknown model must be null, not a guessed price")
	}
	if Cost("m/free:free", 10, 10, 0) == nil {
		t.Error(":free model is known and costs 0")
	}
}

func TestMetaRoundTrip(t *testing.T) {
	m := Meta{Feature: "titles", Window: "@1", Project: "p"}
	if got := MetaFrom(WithMeta(context.Background(), m)); got != m {
		t.Errorf("got %+v", got)
	}
	if got := MetaFrom(context.Background()); got != (Meta{}) {
		t.Errorf("zero ctx = %+v", got)
	}
}
