package dispatch

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
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/safefs"
	"github.com/blsmth/agentctl/internal/tokens"
)

const (
	nousCatalogTimeout = 3 * time.Second
	nousCatalogMaxBody = 8 << 20
	nousCatalogTTL     = 10 * time.Minute
	nousMaxSuggestions = 10
)

// nousModel is the slice of a /v1/models entry that preflight uses.
type nousModel struct {
	ID            string          `json:"id"`
	ContextLength int             `json:"context_length,omitempty"`
	Endpoints     json.RawMessage `json:"endpoints,omitempty"`
	// Pricing is the catalog's $/token price strings. It is kept in the cache
	// file so internal/tokens can price sessions on this model without any
	// network access (tokens.ParseCatalog converts to $/MTok and applies the
	// cache fallback rules).
	Pricing *tokens.CatalogPricing `json:"pricing,omitempty"`
}

type nousCatalog struct {
	Data []nousModel `json:"data"`
}

// nousInfo is what preflight learns about the backend.
type nousInfo struct {
	ContextLength int // catalog context_length of the primary model, 0 if unknown
}

// nowFn is the clock for catalog cache expiry; tests replace it.
var nowFn = time.Now

// preflightNous validates a nous launch without sending the key anywhere:
//  1. no settings layer sets a competing Anthropic credential,
//  2. every configured model is in the public catalog (GET /v1/models),
//  3. an API key can be resolved locally.
func preflightNous(name string, b config.BackendConfig, workDir, explicitModel string) (nousInfo, error) {
	if err := checkShadowingCredentials(workDir); err != nil {
		return nousInfo{}, err
	}
	cat, err := loadNousCatalog(b.BaseURL)
	if err != nil {
		return nousInfo{}, err
	}
	byID := make(map[string]nousModel, len(cat.Data))
	for _, m := range cat.Data {
		byID[m.ID] = m
	}
	wanted := []string{b.Model}
	if explicitModel != "" {
		wanted = append(wanted, explicitModel)
	}
	for _, a := range []string{"opus", "sonnet", "haiku", "subagent"} {
		wanted = append(wanted, b.AliasModel(a))
	}
	seen := map[string]bool{}
	for _, id := range wanted {
		if seen[id] {
			continue
		}
		seen[id] = true
		m, ok := byID[id]
		if !ok {
			return nousInfo{}, fmt.Errorf("model %q is not in the catalog at %s%s", id, b.BaseURL, suggestModels(cat.Data, id))
		}
		if len(m.Endpoints) > 0 && string(m.Endpoints) != "null" && !strings.Contains(string(m.Endpoints), "messages") {
			// The endpoints field is copied from OpenRouter and unreliable.
			logging.Warn("nous: model %q does not list a messages endpoint in the catalog; it may not work with Claude Code", id)
		}
	}
	effective := b.Model
	if explicitModel != "" {
		effective = explicitModel
	}
	info := nousInfo{ContextLength: byID[effective].ContextLength}

	// Only check that a key is available: no network call with the key, and
	// an api_key_cmd is not run (it may prompt, e.g. 1Password).
	if err := backendauth.Available(name, b); err != nil {
		return nousInfo{}, err
	}
	return info, nil
}

