package tokens

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxCatalogFile bounds how much of a cached catalog file is read.
const maxCatalogFile = 8 << 20

// CatalogPrice is a catalog price in $ per TOKEN, as the Nous /v1/models
// endpoint publishes it: usually a decimal string ("0.0000011"), tolerated as
// a bare JSON number. Empty means missing.
type CatalogPrice string

// UnmarshalJSON accepts a string, a number, or null.
func (c *CatalogPrice) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*c = ""
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s = str
	}
	*c = CatalogPrice(strings.TrimSpace(s))
	return nil
}

// perMTok converts $/token to $/MTok. ok is false when the value is missing
// or unparsable; a negative value is treated as missing.
func (c CatalogPrice) perMTok() (v float64, ok bool) {
	if c == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(c), 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return f * 1_000_000, true
}

// CatalogPricing is the `pricing` object of a Nous catalog entry.
type CatalogPricing struct {
	Prompt          CatalogPrice `json:"prompt,omitempty"`
	Completion      CatalogPrice `json:"completion,omitempty"`
	InputCacheRead  CatalogPrice `json:"input_cache_read,omitempty"`
	InputCacheWrite CatalogPrice `json:"input_cache_write,omitempty"`
}

// PerMTok converts catalog pricing to Pricing. ok is false when prompt or
// completion is missing (the model is then treated as unpriced). Rules:
//   - missing or zero input_cache_write: cache writes bill as input;
//   - missing or zero input_cache_read: the model does not cache, so cache
//     reads bill at the input price.
func (cp CatalogPricing) PerMTok() (Pricing, bool) {
	in, okIn := cp.Prompt.perMTok()
	out, okOut := cp.Completion.perMTok()
	if !okIn || !okOut {
		return Pricing{}, false
	}
	p := Pricing{InputPerMTok: in, OutputPerMTok: out, CacheReadPerMTok: in, CacheCreatePerMTok: in}
	if v, ok := cp.InputCacheRead.perMTok(); ok && v > 0 {
		p.CacheReadPerMTok = v
	}
	if v, ok := cp.InputCacheWrite.perMTok(); ok && v > 0 {
		p.CacheCreatePerMTok = v
	}
	return p, true
}

// ParseCatalog extracts per-model pricing from a Nous catalog document
// ({"data":[{"id":...,"pricing":{...}}]}), keyed by lowercased model id.
// Entries without usable pricing are omitted.
func ParseCatalog(data []byte) map[string]Pricing {
	var doc struct {
		Data []struct {
			ID      string          `json:"id"`
			Pricing *CatalogPricing `json:"pricing"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	out := make(map[string]Pricing, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID == "" || m.Pricing == nil {
			continue
		}
		if p, ok := m.Pricing.PerMTok(); ok {
			out[strings.ToLower(m.ID)] = p
		}
	}
	return out
}

type catalogState struct {
	mu      sync.Mutex
	dir     string
	stateFn func() string
	loaded  bool
	entries map[string]Pricing
	mtimes  map[string]time.Time
}

var catalog catalogState

// reset forgets the loaded catalog. The caller holds c.mu.
func (c *catalogState) reset() {
	c.loaded = false
	c.entries = nil
	c.mtimes = nil
}

// ensure loads the cache files once. The caller holds c.mu.
func (c *catalogState) ensure() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.entries = map[string]Pricing{}
	c.mtimes = map[string]time.Time{}
	var files []catalogFile
	if c.dir != "" {
		files = append(files, listCatalogFiles(filepath.Join(c.dir, "nous-models-*.json"))...)
	}
	if c.stateFn != nil {
		if d := c.stateFn(); d != "" {
			files = append(files, listCatalogFiles(filepath.Join(d, "nous-*.json"))...)
		}
	}
	// Oldest first, so the newest copy of a model wins.
	sort.SliceStable(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for _, f := range files {
		data, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		for id, p := range ParseCatalog(data) {
			c.entries[id] = p
			c.mtimes[id] = f.mtime
		}
	}
}

type catalogFile struct {
	path  string
	mtime time.Time
}

func listCatalogFiles(pattern string) []catalogFile {
	matches, _ := filepath.Glob(pattern)
	var out []catalogFile
	for _, f := range matches {
		fi, err := os.Stat(f)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxCatalogFile {
			continue
		}
		out = append(out, catalogFile{f, fi.ModTime()})
	}
	return out
}

// SetCatalogDir points tokens at the directory holding the cached Nous
// catalog files (nous-models-*.json, written by internal/dispatch). The files
// are read lazily, at most once per process (or per call to this function);
// nothing here touches the network. Pass "" to disable.
func SetCatalogDir(dir string) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if catalog.dir != dir {
		catalog.dir = dir
		catalog.reset()
	}
}

// SetStateCatalogDir registers the resolver of the machine-wide price cache
// directory (nous-*.json under the state dir). It is called when the catalog is
// first read, never at package init. Files there and in the SetCatalogDir
// directory are merged, the newest file winning per model; a file's age does
// not matter for pricing. Pass nil to disable.
func SetStateCatalogDir(fn func() string) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if catalog.stateFn == nil && fn != nil {
		catalog.reset() // first registration; later calls keep the loaded data
	}
	catalog.stateFn = fn
	if fn == nil {
		catalog.reset()
	}
}

// catalogPricing returns the catalog price for an exact (lowercased) model id.
func catalogPricing(id string) (Pricing, bool) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	catalog.ensure()
	p, ok := catalog.entries[id]
	return p, ok
}

// CatalogUpdated reports when the cache file that priced the (lowercased)
// model id was last written.
func CatalogUpdated(id string) (time.Time, bool) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	catalog.ensure()
	t, ok := catalog.mtimes[strings.ToLower(id)]
	return t, ok
}

// ReloadCatalog discards the loaded catalog so the next lookup re-reads the
// cache files (used after the cache has been refreshed).
func ReloadCatalog() {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	catalog.reset()
}

// CatalogModelIDs returns the model ids in the cached Nous catalog, sorted and
// de-duplicated. It reads only the local cache files; nothing here touches the
// network, and it is empty when no catalog has been cached.
func CatalogModelIDs() []string {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	catalog.ensure()
	ids := make([]string, 0, len(catalog.entries))
	for id := range catalog.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
