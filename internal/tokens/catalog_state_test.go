package tokens

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCat(t *testing.T, path, id, prompt string, mtime time.Time) {
	t.Helper()
	doc := `{"data":[{"id":"` + id + `","pricing":{"prompt":"` + prompt + `","completion":"0.000002"}}]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestStateCatalogDirAnyAgeAndNewestWins(t *testing.T) {
	runtimeDir, stateDir := t.TempDir(), t.TempDir()
	old := time.Now().Add(-90 * 24 * time.Hour)
	recent := time.Now().Add(-time.Hour)
	// A 90-day-old state copy still prices (age is irrelevant).
	writeCat(t, filepath.Join(stateDir, "nous-aaa.json"), "x/old", "0.000001", old)
	// The same model in both dirs: the newer file wins.
	writeCat(t, filepath.Join(runtimeDir, "nous-models-bbb.json"), "x/both", "0.000003", old)
	writeCat(t, filepath.Join(stateDir, "nous-ccc.json"), "x/both", "0.000005", recent)

	SetCatalogDir(runtimeDir)
	SetStateCatalogDir(func() string { return stateDir })
	t.Cleanup(func() { SetCatalogDir(""); SetStateCatalogDir(nil) })

	m := Lookup("x/old")
	if m.Source != "catalog" || m.Pricing.InputPerMTok != 1 {
		t.Fatalf("old state copy: source=%q input=%v", m.Source, m.Pricing.InputPerMTok)
	}
	if got := Lookup("x/both").Pricing.InputPerMTok; got != 5 {
		t.Errorf("newest copy should win: input = %v, want 5", got)
	}
	if ts, ok := CatalogUpdated("X/Both"); !ok || ts.Sub(recent).Abs() > time.Second {
		t.Errorf("CatalogUpdated = %v, %v", ts, ok)
	}
	if _, ok := CatalogUpdated("nope"); ok {
		t.Error("CatalogUpdated(unknown) should be false")
	}
}

func TestStateCatalogDirResolvedLazily(t *testing.T) {
	stateDir := t.TempDir()
	calls := 0
	SetCatalogDir("")
	SetStateCatalogDir(func() string { calls++; return stateDir })
	t.Cleanup(func() { SetStateCatalogDir(nil) })
	if calls != 0 {
		t.Fatalf("resolver ran at registration (%d calls)", calls)
	}
	_ = Lookup("x/y")
	_ = Lookup("x/z")
	if calls != 1 {
		t.Errorf("resolver calls = %d, want 1 (loaded once)", calls)
	}
}
