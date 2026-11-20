// Package pricecache keeps a machine-wide copy of each Nous backend's model
// catalog (with prices) under <stateDir>/pricing, so `agentctl cost` and the
// usage ledger can price models without a per-project runtime dir or a manual
// curl. internal/tokens reads the files (any age); this package writes them,
// fetches them on request, and refreshes them at most once a day.
package pricecache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/safefs"
	"github.com/blsmth/agentctl/internal/stamp"
	"github.com/blsmth/agentctl/internal/tokens"
)

const (
	// FetchTimeout bounds one catalog request.
	FetchTimeout = 5 * time.Second
	// MaxAge is how old the cache may get before the title hook refreshes it.
	MaxAge = 24 * time.Hour

	maxBody = 8 << 20
)

// Seams; tests replace them. Resolved per call, never at package init.
var (
	dirFn      = config.PricingDir
	rootFn     = config.StateDir
	now        = time.Now
	httpClient = &http.Client{}
)

// KeyFunc resolves a backend's API key.
type KeyFunc func(ctx context.Context, name string, b config.BackendConfig) (string, error)

// DefaultKey resolves keys through backendauth (the store or api_key_cmd,
// never env or config).
func DefaultKey(ctx context.Context, name string, b config.BackendConfig) (string, error) {
	return backendauth.Resolve(ctx, name, b)
}

func normalizeBase(baseURL string) string {
	if baseURL == "" {
		baseURL = config.NousDefaultBaseURL
	}
	return strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
}

// FileName is the cache file name for a catalog base URL.
func FileName(baseURL string) string {
	sum := sha256.Sum256([]byte(normalizeBase(baseURL)))
	return "nous-" + hex.EncodeToString(sum[:6]) + ".json"
}

// Write stores catalog JSON for baseURL atomically (0600 file, 0700 dir).
func Write(baseURL string, data []byte) error {
	root, err := rootFn()
	if err != nil {
		return err
	}
	dir, err := dirFn()
	if err != nil {
		return err
	}
	if err := safefs.WriteFile(root, filepath.Join(dir, FileName(baseURL)), data, 0o600); err != nil {
		return fmt.Errorf("writing price cache: %w", err)
	}
	return nil
}

// LastWrite is the newest modification time of any cache file.
func LastWrite() (time.Time, bool) {
	dir, err := dirFn()
	if err != nil {
		return time.Time{}, false
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "nous-*.json"))
	var newest time.Time
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.Mode().IsRegular() && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest, !newest.IsZero()
}

type catalogDoc struct {
	Data []catalogModel `json:"data"`
}

type catalogModel struct {
	ID            string                 `json:"id"`
	ContextLength int                    `json:"context_length,omitempty"`
	Pricing       *tokens.CatalogPricing `json:"pricing,omitempty"`
}

// Fetch downloads the catalog of baseURL (key may be empty) and returns it in
// the compact cache form.
func Fetch(ctx context.Context, baseURL, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	url := normalizeBase(baseURL) + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", url, err)
	}
	backendauth.SetUserAgent(req)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, errors.New(redact(fmt.Sprintf("backend unreachable at %s: %v", url, err), key))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model catalog %s returned HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("reading model catalog: %s", redact(err.Error(), key))
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("model catalog %s exceeds %d bytes", url, maxBody)
	}
	var doc catalogDoc
	if err := json.Unmarshal(body, &doc); err != nil || len(doc.Data) == 0 {
		return nil, fmt.Errorf("model catalog %s is not a valid model list", url)
	}
	return json.Marshal(doc)
}

func redact(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[redacted]")
}

// Result is the outcome of refreshing one backend.
type Result struct {
	Backend string
	Models  int
	Err     error
}

// Refresh fetches the catalog of every nous-kind backend (sorted by name; one
// fetch per distinct base URL) and rewrites the cache. A backend whose key
// cannot be resolved is fetched without one (the catalog is public); one that
// fails does not stop the rest. tokens is told to reload afterwards.
func Refresh(ctx context.Context, backends map[string]config.BackendConfig, key KeyFunc) []Result {
	names := make([]string, 0, len(backends))
	for n, b := range backends {
		if b.IsNous() {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	done := map[string]Result{}
	var out []Result
	for _, n := range names {
		b := backends[n]
		base := normalizeBase(b.BaseURL)
		if prev, ok := done[base]; ok {
			prev.Backend = n
			out = append(out, prev)
			continue
		}
		r := Result{Backend: n}
		var k string
		if key != nil {
			k, _ = key(ctx, n, b)
		}
		data, err := Fetch(ctx, base, k)
		if err == nil {
			r.Models = len(tokens.ParseCatalog(data))
			err = Write(base, data)
		}
		r.Err = err
		done[base] = r
		out = append(out, r)
	}
	tokens.ReloadCatalog()
	return out
}

// RefreshIfStale refreshes the cache when it is older than MaxAge, at most
// once per MaxAge on the machine (a flocked stamp file). It reports whether
// this call refreshed. Failures are returned for logging only; callers treat
// them as non-fatal.
func RefreshIfStale(ctx context.Context, backends map[string]config.BackendConfig, key KeyFunc) (bool, error) {
	t := now()
	if last, ok := LastWrite(); ok && t.Sub(last) < MaxAge {
		return false, nil
	}
	root, err := rootFn()
	if err != nil {
		return false, err
	}
	dir, err := dirFn()
	if err != nil {
		return false, err
	}
	if err := safefs.MkdirAll(root, dir, 0o700); err != nil {
		return false, err
	}
	if !stamp.Try(filepath.Join(dir, "refresh.stamp"), MaxAge, t) {
		return false, nil
	}
	var errs []error
	for _, r := range Refresh(ctx, backends, key) {
		if r.Err != nil {
			errs = append(errs, fmt.Errorf("backend %s: %w", r.Backend, r.Err))
		}
	}
	return true, errors.Join(errs...)
}
