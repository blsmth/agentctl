package backendauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

// HTTPClient performs the verification request; tests replace it.
var HTTPClient = &http.Client{Timeout: 30 * time.Second}

var errorTypeRe = regexp.MustCompile(`^[a-z_]{1,64}$`)

// Verify makes one paid, one-token POST /v1/messages with key against b's
// configured model. Failures are mapped to short messages; the response body
// is never echoed beyond the Anthropic-style error.type.
func Verify(ctx context.Context, b config.BackendConfig, key string) error {
	body, err := json.Marshal(map[string]any{
		"model":      b.Model,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	if err != nil {
		return err
	}
	url := strings.TrimRight(b.BaseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building verify request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("verify request to %s failed: %w", b.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("key invalid or out of credits (HTTP 401)")
	case http.StatusNotFound:
		return fmt.Errorf("model not found: %s (HTTP 404)", b.Model)
	}
	var e struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	typ := ""
	if json.Unmarshal(raw, &e) == nil && errorTypeRe.MatchString(e.Error.Type) {
		typ = ": " + e.Error.Type
	}
	return fmt.Errorf("unexpected HTTP %d from backend%s", resp.StatusCode, typ)
}
