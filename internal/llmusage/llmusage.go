// Package llmusage is the ledger of background LLM calls made by agentctl
// itself (short titles today): one NDJSON line per call in
// <stateDir>/llm-usage/usage-<YYYY-MM>.ndjson, priced through internal/tokens
// at write time. It is separate from session cost, which prices Claude Code
// transcripts.
package llmusage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/safefs"
	"github.com/blsmth/agentctl/internal/tokens"
)

// MaxAge is how long a monthly file is kept.
const MaxAge = 90 * 24 * time.Hour

// maxLine bounds one ledger line when reading.
const maxLine = 64 << 10

// Seams; tests replace them. Resolved per call, never at package init.
var (
	dirFn  = func() (string, error) { d, err := config.StateDir(); return filepath.Join(d, "llm-usage"), err }
	rootFn = config.StateDir
	now    = time.Now
)

// Record is one ledger line.
type Record struct {
	TS               time.Time `json:"ts"`
	Feature          string    `json:"feature"`
	Backend          string    `json:"backend,omitempty"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	CachedTokens     int       `json:"cached_tokens,omitempty"`
	// CostUSD is null when the model has no known price.
	CostUSD *float64 `json:"cost_usd"`
	Window  string   `json:"window,omitempty"`
	Project string   `json:"project,omitempty"`
	OK      bool     `json:"ok"`
	// Error is the failure kind (no_key, no_credits, timeout, http_500, ...).
	Error string `json:"error,omitempty"`
}

// Meta says who a call is for. It travels in the context so any consumer of
// the client is attributed without the client knowing about windows.
type Meta struct {
	Feature string
	Window  string
	Project string
}

type metaKey struct{}

// WithMeta attaches m to ctx.
func WithMeta(ctx context.Context, m Meta) context.Context {
	return context.WithValue(ctx, metaKey{}, m)
}

// MetaFrom returns the Meta in ctx (zero when none).
func MetaFrom(ctx context.Context) Meta {
	m, _ := ctx.Value(metaKey{}).(Meta)
	return m
}

// Cost prices one request. prompt includes cached tokens (OpenAI-style usage).
// It returns nil when the model has no known price: never a guessed default.
func Cost(model string, prompt, completion, cached int) *float64 {
	m := tokens.Lookup(model)
	if !m.HasPricing() {
		return nil
	}
	if cached > prompt {
		cached = prompt
	}
	usd := m.Pricing.Cost(tokens.Usage{Input: prompt - cached, Output: completion, CacheRead: cached})
	return &usd
}

// FileName is the ledger file holding t.
func FileName(t time.Time) string {
	return "usage-" + t.UTC().Format("2006-01") + ".ndjson"
}

// Append writes r (TS defaults to now) to the ledger. The line is written
// with one O_APPEND write under an exclusive flock, so concurrent hooks never
// interleave. Errors are returned for logging; callers never fail on them.
func Append(r Record) error {
	if r.TS.IsZero() {
		r.TS = now()
	}
	r.TS = r.TS.UTC()
	root, err := rootFn()
	if err != nil {
		return err
	}
	dir, err := dirFn()
	if err != nil {
		return err
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	f, err := safefs.OpenNoFollow(root, filepath.Join(dir, FileName(r.TS)), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening usage ledger: %w", err)
	}
	defer f.Close() //nolint:errcheck
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking usage ledger: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("writing usage ledger: %w", err)
	}
	return nil
}

// Load returns the records with TS at or after since, oldest first. Malformed
// lines are skipped.
func Load(since time.Time) ([]Record, error) {
	dir, err := dirFn()
	if err != nil {
		return nil, err
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "usage-*.ndjson"))
	sort.Strings(matches)
	floor := FileName(since)
	var out []Record
	for _, m := range matches {
		if filepath.Base(m) < floor {
			continue
		}
		recs, err := readFile(m)
		if err != nil {
			continue
		}
		for _, r := range recs {
			if !r.TS.Before(since) {
				out = append(out, r)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out, nil
}

func readFile(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), maxLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if json.Unmarshal([]byte(line), &r) == nil && !r.TS.IsZero() {
			out = append(out, r)
		}
	}
	return out, nil
}

// Prune removes ledger files not modified for maxAge and reports how many.
func Prune(at time.Time, maxAge time.Duration) (int, error) {
	dir, err := dirFn()
	if err != nil {
		return 0, err
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "usage-*.ndjson"))
	n := 0
	for _, m := range matches {
		fi, err := os.Lstat(m)
		if err != nil || !fi.Mode().IsRegular() || at.Sub(fi.ModTime()) <= maxAge {
			continue
		}
		if os.Remove(m) == nil {
			n++
		}
	}
	return n, nil
}
