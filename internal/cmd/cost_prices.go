package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/pricecache"
	"github.com/blsmth/agentctl/internal/tokens"
)

// PricesOptions are the flags of `agentctl cost prices`.
type PricesOptions struct {
	Filter  string // substring of the model id; empty lists every known model
	Refresh bool   // fetch every nous backend's catalog first
	JSON    bool
}

// priceRow is one model's resolved price. Prices are $ per million tokens.
type priceRow struct {
	ID         string
	Input      float64
	Output     float64
	CacheRead  *float64
	CacheWrite *float64
	// Source is tokens.Lookup's: config, catalog, built-in, family, free, or
	// "unknown".
	Source  string
	Updated time.Time // catalog file time; zero unless Source is catalog
}

// pricesRefresh fetches the catalogs; tests replace it.
var pricesRefresh = func(ctx context.Context, backends map[string]config.BackendConfig) []pricecache.Result {
	return pricecache.Refresh(ctx, backends, pricecache.DefaultKey)
}

// priceRowFor resolves id through tokens.Lookup, the one precedence rule.
func priceRowFor(id string) priceRow {
	m := tokens.Lookup(id)
	r := priceRow{ID: id, Source: m.Source}
	if !m.HasPricing() {
		r.Source = "unknown"
		return r
	}
	r.Input, r.Output = m.Pricing.InputPerMTok, m.Pricing.OutputPerMTok
	// A catalog entry without cache prices bills them as input, so there is
	// nothing to show.
	if v := m.Pricing.CacheReadPerMTok; v > 0 && (m.Source != "catalog" || v != r.Input) {
		r.CacheRead = &v
	}
	if v := m.Pricing.CacheCreatePerMTok; v > 0 && (m.Source != "catalog" || v != r.Input) {
		r.CacheWrite = &v
	}
	if m.Source == "catalog" {
		r.Updated, _ = tokens.CatalogUpdated(id)
	}
	return r
}

// buildPriceRows lists the models whose id contains filter (case-insensitive):
// config overrides, built-ins and the cached catalog. A filter that matches
// none is resolved as a model id of its own, which shows the family fallback
// (or that the model is unknown) instead of an empty table.
func buildPriceRows(filter string) []priceRow {
	filter = strings.ToLower(strings.TrimSpace(filter))
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		id = strings.ToLower(id)
		if !seen[id] && strings.Contains(id, filter) {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, m := range tokens.Registry() {
		add(m.Key)
	}
	for _, id := range tokens.CatalogModelIDs() {
		add(id)
	}
	sort.Strings(ids)
	rows := make([]priceRow, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, priceRowFor(id))
	}
	if len(rows) == 0 && filter != "" {
		rows = append(rows, priceRowFor(filter))
	}
	return rows
}

// RunCostPrices implements `agentctl cost prices`.
func RunCostPrices(o PricesOptions) error {
	cfg := config.Load() // pushes overrides and cache dirs into tokens
	if o.Refresh {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		results := pricesRefresh(ctx, cfg.Backends)
		if len(results) == 0 {
			fmt.Fprintln(os.Stderr, "note: no nous backend configured; nothing to refresh")
		}
		var failed error
		for _, r := range results {
			if r.Err != nil {
				fmt.Fprintf(os.Stderr, "note: backend %s: %v\n", r.Backend, r.Err)
				failed = r.Err
			} else if !o.JSON {
				fmt.Fprintf(os.Stderr, "refreshed %s: %d priced model(s)\n", r.Backend, r.Models)
			}
		}
		tokens.ReloadCatalog()
		if failed != nil && len(results) == countFailed(results) {
			// Nothing refreshed: still show the cached prices, but exit non-zero.
			if err := printPrices(buildPriceRows(o.Filter), o, costNow()); err != nil {
				return err
			}
			return fmt.Errorf("refreshing prices: %w", failed)
		}
	}
	return printPrices(buildPriceRows(o.Filter), o, costNow())
}

func countFailed(rs []pricecache.Result) int {
	n := 0
	for _, r := range rs {
		if r.Err != nil {
			n++
		}
	}
	return n
}

func fmtPrice(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), ".")
}

func optPrice(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmtPrice(*v)
}

func priceAge(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

type priceRowJSON struct {
	ID             string   `json:"id"`
	InputPerMTok   *float64 `json:"input_per_mtok"`
	OutputPerMTok  *float64 `json:"output_per_mtok"`
	CacheReadMTok  *float64 `json:"cache_read_per_mtok"`
	CacheWriteMTok *float64 `json:"cache_write_per_mtok"`
	Source         string   `json:"source"`
	CatalogUpdated *string  `json:"catalog_updated"`
	CatalogAgeSec  *int64   `json:"catalog_age_seconds"`
}

func printPrices(rows []priceRow, o PricesOptions, now time.Time) error {
	if o.JSON {
		out := []priceRowJSON{}
		for _, r := range rows {
			j := priceRowJSON{ID: r.ID, Source: r.Source, CacheReadMTok: r.CacheRead, CacheWriteMTok: r.CacheWrite}
			if r.Source != "unknown" {
				in, outp := r.Input, r.Output
				j.InputPerMTok, j.OutputPerMTok = &in, &outp
			}
			if !r.Updated.IsZero() {
				ts := r.Updated.UTC().Format(time.RFC3339)
				age := int64(now.Sub(r.Updated).Seconds())
				j.CatalogUpdated, j.CatalogAgeSec = &ts, &age
			}
			out = append(out, j)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	if len(rows) == 0 {
		fmt.Println(cliMuted().Render("No models known. Run `agentctl cost prices --refresh` to fetch the Nous catalog."))
		return nil
	}
	t := costTable("MODEL", "INPUT $/M", "OUTPUT $/M", "CACHE READ $/M", "CACHE WRITE $/M", "SOURCE", "CATALOG AGE")
	for _, r := range rows {
		if r.Source == "unknown" {
			t.Row(r.ID, "?", "?", "-", "-", r.Source, "-")
			continue
		}
		t.Row(r.ID, fmtPrice(r.Input), fmtPrice(r.Output), optPrice(r.CacheRead), optPrice(r.CacheWrite), r.Source, priceAge(r.Updated, now))
	}
	fmt.Println(t.Render())
	return nil
}
