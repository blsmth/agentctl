package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/llmusage"
	"github.com/blsmth/agentctl/internal/pricecache"
	"github.com/blsmth/agentctl/internal/tokens"
)

func catalogDir(t *testing.T, doc string, age time.Duration) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "nous-abc.json")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-age)
	_ = os.Chtimes(p, ts, ts)
	tokens.SetCatalogDir("")
	tokens.SetStateCatalogDir(func() string { return dir })
	t.Cleanup(func() { tokens.SetStateCatalogDir(nil); tokens.SetOverrides(nil, 0) })
}

func TestBuildPriceRows(t *testing.T) {
	catalogDir(t, `{"data":[
	 {"id":"qwen/qwen3-max","pricing":{"prompt":"0.000001","completion":"0.000004"}},
	 {"id":"qwen/cached","pricing":{"prompt":"0.000001","completion":"0.000004","input_cache_read":"0.0000001"}}]}`, 72*time.Hour)
	tokens.SetOverrides(map[string]tokens.ModelOverride{"qwen/cached": {InputPerMTok: 2, OutputPerMTok: 8}}, 0)

	rows := buildPriceRows("QWEN")
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	byID := map[string]priceRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	// Plain catalog row: no cache prices when the catalog has none; age set.
	m := byID["qwen/qwen3-max"]
	if m.Source != "catalog" || m.Input != 1 || m.Output != 4 || m.CacheRead != nil || m.CacheWrite != nil {
		t.Errorf("catalog row = %+v", m)
	}
	if age := time.Since(m.Updated); age < 71*time.Hour || age > 73*time.Hour {
		t.Errorf("catalog age = %v", age)
	}
	// A config override outranks the catalog (tokens.Lookup precedence).
	if c := byID["qwen/cached"]; c.Source != "config" || c.Input != 2 || !c.Updated.IsZero() {
		t.Errorf("override row = %+v", c)
	}
}

func TestBuildPriceRowsFallbacks(t *testing.T) {
	catalogDir(t, `{"data":[]}`, time.Hour)
	// Built-in with cache prices.
	rows := buildPriceRows("claude-opus-4-8")
	if len(rows) != 1 || rows[0].Source != "built-in" || rows[0].CacheRead == nil || rows[0].CacheWrite == nil {
		t.Fatalf("built-in rows = %+v", rows)
	}
	// A newer model of a known family has no entry: family fallback.
	rows = buildPriceRows("claude-opus-9-9")
	if len(rows) != 1 || rows[0].Source != "family" || rows[0].Input == 0 {
		t.Fatalf("family rows = %+v", rows)
	}
	// Unknown: shown as unknown, never a guessed price.
	rows = buildPriceRows("acme/mystery-1")
	if len(rows) != 1 || rows[0].Source != "unknown" || rows[0].Input != 0 {
		t.Fatalf("unknown rows = %+v", rows)
	}
}

func TestPriceCacheColumns(t *testing.T) {
	catalogDir(t, `{"data":[{"id":"x/c","pricing":{"prompt":"0.000001","completion":"0.000004","input_cache_read":"0.0000001","input_cache_write":"0.00000125"}}]}`, time.Hour)
	r := buildPriceRows("x/c")[0]
	if r.CacheRead == nil || fmtPrice(*r.CacheRead) != "0.1" || r.CacheWrite == nil || fmtPrice(*r.CacheWrite) != "1.25" {
		t.Errorf("cache columns = %+v", r)
	}
}

func TestPriceAge(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "-"}, {now.Add(-10 * time.Second), "just now"}, {now.Add(-5 * time.Minute), "5m"},
		{now.Add(-5 * time.Hour), "5h"}, {now.Add(-72 * time.Hour), "3d"},
	} {
		if got := priceAge(c.t, now); got != c.want {
			t.Errorf("priceAge(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestGroupLLM(t *testing.T) {
	c1, c2 := 0.01, 0.03
	ts := time.Date(2026, 11, 20, 12, 0, 0, 0, time.Local)
	recs := []llmusage.Record{
		{TS: ts, Model: "a/x", Project: "p1", PromptTokens: 100, CompletionTokens: 10, CostUSD: &c1, OK: true},
		{TS: ts, Model: "a/x", Project: "p2", PromptTokens: 50, CompletionTokens: 5, CostUSD: &c2, OK: true},
		{TS: ts.Add(-48 * time.Hour), Model: "b/y", Project: "p1", OK: false, Error: "timeout"},
	}
	tot := sumLLM(recs)
	if tot.Calls != 3 || tot.Failed != 1 || tot.Prompt != 150 || tot.Completion != 15 || tot.Unpriced != 1 || tot.Cost < 0.0399 || tot.Cost > 0.0401 {
		t.Errorf("totals = %+v", tot)
	}
	g := groupLLM(recs, "model")
	if len(g) != 2 || g[0].Key != "a/x" || g[0].Calls != 2 || g[1].Key != "b/y" {
		t.Errorf("by model = %+v", g)
	}
	if g := groupLLM(recs, "day"); len(g) != 2 || g[0].Key >= g[1].Key {
		t.Errorf("by day = %+v", g)
	}
	if g := groupLLM(recs, "project"); len(g) != 2 || g[0].Key != "p2" && g[0].Key != "p1" {
		t.Errorf("by project = %+v", g)
	}
	if groupLLM(recs, "agent") != nil || groupLLM(recs, "") != nil {
		t.Error("agent/none grouping should yield no groups")
	}
	js := llmSection(recs, "model")
	if js.By != "model" || len(js.Groups) != 2 || js.Totals.Calls != 3 || js.Totals.Unpriced != 1 {
		t.Errorf("json section = %+v", js)
	}
}

func TestRunCostPricesRefreshFailureStillPrintsAndErrors(t *testing.T) {
	catalogDir(t, `{"data":[]}`, time.Hour)
	old := pricesRefresh
	var called bool
	pricesRefresh = func(_ context.Context, _ map[string]config.BackendConfig) []pricecache.Result {
		called = true
		return []pricecache.Result{{Backend: "nous", Err: errors.New("boom")}}
	}
	t.Cleanup(func() { pricesRefresh = old })
	err := RunCostPrices(PricesOptions{Filter: "claude-opus-4-8", Refresh: true, JSON: true})
	if !called || err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("called=%v err=%v", called, err)
	}
	// Without --refresh the refresher is never touched.
	called = false
	if err := RunCostPrices(PricesOptions{Filter: "claude-opus-4-8", JSON: true}); err != nil || called {
		t.Fatalf("called=%v err=%v", called, err)
	}
}
