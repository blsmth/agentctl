package pricecache

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/tokens"
)

const catalogBody = `{"data":[
 {"id":"a/one","context_length":1000,"endpoints":["chat"],"pricing":{"prompt":"0.000001","completion":"0.000004","input_cache_read":"0.0000001"}},
 {"id":"a/nopricing"}]}`

type env struct {
	hits atomic.Int32
	auth atomic.Value
	srv  *httptest.Server
	dir  string
	cur  time.Time
}

func setup(t *testing.T, status int, body string) *env {
	t.Helper()
	state := t.TempDir()
	e := &env{dir: filepath.Join(state, "pricing"), cur: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.hits.Add(1)
		e.auth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(e.srv.Close)
	oldD, oldR, oldN := dirFn, rootFn, now
	dirFn = func() (string, error) { return e.dir, nil }
	rootFn = func() (string, error) { return state, nil }
	now = func() time.Time { return e.cur }
	tokens.SetCatalogDir("")
	tokens.SetStateCatalogDir(func() string { return e.dir })
	t.Cleanup(func() {
		dirFn, rootFn, now = oldD, oldR, oldN
		tokens.SetStateCatalogDir(nil)
	})
	return e
}

func backends(url string) map[string]config.BackendConfig {
	return map[string]config.BackendConfig{"nous": {Kind: config.BackendKindNous, BaseURL: url}}
}

func staticKey(context.Context, string, config.BackendConfig) (string, error) { return "sk-secret", nil }

func TestRefreshWritesPrivateCacheAndPricesModels(t *testing.T) {
	e := setup(t, 200, catalogBody)
	res := Refresh(context.Background(), backends(e.srv.URL), staticKey)
	if len(res) != 1 || res[0].Err != nil || res[0].Models != 1 {
		t.Fatalf("results = %+v", res)
	}
	if got := e.auth.Load(); got != "Bearer sk-secret" {
		t.Errorf("auth = %v", got)
	}
	path := filepath.Join(e.dir, FileName(e.srv.URL))
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(e.dir)
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", di.Mode().Perm())
	}
	m := tokens.Lookup("a/one")
	if m.Source != "catalog" || m.Pricing.InputPerMTok != 1 || m.Pricing.OutputPerMTok != 4 {
		t.Errorf("Lookup after refresh = %+v", m)
	}
}

func TestRefreshOneFetchPerBaseURLAndFailureKeepsOldCache(t *testing.T) {
	e := setup(t, 200, catalogBody)
	bs := backends(e.srv.URL)
	bs["nous2"] = config.BackendConfig{Kind: config.BackendKindNous, BaseURL: e.srv.URL + "/"}
	bs["local"] = config.BackendConfig{Kind: config.BackendKindOllama}
	res := Refresh(context.Background(), bs, nil)
	if len(res) != 2 || e.hits.Load() != 1 {
		t.Fatalf("results=%d hits=%d", len(res), e.hits.Load())
	}
	path := filepath.Join(e.dir, FileName(e.srv.URL))
	before, _ := os.ReadFile(path)

	bad := setup(t, 500, "boom")
	// setup replaced the seams with a new dir; point back at the first cache.
	dirFn = func() (string, error) { return e.dir, nil }
	res = Refresh(context.Background(), backends(bad.srv.URL), nil)
	if res[0].Err == nil {
		t.Fatal("want an error")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("a failed refresh changed the cache")
	}
}

func TestFetchRedactsKeyAndRejectsBadCatalog(t *testing.T) {
	e := setup(t, 200, `{"data":[]}`)
	if _, err := Fetch(context.Background(), e.srv.URL, "k"); err == nil {
		t.Error("empty catalog should be rejected")
	}
	e.srv.Close()
	_, err := Fetch(context.Background(), strings.Replace(e.srv.URL, "http://", "http://sk-secret@", 1), "sk-secret")
	if err == nil || strings.Contains(err.Error(), "sk-secret") {
		t.Errorf("err = %v", err)
	}
}

func TestRefreshIfStale(t *testing.T) {
	e := setup(t, 200, catalogBody)
	bs := backends(e.srv.URL)

	did, err := RefreshIfStale(context.Background(), bs, nil)
	if err != nil || !did || e.hits.Load() != 1 {
		t.Fatalf("first: did=%v err=%v hits=%d", did, err, e.hits.Load())
	}
	// Fresh: nothing.
	if did, _ := RefreshIfStale(context.Background(), bs, nil); did {
		t.Error("refreshed a fresh cache")
	}
	// Cache over 24h old but the claim is also 24h old only after a day: the
	// stamp holds even when the file is stale (a failing backend is not
	// hammered).
	path := filepath.Join(e.dir, FileName(e.srv.URL))
	old := e.cur.Add(-25 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if did, _ := RefreshIfStale(context.Background(), bs, nil); did {
		t.Error("refreshed twice within the claim window")
	}
	e.cur = e.cur.Add(25 * time.Hour)
	did, err = RefreshIfStale(context.Background(), bs, nil)
	if err != nil || !did || e.hits.Load() != 2 {
		t.Fatalf("after a day: did=%v err=%v hits=%d", did, err, e.hits.Load())
	}
}

func TestRefreshIfStaleFailureIsReturnedNotFatal(t *testing.T) {
	e := setup(t, 503, "down")
	did, err := RefreshIfStale(context.Background(), backends(e.srv.URL), nil)
	if !did || err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("did=%v err=%v", did, err)
	}
}

func TestLastWrite(t *testing.T) {
	e := setup(t, 200, catalogBody)
	if _, ok := LastWrite(); ok {
		t.Fatal("empty cache has no last write")
	}
	if err := Write(e.srv.URL, []byte(catalogBody)); err != nil {
		t.Fatal(err)
	}
	if _, ok := LastWrite(); !ok {
		t.Fatal("want a last write")
	}
}
