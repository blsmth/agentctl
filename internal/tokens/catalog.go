package tokens

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

var catalog struct {
	mu      sync.Mutex
	dir     string
	loaded  bool
	entries map[string]Pricing
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
		catalog.loaded = false
		catalog.entries = nil
	}
}

// catalogPricing returns the catalog price for an exact (lowercased) model id.
func catalogPricing(id string) (Pricing, bool) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if catalog.dir == "" {
		return Pricing{}, false
	}
	if !catalog.loaded {
		catalog.loaded = true
		catalog.entries = loadCatalogDir(catalog.dir)
	}
	p, ok := catalog.entries[id]
	return p, ok
}

func loadCatalogDir(dir string) map[string]Pricing {
	files, _ := filepath.Glob(filepath.Join(dir, "nous-models-*.json"))
	out := map[string]Pricing{}
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxCatalogFile {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for id, p := range ParseCatalog(data) {
			out[id] = p
		}
	}
	return out
}

// ReloadCatalog discards the loaded catalog so the next lookup re-reads the
// cache files (used after the cache has been refreshed).
func ReloadCatalog() {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	catalog.loaded = false
	catalog.entries = nil
}