// suggestModels lists up to nousMaxSuggestions catalog ids sharing id's
// provider prefix ("qwen/"), formatted for an error message.
func suggestModels(models []nousModel, id string) string {
	prefix := id
	if i := strings.Index(id, "/"); i >= 0 {
		prefix = id[:i+1]
	}
	var ids []string
	for _, m := range models {
		if strings.HasPrefix(m.ID, prefix) {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	more := ""
	if len(ids) > nousMaxSuggestions {
		ids, more = ids[:nousMaxSuggestions], ", ..."
	}
	return " (similar: " + strings.Join(ids, ", ") + more + ")"
}

func catalogCachePath(baseURL string) string {
	rd := paths.RuntimeDir()
	if rd == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(baseURL))
	return filepath.Join(rd, "nous-models-"+hex.EncodeToString(sum[:6])+".json")
}

// loadNousCatalog returns the model catalog, from the runtime-dir cache when
// it is under nousCatalogTTL old, else fetched (and cached compactly).
func loadNousCatalog(baseURL string) (nousCatalog, error) {
	cachePath := catalogCachePath(baseURL)
	if cachePath != "" {
		if cat, ok := readCatalogCache(cachePath); ok {
			return cat, nil
		}
	}
	cat, err := fetchNousCatalog(baseURL)
	if err != nil {
		return nousCatalog{}, err
	}
	if cachePath != "" {
		if data, err := json.Marshal(cat); err == nil {
			if err := safefs.WriteFile(paths.RuntimeDir(), cachePath, data, 0o600); err != nil {
				logging.Debug("nous: catalog cache write: %v", err)
			}
		}
	}
	return cat, nil
}

func readCatalogCache(path string) (nousCatalog, bool) {
	f, err := safefs.OpenNoFollow(paths.RuntimeDir(), path, os.O_RDONLY, 0)
	if err != nil {
		return nousCatalog{}, false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || nowFn().Sub(fi.ModTime()) > nousCatalogTTL {
		return nousCatalog{}, false
	}
	data, err := io.ReadAll(io.LimitReader(f, nousCatalogMaxBody))
	var cat nousCatalog
	if err != nil || json.Unmarshal(data, &cat) != nil || len(cat.Data) == 0 {
		return nousCatalog{}, false
	}
	if !catalogHasPricing(cat) {
		return nousCatalog{}, false // written before pricing was cached: refetch
	}
	return cat, true
}

func catalogHasPricing(cat nousCatalog) bool {
	for _, m := range cat.Data {
		if m.Pricing != nil {
			return true
		}
	}
	return false
}

// RefreshNousPricing makes sure the cached catalog of every configured nous
// backend carries pricing, fetching (3s timeout) when the cache is missing,
// expired or predates pricing, and reloads tokens' view of it. Intended for
// explicit commands such as `agentctl cost`, never a hot path. Errors are
// returned joined; a failure to reach one backend does not stop the rest.
func RefreshNousPricing(backends map[string]config.BackendConfig) error {
	var errs []error
	for name, b := range backends {
		if !b.IsNous() {
			continue
		}
		if _, err := loadNousCatalog(b.BaseURL); err != nil {
			errs = append(errs, fmt.Errorf("backend %s: %w", name, err))
		}
	}
	tokens.ReloadCatalog()
	return errors.Join(errs...)
}

func fetchNousCatalog(baseURL string) (nousCatalog, error) {
	url := strings.TrimRight(baseURL, "/") + "/v1/models"
	ctx, cancel := context.WithTimeout(context.Background(), nousCatalogTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nousCatalog{}, fmt.Errorf("building request for %s: %w", url, err)
	}
	backendauth.SetUserAgent(req)
	resp, err := backendHTTPClient.Do(req)
	if err != nil {
		return nousCatalog{}, fmt.Errorf("backend unreachable at %s: %w", baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nousCatalog{}, fmt.Errorf("model catalog %s returned HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, nousCatalogMaxBody+1))
	if err != nil {
		return nousCatalog{}, fmt.Errorf("reading model catalog: %w", err)
	}
	if len(body) > nousCatalogMaxBody {
		return nousCatalog{}, fmt.Errorf("model catalog %s exceeds %d bytes", url, nousCatalogMaxBody)
	}
	var cat nousCatalog
	if err := json.Unmarshal(body, &cat); err != nil || len(cat.Data) == 0 {
		return nousCatalog{}, fmt.Errorf("model catalog %s is not a valid model list", url)
	}
	return cat, nil
}

// --- credential shadowing scan ---------------------------------------------

// managedSettingsPaths are Claude Code's managed-settings locations.
var managedSettingsPaths = []string{
	"/Library/Application Support/ClaudeCode/managed-settings.json",
	"/etc/claude-code/managed-settings.json",
}

// shadowingEnvKeys are env settings that outrank the apiKeyHelper and would
// be sent to Nous.
var shadowingEnvKeys = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}

const maxSettingsScanHops = 64

// checkShadowingCredentials refuses a nous launch when any Claude Code
// settings layer that applies to workDir sets a non-empty ANTHROPIC_API_KEY
// or ANTHROPIC_AUTH_TOKEN in env: a settings env value overrides the shell
// and would send that credential to Nous. Layers: managed settings,
// ~/.claude/settings.json, and .claude/settings{,.local}.json in workDir and
// every parent directory.
func checkShadowingCredentials(workDir string) error {
	files := append([]string{}, managedSettingsPaths...)
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".claude", "settings.json"))
	}
	if workDir != "" {
		dir := filepath.Clean(workDir)
		for i := 0; i < maxSettingsScanHops; i++ {
			files = append(files,
				filepath.Join(dir, ".claude", "settings.json"),
				filepath.Join(dir, ".claude", "settings.local.json"))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	for _, f := range files {
		if key := settingsFileCredential(f); key != "" {
			return fmt.Errorf("%s sets env.%s, which would be sent to the backend instead of its API key; remove it (or set it to \"\") before launching on this backend", f, key)
		}
	}
	return nil
}

// settingsFileCredential returns the first shadowing env key set non-empty in
// the settings file at path, or "". Missing or unparsable files return "".
func settingsFileCredential(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var s struct {
		Env map[string]any `json:"env"`
	}
	if json.Unmarshal(data, &s) != nil {
		return ""
	}
	for _, k := range shadowingEnvKeys {
		if v, ok := s.Env[k]; ok && v != nil && v != "" {
			return k
		}
	}
	return ""
}
