package tokens

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCatalogModelIDs(t *testing.T) {
	dir := t.TempDir()
	doc := `{"data":[{"id":"z/b","pricing":{"prompt":"0.000001","completion":"0.000002"}},{"id":"A/a","pricing":{"prompt":"0.000001","completion":"0.000002"}},{"id":"nopricing"}]}`
	if err := os.WriteFile(filepath.Join(dir, "nous-models-abc.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	SetCatalogDir(dir)
	t.Cleanup(func() { SetCatalogDir("") })

	want := []string{"a/a", "z/b"}
	if got := CatalogModelIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("CatalogModelIDs = %v, want %v", got, want)
	}
	SetCatalogDir("")
	if got := CatalogModelIDs(); len(got) != 0 {
		t.Fatalf("disabled catalog returned %v", got)
	}
}
