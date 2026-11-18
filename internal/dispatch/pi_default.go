package dispatch

import (
	"context"
	"errors"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/harness"
)

// preflightPiDefault checks that pi's own default provider is reachable when
// no agentctl backend applies. Otherwise pi launches against a dead endpoint,
// prints connection errors and idles while dispatch reports success. It is
// conservative: missing or unparsable pi files, or a provider without a
// baseUrl (a hosted provider pi knows by name), skip the check.
func preflightPiDefault() error {
	dir := harness.PiAgentDir()
	if dir == "" {
		return nil
	}
	var settings struct {
		DefaultProvider string `json:"defaultProvider"`
		DefaultModel    string `json:"defaultModel"`
	}
	if readJSONFile(filepath.Join(dir, "settings.json"), &settings) != nil || settings.DefaultProvider == "" {
		return nil
	}
	var models struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
		} `json:"providers"`
	}
	if readJSONFile(filepath.Join(dir, "models.json"), &models) != nil {
		return nil
	}
	url := models.Providers[settings.DefaultProvider].BaseURL
	if url == "" {
		return nil
	}
	// Reachability only: the model check does not apply to pi's config.
	ctx, cancel := context.WithTimeout(context.Background(), preflightTimeout)
	defer cancel()
	if _, err := ListOllamaModels(ctx, config.BackendConfig{BaseURL: url}); err != nil && !errors.Is(err, ErrNoModelList) {
		return fmt.Errorf("pi default provider %q is unreachable at %s: %w (start it, or pass --backend)", settings.DefaultProvider, url, err)
	}
	return nil
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
